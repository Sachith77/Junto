package domain

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

// The two conflict classes that are NOT field-mergeable.
//
// Everything in op_test.go establishes that an operation touches only the fields it names —
// the property that makes concurrent itinerary editing work. These tests establish the
// opposite property for the two entities where field-level merge is the wrong answer, and they
// exist because "atomic" and "broadcast-only" are otherwise just words in a doc comment.
//
// What they claim: a budget write cannot be expressed as a partial edit, a fold of a budget op
// replaces the split set wholesale, and an attachment carries no version. What they do NOT
// claim is convergence under a real race — that lives in tests/convergence_api_test.go, for
// the same reason stated at the top of op_test.go.

func fixtureBudget() *BudgetEntry {
	incurred := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	a, b := NewID(), NewID()
	// Sorted so the fixture's own order does not decide the assertions below; splitValues is
	// responsible for canonicalising, and a fixture that arrived pre-sorted would hide a
	// regression in it.
	if a.String() < b.String() {
		a, b = b, a
	}
	return &BudgetEntry{
		ID: NewID(), TripID: NewID(),
		Label: "Beach house, three nights", Category: BudgetCategoryLodging,
		AmountMinor: 45000, IncurredOn: &incurred,
		Splits: []BudgetSplit{
			{ID: NewID(), UserID: a, AmountMinor: 15000},
			{ID: NewID(), UserID: b, AmountMinor: 30000},
		},
		Version: 2, UpdatedAt: time.Now().UTC().Truncate(time.Second),
	}
}

func fixtureAttachment() *Attachment {
	optionID := NewID()
	size := int64(1024)
	return &Attachment{
		ID: NewID(), TripID: NewID(),
		SlotOptionID: &optionID,
		Kind:         AttachmentKindFile, Status: AttachmentStatusReady,
		StorageKey: "trips/x/att/y.png", ContentType: "image/png",
		SizeBytes: &size, OriginalName: "booking.png",
		UpdatedAt: time.Now().UTC().Truncate(time.Second),
	}
}

func budgetMask() FieldMask     { return NewFieldMask(OpBudgetSet.AllowedFields()...) }
func attachmentMask() FieldMask { return NewFieldMask(OpAttachmentAdd.AllowedFields()...) }

// TestBudgetOpCarriesTheWholeEntry is the structural form of "the budget is atomic".
//
// The claim in CLAUDE.md is that budget writes are applied whole rather than merged per field.
// That is only true if a partial budget operation cannot be BUILT — otherwise the guarantee
// depends on every future caller remembering, which is the kind of guarantee that stops being
// true quietly.
func TestBudgetOpCarriesTheWholeEntry(t *testing.T) {
	entry := fixtureBudget()

	// The mask a careless caller would write: "I only changed the amount."
	partial := NewFieldMask(FieldAmountMinor)
	if err := partial.Validate(OpBudgetSet); err == nil {
		t.Fatal("a partial budget mask was accepted; the sum invariant is now breakable by " +
			"two concurrent writes that each name a different field")
	}

	full := budgetMask()
	if err := full.Validate(OpBudgetSet); err != nil {
		t.Fatalf("the total mask was rejected: %v", err)
	}

	raw, err := BudgetPayload(entry, full)
	if err != nil {
		t.Fatalf("building the payload: %v", err)
	}
	var decoded opPayload
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decoding the payload: %v", err)
	}
	got := make([]string, 0, len(decoded.Fields))
	for k := range decoded.Fields {
		got = append(got, k)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string(full)) {
		t.Errorf("payload keys %v do not match the total mask %v", got, full)
	}
}

