// Package service holds the application's business logic.
//
// It depends ONLY on interfaces declared in internal/domain. It must never import
// internal/repository, internal/transport, or any driver package — a rule enforced by
// tests/arch_test.go rather than by discipline. Nothing here knows what an HTTP status code
// is, what SQL looks like, or that WebSockets exist.
//
// Every mutation in the application goes through a method in this package. That is the
// single most important structural rule in the project: Stage 2 adds operation-log writes in
// one place here, and both the REST and WebSocket transports stay consistent by
// construction. A handler that writes to a repository directly breaks resync silently.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/pkg/secrets"
)

// AuthService implements signup, login, refresh-token rotation, email verification and
// password reset.
type AuthService struct {
	users    domain.UserRepository
	sessions domain.SessionRepository
	tokens   domain.UserTokenRepository
	hasher   domain.PasswordHasher
	issuer   domain.TokenIssuer
	mailer   domain.EmailSender
	tx       domain.TxManager
	clock    domain.Clock
	cfg      AuthConfig
	log      *slog.Logger

	// revocations closes live WebSocket connections whose session has just died (D91).
	//
	// A port, not the transport itself: this service knows that revoking a session must reach
	// anything still holding it, and nothing about sockets, instances or Redis. Every
	// revocation path below calls it, which is the point — a path that revoked in the database
	// and forgot to publish would leave exactly the credential-outlives-revocation gap D73
	// described, on that one path only, with every other test still green.
	revocations domain.RevocationPublisher

	// dummyHash defends against user enumeration by timing. See Login.
	dummyHash string
}

// AuthConfig is the subset of configuration this service needs.
//
// A narrow struct rather than the whole *configs.Config: a service should declare what it
// depends on, and passing the entire configuration would let it quietly start reading
// unrelated settings.
type AuthConfig struct {
	AccessTokenTTL   time.Duration
	RefreshTokenTTL  time.Duration
	SessionTTL       time.Duration
	EmailVerifyTTL   time.Duration
	PasswordResetTTL time.Duration
	WebBaseURL       string
}

// AuthDeps collects the service's dependencies.
//
// A params struct rather than a nine-argument constructor: positional arguments of the same
// type are trivially transposable, and two swapped repositories would compile.
type AuthDeps struct {
	Users    domain.UserRepository
	Sessions domain.SessionRepository
	Tokens   domain.UserTokenRepository
	Hasher   domain.PasswordHasher
	Issuer   domain.TokenIssuer
	Mailer   domain.EmailSender
	Tx       domain.TxManager
	Clock    domain.Clock
	Config   AuthConfig
	Logger   *slog.Logger

	// Revocations may be nil, which selects NoopRevocationPublisher: correct for a REST-only
	// deployment and for tests that are not about sockets, and never correct in production.
	Revocations domain.RevocationPublisher
}

