package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/pkg/fracdex"
)

// --- optimistic concurrency ---

// TestOptimisticConcurrencyDistinguishesConflictFromNotFound is the behaviour the whole
// resolveWriteMiss follow-up query exists for. Both situations produce zero rows, and
// collapsing them would make the API answer 404 for what is really a concurrent edit â€” a
// client that retried a 404 would give up on data that is still there.
func TestOptimisticConcurrencyDistinguishesConflictFromNotFound(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)

	// Two actors read the same version.
	first, err := r.trips.GetByID(ctx, trip.ID)
	if err != nil {
		t.Fatalf("reading trip: %v", err)
	}
	second, err := r.trips.GetByID(ctx, trip.ID)
	if err != nil {
		t.Fatalf("reading trip: %v", err)
	}

	first.Name = "Winner"
	if err := r.trips.Update(ctx, first); err != nil {
		t.Fatalf("first update should succeed: %v", err)
	}
	if first.Version != 2 {
		t.Errorf("version should have advanced to 2, got %d", first.Version)
	}

	second.Name = "Loser"
	err = r.trips.Update(ctx, second)
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("second update should be a version conflict, got %v", err)
	}

	// The row must be untouched by the losing write.
	after, err := r.trips.GetByID(ctx, trip.ID)
	if err != nil {
		t.Fatalf("re-reading trip: %v", err)
	}
	if after.Name != "Winner" {
		t.Errorf("losing update must not have applied; name is %q", after.Name)
	}

	// A genuinely absent row must report not-found, not a conflict.
	missing := &domain.Trip{ID: domain.NewID(), Name: "Ghost", TimeZone: "UTC", Version: 1}
	if err := r.trips.Update(ctx, missing); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("updating a nonexistent trip should be not-found, got %v", err)
	}
}

func TestSoftDeleteHidesRowsFromReads(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)

	if err := r.trips.SoftDelete(ctx, trip.ID, time.Now().UTC(), trip.Version); err != nil {
		t.Fatalf("soft deleting: %v", err)
	}

	if _, err := r.trips.GetByID(ctx, trip.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a soft-deleted trip must read as not-found, got %v", err)
	}

	// The row is still there, so a second delete is a conflict rather than a not-found â€”
	// the version moved on. This is the tombstone behaviour Stage 2 depends on.
	err := r.trips.SoftDelete(ctx, trip.ID, time.Now().UTC(), trip.Version)
	if !errors.Is(err, domain.ErrVersionConflict) && !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("re-deleting should fail cleanly, got %v", err)
	}

	page, err := r.trips.ListForUser(ctx, owner.ID, domain.PageRequest{})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(page.Items) != 0 {
		t.Errorf("a soft-deleted trip must not appear in listings, got %d", len(page.Items))
	}
}

// --- constraint mapping ---