// TestBudgetFoldReplacesTheSplitSetWholesale is the fold-side half of the same guarantee.
//
// Enforcing the total mask at write time is not enough on its own: a fold that merged splits
// element-wise — keeping a member's old share because the new set does not mention them —
// would let the replica and the database disagree while both looked internally consistent.
// That is the failure mode this pins, because it is invisible to every other test.
func TestBudgetFoldReplacesTheSplitSetWholesale(t *testing.T) {
	entry := fixtureBudget()
	mask := budgetMask()
	r := NewReplica()

	if err := r.Apply(Op{
		Seq: 1, TripID: entry.TripID, EntityID: entry.ID, Kind: OpBudgetSet,
		Fields: mask, Payload: must(BudgetPayload(entry, mask)),
	}); err != nil {
		t.Fatalf("applying the first budget op: %v", err)
	}
	if n := len(r.Budgets[entry.ID].Splits); n != 2 {
		t.Fatalf("folded %d splits, want 2", n)
	}

	// The group re-splits the same cost between ONE person. The member who used to owe 15000
	// must disappear entirely — not linger with a stale share that keeps the old sum.
	soleUser := NewID()
	rewritten := *entry
	rewritten.Splits = []BudgetSplit{{ID: NewID(), UserID: soleUser, AmountMinor: 45000}}
	rewritten.Version = 3

	if err := r.Apply(Op{
		Seq: 2, TripID: entry.TripID, EntityID: entry.ID, Kind: OpBudgetSet,
		Fields: mask, Payload: must(BudgetPayload(&rewritten, mask)),
	}); err != nil {
		t.Fatalf("applying the second budget op: %v", err)
	}

	folded := r.Budgets[entry.ID]
	if len(folded.Splits) != 1 {
		t.Fatalf("folded %d splits after a wholesale rewrite, want 1: %+v", len(folded.Splits), folded.Splits)
	}
	if folded.Splits[0].UserID != soleUser {
		t.Errorf("folded split belongs to %s, want %s", folded.Splits[0].UserID, soleUser)
	}
	if folded.SplitTotal() != folded.AmountMinor {
		t.Errorf("the folded entry violates its own sum invariant: splits total %d, entry total %d",
			folded.SplitTotal(), folded.AmountMinor)
	}
	if folded.Version != 3 {
		t.Errorf("folded version = %d, want 3", folded.Version)
	}
}

// TestBudgetSplitOrderIsCanonical means two replicas that folded the same log hold
// byte-comparable split sets.
//
// Without it, fold(log) == database is only assertable with an order-insensitive comparison,
// and every test that makes the comparison has to remember to sort. Canonicalising once, where
// the payload is built, is cheaper than being careful in every assertion forever.
func TestBudgetSplitOrderIsCanonical(t *testing.T) {
	entry := fixtureBudget()
	mask := budgetMask()

	shuffled := *entry
	shuffled.Splits = []BudgetSplit{entry.Splits[1], entry.Splits[0]}

	a := must(BudgetPayload(entry, mask))
	b := must(BudgetPayload(&shuffled, mask))
	if string(a) != string(b) {
		t.Errorf("the same split set serialised differently depending on row order:\n %s\n %s", a, b)
	}
}

// TestAttachmentOpsAreAddAndRemoveOnly pins the vocabulary itself.
//
// The migration comment and the domain doc comment both promise that attachments get no
// conflict-resolution machinery. The way that promise breaks is not dramatically — it is
// somebody adding attachment.edit.v1 because a caption field appeared. This fails when that
// happens, and points at the decision rather than at a diff.
func TestAttachmentOpsAreAddAndRemoveOnly(t *testing.T) {
	var attachmentKinds []OpKind
	for k := range opKinds {
		if k.Entity() == "attachment" {
			attachmentKinds = append(attachmentKinds, k)
		}
	}
	slices.Sort(attachmentKinds)
	want := []OpKind{OpAttachmentAdd, OpAttachmentRemove}
	slices.Sort(want)

	if !slices.Equal(attachmentKinds, want) {
		t.Errorf("attachment vocabulary is %v, want %v — attachments are broadcast-only (D46); "+
			"an edit verb would imply a merge grain they do not have", attachmentKinds, want)
	}
}

// TestAttachmentOpsCarryNoVersion checks that the log records the absence honestly.
//
// Attachments have no version column, so writing anything other than zero would invent an
// optimistic-concurrency story for an entity that does not have one — and the log outlives the
// code, so a future reader would have no way to tell the invention from the truth.
func TestAttachmentOpsCarryNoVersion(t *testing.T) {
	a := fixtureAttachment()
	raw := must(AttachmentPayload(a, attachmentMask()))

	var decoded opPayload
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decoding the payload: %v", err)
	}
	if decoded.Meta.Version != 0 {
		t.Errorf("attachment op meta.version = %d, want 0", decoded.Meta.Version)
	}
}

