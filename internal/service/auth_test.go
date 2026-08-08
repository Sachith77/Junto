package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/pkg/secrets"
)

type harness struct {
	svc      *AuthService
	users    *fakeUsers
	sessions *fakeSessions
	tokens   *fakeUserTokens
	mailer   *fakeMailer
	hasher   *fakeHasher
	issuer   *fakeIssuer
	clock    *fakeClock
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	h := &harness{
		users:    newFakeUsers(),
		sessions: newFakeSessions(),
		tokens:   newFakeUserTokens(),
		mailer:   &fakeMailer{},
		hasher:   &fakeHasher{},
		issuer:   newFakeIssuer(15 * time.Minute),
		clock:    newFakeClock(),
	}

	svc, err := NewAuthService(AuthDeps{
		Users:    h.users,
		Sessions: h.sessions,
		Tokens:   h.tokens,
		Hasher:   h.hasher,
		Issuer:   h.issuer,
		Mailer:   h.mailer,
		Tx:       &fakeTx{},
		Clock:    h.clock,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: AuthConfig{
			AccessTokenTTL:   15 * time.Minute,
			RefreshTokenTTL:  30 * 24 * time.Hour,
			SessionTTL:       90 * 24 * time.Hour,
			EmailVerifyTTL:   24 * time.Hour,
			PasswordResetTTL: time.Hour,
			WebBaseURL:       "https://junto.test",
		},
	})
	if err != nil {
		t.Fatalf("building service: %v", err)
	}
	h.svc = svc
	return h
}

const testPassword = "correct horse battery staple"

// signupAndVerify runs the full onboarding path and returns the verified user.
func (h *harness) signupAndVerify(t *testing.T, email string) *domain.User {
	t.Helper()
	ctx := context.Background()

	user, err := h.svc.Signup(ctx, SignupInput{
		Email: email, Password: testPassword, DisplayName: "Test User",
	})
	if err != nil {
		t.Fatalf("signup: %v", err)
	}

	token := h.lastEmailToken(t, "verify-email")
	if err := h.svc.VerifyEmail(ctx, token); err != nil {
		t.Fatalf("verify email: %v", err)
	}

	verified, err := h.users.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("reloading user: %v", err)
	}
	return verified
}

// lastEmailToken extracts a token from the most recent email, the way a user would click it.
func (h *harness) lastEmailToken(t *testing.T, pathFragment string) string {
	t.Helper()
	msg, ok := h.mailer.Last()
	if !ok {
		t.Fatal("no email was sent")
	}
	idx := strings.Index(msg.TextBody, pathFragment+"?token=")
	if idx < 0 {
		t.Fatalf("no %s link in email body:\n%s", pathFragment, msg.TextBody)
	}
	rest := msg.TextBody[idx+len(pathFragment)+len("?token="):]
	if end := strings.IndexAny(rest, "\n \r"); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

// --- signup ---

func TestSignupCreatesUnverifiedUserAndSendsLink(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	user, err := h.svc.Signup(ctx, SignupInput{
		Email: "New.User@Example.com", Password: testPassword, DisplayName: "New User",
	})
	if err != nil {
		t.Fatalf("signup: %v", err)
	}

	// Stored normalised, so lookup can never disagree with the lower(email) unique index.
	if user.Email != "new.user@example.com" {
		t.Errorf("email = %q, want normalised", user.Email)
	}
	if user.IsEmailVerified() {
		t.Error("a new account must start unverified")
	}
	if user.PasswordHash == testPassword {
		t.Fatal("the password was stored in plaintext")
	}

	msgs := h.mailer.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 verification email, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].TextBody, "https://junto.test/verify-email?token=") {
		t.Errorf("verification link missing from email:\n%s", msgs[0].TextBody)
	}
}

