package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/junto/junto/internal/domain"
)

// Budget repository tests.
//
// The behaviour worth testing here is almost entirely the database's, which is why this layer
// is tested against a real Postgres and not a mock (D26). Specifically: a DEFERRED constraint
// trigger, whose entire reason for being deferred is that the split set is legitimately
// inconsistent in the middle of a rewrite. A mock cannot have a commit, so it cannot have a
// deferred anything, and a test against one would pass no matter what this code did.
//
// Note the split between the two context helpers below, because it decides what each test can
// prove. txContext wraps a test in a transaction that is rolled back and never commits — so a
// deferred trigger NEVER FIRES under it. Any test whose subject is the trigger must therefore
// use a committing transaction, and the ones here that do are labelled with why.

func makeBudgetEntry(t *testing.T, ctx context.Context, r repos, trip *domain.Trip, label string, amount int64, splits []domain.BudgetSplit) *domain.BudgetEntry {
	t.Helper()
	e := &domain.BudgetEntry{
		ID:          domain.NewID(),
		TripID:      trip.ID,
		Label:       label,
		Category:    domain.BudgetCategoryOther,
		AmountMinor: amount,
		Splits:      splits,
	}
	if err := r.budget.Save(ctx, e); err != nil {
		t.Fatalf("saving budget entry %q: %v", label, err)
	}
	return e
}

// TestBudgetEntryRoundTripsWithItsSplits is the baseline: an entry and its split set are read
// back as one object, in the canonical order the operation log also uses.
func TestBudgetEntryRoundTripsWithItsSplits(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	other := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)

	entry := makeBudgetEntry(t, ctx, r, trip, "Beach house", 45000, []domain.BudgetSplit{
		{ID: domain.NewID(), UserID: owner.ID, AmountMinor: 20000},
		{ID: domain.NewID(), UserID: other.ID, AmountMinor: 25000},
	})
	if entry.Version != 1 {
		t.Errorf("a newly saved entry is at version %d, want 1", entry.Version)
	}

	fresh, err := r.budget.GetByID(ctx, entry.ID)
	if err != nil {
		t.Fatalf("reading the entry back: %v", err)
	}
	if len(fresh.Splits) != 2 {
		t.Fatalf("read back %d splits, want 2", len(fresh.Splits))
	}
	if fresh.SplitTotal() != fresh.AmountMinor {
		t.Errorf("splits total %d, entry total %d", fresh.SplitTotal(), fresh.AmountMinor)
	}
	// Ordered by user id, matching domain.splitValues. If these two ever disagree, every
	// fold(log) == database assertion silently becomes order-sensitive.
	if fresh.Splits[0].UserID.String() > fresh.Splits[1].UserID.String() {
		t.Errorf("splits came back unordered: %s then %s",
			fresh.Splits[0].UserID, fresh.Splits[1].UserID)
	}
}

