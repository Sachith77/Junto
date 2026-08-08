package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/junto/junto/internal/domain"
)

// Coverage of the remaining query paths. Each asserts a behaviour a service will rely on,
// rather than only proving the SQL parses.

func TestUserLookupAndUpdate(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	user := makeUser(t, ctx, r)

	t.Run("get by id", func(t *testing.T) {
		got, err := r.users.GetByID(ctx, user.ID)
		if err != nil {
			t.Fatalf("reading: %v", err)
		}
		if got.Email != user.Email || got.Version != 1 {
			t.Errorf("round trip lost data: %+v", got)
		}
	})

	t.Run("get by email is case-insensitive", func(t *testing.T) {
		// Login must work regardless of how the user typed their address. This has to agree
		// with the lower(email) unique index or duplicate accounts become possible.
		for _, variant := range []string{user.Email, strings.ToUpper(user.Email), "  " + user.Email + "  "} {
			got, err := r.users.GetByEmail(ctx, variant)
			if err != nil {
				t.Errorf("lookup of %q failed: %v", variant, err)
				continue
			}
			if got.ID != user.ID {
				t.Errorf("lookup of %q returned the wrong user", variant)
			}
		}
	})

	t.Run("unknown email is not found", func(t *testing.T) {
		_, err := r.users.GetByEmail(ctx, "nobody@example.test")
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("expected not-found, got %v", err)
		}
	})

	t.Run("verifying email", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Microsecond)
		user.EmailVerifiedAt = &now
		if err := r.users.Update(ctx, user); err != nil {
			t.Fatalf("updating: %v", err)
		}
		if user.Version != 2 {
			t.Errorf("version = %d, want 2", user.Version)
		}

		got, err := r.users.GetByID(ctx, user.ID)
		if err != nil {
			t.Fatalf("re-reading: %v", err)
		}
		if !got.IsEmailVerified() {
			t.Fatal("email should be verified")
		}
		if !got.EmailVerifiedAt.Equal(now) {
			t.Errorf("verified_at = %v, want %v", got.EmailVerifiedAt, now)
		}
	})

	t.Run("soft delete hides the user but frees nothing", func(t *testing.T) {
		email := user.Email
		if err := r.users.SoftDelete(ctx, user.ID, time.Now().UTC()); err != nil {
			t.Fatalf("soft deleting: %v", err)
		}
		if _, err := r.users.GetByID(ctx, user.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("a deleted user must read as not-found, got %v", err)
		}
		// The partial index means the address becomes available again â€” which is the
		// intended behaviour, and worth pinning so nobody "fixes" it into a full index.
		fresh := &domain.User{
			ID: domain.NewID(), Email: email, PasswordHash: "x", DisplayName: "Reused",
		}
		if err := r.users.Create(ctx, fresh); err != nil {
			t.Errorf("the address of a deleted account should be reusable: %v", err)
		}
	})
}

func TestDayCRUD(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)

	date := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	day := makeDay(t, ctx, r, trip, &date)

	got, err := r.days.GetByID(ctx, day.ID)
	if err != nil {
		t.Fatalf("reading day: %v", err)
	}
	if got.Date == nil || !got.Date.Equal(date) {
		t.Errorf("date = %v, want %v", got.Date, date)
	}

	// Relabel and reschedule.
	newDate := date.AddDate(0, 0, 3)
	got.Date = &newDate
	got.Label = "Arrival"
	if err := r.days.Update(ctx, got); err != nil {
		t.Fatalf("updating day: %v", err)
	}
	if got.Version != 2 {
		t.Errorf("version = %d, want 2", got.Version)
	}

	reread, err := r.days.GetByID(ctx, day.ID)
	if err != nil {
		t.Fatalf("re-reading: %v", err)
	}
	if reread.Label != "Arrival" || !reread.Date.Equal(newDate) {
		t.Errorf("update did not persist: %+v", reread)
	}

	// A duplicate date within the trip must be rejected by days_trip_date_uq.
	//
	// Run inside a nested WithinTx so the expected failure rolls back to a savepoint. Without
	// it, Postgres aborts the whole transaction (SQLSTATE 25P02) and every assertion after
	// this point fails for the wrong reason. This is the same constraint production code
	// lives under â€” see TestConstraintViolationInNestedTxIsRecoverable.
	clash := makeDay(t, ctx, r, trip, nil)
	clash.Date = &newDate
	clashErr := r.tx.WithinTx(ctx, func(spCtx context.Context) error {
		return r.days.Update(spCtx, clash)
	})
	if clashErr == nil {
		t.Error("two days in one trip must not share a date")
	}
	if _, ok := domain.AsValidationError(clashErr); !ok {
		t.Errorf("expected a field-level violation on date, got %T: %v", clashErr, clashErr)
	}

	// Stale version.
	stale := *reread
	stale.Version = 1
	if err := r.days.Update(ctx, &stale); !errors.Is(err, domain.ErrVersionConflict) {
		t.Errorf("stale update should conflict, got %v", err)
	}

	if err := r.days.SoftDelete(ctx, day.ID, time.Now().UTC(), reread.Version); err != nil {
		t.Fatalf("soft deleting day: %v", err)
	}
	if _, err := r.days.GetByID(ctx, day.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a deleted day must read as not-found, got %v", err)
	}

	days, err := r.days.ListForTrip(ctx, trip.ID)
	if err != nil {
		t.Fatalf("listing days: %v", err)
	}
	for _, d := range days {
		if d.ID == day.ID {
			t.Error("a soft-deleted day must not appear in listings")
		}
	}
}

