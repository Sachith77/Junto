package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/junto/junto/internal/domain"
)

// In-memory fakes for the domain ports.
//
// These are FAKES, not mocks: they implement the real behaviour (including the atomic
// guards that matter, like "consume only if unconsumed") rather than asserting call
// sequences. A mock that verifies "Consume was called once" passes even when the guard is
// broken; a fake that actually enforces the guard fails, which is what a test is for.
//
// Repository behaviour against real Postgres is covered separately in internal/repository.
// These exist so business logic can be tested exhaustively without a container per case.

// fakeClock lets tests move time without sleeping.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeHasher is a trivial reversible "hash".
//
// Deliberately not Argon2id: the point of a port here is that tests do not pay 55ms per
// hash. Password-hashing correctness is covered in internal/security.
type fakeHasher struct {
	needsRehash bool
	hashErr     error
}

func (h *fakeHasher) Hash(_ context.Context, plaintext string) (string, error) {
	if h.hashErr != nil {
		return "", h.hashErr
	}
	return "fake$" + plaintext, nil
}

func (h *fakeHasher) Verify(_ context.Context, encoded, plaintext string) (bool, bool, error) {
	return encoded == "fake$"+plaintext, h.needsRehash, nil
}

// fakeIssuer mints inspectable tokens.
type fakeIssuer struct {
	ttl    time.Duration
	issued map[string]*domain.AccessTokenClaims
	mu     sync.Mutex
}

func newFakeIssuer(ttl time.Duration) *fakeIssuer {
	return &fakeIssuer{ttl: ttl, issued: map[string]*domain.AccessTokenClaims{}}
}

func (i *fakeIssuer) Issue(userID, sessionID domain.ID, now time.Time) (string, time.Time, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	token := fmt.Sprintf("access-%s-%s-%d", userID, sessionID, now.UnixNano())
	exp := now.Add(i.ttl)
	i.issued[token] = &domain.AccessTokenClaims{
		UserID: userID, SessionID: sessionID, IssuedAt: now, ExpiresAt: exp,
	}
	return token, exp, nil
}

func (i *fakeIssuer) Parse(token string) (*domain.AccessTokenClaims, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	c, ok := i.issued[token]
	if !ok {
		return nil, domain.ErrTokenInvalid
	}
	return c, nil
}

// fakeMailer records what would have been sent, so tests can extract the token from a link
// exactly as a user would from their inbox.
type fakeMailer struct {
	mu   sync.Mutex
	sent []domain.EmailMessage
	err  error
}

func (m *fakeMailer) Send(_ context.Context, msg domain.EmailMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, msg)
	return nil
}

func (m *fakeMailer) Messages() []domain.EmailMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]domain.EmailMessage(nil), m.sent...)
}

func (m *fakeMailer) Last() (domain.EmailMessage, bool) {
	msgs := m.Messages()
	if len(msgs) == 0 {
		return domain.EmailMessage{}, false
	}
	return msgs[len(msgs)-1], true
}

// fakeTx runs the callback directly.
//
// It does NOT simulate rollback. That is a real limitation and it is stated rather than
// glossed over: these tests verify business logic, not transactional atomicity, which is
// covered against real Postgres in internal/repository (including the savepoint semantics
// that a fake cannot reproduce).
type fakeTx struct{ failAfter error }

func (t *fakeTx) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	if err := fn(ctx); err != nil {
		return err
	}
	return t.failAfter
}

// --- repositories ---

type fakeUsers struct {
	mu    sync.Mutex
	byID  map[domain.ID]*domain.User
	getBy func(email string) error // optional error injection
}

func newFakeUsers() *fakeUsers {
	return &fakeUsers{byID: map[domain.ID]*domain.User{}}
}

func (r *fakeUsers) Create(_ context.Context, u *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.byID {
		if existing.DeletedAt == nil && domain.NormalizeEmail(existing.Email) == domain.NormalizeEmail(u.Email) {
			ve := &domain.ValidationError{}
			ve.Add("email", "email_taken", "an account with this email already exists")
			return ve
		}
	}
	clone := *u
	r.byID[u.ID] = &clone
	return nil
}