// TestSaveOutsideATransactionIsRefused pins the atomicity enforcement.
//
// Save issues several statements, and outside a transaction each commits alone: the DELETE
// would commit an entry with no splits (a legal state the trigger permits), and a crash before
// the reinserts would leave a ledger that is permanently, silently wrong. There is no
// constraint that can catch that, because every individual state it passes through is valid —
// so the only defence is refusing to start.
//
// # Why this test commits its fixtures, and asserts on the exact sentinel
//
// The first version of this test did neither, and PASSED WITH THE GUARD REMOVED. It built its
// trip under txContext — uncommitted, so invisible to any other connection — and then called
// Save on a background context. Save was refused, but by a foreign-key violation on trip_id
// from a connection that could not see the trip, not by the guard at all. The assertion was
// `err != nil`, which both explanations satisfy.
//
// It is fixed here in the two ways that make the pass mean something: the fixtures are
// committed, so the trip genuinely exists for the pool connection and the FK cannot fire; and
// the assertion names errSaveNeedsTx specifically, so no other error can be mistaken for it.
func TestSaveOutsideATransactionIsRefused(t *testing.T) {
	ctx := concurrentCtx(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	t.Cleanup(func() { cleanupTrip(t, r, trip.ID, owner.ID) })

	// A bare background context is what a caller who forgot to open a transaction would pass.
	err := r.budget.Save(context.Background(), &domain.BudgetEntry{
		ID: domain.NewID(), TripID: trip.ID, Label: "Ad hoc", AmountMinor: 100,
		Category: domain.BudgetCategoryOther,
	})
	if !errors.Is(err, errSaveNeedsTx) {
		t.Fatalf("Save outside a transaction returned %v, want errSaveNeedsTx — a partial "+
			"split rewrite can now commit", err)
	}
}

// TestSplitRewriteShrinksTheMemberSet is the case that makes the trigger's DEFERRAL necessary
// rather than stylistic.
//
// Rewriting a split set means deleting every row and reinserting, so the intermediate state has
// splits summing to zero against a non-zero entry total. Under an IMMEDIATE constraint that
// intermediate state is a violation and the rewrite is impossible; deferring to COMMIT is what
// makes "the entry and its complete split set are one atomic unit" expressible in SQL at all.
//
// It commits for real, because a deferred trigger has nothing to fire at otherwise.
func TestSplitRewriteShrinksTheMemberSet(t *testing.T) {
	ctx := concurrentCtx(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	other := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	t.Cleanup(func() {
		cleanupTrip(t, r, trip.ID, owner.ID)
		deleteUser(t, r, other.ID)
	})

	var entry *domain.BudgetEntry
	if err := r.tx.WithinTx(ctx, func(ctx context.Context) error {
		entry = makeBudgetEntry(t, ctx, r, trip, "Split three ways then two", 900, []domain.BudgetSplit{
			{ID: domain.NewID(), UserID: owner.ID, AmountMinor: 450},
			{ID: domain.NewID(), UserID: other.ID, AmountMinor: 450},
		})
		return nil
	}); err != nil {
		t.Fatalf("creating the entry: %v", err)
	}

	// The whole cost moves onto one person. The other member's row must be gone, not merely
	// zeroed — and at no point between the DELETE and the INSERT is the sum correct.
	if err := r.tx.WithinTx(ctx, func(ctx context.Context) error {
		entry.Splits = []domain.BudgetSplit{{ID: domain.NewID(), UserID: owner.ID, AmountMinor: 900}}
		return r.budget.Save(ctx, entry)
	}); err != nil {
		t.Fatalf("rewriting the split set: %v", err)
	}

	fresh, err := r.budget.GetByID(ctx, entry.ID)
	if err != nil {
		t.Fatalf("reading the entry back: %v", err)
	}
	if len(fresh.Splits) != 1 {
		t.Fatalf("entry has %d splits after the rewrite, want 1", len(fresh.Splits))
	}
	if fresh.Splits[0].UserID != owner.ID || fresh.Splits[0].AmountMinor != 900 {
		t.Errorf("split rewrote to %+v", fresh.Splits[0])
	}
	if fresh.Version != 2 {
		t.Errorf("version = %d after one update, want 2", fresh.Version)
	}
}

// TestSplitsThatDoNotSumAreRejectedAtCommit is the database-level backstop from CLAUDE.md,
// tested rather than asserted.
//
// The sync engine is responsible for applying budget writes whole, but "responsible for" is not
// "incapable of getting wrong" — and the point of the trigger is that a violating ledger cannot
// be committed by ANY writer, including a future one that bypasses the service layer entirely.
// So this test writes the bad state through the repository and expects COMMIT to refuse it.
func TestSplitsThatDoNotSumAreRejectedAtCommit(t *testing.T) {
	ctx := concurrentCtx(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	other := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	t.Cleanup(func() {
		cleanupTrip(t, r, trip.ID, owner.ID)
		deleteUser(t, r, other.ID)
	})

	entryID := domain.NewID()
	err := r.tx.WithinTx(ctx, func(ctx context.Context) error {
		// 600 + 600 against a total of 1000. Each statement individually is fine; only the
		// commit can see that the set is wrong.
		return r.budget.Save(ctx, &domain.BudgetEntry{
			ID: entryID, TripID: trip.ID, Label: "Does not add up",
			Category: domain.BudgetCategoryFood, AmountMinor: 1000,
			Splits: []domain.BudgetSplit{
				{ID: domain.NewID(), UserID: owner.ID, AmountMinor: 600},
				{ID: domain.NewID(), UserID: other.ID, AmountMinor: 600},
			},
		})
	})
	if err == nil {
		t.Fatal("a split set that does not sum to the entry total was committed; " +
			"the deferred trigger is not protecting the invariant")
	}

	// And nothing survived: the whole transaction rolled back, so there is no half-written
	// entry to confuse a later reader.
	if _, err := r.budget.GetByID(ctx, entryID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("the rejected entry is still readable (err = %v)", err)
	}
}

// TestChangingTheTotalWithoutTheSplitsIsRejected covers the same invariant from the other side
// — the second trigger in migration 000003.
//
// It is the exact failure D44 describes: A sets the total, B sets their split, each write is
// locally plausible and the pair is wrong. Here the service layer is bypassed entirely, which
// is the point: the total-mask rule in the domain makes this unreachable through the sync
// engine, and this trigger makes it unreachable through anything else.
func TestChangingTheTotalWithoutTheSplitsIsRejected(t *testing.T) {
	ctx := concurrentCtx(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	t.Cleanup(func() { cleanupTrip(t, r, trip.ID, owner.ID) })

	var entry *domain.BudgetEntry
	if err := r.tx.WithinTx(ctx, func(ctx context.Context) error {
		entry = makeBudgetEntry(t, ctx, r, trip, "Dinner", 5000, []domain.BudgetSplit{
			{ID: domain.NewID(), UserID: owner.ID, AmountMinor: 5000},
		})
		return nil
	}); err != nil {
		t.Fatalf("creating the entry: %v", err)
	}

	// Issued straight at the pool, deliberately: the point is that a writer bypassing every
	// layer of this application still cannot commit an entry whose total disagrees with its
	// splits. Wrapping it in the TxManager would only obscure that.
	_, err := testPool.Exec(ctx,
		`UPDATE budget_entries SET amount_minor = 9999, version = version + 1 WHERE id = $1`,
		entry.ID)
	if err == nil {
		t.Fatal("an entry total was changed out from under its splits; " +
			"budget_entry_total_check is not protecting the invariant")
	}
}

// TestBudgetUpdateIsOptimistic — a stale version is a conflict, not a silent overwrite. For the
// budget this is the ONLY conflict behaviour: there is no merge path to fall back to (D85).
func TestBudgetUpdateIsOptimistic(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)

	entry := makeBudgetEntry(t, ctx, r, trip, "Taxi", 1200, nil)

	stale := *entry
	stale.Version = entry.Version // fresh, will succeed
	stale.AmountMinor = 1500
	if err := r.budget.Save(ctx, &stale); err != nil {
		t.Fatalf("the first update should have succeeded: %v", err)
	}

	// Now replay the ORIGINAL version, as a second client holding pre-update state would.
	replay := *entry
	replay.AmountMinor = 9000
	err := r.budget.Save(ctx, &replay)
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("a stale budget write returned %v, want ErrVersionConflict", err)
	}
}

// TestBudgetWriteMissDistinguishesMissingFromStale is D22 applied to the ledger: "row absent"
// and "row at another version" both produce zero rows, and answering 404 for a concurrent edit
// would make a retrying client abandon data that is still there.
func TestBudgetWriteMissDistinguishesMissingFromStale(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)

	absent := &domain.BudgetEntry{
		ID: domain.NewID(), TripID: trip.ID, Label: "Never existed",
		Category: domain.BudgetCategoryOther, AmountMinor: 10, Version: 4,
	}
	if err := r.budget.Save(ctx, absent); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("updating an absent entry returned %v, want ErrNotFound", err)
	}

	entry := makeBudgetEntry(t, ctx, r, trip, "Real", 10, nil)
	entry.Version = 99
	if err := r.budget.Save(ctx, entry); !errors.Is(err, domain.ErrVersionConflict) {
		t.Errorf("updating at a stale version returned %v, want ErrVersionConflict", err)
	}
}