func TestSignupRejectsInvalidInput(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	cases := []struct {
		name  string
		in    SignupInput
		field string
	}{
		{"bad email", SignupInput{Email: "nope", Password: testPassword, DisplayName: "A"}, "email"},
		{"short password", SignupInput{Email: "a@b.co", Password: "short", DisplayName: "A"}, "password"},
		{"blank name", SignupInput{Email: "a@b.co", Password: testPassword, DisplayName: "  "}, "display_name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.svc.Signup(ctx, tc.in)
			if !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("expected a validation error, got %v", err)
			}
			ve, _ := domain.AsValidationError(err)
			found := false
			for _, v := range ve.Violations {
				if v.Field == tc.field {
					found = true
				}
			}
			if !found {
				t.Errorf("expected a violation on %q, got %+v", tc.field, ve.Violations)
			}
		})
	}

	// All three problems must be reported together, not one per round trip.
	_, err := h.svc.Signup(ctx, SignupInput{Email: "nope", Password: "x", DisplayName: ""})
	ve, ok := domain.AsValidationError(err)
	if !ok || len(ve.Violations) != 3 {
		t.Errorf("expected 3 aggregated violations, got %v", err)
	}
}

func TestSignupDoesNotEmailOnFailure(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.signupAndVerify(t, "taken@example.com")
	before := len(h.mailer.Messages())

	_, err := h.svc.Signup(ctx, SignupInput{
		Email: "taken@example.com", Password: testPassword, DisplayName: "Impostor",
	})
	if err == nil {
		t.Fatal("a duplicate signup must fail")
	}
	// The email is sent only after the transaction commits. Sending inside it would leave a
	// live verification link for an account that does not exist.
	if len(h.mailer.Messages()) != before {
		t.Error("no email should be sent when signup fails")
	}
}

// --- login ---

func TestLoginRequiresVerifiedEmail(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Signup(ctx, SignupInput{
		Email: "unverified@example.com", Password: testPassword, DisplayName: "Unverified",
	}); err != nil {
		t.Fatalf("signup: %v", err)
	}

	_, _, err := h.svc.Login(ctx, LoginInput{Email: "unverified@example.com", Password: testPassword})
	if !errors.Is(err, domain.ErrEmailNotVerified) {
		t.Fatalf("expected ErrEmailNotVerified, got %v", err)
	}

	// Verification is checked AFTER the password. Reporting "not verified" to someone with
	// the wrong password would confirm the account exists.
	_, _, err = h.svc.Login(ctx, LoginInput{Email: "unverified@example.com", Password: "wrong password here"})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("a wrong password must report invalid credentials, not unverified: %v", err)
	}
}

func TestLoginSucceedsAfterVerification(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user := h.signupAndVerify(t, "verified@example.com")

	got, pair, err := h.svc.Login(ctx, LoginInput{Email: "verified@example.com", Password: testPassword})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if got.ID != user.ID {
		t.Error("login returned the wrong user")
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatal("login must return both tokens")
	}
	if pair.AccessExpiresAt.After(pair.RefreshExpiresAt) {
		t.Error("the access token must expire before the refresh token")
	}

	// The raw refresh token must never be persisted; only its hash.
	stored, err := h.sessions.GetRefreshTokenByHash(ctx, hashOf(pair.RefreshToken))
	if err != nil {
		t.Fatalf("refresh token not stored: %v", err)
	}
	if string(stored.TokenHash) == pair.RefreshToken {
		t.Error("the raw refresh token was stored instead of its hash")
	}
}

// TestLoginIsNotAnAccountOracle covers the enumeration defence.
func TestLoginIsNotAnAccountOracle(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndVerify(t, "real@example.com")

	_, _, unknownErr := h.svc.Login(ctx, LoginInput{Email: "ghost@example.com", Password: testPassword})
	_, _, wrongErr := h.svc.Login(ctx, LoginInput{Email: "real@example.com", Password: "some other password"})

	// Identical errors: a caller cannot tell a nonexistent account from a wrong password.
	if !errors.Is(unknownErr, domain.ErrInvalidCredentials) || !errors.Is(wrongErr, domain.ErrInvalidCredentials) {
		t.Fatalf("both paths must report invalid credentials: unknown=%v wrong=%v", unknownErr, wrongErr)
	}
	if unknownErr.Error() != wrongErr.Error() {
		t.Errorf("error text differs between unknown account and wrong password:\n  %v\n  %v",
			unknownErr, wrongErr)
	}
}