// NewAuthService builds an AuthService.
func NewAuthService(deps AuthDeps) (*AuthService, error) {
	if deps.Clock == nil {
		deps.Clock = domain.SystemClock{}
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Revocations == nil {
		deps.Revocations = domain.NoopRevocationPublisher{}
	}

	// Precompute a valid hash of a value nobody knows. Login verifies against this when the
	// email is unknown, so the response time of "no such user" matches "wrong password".
	// Without it, an attacker distinguishes registered addresses by latency alone: the
	// not-found path would skip ~55ms of Argon2id work.
	filler, err := secrets.New()
	if err != nil {
		return nil, fmt.Errorf("service: generating enumeration guard: %w", err)
	}
	dummy, err := deps.Hasher.Hash(context.Background(), filler.Raw)
	if err != nil {
		return nil, fmt.Errorf("service: preparing enumeration guard: %w", err)
	}

	return &AuthService{
		users:       deps.Users,
		sessions:    deps.Sessions,
		tokens:      deps.Tokens,
		hasher:      deps.Hasher,
		issuer:      deps.Issuer,
		mailer:      deps.Mailer,
		tx:          deps.Tx,
		clock:       deps.Clock,
		cfg:         deps.Config,
		log:         deps.Logger,
		revocations: deps.Revocations,
		dummyHash:   dummy,
	}, nil
}

// RequestMeta describes where a request came from, for session bookkeeping.
type RequestMeta struct {
	UserAgent string
	IP        *netip.Addr
}

// TokenPair is what a successful authentication returns.
//
// RefreshToken is the RAW value and exists only in this struct, in transit. It is never
// stored, never logged, and never returned again.
type TokenPair struct {
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
	SessionID        domain.ID
}

// SignupInput is the input to Signup.
type SignupInput struct {
	Email       string
	Password    string
	DisplayName string
}

// Signup creates an account and emails a verification link.
//
// It deliberately does NOT return a session. Login requires a verified email, because an
// account whose address was never proven cannot be recovered by password reset — and an
// account you cannot recover is worse than one you cannot yet use. That is a product
// decision as much as a security one; it is the stricter of the two defensible options.
func (s *AuthService) Signup(ctx context.Context, in SignupInput) (*domain.User, error) {
	ve := &domain.ValidationError{}
	domain.ValidateEmail(ve, "email", in.Email)
	domain.ValidatePassword(ve, "password", in.Password)
	domain.ValidateDisplayName(ve, "display_name", in.DisplayName)
	if err := ve.OrNil(); err != nil {
		return nil, err
	}

	hash, err := s.hasher.Hash(ctx, in.Password)
	if err != nil {
		return nil, fmt.Errorf("hashing password: %w", err)
	}

	now := s.clock.Now()
	user := &domain.User{
		ID:           domain.NewID(),
		Email:        domain.NormalizeEmail(in.Email),
		PasswordHash: hash,
		DisplayName:  in.DisplayName,
		Version:      1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	var verifyToken secrets.Token
	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.users.Create(ctx, user); err != nil {
			return err
		}
		verifyToken, err = s.issueUserToken(ctx, user.ID, domain.TokenPurposeEmailVerify, s.cfg.EmailVerifyTTL)
		return err
	})
	if err != nil {
		return nil, err
	}

	// Sent AFTER the transaction commits. Inside it, a rollback would leave a live link to
	// an account that does not exist; email is the one side effect that cannot be undone.
	s.sendVerificationEmail(ctx, user, verifyToken.Raw)

	return user, nil
}

// LoginInput is the input to Login.
type LoginInput struct {
	Email    string
	Password string
	Meta     RequestMeta
}

// Login authenticates and opens a session.
func (s *AuthService) Login(ctx context.Context, in LoginInput) (*domain.User, *TokenPair, error) {
	now := s.clock.Now()

	user, err := s.users.GetByEmail(ctx, in.Email)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			// Burn the same time the real path would, then fail identically. Both the
			// timing and the message must be indistinguishable from a wrong password, or
			// the endpoint becomes an account-existence oracle.
			_, _, _ = s.hasher.Verify(ctx, s.dummyHash, in.Password)
			return nil, nil, domain.ErrInvalidCredentials
		}
		return nil, nil, err
	}

	ok, needsRehash, err := s.hasher.Verify(ctx, user.PasswordHash, in.Password)
	if err != nil {
		return nil, nil, fmt.Errorf("verifying password: %w", err)
	}
	if !ok {
		return nil, nil, domain.ErrInvalidCredentials
	}

	if !user.IsEmailVerified() {
		return nil, nil, domain.ErrEmailNotVerified
	}

	// The only moment the plaintext is available to re-hash with, so cost upgrades happen
	// here or not at all. A failure must not block a legitimate login.
	if needsRehash {
		if err := s.rehashPassword(ctx, user, in.Password); err != nil {
			s.log.WarnContext(ctx, "could not upgrade password hash",
				"user_id", user.ID, "error", err)
		}
	}

	pair, err := s.openSession(ctx, user.ID, in.Meta, now)
	if err != nil {
		return nil, nil, err
	}
	return user, pair, nil
}

