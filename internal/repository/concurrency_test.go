package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/junto/junto/internal/domain"
)

// Tests that need REAL concurrency: separate connections, separate transactions, genuinely
// racing.
//
// These cannot use txContext. Two goroutines sharing one pgx transaction are serialised by
// the driver, so the very race being tested would not occur â€” the test would pass while
// proving nothing. They commit instead, and clean up after themselves.

// TestConcurrentInvitationRedemptionIsAtomic is the headline concurrency guarantee of this
// layer. A single-use invite link redeemed by two people at the same instant must admit
// exactly one.
//
// The read-then-write implementation everyone writes first â€” check redeemable, then
// increment â€” fails this: both requests observe use_count = 0, both conclude the link is
// valid, and two people join on a one-use invite. Putting every condition inside the same
// UPDATE is what closes it.
func TestConcurrentInvitationRedemptionIsAtomic(t *testing.T) {
	ctx := concurrentCtx(t)
	r := newRepos()

	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	t.Cleanup(func() { cleanupTrip(t, r, trip.ID, owner.ID) })

	inv := &domain.Invitation{
		ID:        domain.NewID(),
		TripID:    trip.ID,
		Role:      domain.RoleEditor,
		TokenHash: randomHash(),
		CreatedBy: owner.ID,
		MaxUses:   ptr(1), // single use: exactly one redemption may succeed
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := r.invitations.Create(ctx, inv); err != nil {
		t.Fatalf("creating invitation: %v", err)
	}

	const racers = 12
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		successes int
		rejects   int
		other     []error
	)

	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release all goroutines at once to maximise the overlap
			err := r.invitations.IncrementUseCount(ctx, inv.ID)

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
			case errors.Is(err, domain.ErrTokenInvalid):
				rejects++
			default:
				other = append(other, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	for _, err := range other {
		t.Errorf("unexpected error from a racer: %v", err)
	}
	if successes != 1 {
		t.Fatalf("exactly one redemption must succeed, got %d successes and %d rejections",
			successes, rejects)
	}
	if rejects != racers-1 {
		t.Errorf("expected %d rejections, got %d", racers-1, rejects)
	}

	// And the stored count must match reality, not just the tally of return values.
	stored, err := r.invitations.GetByHash(ctx, inv.TokenHash)
	if err != nil {
		t.Fatalf("reading invitation: %v", err)
	}
	if stored.UseCount != 1 {
		t.Errorf("use_count = %d, want 1 â€” the counter must never exceed max_uses", stored.UseCount)
	}
	if stored.IsRedeemable(time.Now().UTC()) {
		t.Error("an exhausted single-use invitation must no longer be redeemable")
	}
}

// TestConcurrentOptimisticUpdatesSerialise proves the version column actually prevents lost
// updates across real transactions, not just within one.
func TestConcurrentOptimisticUpdatesSerialise(t *testing.T) {
	ctx := concurrentCtx(t)
	r := newRepos()

	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	t.Cleanup(func() { cleanupTrip(t, r, trip.ID, owner.ID) })

	const racers = 8
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		successes int
		conflicts int
		other     []error
	)

	// Every racer reads the SAME version, then tries to write. Only one can win; the rest
	// must be told so rather than silently overwriting each other.
	base, err := r.trips.GetByID(ctx, trip.ID)
	if err != nil {
		t.Fatalf("reading trip: %v", err)
	}

	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start

			candidate := *base // each racer holds its own copy at the same version
			candidate.Name = "racer"
			err := r.trips.Update(ctx, &candidate)

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
			case errors.Is(err, domain.ErrVersionConflict):
				conflicts++
			default:
				other = append(other, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	for _, err := range other {
		t.Errorf("unexpected error from a racer: %v", err)
	}
	if successes != 1 {
		t.Errorf("exactly one concurrent update may succeed, got %d", successes)
	}
	if conflicts != racers-1 {
		t.Errorf("expected %d version conflicts, got %d", racers-1, conflicts)
	}

	final, err := r.trips.GetByID(ctx, trip.ID)
	if err != nil {
		t.Fatalf("reading final trip: %v", err)
	}
	// The version must advance exactly once. Anything else means a lost update.
	if final.Version != base.Version+1 {
		t.Errorf("version = %d, want %d â€” a lost update occurred", final.Version, base.Version+1)
	}
}

// TestTxManagerRollsBackOnError proves the transaction boundary is real.
func TestTxManagerRollsBackOnError(t *testing.T) {
	ctx := concurrentCtx(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	t.Cleanup(func() { deleteUser(t, r, owner.ID) })

	sentinel := errors.New("deliberate failure")
	var tripID domain.ID

	err := r.tx.WithinTx(ctx, func(txCtx context.Context) error {
		trip := &domain.Trip{ID: domain.NewID(), Name: "Doomed", TimeZone: "UTC"}
		if err := r.trips.Create(txCtx, trip); err != nil {
			return err
		}
		tripID = trip.ID

		// Visible inside the transaction...
		if _, err := r.trips.GetByID(txCtx, trip.ID); err != nil {
			t.Errorf("the trip should be visible inside its own transaction: %v", err)
		}
		return sentinel
	})

	if !errors.Is(err, sentinel) {
		t.Fatalf("WithinTx must return the callback's error unchanged, got %v", err)
	}
	// ...and gone afterwards.
	if _, err := r.trips.GetByID(ctx, tripID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a rolled-back trip must not exist, got %v", err)
	}
}

// TestTxManagerNestingUsesSavepoints proves nested WithinTx gives an inner rollback boundary
// rather than destroying the caller's work or silently committing alongside it.
func TestTxManagerNestingUsesSavepoints(t *testing.T) {
	ctx := concurrentCtx(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	t.Cleanup(func() { deleteUser(t, r, owner.ID) })

	var outerID, innerID domain.ID

	err := r.tx.WithinTx(ctx, func(outerCtx context.Context) error {
		outer := &domain.Trip{ID: domain.NewID(), Name: "Outer", TimeZone: "UTC"}
		if err := r.trips.Create(outerCtx, outer); err != nil {
			return err
		}
		outerID = outer.ID

		// The inner unit fails. Only its own work should unwind.
		innerErr := r.tx.WithinTx(outerCtx, func(innerCtx context.Context) error {
			inner := &domain.Trip{ID: domain.NewID(), Name: "Inner", TimeZone: "UTC"}
			if err := r.trips.Create(innerCtx, inner); err != nil {
				return err
			}
			innerID = inner.ID
			return errors.New("inner failed")
		})
		if innerErr == nil {
			t.Error("the inner transaction should have returned its error")
		}

		// The outer transaction must still be usable. Without savepoints, the failed inner
		// work would have aborted the whole transaction and this read would error.
		if _, err := r.trips.GetByID(outerCtx, outer.ID); err != nil {
			t.Errorf("outer work must survive an inner failure: %v", err)
		}
		return nil // outer commits
	})
	if err != nil {
		t.Fatalf("outer transaction should commit: %v", err)
	}
	t.Cleanup(func() { deleteTrip(t, r, outerID) })

	if _, err := r.trips.GetByID(ctx, outerID); err != nil {
		t.Errorf("outer trip should have been committed: %v", err)
	}
	if _, err := r.trips.GetByID(ctx, innerID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("inner trip should have been rolled back to the savepoint, got %v", err)
	}
}

// TestConstraintViolationInNestedTxIsRecoverable pins a Postgres rule that bites services,
// not just tests.
//
// Once ANY statement fails inside a transaction, Postgres aborts the whole thing: every
// subsequent command returns "current transaction is aborted" (SQLSTATE 25P02) until
// rollback. So a service that does the natural thing â€”
//
//	WithinTx: try to add a member; if already_member, update their role instead
//
// would find the transaction dead at the point it tried to recover, and the friendly
// field-level error from mapError would be useless.
//
// A savepoint is the only way to recover, and nested WithinTx opens one. This test exists so
// that if the nesting behaviour is ever "simplified" to reuse the outer transaction
// directly, the resulting breakage shows up here rather than in production error handling.
func TestConstraintViolationInNestedTxIsRecoverable(t *testing.T) {
	ctx := concurrentCtx(t)
	r := newRepos()

	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	t.Cleanup(func() { cleanupTrip(t, r, trip.ID, owner.ID) })

	joiner := makeUser(t, ctx, r)
	t.Cleanup(func() { deleteUser(t, r, joiner.ID) })

	err := r.tx.WithinTx(ctx, func(txCtx context.Context) error {
		if err := r.members.Add(txCtx, &domain.Member{
			ID: domain.NewID(), TripID: trip.ID, UserID: joiner.ID, Role: domain.RoleViewer,
		}); err != nil {
			return err
		}

		// Deliberately violate trip_members_uq inside a savepoint.
		addErr := r.tx.WithinTx(txCtx, func(spCtx context.Context) error {
			return r.members.Add(spCtx, &domain.Member{
				ID: domain.NewID(), TripID: trip.ID, UserID: joiner.ID, Role: domain.RoleEditor,
			})
		})
		if addErr == nil {
			t.Error("adding a duplicate member should have failed")
		}
		if _, ok := domain.AsValidationError(addErr); !ok {
			t.Errorf("expected a field-level violation, got %T: %v", addErr, addErr)
		}

		// The savepoint rolled back, so the OUTER transaction is still usable and can carry
		// out the fallback. Without the savepoint this read fails with 25P02.
		existing, err := r.members.Get(txCtx, trip.ID, joiner.ID)
		if err != nil {
			return err
		}
		existing.Role = domain.RoleEditor
		return r.members.UpdateRole(txCtx, existing)
	})
	if err != nil {
		t.Fatalf("the recovering transaction should commit: %v", err)
	}

	got, err := r.members.Get(ctx, trip.ID, joiner.ID)
	if err != nil {
		t.Fatalf("reading member: %v", err)
	}
	if got.Role != domain.RoleEditor {
		t.Errorf("role = %q, want editor: the fallback path should have committed", got.Role)
	}
}

// TestKeysetPaginationIsStableUnderConcurrentInserts is the concrete justification for
// rejecting OFFSET. With OFFSET, an insert that lands before the current page shifts every
// subsequent page by one, so a client walking the list silently re-reads one row and skips
// another. A cursor anchored to a real row cannot do that.
func TestKeysetPaginationIsStableUnderConcurrentInserts(t *testing.T) {
	ctx := concurrentCtx(t)
	r := newRepos()

	user := makeUser(t, ctx, r)
	t.Cleanup(func() { deleteUser(t, r, user.ID) })

	const total = 10
	created := make([]domain.ID, 0, total)
	for i := 0; i < total; i++ {
		trip := makeTrip(t, ctx, r, user)
		created = append(created, trip.ID)
		// Distinct created_at values keep the expected order unambiguous; the id tiebreak
		// is exercised separately by the fracdex tests.
		time.Sleep(2 * time.Millisecond)
	}
	t.Cleanup(func() {
		for _, id := range created {
			deleteTripCascade(t, r, id)
		}
	})

	// Page 1.
	first, err := r.trips.ListForUser(ctx, user.ID, domain.PageRequest{Limit: 4})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Items) != 4 || !first.HasMore {
		t.Fatalf("first page = %d items, hasMore=%v; want 4 and true", len(first.Items), first.HasMore)
	}

	// Someone inserts a NEWER trip between page reads. With OFFSET this is exactly what
	// causes a duplicate on page 2.
	intruder := makeTrip(t, ctx, r, user)
	t.Cleanup(func() { deleteTripCascade(t, r, intruder.ID) })

	second, err := r.trips.ListForUser(ctx, user.ID, domain.PageRequest{Limit: 4, After: first.NextCursor})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}

	seen := map[domain.ID]bool{}
	for _, tr := range first.Items {
		seen[tr.ID] = true
	}
	for _, tr := range second.Items {
		if seen[tr.ID] {
			t.Errorf("trip %s appeared on both pages: the cursor did not hold its position", tr.ID)
		}
		seen[tr.ID] = true
		if tr.ID == intruder.ID {
			t.Error("a trip created after the first page must not appear on the second")
		}
	}

	// Walking to exhaustion must yield each trip exactly once.
	cursor := second.NextCursor
	for second.HasMore {
		second, err = r.trips.ListForUser(ctx, user.ID, domain.PageRequest{Limit: 4, After: cursor})
		if err != nil {
			t.Fatalf("paging: %v", err)
		}
		for _, tr := range second.Items {
			if seen[tr.ID] && tr.ID != intruder.ID {
				t.Errorf("trip %s was returned twice while paging", tr.ID)
			}
			seen[tr.ID] = true
		}
		cursor = second.NextCursor
	}
	for _, id := range created {
		if !seen[id] {
			t.Errorf("trip %s was skipped entirely while paging", id)
		}
	}
}

// --- cleanup helpers for the committing tests ---

func cleanupTrip(t *testing.T, r repos, tripID, userID domain.ID) {
	t.Helper()
	deleteTripCascade(t, r, tripID)
	deleteUser(t, r, userID)
}

func deleteTripCascade(t *testing.T, r repos, tripID domain.ID) {
	t.Helper()
	// Hard delete: these tests committed, so soft deletion would leave rows behind for the
	// next run. FK cascades take days, items, members and invitations with it.
	if _, err := testPool.Exec(context.Background(), `DELETE FROM trips WHERE id = $1`, tripID); err != nil {
		t.Errorf("cleaning up trip %s: %v", tripID, err)
	}
}

func deleteTrip(t *testing.T, r repos, tripID domain.ID) {
	t.Helper()
	deleteTripCascade(t, r, tripID)
}

func deleteUser(t *testing.T, r repos, userID domain.ID) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID); err != nil {
		t.Errorf("cleaning up user %s: %v", userID, err)
	}
}