// TestLoginVerifiesAgainstDummyHashForUnknownUser pins the timing side of that defence.
//
// With a real Argon2id hasher the not-found path would otherwise skip ~55ms of work, making
// registered addresses trivially distinguishable by latency alone.
func TestLoginVerifiesAgainstDummyHashForUnknownUser(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	var verifyCalls int
	counting := &countingHasher{inner: h.hasher, calls: &verifyCalls}

	svc, err := NewAuthService(AuthDeps{
		Users: h.users, Sessions: h.sessions, Tokens: h.tokens,
		Hasher: counting, Issuer: h.issuer, Mailer: h.mailer,
		Tx: &fakeTx{}, Clock: h.clock,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: AuthConfig{AccessTokenTTL: time.Minute, RefreshTokenTTL: time.Hour, SessionTTL: time.Hour},
	})
	if err != nil {
		t.Fatalf("building service: %v", err)
	}

	verifyCalls = 0
	_, _, _ = svc.Login(ctx, LoginInput{Email: "nobody@example.com", Password: testPassword})
	if verifyCalls != 1 {
		t.Errorf("an unknown account must still perform one Verify to equalise timing, got %d", verifyCalls)
	}
}

type countingHasher struct {
	inner domain.PasswordHasher
	calls *int
}

func (c *countingHasher) Hash(ctx context.Context, p string) (string, error) {
	return c.inner.Hash(ctx, p)
}

func (c *countingHasher) Verify(ctx context.Context, encoded, p string) (bool, bool, error) {
	*c.calls++
	return c.inner.Verify(ctx, encoded, p)
}

func TestLoginUpgradesWeakPasswordHash(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user := h.signupAndVerify(t, "upgrade@example.com")

	// Simulate a cost increase: existing hashes now report as needing a rehash.
	h.hasher.needsRehash = true

	if _, _, err := h.svc.Login(ctx, LoginInput{Email: "upgrade@example.com", Password: testPassword}); err != nil {
		t.Fatalf("login: %v", err)
	}

	after, err := h.users.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("reloading user: %v", err)
	}
	// The version advancing proves the upgrade was written. Login is the only moment the
	// plaintext exists to re-hash with, so it happens here or never.
	if after.Version <= user.Version {
		t.Errorf("the password hash should have been upgraded on login (version %d -> %d)",
			user.Version, after.Version)
	}
}

func TestLoginFailureDoesNotBlockOnRehashError(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndVerify(t, "resilient@example.com")

	h.hasher.needsRehash = true
	h.hasher.hashErr = errInjected // the rehash will fail

	// A failed cost upgrade is logged, never fatal: it must not lock out a user whose
	// credentials are correct.
	if _, _, err := h.svc.Login(ctx, LoginInput{Email: "resilient@example.com", Password: testPassword}); err != nil {
		t.Errorf("a failed rehash must not fail the login: %v", err)
	}
}

// --- refresh rotation and reuse detection ---

func TestRefreshRotatesTokens(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndVerify(t, "rotate@example.com")

	_, first, err := h.svc.Login(ctx, LoginInput{Email: "rotate@example.com", Password: testPassword})
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	h.clock.Advance(time.Minute)
	second, err := h.svc.Refresh(ctx, first.RefreshToken, RequestMeta{})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if second.RefreshToken == first.RefreshToken {
		t.Fatal("refresh must issue a NEW token; reusing it is not rotation")
	}
	if second.AccessToken == first.AccessToken {
		t.Error("refresh must issue a new access token")
	}
	if second.SessionID != first.SessionID {
		t.Error("rotation must stay within the same session")
	}

	// The old token is now spent and points at its successor, so the family can be walked.
	old, err := h.sessions.GetRefreshTokenByHash(ctx, hashOf(first.RefreshToken))
	if err != nil {
		t.Fatalf("reading old token: %v", err)
	}
	if !old.IsUsed() {
		t.Error("the rotated token must be marked used")
	}
	if old.ReplacedBy == nil {
		t.Error("the rotated token must point at its successor")
	}
}