// Refresh rotates a refresh token, returning a new pair.
//
// This is the security-critical path. Every failure mode is distinguished internally, and
// deliberately collapsed into one opaque error for the caller.
func (s *AuthService) Refresh(ctx context.Context, rawToken string, meta RequestMeta) (*TokenPair, error) {
	now := s.clock.Now()

	stored, err := s.sessions.GetRefreshTokenByHash(ctx, secrets.Hash(rawToken))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, domain.ErrTokenInvalid
		}
		return nil, err
	}

	// REPLAY. The token was already exchanged, so either it was stolen or the client has a
	// bug. Both are answered the same way: kill the entire family. Revoking only this token
	// would leave whoever holds the successor still rotating happily.
	if stored.IsUsed() {
		s.log.WarnContext(ctx, "refresh token reuse detected; revoking session",
			"session_id", stored.SessionID, "token_id", stored.ID)
		if err := s.sessions.RevokeSession(ctx, stored.SessionID, now, domain.RevokeReasonTokenReuse); err != nil {
			s.log.ErrorContext(ctx, "could not revoke session after reuse detection",
				"session_id", stored.SessionID, "error", err)
		}
		// Of every revocation path, this is the one where a live socket matters most: reuse
		// detection means the token was probably stolen, so someone else may be holding a
		// connection on this session right now.
		s.announceSessionRevoked(ctx, stored.SessionID, domain.RevokeReasonTokenReuse)
		return nil, domain.ErrTokenConsumed
	}

	if stored.IsExpired(now) {
		return nil, domain.ErrTokenExpired
	}

	session, err := s.sessions.GetSession(ctx, stored.SessionID)
	if err != nil {
		return nil, err
	}
	if !session.IsActive(now) {
		return nil, domain.ErrTokenInvalid
	}

	// IDs are generated up front so the old token can be marked used and pointed at its
	// successor in a single atomic statement.
	next, err := secrets.New()
	if err != nil {
		return nil, fmt.Errorf("generating refresh token: %w", err)
	}
	nextID := domain.NewID()

	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		// ORDER MATTERS, and not for style. refresh_tokens.replaced_by is a SELF-REFERENCING
		// foreign key, so marking the old token as replaced by nextID before that row exists
		// violates it. The successor must be inserted first.
		//
		// Doing it in this order is safe because both statements share the transaction: if
		// the guard below rejects the rotation, this insert rolls back with it and no orphan
		// token survives.
		if err := s.sessions.CreateRefreshToken(ctx, &domain.RefreshToken{
			ID:        nextID,
			SessionID: session.ID,
			TokenHash: next.Hash,
			ExpiresAt: now.Add(s.cfg.RefreshTokenTTL),
		}); err != nil {
			return err
		}
		// The `used_at IS NULL` guard makes this the real serialisation point: if two
		// requests race with the same token, exactly one updates a row. The loser lands in
		// the reuse branch below, which is the correct conclusion — it cannot tell a
		// double-firing client from a thief, and must assume the worse one.
		if err := s.sessions.MarkRefreshTokenUsed(ctx, stored.ID, now, nextID); err != nil {
			return err
		}
		return s.sessions.TouchSession(ctx, session.ID, now)
	})
	if err != nil {
		// Lost the race: another request already consumed this token. Treated as reuse.
		if errors.Is(err, domain.ErrTokenConsumed) {
			if rErr := s.sessions.RevokeSession(ctx, session.ID, now, domain.RevokeReasonTokenReuse); rErr != nil {
				s.log.ErrorContext(ctx, "could not revoke session after rotation race",
					"session_id", session.ID, "error", rErr)
			}
			s.publishRevocation(ctx, domain.RevocationEvent{
				Scope: domain.RevokeScopeSession, UserID: session.UserID, SessionID: session.ID,
				Reason: domain.RevokeReasonTokenReuse, At: now,
			})
			return nil, domain.ErrTokenConsumed
		}
		return nil, err
	}

	access, accessExp, err := s.issuer.Issue(session.UserID, session.ID, now)
	if err != nil {
		return nil, err
	}

	return &TokenPair{
		AccessToken:      access,
		AccessExpiresAt:  accessExp,
		RefreshToken:     next.Raw,
		RefreshExpiresAt: now.Add(s.cfg.RefreshTokenTTL),
		SessionID:        session.ID,
	}, nil
}

// Logout revokes the session behind a refresh token.
//
// Idempotent and quiet: an unknown or already-revoked token still reports success. A logout
// that returns an error tells the caller something about a token they may not own, and there
// is nothing useful for a client to do differently.
func (s *AuthService) Logout(ctx context.Context, rawToken string) error {
	stored, err := s.sessions.GetRefreshTokenByHash(ctx, secrets.Hash(rawToken))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return err
	}
	if err := s.sessions.RevokeSession(ctx, stored.SessionID, s.clock.Now(), domain.RevokeReasonLogout); err != nil {
		return err
	}
	// After the database write, never before. Publishing first would close the socket of a
	// session that then failed to revoke — the client would be disconnected and still logged in,
	// which is the worst of both outcomes.
	s.announceSessionRevoked(ctx, stored.SessionID, domain.RevokeReasonLogout)
	return nil
}