// TestConstraintViolationsBecomeFieldErrors proves the database's rejections reach the API
// as actionable field-level messages rather than opaque conflicts.
//
// Each subtest opens its OWN transaction. That is not tidiness: in Postgres a failed
// statement aborts the entire transaction until rollback (SQLSTATE 25P02), so a shared
// transaction would be poisoned by the first deliberate violation and every later subtest
// would fail for the wrong reason. See TestConstraintViolationInNestedTxIsRecoverable for
// what production code must do about the same rule.
func TestConstraintViolationsBecomeFieldErrors(t *testing.T) {
	r := newRepos()

	t.Run("duplicate email", func(t *testing.T) {
		ctx := txContext(t)
		existing := makeUser(t, ctx, r)
		dup := &domain.User{
			ID:           domain.NewID(),
			Email:        existing.Email,
			PasswordHash: "x",
			DisplayName:  "Impostor",
		}
		err := r.users.Create(ctx, dup)
		assertFieldViolation(t, err, "email", "email_taken")
	})

	t.Run("case-variant duplicate email", func(t *testing.T) {
		ctx := txContext(t)
		// Must collide via the lower(email) functional index, not just on exact match.
		existing := makeUser(t, ctx, r)
		dup := &domain.User{
			ID:           domain.NewID(),
			Email:        upperFirst(existing.Email),
			PasswordHash: "x",
			DisplayName:  "Impostor",
		}
		err := r.users.Create(ctx, dup)
		assertFieldViolation(t, err, "email", "email_taken")
	})

	t.Run("second owner", func(t *testing.T) {
		ctx := txContext(t)
		owner := makeUser(t, ctx, r)
		trip := makeTrip(t, ctx, r, owner)
		other := makeUser(t, ctx, r)

		err := r.members.Add(ctx, &domain.Member{
			ID: domain.NewID(), TripID: trip.ID, UserID: other.ID, Role: domain.RoleOwner,
		})
		assertFieldViolation(t, err, "role", "owner_exists")
	})

	t.Run("duplicate membership", func(t *testing.T) {
		ctx := txContext(t)
		owner := makeUser(t, ctx, r)
		trip := makeTrip(t, ctx, r, owner)

		err := r.members.Add(ctx, &domain.Member{
			ID: domain.NewID(), TripID: trip.ID, UserID: owner.ID, Role: domain.RoleEditor,
		})
		assertFieldViolation(t, err, "user_id", "already_member")
	})

	t.Run("cross-trip day reference", func(t *testing.T) {
		ctx := txContext(t)
		// The composite FK making cross-trip drift unrepresentable, seen from Go.
		owner := makeUser(t, ctx, r)
		tripA := makeTrip(t, ctx, r, owner)
		tripB := makeTrip(t, ctx, r, owner)
		dayInB := makeDay(t, ctx, r, tripB, nil)

		err := r.slots.Create(ctx, &domain.Slot{
			ID: domain.NewID(), TripID: tripA.ID, DayID: &dayInB.ID,
			Kind: domain.SlotKindPlace, Title: "Smuggled", Position: "a0",
			Status: domain.SlotStatusPlanned,
		})
		assertFieldViolation(t, err, "day_id", "day_not_in_trip")
	})
}

func assertFieldViolation(t *testing.T, err error, field, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a violation on %s/%s, got nil", field, code)
	}
	ve, ok := domain.AsValidationError(err)
	if !ok {
		t.Fatalf("expected a *domain.ValidationError, got %T: %v", err, err)
	}
	for _, v := range ve.Violations {
		if v.Field == field && v.Code == code {
			return
		}
	}
	t.Errorf("expected violation %s/%s, got %+v", field, code, ve.Violations)
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'a' && b[0] <= 'z' {
		b[0] -= 32
	}
	return string(b)
}

// TestSoftDeleteAllowsReAdd proves the partial unique indexes do their job: a removed member
// can rejoin. With plain unique indexes, a departure would burn that (trip, user) pair
// permanently.
func TestSoftDeleteAllowsReAdd(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	member := makeUser(t, ctx, r)

	add := func(role domain.Role) error {
		return r.members.Add(ctx, &domain.Member{
			ID: domain.NewID(), TripID: trip.ID, UserID: member.ID, Role: role,
		})
	}

	if err := add(domain.RoleEditor); err != nil {
		t.Fatalf("adding member: %v", err)
	}
	if err := r.members.Remove(ctx, trip.ID, member.ID, time.Now().UTC()); err != nil {
		t.Fatalf("removing member: %v", err)
	}
	if _, err := r.members.Get(ctx, trip.ID, member.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a removed member must read as not-found, got %v", err)
	}
	if err := add(domain.RoleViewer); err != nil {
		t.Fatalf("re-adding a removed member must be allowed: %v", err)
	}

	got, err := r.members.Get(ctx, trip.ID, member.ID)
	if err != nil {
		t.Fatalf("reading re-added member: %v", err)
	}
	if got.Role != domain.RoleViewer {
		t.Errorf("re-added member role = %q, want viewer", got.Role)
	}

	members, err := r.members.List(ctx, trip.ID)
	if err != nil {
		t.Fatalf("listing members: %v", err)
	}
	if len(members) != 2 {
		t.Errorf("expected owner + re-added member, got %d", len(members))
	}
	if members[0].Role != domain.RoleOwner {
		t.Errorf("owner must sort first, got %q", members[0].Role)
	}
}

// --- refresh token rotation ---