func TestSlotContentUpdateAndDelete(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	day := makeDay(t, ctx, r, trip, ptr(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)))
	slot := makeSlot(t, ctx, r, trip, day, "original")

	originalPosition := slot.Position

	slot.Title = "Where are we eating"
	slot.Notes = "Book ahead"
	slot.Kind = domain.SlotKindActivity
	slot.StartTime = &domain.TimeOfDay{Hour: 10, Minute: 15}

	if err := r.slots.Update(ctx, slot); err != nil {
		t.Fatalf("updating slot: %v", err)
	}

	got, err := r.slots.GetByID(ctx, slot.ID)
	if err != nil {
		t.Fatalf("re-reading: %v", err)
	}
	if got.Title != "Where are we eating" || got.Notes != "Book ahead" || got.Kind != domain.SlotKindActivity {
		t.Errorf("content did not persist: %+v", got)
	}
	if got.StartTime == nil || got.StartTime.String() != "10:15" {
		t.Errorf("start time = %v, want 10:15", got.StartTime)
	}
	// Update must not touch placement — that is Move's job, and conflating them would make a
	// content edit look like a reorder to the Stage 2 sync engine.
	if got.Position != originalPosition {
		t.Errorf("a content update changed the position: %q -> %q", originalPosition, got.Position)
	}
	if got.DayID == nil || *got.DayID != day.ID {
		t.Error("a content update must not change the day")
	}
	// Nor may it touch coverage or the resolution; each has its own method.
	if got.Status != domain.SlotStatusPlanned {
		t.Errorf("a content update changed the status to %q", got.Status)
	}

	// ListForTrip spans all buckets.
	backlogSlot := makeSlot(t, ctx, r, trip, nil, "backlog")
	all, err := r.slots.ListForTrip(ctx, trip.ID)
	if err != nil {
		t.Fatalf("listing trip slots: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected both scheduled and backlog slots, got %v", titles(all))
	}

	// Tombstone.
	if err := r.slots.SoftDelete(ctx, backlogSlot.ID, time.Now().UTC(), backlogSlot.Version); err != nil {
		t.Fatalf("deleting slot: %v", err)
	}
	if _, err := r.slots.GetByID(ctx, backlogSlot.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a tombstoned slot must read as not-found, got %v", err)
	}
	remaining, err := r.slots.ListForTrip(ctx, trip.ID)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(remaining) != 1 {
		t.Errorf("expected 1 live slot, got %v", titles(remaining))
	}

	// Deleting with a stale version must be refused: a client that has not seen the latest
	// edit must not be able to delete over it.
	if err := r.slots.SoftDelete(ctx, slot.ID, time.Now().UTC(), 1); !errors.Is(err, domain.ErrVersionConflict) {
		t.Errorf("stale delete should conflict, got %v", err)
	}
}