// TestBudgetEntryCannotReferenceAnotherTripsOption pins the composite foreign key. Without it a
// ledger line could be attached to a proposal from someone else's trip.
func TestBudgetEntryCannotReferenceAnotherTripsOption(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	tripA := makeTrip(t, ctx, r, owner)
	tripB := makeTrip(t, ctx, r, owner)

	slotB := makeSlot(t, ctx, r, tripB, nil, "Hotel in the other trip")
	optionB := makeOption(t, ctx, r, slotB, "Somewhere else")

	mustViolate(t, ctx, r, "a budget entry referencing another trip's option", func(ctx context.Context) error {
		return r.budget.Save(ctx, &domain.BudgetEntry{
			ID: domain.NewID(), TripID: tripA.ID, SlotOptionID: &optionB.ID,
			Label: "Cross-trip", Category: domain.BudgetCategoryLodging, AmountMinor: 100,
		})
	})
}

// TestSoftDeletedEntryDisappearsFromTheLedger — the tombstone convention, and the splits are
// deliberately retained so the record of who owed what survives the deletion.
func TestSoftDeletedEntryDisappearsFromTheLedger(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)

	entry := makeBudgetEntry(t, ctx, r, trip, "Cancelled tour", 2000, []domain.BudgetSplit{
		{ID: domain.NewID(), UserID: owner.ID, AmountMinor: 2000},
	})

	if err := r.budget.SoftDelete(ctx, entry.ID, time.Now().UTC(), entry.Version); err != nil {
		t.Fatalf("soft deleting: %v", err)
	}
	if _, err := r.budget.GetByID(ctx, entry.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a tombstoned entry is still readable (err = %v)", err)
	}
	entries, err := r.budget.ListForTrip(ctx, trip.ID)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	for _, e := range entries {
		if e.ID == entry.ID {
			t.Error("a tombstoned entry is still in the ledger listing")
		}
	}

	// Counted on the test's own transaction: testPool would read from a different connection
	// that cannot see these uncommitted rows, and would report zero regardless of the code.
	var remaining int
	scanInTx(t, ctx, &remaining,
		`SELECT count(*) FROM budget_splits WHERE budget_entry_id = $1`, entry.ID)
	if remaining != 1 {
		t.Errorf("%d splits survived the soft delete, want 1 — the record of who owed what "+
			"is what someone disputing the deletion needs", remaining)
	}
}