// TestRefreshTokenReuseRevokesTheWholeFamily is the security-critical test in this package.
//
// Rotation without reuse detection is theatre: it changes the token but does nothing when the
// old one reappears. Detecting the replay and killing the family is what turns a stolen
// credential into a dead session rather than an open door.
func TestRefreshTokenReuseRevokesTheWholeFamily(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndVerify(t, "victim@example.com")

	_, stolen, err := h.svc.Login(ctx, LoginInput{Email: "victim@example.com", Password: testPassword})
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	// The legitimate client rotates normally.
	legitimate, err := h.svc.Refresh(ctx, stolen.RefreshToken, RequestMeta{})
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}

	// The attacker replays the token they captured earlier.
	_, err = h.svc.Refresh(ctx, stolen.RefreshToken, RequestMeta{})
	if !errors.Is(err, domain.ErrTokenConsumed) {
		t.Fatalf("a replayed token must be detected, got %v", err)
	}

	// The session is dead, so the LEGITIMATE user's newer token is now useless too. That is
	// the intended trade: an unexplained replay means one of the two parties is an attacker
	// and the server cannot tell which, so it ends the session for both.
	session, err := h.sessions.GetSession(ctx, stolen.SessionID)
	if err != nil {
		t.Fatalf("reading session: %v", err)
	}
	if session.RevokedAt == nil {
		t.Fatal("reuse detection must revoke the session")
	}
	if session.RevokedReason != domain.RevokeReasonTokenReuse {
		t.Errorf("revocation reason = %q, want %q", session.RevokedReason, domain.RevokeReasonTokenReuse)
	}

	if _, err := h.svc.Refresh(ctx, legitimate.RefreshToken, RequestMeta{}); err == nil {
		t.Error("the successor token must stop working once the family is revoked")
	}
}

func TestRefreshRejectsUnknownExpiredAndRevoked(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndVerify(t, "reject@example.com")

	t.Run("unknown token", func(t *testing.T) {
		_, err := h.svc.Refresh(ctx, "not-a-real-token", RequestMeta{})
		if !errors.Is(err, domain.ErrTokenInvalid) {
			t.Errorf("expected ErrTokenInvalid, got %v", err)
		}
	})

	t.Run("expired token", func(t *testing.T) {
		_, pair, err := h.svc.Login(ctx, LoginInput{Email: "reject@example.com", Password: testPassword})
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		h.clock.Advance(31 * 24 * time.Hour) // past the refresh TTL
		if _, err := h.svc.Refresh(ctx, pair.RefreshToken, RequestMeta{}); !errors.Is(err, domain.ErrTokenExpired) {
			t.Errorf("expected ErrTokenExpired, got %v", err)
		}
		h.clock.Advance(-31 * 24 * time.Hour)
	})

	t.Run("revoked session", func(t *testing.T) {
		_, pair, err := h.svc.Login(ctx, LoginInput{Email: "reject@example.com", Password: testPassword})
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		if err := h.svc.Logout(ctx, pair.RefreshToken); err != nil {
			t.Fatalf("logout: %v", err)
		}
		if _, err := h.svc.Refresh(ctx, pair.RefreshToken, RequestMeta{}); !errors.Is(err, domain.ErrTokenInvalid) {
			t.Errorf("a logged-out session must not refresh, got %v", err)
		}
	})
}

// --- email verification ---