// TestSlotStatusRecordsAttribution covers the Live-mode field, including the who/when that a
// bare enum would throw away.
func TestSlotStatusRecordsAttribution(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	slot := makeSlot(t, ctx, r, trip, nil, "Beach day")

	if slot.Status != domain.SlotStatusPlanned {
		t.Fatalf("a new slot starts planned, got %q", slot.Status)
	}
	if slot.StatusChangedAt != nil {
		t.Error("an untouched slot has no status change recorded")
	}

	at := time.Now().UTC().Truncate(time.Microsecond)
	if err := r.slots.SetStatus(ctx, slot.ID, domain.SlotStatusCovered, owner.ID, slot.Version, at); err != nil {
		t.Fatalf("setting status: %v", err)
	}

	got, err := r.slots.GetByID(ctx, slot.ID)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if got.Status != domain.SlotStatusCovered {
		t.Errorf("status = %q, want covered", got.Status)
	}
	if got.StatusChangedBy == nil || *got.StatusChangedBy != owner.ID {
		t.Error("status change must record who made it")
	}
	if got.StatusChangedAt == nil || !got.StatusChangedAt.Equal(at) {
		t.Errorf("status_changed_at = %v, want %v", got.StatusChangedAt, at)
	}

	// "Skipped" is a third state, distinct from "not yet".
	if err := r.slots.SetStatus(ctx, slot.ID, domain.SlotStatusSkipped, owner.ID, got.Version, at); err != nil {
		t.Fatalf("skipping: %v", err)
	}

	// Stale version.
	if err := r.slots.SetStatus(ctx, slot.ID, domain.SlotStatusPlanned, owner.ID, 1, at); !errors.Is(err, domain.ErrVersionConflict) {
		t.Errorf("a stale status change should conflict, got %v", err)
	}
}

func TestMembershipRoleManagement(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	member := makeUser(t, ctx, r)

	m := &domain.Member{
		ID: domain.NewID(), TripID: trip.ID, UserID: member.ID,
		Role: domain.RoleViewer, InvitedBy: &owner.ID,
	}
	if err := r.members.Add(ctx, m); err != nil {
		t.Fatalf("adding member: %v", err)
	}

	if n, err := r.members.CountByRole(ctx, trip.ID, domain.RoleOwner); err != nil || n != 1 {
		t.Errorf("owner count = %d (err %v), want 1", n, err)
	}
	if n, err := r.members.CountByRole(ctx, trip.ID, domain.RoleViewer); err != nil || n != 1 {
		t.Errorf("viewer count = %d (err %v), want 1", n, err)
	}
	if n, err := r.members.CountByRole(ctx, trip.ID, domain.RoleEditor); err != nil || n != 0 {
		t.Errorf("editor count = %d (err %v), want 0", n, err)
	}

	// Promotion. A viewer who may vote is exactly the case that motivates the eventual
	// capability migration; today it requires a role change.
	m.Role = domain.RoleEditor
	if err := r.members.UpdateRole(ctx, m); err != nil {
		t.Fatalf("promoting: %v", err)
	}
	if m.Version != 2 {
		t.Errorf("version = %d, want 2", m.Version)
	}

	got, err := r.members.Get(ctx, trip.ID, member.ID)
	if err != nil {
		t.Fatalf("reading member: %v", err)
	}
	if got.Role != domain.RoleEditor {
		t.Errorf("role = %q, want editor", got.Role)
	}
	if got.InvitedBy == nil || *got.InvitedBy != owner.ID {
		t.Error("invited_by should be preserved")
	}
	if !got.Actor().Can(domain.CapEditSlots) {
		t.Error("a promoted editor should be able to edit items")
	}

	// Stale role update.
	stale := *got
	stale.Version = 1
	if err := r.members.UpdateRole(ctx, &stale); !errors.Is(err, domain.ErrVersionConflict) {
		t.Errorf("stale role update should conflict, got %v", err)
	}

	// Updating a member of a trip they do not belong to is not-found, not a conflict.
	ghost := &domain.Member{
		TripID: trip.ID, UserID: domain.NewID(), Role: domain.RoleViewer, Version: 1,
	}
	if err := r.members.UpdateRole(ctx, ghost); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("updating a non-member should be not-found, got %v", err)
	}

	// Removing someone who is not a member.
	if err := r.members.Remove(ctx, trip.ID, domain.NewID(), time.Now().UTC()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("removing a non-member should be not-found, got %v", err)
	}
}