// TestTwoConcurrentAttachmentsBothSurvive is the whole of the attachment conflict story.
//
// There is no merge to test, and that is the point: two uploads that raced simply both exist.
// This is the cheapest claim in the system to make true, and it is worth an explicit test
// because "we did nothing" and "we did nothing correct" look identical without one.
func TestTwoConcurrentAttachmentsBothSurvive(t *testing.T) {
	tripID := NewID()
	mask := attachmentMask()
	r := NewReplica()

	first, second := fixtureAttachment(), fixtureAttachment()
	first.TripID, second.TripID = tripID, tripID
	// Same owner, same instant, different objects — the actual race.
	owner := NewID()
	first.SlotOptionID, second.SlotOptionID = &owner, &owner

	for i, a := range []*Attachment{first, second} {
		if err := r.Apply(Op{
			Seq: int64(i + 1), TripID: tripID, EntityID: a.ID, Kind: OpAttachmentAdd,
			Fields: mask, Payload: must(AttachmentPayload(a, mask)),
		}); err != nil {
			t.Fatalf("applying attachment %d: %v", i, err)
		}
	}

	if len(r.Attachments) != 2 {
		t.Fatalf("folded %d attachments, want 2 — concurrent uploads must not displace each other",
			len(r.Attachments))
	}
	for _, a := range []*Attachment{first, second} {
		folded, ok := r.Attachments[a.ID]
		if !ok {
			t.Fatalf("attachment %s is missing from the fold", a.ID)
		}
		if folded.StorageKey != a.StorageKey || folded.Status != AttachmentStatusReady {
			t.Errorf("attachment %s folded to %+v", a.ID, folded)
		}
		if folded.SlotOptionID == nil || *folded.SlotOptionID != owner {
			t.Errorf("attachment %s lost its owner in the fold", a.ID)
		}
	}
}

// TestAttachmentRemovalIsATombstone checks removal folds like every other tombstone in the
// system, so a resyncing client learns about a deletion instead of silently keeping the row.
func TestAttachmentRemovalIsATombstone(t *testing.T) {
	a := fixtureAttachment()
	r := NewReplica()

	if err := r.Apply(Op{
		Seq: 1, TripID: a.TripID, EntityID: a.ID, Kind: OpAttachmentAdd,
		Fields: attachmentMask(), Payload: must(AttachmentPayload(a, attachmentMask())),
	}); err != nil {
		t.Fatalf("applying the add: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	tombstone := *a
	tombstone.DeletedAt = &now
	removeMask := NewFieldMask(FieldDeletedAt)

	if err := r.Apply(Op{
		Seq: 2, TripID: a.TripID, EntityID: a.ID, Kind: OpAttachmentRemove,
		Fields: removeMask, Payload: must(AttachmentPayload(&tombstone, removeMask)),
	}); err != nil {
		t.Fatalf("applying the removal: %v", err)
	}

	folded := r.Attachments[a.ID]
	if !folded.IsDeleted() {
		t.Error("the attachment survived its removal op")
	}
	// The removal names only deleted_at, so everything else must be untouched — a removal that
	// blanked the row would leave a client unable to render "deleted photo.png".
	if folded.StorageKey != a.StorageKey || folded.OriginalName != a.OriginalName {
		t.Errorf("removal clobbered fields it did not name: %+v", folded)
	}
}

// TestFoldRejectsACoarseEditOfAnUnknownEntity mirrors the itinerary's equivalent guard: an
// operation that is not a create, targeting something the replica has never seen, means a
// dropped operation rather than an entity to invent.
func TestFoldRejectsACoarseEditOfAnUnknownEntity(t *testing.T) {
	tripID := NewID()
	for _, tc := range []struct {
		name string
		op   Op
	}{
		{"budget delete before set", Op{
			Seq: 1, TripID: tripID, EntityID: NewID(), Kind: OpBudgetDelete,
			Fields:  NewFieldMask(FieldDeletedAt),
			Payload: must(BudgetPayload(&BudgetEntry{}, NewFieldMask(FieldDeletedAt))),
		}},
		{"attachment remove before add", Op{
			Seq: 1, TripID: tripID, EntityID: NewID(), Kind: OpAttachmentRemove,
			Fields:  NewFieldMask(FieldDeletedAt),
			Payload: must(AttachmentPayload(&Attachment{}, NewFieldMask(FieldDeletedAt))),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := NewReplica().Apply(tc.op); err == nil {
				t.Error("expected the fold to reject an operation on an entity it has never seen")
			}
		})
	}
}