// VerifyEmail consumes a verification token and marks the address proven.
func (s *AuthService) VerifyEmail(ctx context.Context, rawToken string) error {
	now := s.clock.Now()

	return s.tx.WithinTx(ctx, func(ctx context.Context) error {
		token, err := s.consumeUserToken(ctx, rawToken, domain.TokenPurposeEmailVerify, now)
		if err != nil {
			return err
		}

		user, err := s.users.GetByID(ctx, token.UserID)
		if err != nil {
			return err
		}
		if user.IsEmailVerified() {
			return nil // already done; the token is spent either way
		}

		user.EmailVerifiedAt = &now
		return s.users.Update(ctx, user)
	})
}

// RequestPasswordReset issues a reset link.
//
// Always succeeds from the caller's point of view, whether or not the address exists.
// Reporting "no such account" would turn this endpoint into a free account-enumeration oracle
// for anyone with a list of email addresses.
func (s *AuthService) RequestPasswordReset(ctx context.Context, email string) error {
	ve := &domain.ValidationError{}
	domain.ValidateEmail(ve, "email", email)
	if err := ve.OrNil(); err != nil {
		return err
	}

	user, err := s.users.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.log.InfoContext(ctx, "password reset requested for unknown address")
			return nil
		}
		return err
	}

	var token secrets.Token
	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		// Retire outstanding reset tokens first. Without this, every link ever mailed stays
		// live until it expires, so an old inbox becomes a standing account takeover.
		if err := s.tokens.ConsumeAllForPurpose(ctx, user.ID, domain.TokenPurposePasswordReset, s.clock.Now()); err != nil {
			return err
		}
		token, err = s.issueUserToken(ctx, user.ID, domain.TokenPurposePasswordReset, s.cfg.PasswordResetTTL)
		return err
	})
	if err != nil {
		return err
	}

	s.sendPasswordResetEmail(ctx, user, token.Raw)
	return nil
}

// ResetPassword consumes a reset token and sets a new password.
func (s *AuthService) ResetPassword(ctx context.Context, rawToken, newPassword string) error {
	ve := &domain.ValidationError{}
	domain.ValidatePassword(ve, "password", newPassword)
	if err := ve.OrNil(); err != nil {
		return err
	}

	hash, err := s.hasher.Hash(ctx, newPassword)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}
	now := s.clock.Now()

	// Captured inside the transaction and used after it commits, so the announcement addresses
	// the account the reset actually applied to rather than re-deriving it from a token that
	// has since been consumed.
	var resetUserID domain.ID

	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		token, err := s.consumeUserToken(ctx, rawToken, domain.TokenPurposePasswordReset, now)
		if err != nil {
			return err
		}

		user, err := s.users.GetByID(ctx, token.UserID)
		if err != nil {
			return err
		}
		resetUserID = user.ID
		user.PasswordHash = hash

		// A reset also proves control of the address, so an unverified account becomes
		// verified here. Otherwise a user who never clicked the verification link would be
		// permanently locked out despite demonstrably owning the inbox.
		if !user.IsEmailVerified() {
			user.EmailVerifiedAt = &now
		}
		if err := s.users.Update(ctx, user); err != nil {
			return err
		}

		// Non-negotiable. A reset that leaves an attacker's session alive is worse than
		// useless: it tells the victim they have fixed the problem when they have not.
		return s.sessions.RevokeAllSessions(ctx, user.ID, now, domain.RevokeReasonPasswordReset)
	})
	if err != nil {
		return err
	}
	// Outside the transaction, and therefore only after it has COMMITTED. Closing sockets for a
	// reset that then rolled back would log a user out of their own account on the strength of
	// a write that never happened.
	s.publishRevocation(ctx, domain.RevocationEvent{
		Scope: domain.RevokeScopeUser, UserID: resetUserID,
		Reason: domain.RevokeReasonPasswordReset, At: now,
	})
	return nil
}

// publishRevocation hands a revocation to whatever is holding live connections (D91).
//
// One line, one place. Every revocation path in this service funnels through it for the same
// reason every mutation funnels through the op log helper (Rule 3): a path that revoked in the
// database and forgot to publish would re-open D73 on that path alone — a socket outliving its
// own logout — while every other test stayed green.
func (s *AuthService) publishRevocation(ctx context.Context, ev domain.RevocationEvent) {
	s.revocations.Publish(ctx, ev)
}