// TestRefreshTokenReuseDetection is the security-critical path. Rotation without reuse
// detection is theatre: detecting a replayed token is what turns a stolen credential into a
// revoked session instead of an open door.
func TestRefreshTokenReuseDetection(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	user := makeUser(t, ctx, r)
	now := time.Now().UTC()

	session := &domain.AuthSession{
		ID: domain.NewID(), UserID: user.ID, UserAgent: "test", ExpiresAt: now.Add(24 * time.Hour),
	}
	if err := r.sessions.CreateSession(ctx, session); err != nil {
		t.Fatalf("creating session: %v", err)
	}

	first := &domain.RefreshToken{
		ID: domain.NewID(), SessionID: session.ID,
		TokenHash: make([]byte, 32), ExpiresAt: now.Add(time.Hour),
	}
	first.TokenHash[0] = 1
	if err := r.sessions.CreateRefreshToken(ctx, first); err != nil {
		t.Fatalf("creating token: %v", err)
	}

	second := &domain.RefreshToken{
		ID: domain.NewID(), SessionID: session.ID,
		TokenHash: make([]byte, 32), ExpiresAt: now.Add(time.Hour),
	}
	second.TokenHash[0] = 2
	if err := r.sessions.CreateRefreshToken(ctx, second); err != nil {
		t.Fatalf("creating successor: %v", err)
	}

	// Normal rotation.
	if err := r.sessions.MarkRefreshTokenUsed(ctx, first.ID, now, second.ID); err != nil {
		t.Fatalf("first rotation should succeed: %v", err)
	}

	// Replay of the same token: this is the alarm.
	err := r.sessions.MarkRefreshTokenUsed(ctx, first.ID, now, domain.NewID())
	if !errors.Is(err, domain.ErrTokenConsumed) {
		t.Fatalf("replaying a used token must report ErrTokenConsumed, got %v", err)
	}

	// The chain must be walkable, which is what lets the service revoke the whole family.
	stored, err := r.sessions.GetRefreshTokenByHash(ctx, first.TokenHash)
	if err != nil {
		t.Fatalf("reading token: %v", err)
	}
	if !stored.IsUsed() {
		t.Error("the rotated token must be marked used")
	}
	if stored.ReplacedBy == nil || *stored.ReplacedBy != second.ID {
		t.Errorf("replaced_by should point at the successor, got %v", stored.ReplacedBy)
	}

	// A used token must still be FINDABLE. Filtering it out of the lookup would silently
	// downgrade a detected replay to an ordinary "unknown token" 401.
	if stored.ID != first.ID {
		t.Error("a consumed token must remain retrievable for reuse detection")
	}
}

func TestRevokeSessionPreservesOriginalReason(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	user := makeUser(t, ctx, r)
	now := time.Now().UTC()

	session := &domain.AuthSession{
		ID: domain.NewID(), UserID: user.ID, ExpiresAt: now.Add(24 * time.Hour),
	}
	if err := r.sessions.CreateSession(ctx, session); err != nil {
		t.Fatalf("creating session: %v", err)
	}

	if err := r.sessions.RevokeSession(ctx, session.ID, now, domain.RevokeReasonTokenReuse); err != nil {
		t.Fatalf("revoking: %v", err)
	}
	// A later logout must not overwrite the security-relevant reason.
	if err := r.sessions.RevokeSession(ctx, session.ID, now.Add(time.Minute), domain.RevokeReasonLogout); err != nil {
		t.Fatalf("second revoke should be a no-op, not an error: %v", err)
	}

	got, err := r.sessions.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("reading session: %v", err)
	}
	if got.RevokedReason != domain.RevokeReasonTokenReuse {
		t.Errorf("revocation reason was overwritten: got %q, want %q",
			got.RevokedReason, domain.RevokeReasonTokenReuse)
	}
	if got.IsActive(time.Now().UTC()) {
		t.Error("a revoked session must not be active")
	}
}

func TestRevokeAllSessionsForPasswordReset(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	user := makeUser(t, ctx, r)
	now := time.Now().UTC()

	for i := 0; i < 3; i++ {
		s := &domain.AuthSession{ID: domain.NewID(), UserID: user.ID, ExpiresAt: now.Add(24 * time.Hour)}
		if err := r.sessions.CreateSession(ctx, s); err != nil {
			t.Fatalf("creating session %d: %v", i, err)
		}
	}

	active, err := r.sessions.ListActiveSessions(ctx, user.ID)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(active) != 3 {
		t.Fatalf("expected 3 active sessions, got %d", len(active))
	}

	// A password reset that leaves an attacker's session alive is worse than useless.
	if err := r.sessions.RevokeAllSessions(ctx, user.ID, now, domain.RevokeReasonPasswordReset); err != nil {
		t.Fatalf("revoking all: %v", err)
	}

	active, err = r.sessions.ListActiveSessions(ctx, user.ID)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("password reset must revoke every session, %d survived", len(active))
	}
}