func TestVerifyEmailIsSingleUseAndPurposeBound(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Signup(ctx, SignupInput{
		Email: "single@example.com", Password: testPassword, DisplayName: "Single",
	}); err != nil {
		t.Fatalf("signup: %v", err)
	}
	token := h.lastEmailToken(t, "verify-email")

	if err := h.svc.VerifyEmail(ctx, token); err != nil {
		t.Fatalf("first verification: %v", err)
	}
	if err := h.svc.VerifyEmail(ctx, token); !errors.Is(err, domain.ErrTokenConsumed) {
		t.Errorf("a verification token must be single-use, got %v", err)
	}

	// A verification token must not work as a password reset. Sharing one namespace would
	// let the weaker flow define the security of both.
	if err := h.svc.ResetPassword(ctx, token, "a brand new password"); !errors.Is(err, domain.ErrTokenInvalid) &&
		!errors.Is(err, domain.ErrTokenConsumed) {
		t.Errorf("a verification token must not reset a password, got %v", err)
	}
}

func TestVerifyEmailRejectsExpiredToken(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Signup(ctx, SignupInput{
		Email: "slow@example.com", Password: testPassword, DisplayName: "Slow",
	}); err != nil {
		t.Fatalf("signup: %v", err)
	}
	token := h.lastEmailToken(t, "verify-email")

	h.clock.Advance(25 * time.Hour) // TTL is 24h
	if err := h.svc.VerifyEmail(ctx, token); !errors.Is(err, domain.ErrTokenExpired) {
		t.Errorf("expected ErrTokenExpired, got %v", err)
	}
}

// --- password reset ---

func TestPasswordResetRevokesEverySession(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user := h.signupAndVerify(t, "reset@example.com")

	// Sign in from three devices.
	var pairs []*TokenPair
	for i := 0; i < 3; i++ {
		_, pair, err := h.svc.Login(ctx, LoginInput{Email: "reset@example.com", Password: testPassword})
		if err != nil {
			t.Fatalf("login %d: %v", i, err)
		}
		pairs = append(pairs, pair)
	}

	if err := h.svc.RequestPasswordReset(ctx, "reset@example.com"); err != nil {
		t.Fatalf("requesting reset: %v", err)
	}
	token := h.lastEmailToken(t, "reset-password")

	const newPassword = "an entirely different password"
	if err := h.svc.ResetPassword(ctx, token, newPassword); err != nil {
		t.Fatalf("resetting: %v", err)
	}

	// Every session must be gone. A reset that leaves an attacker signed in is worse than
	// useless: it tells the victim they have fixed the problem when they have not.
	active, err := h.sessions.ListActiveSessions(ctx, user.ID)
	if err != nil {
		t.Fatalf("listing sessions: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("%d sessions survived a password reset", len(active))
	}
	for i, pair := range pairs {
		if _, err := h.svc.Refresh(ctx, pair.RefreshToken, RequestMeta{}); err == nil {
			t.Errorf("session %d could still refresh after a password reset", i)
		}
	}

	// The old password must stop working and the new one must start.
	if _, _, err := h.svc.Login(ctx, LoginInput{Email: "reset@example.com", Password: testPassword}); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("the old password must no longer work, got %v", err)
	}
	if _, _, err := h.svc.Login(ctx, LoginInput{Email: "reset@example.com", Password: newPassword}); err != nil {
		t.Errorf("the new password should work: %v", err)
	}
}

// TestPasswordResetDoesNotLeakAccountExistence covers the enumeration defence on this
// endpoint: the response must be identical whether or not the address is registered.
func TestPasswordResetDoesNotLeakAccountExistence(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndVerify(t, "known@example.com")
	before := len(h.mailer.Messages())

	if err := h.svc.RequestPasswordReset(ctx, "nobody-here@example.com"); err != nil {
		t.Errorf("an unknown address must not produce an error: %v", err)
	}
	if len(h.mailer.Messages()) != before {
		t.Error("no email should be sent for an unknown address")
	}

	// A malformed address is still rejected: that is a client bug, not an account disclosure.
	if err := h.svc.RequestPasswordReset(ctx, "not-an-email"); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("a malformed address should be a validation error, got %v", err)
	}
}