// announceSessionRevoked publishes for a path that knows the session id but not the user id.
//
// It costs one extra read, on paths that run at human frequency — a logout, or a stolen refresh
// token being detected. The alternative is a revocation event carrying no user id, which would
// force RevocationEvent.Matches to accept a session id alone and quietly widen what a forged or
// corrupted message could close. Paying for the lookup keeps the match rule strict.
//
// A failed lookup is logged and swallowed: the session is already revoked in the database, so
// the next HTTP request fails regardless, and the connection lifetime cap still bounds the
// socket. Failing the logout because its announcement could not be addressed would be worse.
func (s *AuthService) announceSessionRevoked(ctx context.Context, sessionID domain.ID, reason string) {
	session, err := s.sessions.GetSession(ctx, sessionID)
	if err != nil {
		s.log.ErrorContext(ctx, "could not resolve the user for a revoked session, so live "+
			"connections on it will close on the lifetime cap instead",
			"session_id", sessionID, "reason", reason, "error", err)
		return
	}
	s.publishRevocation(ctx, domain.RevocationEvent{
		Scope: domain.RevokeScopeSession, UserID: session.UserID, SessionID: sessionID,
		Reason: reason, At: s.clock.Now(),
	})
}

// User loads a user by id, for the authenticated "who am I" endpoint.
func (s *AuthService) User(ctx context.Context, id domain.ID) (*domain.User, error) {
	return s.users.GetByID(ctx, id)
}

// ListSessions returns a user's active sessions, for a device-management screen.
func (s *AuthService) ListSessions(ctx context.Context, userID domain.ID) ([]*domain.AuthSession, error) {
	return s.sessions.ListActiveSessions(ctx, userID)
}

// RevokeSession ends one session on behalf of its owner.
func (s *AuthService) RevokeSession(ctx context.Context, userID, sessionID domain.ID) error {
	session, err := s.sessions.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	// Ownership check before acting. Without it, any authenticated user could revoke any
	// session id they could guess or observe.
	if session.UserID != userID {
		return fmt.Errorf("%w: session belongs to another user", domain.ErrNotFound)
	}
	now := s.clock.Now()
	if err := s.sessions.RevokeSession(ctx, sessionID, now, domain.RevokeReasonUserRequest); err != nil {
		return err
	}
	s.publishRevocation(ctx, domain.RevocationEvent{
		Scope: domain.RevokeScopeSession, UserID: session.UserID, SessionID: sessionID,
		Reason: domain.RevokeReasonUserRequest, At: now,
	})
	return nil
}

// Authenticate verifies an access token and confirms its session is still live.
//
// The session check is what makes revocation effective within one access-token lifetime
// rather than not at all: the JWT itself cannot be withdrawn, so logout would otherwise do
// nothing for up to 15 minutes.
func (s *AuthService) Authenticate(ctx context.Context, accessToken string) (*domain.AccessTokenClaims, error) {
	cl, err := s.issuer.Parse(accessToken)
	if err != nil {
		return nil, err
	}

	session, err := s.sessions.GetSession(ctx, cl.SessionID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, domain.ErrTokenInvalid
		}
		return nil, err
	}
	if !session.IsActive(s.clock.Now()) {
		return nil, domain.ErrTokenInvalid
	}
	if session.UserID != cl.UserID {
		// Should be impossible; treated as tampering rather than ignored.
		return nil, domain.ErrTokenInvalid
	}
	return cl, nil
}

// --- internals ---

func (s *AuthService) openSession(ctx context.Context, userID domain.ID, meta RequestMeta, now time.Time) (*TokenPair, error) {
	refresh, err := secrets.New()
	if err != nil {
		return nil, fmt.Errorf("generating refresh token: %w", err)
	}

	session := &domain.AuthSession{
		ID:        domain.NewID(),
		UserID:    userID,
		UserAgent: truncate(meta.UserAgent, 512),
		IP:        meta.IP,
		ExpiresAt: now.Add(s.cfg.SessionTTL),
	}
	refreshExpiry := now.Add(s.cfg.RefreshTokenTTL)

	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.sessions.CreateSession(ctx, session); err != nil {
			return err
		}
		return s.sessions.CreateRefreshToken(ctx, &domain.RefreshToken{
			ID:        domain.NewID(),
			SessionID: session.ID,
			TokenHash: refresh.Hash,
			ExpiresAt: refreshExpiry,
		})
	})
	if err != nil {
		return nil, err
	}

	access, accessExp, err := s.issuer.Issue(userID, session.ID, now)
	if err != nil {
		return nil, err
	}

	return &TokenPair{
		AccessToken:      access,
		AccessExpiresAt:  accessExp,
		RefreshToken:     refresh.Raw,
		RefreshExpiresAt: refreshExpiry,
		SessionID:        session.ID,
	}, nil
}