func TestInvitationListingAndRevocation(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	now := time.Now().UTC()

	// A targeted email invite and an unlimited share link: the two modes one table covers.
	email := "invitee@example.test"
	targeted := &domain.Invitation{
		ID: domain.NewID(), TripID: trip.ID, Email: &email, Role: domain.RoleEditor,
		TokenHash: randomHash(), CreatedBy: owner.ID, MaxUses: ptr(1),
		ExpiresAt: now.Add(24 * time.Hour),
	}
	link := &domain.Invitation{
		ID: domain.NewID(), TripID: trip.ID, Role: domain.RoleViewer,
		TokenHash: randomHash(), CreatedBy: owner.ID,
		ExpiresAt: now.Add(24 * time.Hour),
	}
	for _, inv := range []*domain.Invitation{targeted, link} {
		if err := r.invitations.Create(ctx, inv); err != nil {
			t.Fatalf("creating invitation: %v", err)
		}
	}

	listed, err := r.invitations.ListForTrip(ctx, trip.ID)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected 2 invitations, got %d", len(listed))
	}

	stored, err := r.invitations.GetByHash(ctx, link.TokenHash)
	if err != nil {
		t.Fatalf("reading link invite: %v", err)
	}
	if !stored.IsLinkInvite() {
		t.Error("an invitation with no email is a link invite")
	}
	if stored.MaxUses != nil {
		t.Error("a link invite should have no use limit")
	}

	// An unlimited link stays redeemable after many uses.
	for i := 0; i < 5; i++ {
		if err := r.invitations.IncrementUseCount(ctx, link.ID); err != nil {
			t.Fatalf("redemption %d: %v", i, err)
		}
	}
	stored, err = r.invitations.GetByHash(ctx, link.TokenHash)
	if err != nil {
		t.Fatalf("re-reading: %v", err)
	}
	if stored.UseCount != 5 || !stored.IsRedeemable(now) {
		t.Errorf("unlimited link should still be redeemable after 5 uses, got count=%d", stored.UseCount)
	}

	// Revocation ends it immediately, mid-life.
	if err := r.invitations.Revoke(ctx, link.ID, now); err != nil {
		t.Fatalf("revoking: %v", err)
	}
	if err := r.invitations.IncrementUseCount(ctx, link.ID); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Errorf("a revoked invitation must not be redeemable, got %v", err)
	}
	// Revoking twice is not an error the caller can act on, but there is nothing live left.
	if err := r.invitations.Revoke(ctx, link.ID, now); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("re-revoking should report nothing to do, got %v", err)
	}

	listed, err = r.invitations.ListForTrip(ctx, trip.ID)
	if err != nil {
		t.Fatalf("listing after revoke: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != targeted.ID {
		t.Errorf("revoked invitations must not be listed, got %d", len(listed))
	}

	// Expiry is enforced by the same atomic statement as everything else.
	expired := &domain.Invitation{
		ID: domain.NewID(), TripID: trip.ID, Role: domain.RoleViewer,
		TokenHash: randomHash(), CreatedBy: owner.ID,
		ExpiresAt: now.Add(-time.Minute),
	}
	if err := r.invitations.Create(ctx, expired); err != nil {
		t.Fatalf("creating expired invitation: %v", err)
	}
	if err := r.invitations.IncrementUseCount(ctx, expired.ID); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Errorf("an expired invitation must not be redeemable, got %v", err)
	}
}

func TestUserTokenPurposeIsolationAndCleanup(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	user := makeUser(t, ctx, r)
	now := time.Now().UTC()

	verify := &domain.UserToken{
		ID: domain.NewID(), UserID: user.ID, Purpose: domain.TokenPurposeEmailVerify,
		TokenHash: randomHash(), ExpiresAt: now.Add(24 * time.Hour),
	}
	reset := &domain.UserToken{
		ID: domain.NewID(), UserID: user.ID, Purpose: domain.TokenPurposePasswordReset,
		TokenHash: randomHash(), ExpiresAt: now.Add(time.Hour),
	}
	for _, tok := range []*domain.UserToken{verify, reset} {
		if err := r.userTokens.Create(ctx, tok); err != nil {
			t.Fatalf("creating token: %v", err)
		}
	}

	got, err := r.userTokens.GetByHash(ctx, reset.TokenHash)
	if err != nil {
		t.Fatalf("reading token: %v", err)
	}
	if got.Purpose != domain.TokenPurposePasswordReset || !got.IsUsable(now) {
		t.Errorf("unexpected token state: %+v", got)
	}

	// Issuing a new reset link must retire outstanding ones, or every link ever mailed
	// stays live until expiry â€” turning an old inbox into a standing account takeover.
	if err := r.userTokens.ConsumeAllForPurpose(ctx, user.ID, domain.TokenPurposePasswordReset, now); err != nil {
		t.Fatalf("retiring reset tokens: %v", err)
	}

	got, err = r.userTokens.GetByHash(ctx, reset.TokenHash)
	if err != nil {
		t.Fatalf("re-reading reset token: %v", err)
	}
	if got.IsUsable(now) {
		t.Error("the superseded reset token must no longer be usable")
	}

	// The verification token must be untouched: retiring one purpose must not invalidate
	// an unrelated flow the user is in the middle of.
	stillGood, err := r.userTokens.GetByHash(ctx, verify.TokenHash)
	if err != nil {
		t.Fatalf("reading verification token: %v", err)
	}
	if !stillGood.IsUsable(now) {
		t.Error("retiring password-reset tokens must not affect email verification")
	}

	// Expired-token cleanup.
	stale := &domain.UserToken{
		ID: domain.NewID(), UserID: user.ID, Purpose: domain.TokenPurposeEmailVerify,
		TokenHash: randomHash(), ExpiresAt: now.Add(-48 * time.Hour),
	}
	if err := r.userTokens.Create(ctx, stale); err != nil {
		t.Fatalf("creating stale token: %v", err)
	}
	n, err := r.userTokens.DeleteExpired(ctx, now)
	if err != nil {
		t.Fatalf("purging: %v", err)
	}
	if n != 1 {
		t.Errorf("purged %d tokens, want 1", n)
	}
	if _, err := r.userTokens.GetByHash(ctx, stale.TokenHash); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("the purged token should be gone, got %v", err)
	}
}