func TestRequestingANewResetLinkRetiresTheOldOne(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndVerify(t, "retire@example.com")

	if err := h.svc.RequestPasswordReset(ctx, "retire@example.com"); err != nil {
		t.Fatalf("first request: %v", err)
	}
	firstToken := h.lastEmailToken(t, "reset-password")

	if err := h.svc.RequestPasswordReset(ctx, "retire@example.com"); err != nil {
		t.Fatalf("second request: %v", err)
	}
	secondToken := h.lastEmailToken(t, "reset-password")

	if firstToken == secondToken {
		t.Fatal("each request must issue a distinct token")
	}
	// Otherwise every link ever mailed stays live until expiry, and an old inbox becomes a
	// standing account takeover.
	if err := h.svc.ResetPassword(ctx, firstToken, "a replacement password"); err == nil {
		t.Error("the superseded reset link must stop working")
	}
	if err := h.svc.ResetPassword(ctx, secondToken, "a replacement password"); err != nil {
		t.Errorf("the newest reset link should work: %v", err)
	}
}

func TestPasswordResetVerifiesTheEmail(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// A user who never clicked the verification link but demonstrably controls the inbox.
	user, err := h.svc.Signup(ctx, SignupInput{
		Email: "never-verified@example.com", Password: testPassword, DisplayName: "Unverified",
	})
	if err != nil {
		t.Fatalf("signup: %v", err)
	}

	if err := h.svc.RequestPasswordReset(ctx, user.Email); err != nil {
		t.Fatalf("requesting reset: %v", err)
	}
	token := h.lastEmailToken(t, "reset-password")

	const newPassword = "proved i own this inbox"
	if err := h.svc.ResetPassword(ctx, token, newPassword); err != nil {
		t.Fatalf("resetting: %v", err)
	}

	// Completing a reset proves control of the address, so the account becomes verified.
	// Without this the user would be permanently locked out despite owning the inbox.
	after, err := h.users.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("reloading: %v", err)
	}
	if !after.IsEmailVerified() {
		t.Error("completing a password reset should verify the email address")
	}
	if _, _, err := h.svc.Login(ctx, LoginInput{Email: user.Email, Password: newPassword}); err != nil {
		t.Errorf("the user should now be able to sign in: %v", err)
	}
}

func TestResetPasswordEnforcesPasswordPolicy(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndVerify(t, "policy@example.com")

	if err := h.svc.RequestPasswordReset(ctx, "policy@example.com"); err != nil {
		t.Fatalf("requesting: %v", err)
	}
	token := h.lastEmailToken(t, "reset-password")

	if err := h.svc.ResetPassword(ctx, token, "short"); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a too-short password must be rejected, got %v", err)
	}
	// The token must survive a rejected attempt, or a typo would burn the user's only link.
	if err := h.svc.ResetPassword(ctx, token, "a sufficiently long password"); err != nil {
		t.Errorf("the token should still be usable after a validation failure: %v", err)
	}
}

// --- sessions ---

func TestAuthenticateRejectsRevokedSession(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.signupAndVerify(t, "sessions@example.com")

	_, pair, err := h.svc.Login(ctx, LoginInput{Email: "sessions@example.com", Password: testPassword})
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	if _, err := h.svc.Authenticate(ctx, pair.AccessToken); err != nil {
		t.Fatalf("a fresh access token should authenticate: %v", err)
	}

	if err := h.svc.Logout(ctx, pair.RefreshToken); err != nil {
		t.Fatalf("logout: %v", err)
	}

	// The JWT is still cryptographically valid and unexpired. The session check is what makes
	// logout take effect immediately instead of up to 15 minutes later.
	if _, err := h.svc.Authenticate(ctx, pair.AccessToken); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Errorf("a revoked session must invalidate its access tokens, got %v", err)
	}
}