func (s *AuthService) issueUserToken(ctx context.Context, userID domain.ID, purpose domain.TokenPurpose, ttl time.Duration) (secrets.Token, error) {
	token, err := secrets.New()
	if err != nil {
		return secrets.Token{}, fmt.Errorf("generating %s token: %w", purpose, err)
	}
	err = s.tokens.Create(ctx, &domain.UserToken{
		ID:        domain.NewID(),
		UserID:    userID,
		Purpose:   purpose,
		TokenHash: token.Hash,
		ExpiresAt: s.clock.Now().Add(ttl),
	})
	if err != nil {
		return secrets.Token{}, err
	}
	return token, nil
}

// consumeUserToken validates and spends a single-use token.
//
// Every rejection returns the same class of error to the caller. Distinguishing "expired"
// from "already used" from "wrong purpose" in an API response tells an attacker holding a
// stolen link exactly what happened to it.
func (s *AuthService) consumeUserToken(ctx context.Context, raw string, purpose domain.TokenPurpose, now time.Time) (*domain.UserToken, error) {
	token, err := s.tokens.GetByHash(ctx, secrets.Hash(raw))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, domain.ErrTokenInvalid
		}
		return nil, err
	}

	// A verification token must not be usable as a password reset. Without this check the
	// two flows share one namespace and the weaker one defines the security of both.
	if token.Purpose != purpose {
		return nil, domain.ErrTokenInvalid
	}
	if token.ConsumedAt != nil {
		return nil, domain.ErrTokenConsumed
	}
	if !now.Before(token.ExpiresAt) {
		return nil, domain.ErrTokenExpired
	}

	// The atomic guard. Two requests racing on the same link produce exactly one winner.
	if err := s.tokens.Consume(ctx, token.ID, now); err != nil {
		return nil, err
	}
	return token, nil
}

func (s *AuthService) rehashPassword(ctx context.Context, user *domain.User, plaintext string) error {
	hash, err := s.hasher.Hash(ctx, plaintext)
	if err != nil {
		return err
	}
	fresh := *user
	fresh.PasswordHash = hash
	return s.users.Update(ctx, &fresh)
}

func (s *AuthService) sendVerificationEmail(ctx context.Context, user *domain.User, rawToken string) {
	link := fmt.Sprintf("%s/verify-email?token=%s", s.cfg.WebBaseURL, rawToken)
	s.send(ctx, domain.EmailMessage{
		To:      user.Email,
		Subject: "Confirm your Junto account",
		TextBody: fmt.Sprintf(
			"Hi %s,\n\nConfirm your email address to finish setting up your Junto account:\n\n%s\n\n"+
				"This link expires in %s. If you did not sign up, you can ignore this message.\n",
			user.DisplayName, link, s.cfg.EmailVerifyTTL),
	})
}

func (s *AuthService) sendPasswordResetEmail(ctx context.Context, user *domain.User, rawToken string) {
	link := fmt.Sprintf("%s/reset-password?token=%s", s.cfg.WebBaseURL, rawToken)
	s.send(ctx, domain.EmailMessage{
		To:      user.Email,
		Subject: "Reset your Junto password",
		TextBody: fmt.Sprintf(
			"Hi %s,\n\nUse this link to choose a new password:\n\n%s\n\n"+
				"This link expires in %s and can only be used once. "+
				"If you did not request it, no action is needed — your password has not changed.\n",
			user.DisplayName, link, s.cfg.PasswordResetTTL),
	})
}

// send delivers mail, logging failures rather than propagating them.
//
// Email is best-effort by design. A signup must not fail because an SMTP server was briefly
// unreachable — the account exists, the token exists, and the user can request another link.
// Propagating the error would roll back an account creation that actually succeeded.
func (s *AuthService) send(ctx context.Context, msg domain.EmailMessage) {
	if err := s.mailer.Send(ctx, msg); err != nil {
		s.log.ErrorContext(ctx, "sending email failed",
			"subject", msg.Subject, "error", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