func TestSingleUseTokenCannotBeConsumedTwice(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	user := makeUser(t, ctx, r)
	now := time.Now().UTC()

	tok := &domain.UserToken{
		ID: domain.NewID(), UserID: user.ID,
		Purpose: domain.TokenPurposePasswordReset,
		// Distinct hash per test run so the unique index cannot collide across tests.
		TokenHash: randomHash(),
		ExpiresAt: now.Add(time.Hour),
	}
	if err := r.userTokens.Create(ctx, tok); err != nil {
		t.Fatalf("creating token: %v", err)
	}

	if err := r.userTokens.Consume(ctx, tok.ID, now); err != nil {
		t.Fatalf("first consume should succeed: %v", err)
	}
	if err := r.userTokens.Consume(ctx, tok.ID, now); !errors.Is(err, domain.ErrTokenConsumed) {
		t.Errorf("second consume must fail with ErrTokenConsumed, got %v", err)
	}
}

// --- fractional indexing against a real database ---

// TestFractionalIndexOrderingRoundTrip is the integration point between pkg/fracdex and the
// COLLATE "C" columns. The unit tests prove Go orders these keys correctly; this proves
// Postgres agrees, which is what convergence actually depends on.
func TestFractionalIndexOrderingRoundTrip(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	day := makeDay(t, ctx, r, trip, ptr(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)))

	first := makeSlot(t, ctx, r, trip, day, "first")
	second := makeSlot(t, ctx, r, trip, day, "second")
	third := makeSlot(t, ctx, r, trip, day, "third")

	assertOrder(t, ctx, r, day, []string{"first", "second", "third"})

	// Insert between first and second, the operation integer positions handle badly.
	prev, next, err := r.slots.NeighbourPositions(ctx, trip.ID, &day.ID, &first.ID)
	if err != nil {
		t.Fatalf("neighbour positions: %v", err)
	}
	if prev != first.Position || next != second.Position {
		t.Fatalf("brackets = (%q, %q), want (%q, %q)", prev, next, first.Position, second.Position)
	}

	pos, err := fracdex.KeyBetween(prev, next)
	if err != nil {
		t.Fatalf("KeyBetween: %v", err)
	}
	inserted := &domain.Slot{
		ID: domain.NewID(), TripID: trip.ID, DayID: &day.ID,
		Kind: domain.SlotKindNote, Title: "inserted", Position: pos,
		Status: domain.SlotStatusPlanned,
	}
	if err := r.slots.Create(ctx, inserted); err != nil {
		t.Fatalf("creating inserted item: %v", err)
	}

	// The crucial property: ONE row was written, and no other row's position changed.
	assertOrder(t, ctx, r, day, []string{"first", "inserted", "second", "third"})
	for _, unchanged := range []*domain.Slot{first, second, third} {
		got, err := r.slots.GetByID(ctx, unchanged.ID)
		if err != nil {
			t.Fatalf("re-reading %s: %v", unchanged.Title, err)
		}
		if got.Position != unchanged.Position {
			t.Errorf("%s moved: %q -> %q; an insert must not rewrite its neighbours",
				unchanged.Title, unchanged.Position, got.Position)
		}
		if got.Version != unchanged.Version {
			t.Errorf("%s version changed; an insert must not bump other rows", unchanged.Title)
		}
	}

	// Bracketing the last item must report an unbounded upper edge.
	prev, next, err = r.slots.NeighbourPositions(ctx, trip.ID, &day.ID, &third.ID)
	if err != nil {
		t.Fatalf("neighbour positions at end: %v", err)
	}
	if prev != third.Position || next != "" {
		t.Errorf("end brackets = (%q, %q), want (%q, \"\")", prev, next, third.Position)
	}
}

func assertOrder(t *testing.T, ctx context.Context, r repos, day *domain.Day, want []string) {
	t.Helper()
	items, err := r.slots.ListForDay(ctx, day.ID)
	if err != nil {
		t.Fatalf("listing day items: %v", err)
	}
	got := titles(items)
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}