func TestRevokeSessionChecksOwnership(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.signupAndVerify(t, "owner@example.com")
	victim := h.signupAndVerify(t, "victim2@example.com")

	attacker, _, err := h.svc.Login(ctx, LoginInput{Email: "owner@example.com", Password: testPassword})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	_, victimPair, err := h.svc.Login(ctx, LoginInput{Email: "victim2@example.com", Password: testPassword})
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	// Without the ownership check, any authenticated user could revoke any session id they
	// could guess or observe.
	err = h.svc.RevokeSession(ctx, attacker.ID, victimPair.SessionID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("revoking another user's session must fail, got %v", err)
	}

	active, err := h.sessions.ListActiveSessions(ctx, victim.ID)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(active) != 1 {
		t.Error("the victim's session should still be active")
	}
}

func TestListSessionsReturnsOnlyOwnActiveSessions(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user := h.signupAndVerify(t, "list@example.com")
	other := h.signupAndVerify(t, "other@example.com")

	for i := 0; i < 2; i++ {
		if _, _, err := h.svc.Login(ctx, LoginInput{Email: "list@example.com", Password: testPassword}); err != nil {
			t.Fatalf("login: %v", err)
		}
	}
	if _, _, err := h.svc.Login(ctx, LoginInput{Email: "other@example.com", Password: testPassword}); err != nil {
		t.Fatalf("login: %v", err)
	}

	sessions, err := h.svc.ListSessions(ctx, user.ID)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	for _, s := range sessions {
		if s.UserID != user.ID {
			t.Errorf("session %s belongs to another user", s.ID)
		}
	}
	_ = other
}

// hashOf mirrors what the service stores, so tests can look tokens up by hash.
func hashOf(raw string) []byte { return secrets.Hash(raw) }

// --- AUTH_AUTO_VERIFY_EMAIL (D105) ---

// TestAutoVerifyEmailSkipsTheLinkEntirely pins the development escape hatch.
//
// The interesting assertion is not just that the account comes back verified — it is that NO
// email is sent and NO verification token is issued. A version that emailed a link and then
// also verified the account would leave a live, unspent credential lying in an inbox for an
// account that never needed it.
func TestAutoVerifyEmailSkipsTheLinkEntirely(t *testing.T) {
	h := newHarness(t)
	h.svc.cfg.AutoVerifyEmail = true
	ctx := context.Background()

	user, err := h.svc.Signup(ctx, SignupInput{
		Email: "skip@example.com", Password: testPassword, DisplayName: "Skip",
	})
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	if !user.IsEmailVerified() {
		t.Error("with AutoVerifyEmail the account must be verified at signup")
	}
	if msgs := h.mailer.Messages(); len(msgs) != 0 {
		t.Errorf("expected no verification email, got %d", len(msgs))
	}

	// And the account is genuinely usable: login enforces verification (D29), so a login
	// that succeeds is the real proof rather than a flag read back from memory.
	if _, _, err := h.svc.Login(ctx, LoginInput{
		Email: "skip@example.com", Password: testPassword,
	}); err != nil {
		t.Errorf("an auto-verified account must be able to log in: %v", err)
	}
}

// TestSignupStillRequiresVerificationByDefault is the other half: the escape hatch must be
// OFF unless explicitly switched on, so the default build keeps D29's guarantee.
func TestSignupStillRequiresVerificationByDefault(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Signup(ctx, SignupInput{
		Email: "normal@example.com", Password: testPassword, DisplayName: "Normal",
	}); err != nil {
		t.Fatalf("signup: %v", err)
	}
	if _, _, err := h.svc.Login(ctx, LoginInput{
		Email: "normal@example.com", Password: testPassword,
	}); !errors.Is(err, domain.ErrEmailNotVerified) {
		t.Errorf("login before verification = %v, want ErrEmailNotVerified", err)
	}
}