// TestListForTripLoadsEveryEntrysSplits guards against the N+1 read being "fixed" into a
// version that returns entries with empty split sets — which is indistinguishable, to a caller,
// from entries nobody has split.
func TestListForTripLoadsEveryEntrysSplits(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	other := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)

	makeBudgetEntry(t, ctx, r, trip, "First", 300, []domain.BudgetSplit{
		{ID: domain.NewID(), UserID: owner.ID, AmountMinor: 100},
		{ID: domain.NewID(), UserID: other.ID, AmountMinor: 200},
	})
	makeBudgetEntry(t, ctx, r, trip, "Second", 500, []domain.BudgetSplit{
		{ID: domain.NewID(), UserID: owner.ID, AmountMinor: 500},
	})
	unsplit := makeBudgetEntry(t, ctx, r, trip, "Not split yet", 700, nil)

	entries, err := r.budget.ListForTrip(ctx, trip.ID)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("listed %d entries, want 3", len(entries))
	}
	for _, e := range entries {
		switch e.ID {
		case unsplit.ID:
			if e.IsSplit() {
				t.Errorf("%q reports splits it does not have", e.Label)
			}
		default:
			if !e.IsSplit() {
				t.Errorf("%q came back with no splits attached", e.Label)
			}
			if e.SplitTotal() != e.AmountMinor {
				t.Errorf("%q: splits total %d, entry total %d", e.Label, e.SplitTotal(), e.AmountMinor)
			}
		}
	}
}