func TestSessionTouchAndExpiryCleanup(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	user := makeUser(t, ctx, r)
	now := time.Now().UTC()

	live := &domain.AuthSession{
		ID: domain.NewID(), UserID: user.ID, UserAgent: "test", ExpiresAt: now.Add(24 * time.Hour),
	}
	if err := r.sessions.CreateSession(ctx, live); err != nil {
		t.Fatalf("creating session: %v", err)
	}

	later := now.Add(time.Hour).Truncate(time.Microsecond)
	if err := r.sessions.TouchSession(ctx, live.ID, later); err != nil {
		t.Fatalf("touching: %v", err)
	}
	got, err := r.sessions.GetSession(ctx, live.ID)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if !got.LastUsedAt.Equal(later) {
		t.Errorf("last_used_at = %v, want %v", got.LastUsedAt, later)
	}

	// Touching a revoked session is a benign race, not an error: the request that triggered
	// it will fail its own authorization check anyway.
	if err := r.sessions.RevokeSession(ctx, live.ID, now, domain.RevokeReasonLogout); err != nil {
		t.Fatalf("revoking: %v", err)
	}
	if err := r.sessions.TouchSession(ctx, live.ID, later.Add(time.Minute)); err != nil {
		t.Errorf("touching a revoked session should be a no-op, not an error: %v", err)
	}

	// Expired refresh tokens are purged; live ones survive.
	expiredTok := &domain.RefreshToken{
		ID: domain.NewID(), SessionID: live.ID, TokenHash: randomHash(),
		ExpiresAt: now.Add(-time.Hour),
	}
	liveTok := &domain.RefreshToken{
		ID: domain.NewID(), SessionID: live.ID, TokenHash: randomHash(),
		ExpiresAt: now.Add(time.Hour),
	}
	for _, tok := range []*domain.RefreshToken{expiredTok, liveTok} {
		if err := r.sessions.CreateRefreshToken(ctx, tok); err != nil {
			t.Fatalf("creating token: %v", err)
		}
	}

	n, err := r.sessions.DeleteExpiredRefreshTokens(ctx, now)
	if err != nil {
		t.Fatalf("purging tokens: %v", err)
	}
	if n != 1 {
		t.Errorf("purged %d refresh tokens, want 1", n)
	}
	if _, err := r.sessions.GetRefreshTokenByHash(ctx, liveTok.TokenHash); err != nil {
		t.Errorf("a live token must survive the purge: %v", err)
	}

	deadSession := &domain.AuthSession{
		ID: domain.NewID(), UserID: user.ID, ExpiresAt: now.Add(-time.Hour),
	}
	if err := r.sessions.CreateSession(ctx, deadSession); err != nil {
		t.Fatalf("creating expired session: %v", err)
	}
	n, err = r.sessions.DeleteExpiredSessions(ctx, now)
	if err != nil {
		t.Fatalf("purging sessions: %v", err)
	}
	if n != 1 {
		t.Errorf("purged %d sessions, want 1", n)
	}
}