func (r *fakeUsers) GetByID(_ context.Context, id domain.ID) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.byID[id]
	if !ok || u.DeletedAt != nil {
		return nil, domain.ErrNotFound
	}
	clone := *u
	return &clone, nil
}

func (r *fakeUsers) GetByEmail(_ context.Context, email string) (*domain.User, error) {
	if r.getBy != nil {
		if err := r.getBy(email); err != nil {
			return nil, err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	want := domain.NormalizeEmail(email)
	for _, u := range r.byID {
		if u.DeletedAt == nil && domain.NormalizeEmail(u.Email) == want {
			clone := *u
			return &clone, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (r *fakeUsers) Update(_ context.Context, u *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.byID[u.ID]
	if !ok {
		return domain.ErrNotFound
	}
	// Enforce optimistic concurrency, so a service bug that writes a stale version fails
	// here rather than only against a real database.
	if existing.Version != u.Version {
		return domain.ErrVersionConflict
	}
	clone := *u
	clone.Version++
	r.byID[u.ID] = &clone
	*u = clone
	return nil
}

func (r *fakeUsers) SoftDelete(_ context.Context, id domain.ID, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.byID[id]
	if !ok {
		return domain.ErrNotFound
	}
	u.DeletedAt = &at
	return nil
}

type fakeSessions struct {
	mu       sync.Mutex
	sessions map[domain.ID]*domain.AuthSession
	tokens   map[string]*domain.RefreshToken // keyed by hex of hash
}

func newFakeSessions() *fakeSessions {
	return &fakeSessions{
		sessions: map[domain.ID]*domain.AuthSession{},
		tokens:   map[string]*domain.RefreshToken{},
	}
}

func hashKey(h []byte) string { return fmt.Sprintf("%x", h) }

func (r *fakeSessions) CreateSession(_ context.Context, s *domain.AuthSession) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := *s
	r.sessions[s.ID] = &clone
	return nil
}

func (r *fakeSessions) GetSession(_ context.Context, id domain.ID) (*domain.AuthSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	clone := *s
	return &clone, nil
}

func (r *fakeSessions) ListActiveSessions(_ context.Context, userID domain.ID) ([]*domain.AuthSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.AuthSession
	for _, s := range r.sessions {
		if s.UserID == userID && s.RevokedAt == nil {
			clone := *s
			out = append(out, &clone)
		}
	}
	return out, nil
}

func (r *fakeSessions) TouchSession(_ context.Context, id domain.ID, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.sessions[id]; ok && s.RevokedAt == nil {
		s.LastUsedAt = at
	}
	return nil
}

func (r *fakeSessions) RevokeSession(_ context.Context, id domain.ID, at time.Time, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok {
		return nil
	}
	// Idempotent, preserving the ORIGINAL reason — matching the SQL, so a service relying on
	// that behaviour is tested against it here too.
	if s.RevokedAt == nil {
		s.RevokedAt = &at
		s.RevokedReason = reason
	}
	return nil
}

func (r *fakeSessions) RevokeAllSessions(_ context.Context, userID domain.ID, at time.Time, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sessions {
		if s.UserID == userID && s.RevokedAt == nil {
			revoked := at
			s.RevokedAt = &revoked
			s.RevokedReason = reason
		}
	}
	return nil
}

func (r *fakeSessions) CreateRefreshToken(_ context.Context, t *domain.RefreshToken) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := *t
	r.tokens[hashKey(t.TokenHash)] = &clone
	return nil
}

func (r *fakeSessions) GetRefreshTokenByHash(_ context.Context, hash []byte) (*domain.RefreshToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tokens[hashKey(hash)]
	if !ok {
		return nil, domain.ErrNotFound
	}
	clone := *t
	return &clone, nil
}

func (r *fakeSessions) MarkRefreshTokenUsed(_ context.Context, id domain.ID, at time.Time, replacedBy domain.ID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Enforce the self-referencing foreign key on replaced_by.
	//
	// This check exists because its absence hid a real bug. The service originally marked the
	// old token as replaced BEFORE inserting the successor, which the real database rejects
	// with a foreign-key violation — but the fake happily accepted it, so every unit test
	// passed and only the full-stack HTTP test caught it.
	//
	// The lesson generalises: a fake that does not enforce the constraints the real thing
	// enforces is not a cheaper test double, it is a different system that agrees with the
	// bug. Constraints that exist in the schema belong here too.
	if replacedBy != domain.NilID {
		found := false
		for _, t := range r.tokens {
			if t.ID == replacedBy {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: replaced_by references a token that does not exist", domain.ErrNotFound)
		}
	}

	for _, t := range r.tokens {
		if t.ID != id {
			continue
		}
		// The atomic guard, reproduced faithfully. This is the behaviour reuse detection
		// depends on, so a fake that skipped it would make the most important test in this
		// package vacuous.
		if t.UsedAt != nil {
			return domain.ErrTokenConsumed
		}
		used := at
		t.UsedAt = &used
		next := replacedBy
		t.ReplacedBy = &next
		return nil
	}
	return domain.ErrNotFound
}

func (r *fakeSessions) DeleteExpiredRefreshTokens(_ context.Context, before time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	for k, t := range r.tokens {
		if t.ExpiresAt.Before(before) {
			delete(r.tokens, k)
			n++
		}
	}
	return n, nil
}

type fakeUserTokens struct {
	mu     sync.Mutex
	byHash map[string]*domain.UserToken
}

func newFakeUserTokens() *fakeUserTokens {
	return &fakeUserTokens{byHash: map[string]*domain.UserToken{}}
}

func (r *fakeUserTokens) Create(_ context.Context, t *domain.UserToken) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := *t
	r.byHash[hashKey(t.TokenHash)] = &clone
	return nil
}

func (r *fakeUserTokens) GetByHash(_ context.Context, hash []byte) (*domain.UserToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.byHash[hashKey(hash)]
	if !ok {
		return nil, domain.ErrNotFound
	}
	clone := *t
	return &clone, nil
}

func (r *fakeUserTokens) Consume(_ context.Context, id domain.ID, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.byHash {
		if t.ID != id {
			continue
		}
		if t.ConsumedAt != nil {
			return domain.ErrTokenConsumed
		}
		used := at
		t.ConsumedAt = &used
		return nil
	}
	return domain.ErrNotFound
}

func (r *fakeUserTokens) ConsumeAllForPurpose(_ context.Context, userID domain.ID, purpose domain.TokenPurpose, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.byHash {
		if t.UserID == userID && t.Purpose == purpose && t.ConsumedAt == nil {
			used := at
			t.ConsumedAt = &used
		}
	}
	return nil
}

func (r *fakeUserTokens) DeleteExpired(_ context.Context, before time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	for k, t := range r.byHash {
		if t.ExpiresAt.Before(before) {
			delete(r.byHash, k)
			n++
		}
	}
	return n, nil
}

// Compile-time proof the fakes still satisfy the ports they stand in for. Without these, a
// port could gain a method and the fakes would silently drift out of date.
var (
	_ domain.UserRepository      = (*fakeUsers)(nil)
	_ domain.SessionRepository   = (*fakeSessions)(nil)
	_ domain.UserTokenRepository = (*fakeUserTokens)(nil)
	_ domain.PasswordHasher      = (*fakeHasher)(nil)
	_ domain.TokenIssuer         = (*fakeIssuer)(nil)
	_ domain.EmailSender         = (*fakeMailer)(nil)
	_ domain.TxManager           = (*fakeTx)(nil)
	_ domain.Clock               = (*fakeClock)(nil)
)

var errInjected = errors.New("injected failure")
