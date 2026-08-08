package domain

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

// What these tests claim, and — just as importantly — what they do NOT.
//
// They prove the three properties a pure unit test can actually establish about this design:
// that an operation writes only the fields its mask names, that a fold of the log is
// deterministic and total, and that the tombstone behaves as its own register.
//
// They do NOT prove convergence. A permutation test ("apply these operations in every order,
// get the same state") would be TRIVIALLY TRUE here, because operations are sorted by their
// server-assigned sequence number before folding — presenting one as a convergence proof
// would be dishonest. The concurrency in this system lives in the SUBMISSION race, not in
// application order, so the convergence claim rests on the racing WebSocket test in
// tests/convergence_api_test.go and nowhere else.

// must unwraps a (value, error) pair inline.
//
// It panics rather than calling t.Fatalf because Go does not allow f(t, g()) when g returns
// two values, and threading an error check through every payload construction would bury the
// point of each test in plumbing. A panic here fails the test with a usable stack.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func fixtureSlot() *Slot {
	start := TimeOfDay{Hour: 9, Minute: 30}
	return &Slot{
		ID: NewID(), TripID: NewID(),
		Kind: SlotKindLodging, Title: "Where are we staying in Goa",
		Notes: "near the beach", StartTime: &start, Position: "a0",
		Status: SlotStatusPlanned, Version: 3, UpdatedAt: time.Now().UTC().Truncate(time.Second),
	}
}

// TestPayloadKeysMatchTheFieldMask pins the invariant that makes the mask meaningful.
//
// If the payload could carry a field the mask does not name, the mask would be decorative and
// a fold would have to guess which values are authoritative. If it could omit one the mask
// DOES name, a fold would silently skip a change.
func TestPayloadKeysMatchTheFieldMask(t *testing.T) {
	slot := fixtureSlot()

	for _, mask := range []FieldMask{
		NewFieldMask(FieldTitle),
		NewFieldMask(FieldTitle, FieldNotes),
		NewFieldMask(FieldDayID, FieldPosition),
		NewFieldMask(OpSlotCreate.AllowedFields()...),
	} {
		raw := must(SlotPayload(slot, mask))

		var decoded opPayload
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("decoding payload: %v", err)
		}

		got := make([]string, 0, len(decoded.Fields))
		for k := range decoded.Fields {
			got = append(got, k)
		}
		slices.Sort(got)

		if !slices.Equal(got, []string(mask)) {
			t.Errorf("payload keys %v do not match mask %v", got, mask)
		}
		if decoded.Meta.Version != slot.Version {
			t.Errorf("payload version = %d, want %d", decoded.Meta.Version, slot.Version)
		}
	}
}

// TestFieldMaskIsolation is the property that makes concurrent editing work at all.
//
// Two members editing different fields of one slot both succeed precisely because an
// operation touches nothing it did not name. If this regresses, the last writer silently
// clobbers the other's field and the system degrades into last-writer-wins on whole entities
// — which is exactly what Stage 1's version check already did, and what Stage 2 exists to
// replace.
func TestFieldMaskIsolation(t *testing.T) {
	slot := fixtureSlot()
	r := NewReplica()

	if err := r.Apply(Op{
		Seq: 1, TripID: slot.TripID, EntityID: slot.ID, Kind: OpSlotCreate,
		Fields:  NewFieldMask(OpSlotCreate.AllowedFields()...),
		Payload: must(SlotPayload(slot, NewFieldMask(OpSlotCreate.AllowedFields()...))),
	}); err != nil {
		t.Fatalf("applying create: %v", err)
	}

	// Someone edits ONLY the notes. Everything else must survive untouched.
	edited := *slot
	edited.Notes = "actually, near the old town"
	edited.Title = "THIS MUST NOT BE APPLIED" // present on the entity, absent from the mask
	edited.Version = 4

	if err := r.Apply(Op{
		Seq: 2, TripID: slot.TripID, EntityID: slot.ID, Kind: OpSlotEdit,
		Fields:  NewFieldMask(FieldNotes),
		Payload: must(SlotPayload(&edited, NewFieldMask(FieldNotes))),
	}); err != nil {
		t.Fatalf("applying edit: %v", err)
	}

	got := r.Slots[slot.ID]
	if got.Notes != "actually, near the old town" {
		t.Errorf("notes = %q, want the edited value", got.Notes)
	}
	if got.Title != slot.Title {
		t.Errorf("title = %q, want it untouched at %q — an unmasked field was clobbered",
			got.Title, slot.Title)
	}
	if got.Position != slot.Position || got.Status != slot.Status {
		t.Errorf("an unmasked placement/status field changed: %+v", got)
	}
	if got.StartTime == nil || got.StartTime.Minutes() != slot.StartTime.Minutes() {
		t.Errorf("start_time = %v, want it untouched — nullable unmasked fields must survive too",
			got.StartTime)
	}
}

// TestTombstoneIsItsOwnRegister covers concurrent delete-versus-edit.
//
// An edit arriving after a delete applies to its own fields and leaves the entity deleted.
// The alternative — dropping edits to tombstoned entities — was rejected because it breaks
// fold(log) == database state, which is the invariant everything else is checked against. It
// also means a later undelete restores real content rather than a stale snapshot.
func TestTombstoneIsItsOwnRegister(t *testing.T) {
	slot := fixtureSlot()
	r := NewReplica()

	create := NewFieldMask(OpSlotCreate.AllowedFields()...)
	if err := r.Apply(Op{
		Seq: 1, TripID: slot.TripID, EntityID: slot.ID, Kind: OpSlotCreate,
		Fields: create, Payload: must(SlotPayload(slot, create)),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	deletedAt := time.Now().UTC().Truncate(time.Second)
	tombstone := *slot
	tombstone.DeletedAt = &deletedAt
	tombstone.Version = 4
	if err := r.Apply(Op{
		Seq: 2, TripID: slot.TripID, EntityID: slot.ID, Kind: OpSlotDelete,
		Fields:  NewFieldMask(FieldDeletedAt),
		Payload: must(SlotPayload(&tombstone, NewFieldMask(FieldDeletedAt))),
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// The concurrent edit lands afterwards. It must be recorded, and must not resurrect.
	late := tombstone
	late.Title = "Renamed while being deleted"
	late.Version = 5
	if err := r.Apply(Op{
		Seq: 3, TripID: slot.TripID, EntityID: slot.ID, Kind: OpSlotEdit,
		Fields:  NewFieldMask(FieldTitle),
		Payload: must(SlotPayload(&late, NewFieldMask(FieldTitle))),
	}); err != nil {
		t.Fatalf("late edit: %v", err)
	}

	got := r.Slots[slot.ID]
	if !got.IsDeleted() {
		t.Error("an edit after a delete resurrected the slot; the tombstone is not a register")
	}
	if got.Title != "Renamed while being deleted" {
		t.Errorf("title = %q, want the late edit applied — dropping it would break "+
			"fold(log) == database state", got.Title)
	}
}

// TestVoteIsALastWriterWinsRegister exercises the simplest convergent entity in the system
// through exactly the same machinery as everything else. No special case, by design.
func TestVoteIsALastWriterWinsRegister(t *testing.T) {
	tripID, slotID, userID := NewID(), NewID(), NewID()
	optionA, optionB := NewID(), NewID()
	voteID := NewID()
	mask := NewFieldMask(OpVoteSet.AllowedFields()...)

	r := NewReplica()
	for i, choice := range []*ID{&optionA, &optionB, nil} {
		v := &Vote{ID: voteID, SlotID: slotID, TripID: tripID, UserID: userID,
			OptionID: choice, Version: i + 1}
		if err := r.Apply(Op{
			Seq: int64(i + 1), TripID: tripID, EntityID: voteID, Kind: OpVoteSet,
			Fields: mask, Payload: must(VotePayload(v, mask)),
		}); err != nil {
			t.Fatalf("vote %d: %v", i, err)
		}
	}

	if n := len(r.Votes); n != 1 {
		t.Fatalf("got %d vote rows, want exactly 1 — the register shape is broken", n)
	}
	got := r.Votes[voteID]
	if !got.IsRetracted() {
		t.Errorf("option_id = %v, want nil: the last write was a retraction", got.OptionID)
	}
	if got.SlotID != slotID || got.UserID != userID {
		t.Error("the vote lost its identity keys across the fold")
	}
}

// TestFoldToleratesRedeliveryButRejectsAGap separates two things that look identical and are
// not.
//
// Delivery is at-least-once by construction: the broadcast is an accelerator and the log is
// the guarantee (D70), so a resume, a retry after a lost acknowledgement, or a reconnect can
// all re-deliver an operation the replica already folded. Refusing those would make correct
// delivery unusable — which is exactly what happened before this distinction existed.
//
// A GAP is the case that must stay fatal. It means the replica missed something and every
// value it holds afterwards may be wrong; the gapless sequence (D61) exists precisely so this
// is detectable rather than silent.
func TestFoldToleratesRedeliveryButRejectsAGap(t *testing.T) {
	slot := fixtureSlot()
	mask := NewFieldMask(OpSlotCreate.AllowedFields()...)
	create := Op{
		Seq: 1, TripID: slot.TripID, EntityID: slot.ID, Kind: OpSlotCreate,
		Fields: mask, Payload: must(SlotPayload(slot, mask)),
	}

	r := NewReplica()
	if err := r.Apply(create); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if err := r.Apply(create); err != nil {
		t.Errorf("redelivery of an already-folded operation was rejected: %v", err)
	}
	if r.Seq != 1 {
		t.Errorf("redelivery moved the sequence to %d; it must be a no-op", r.Seq)
	}

	gapped := create
	gapped.Seq = 5
	if err := r.Apply(gapped); err == nil {
		t.Error("a gap in the log was accepted; a stale replica would go undetected")
	}
}

// TestFoldRejectsAnEditOfAnUnknownEntity keeps the fold TOTAL: every operation either applies
// or reports why. Silently creating a placeholder entity would hide a genuinely broken log.
func TestFoldRejectsAnEditOfAnUnknownEntity(t *testing.T) {
	slot := fixtureSlot()
	r := NewReplica()
	err := r.Apply(Op{
		Seq: 1, TripID: slot.TripID, EntityID: slot.ID, Kind: OpSlotEdit,
		Fields:  NewFieldMask(FieldTitle),
		Payload: must(SlotPayload(slot, NewFieldMask(FieldTitle))),
	})
	if err == nil {
		t.Error("editing an unknown slot was accepted; the fold is not total")
	}
}

// TestFieldMaskValidation refuses to write anything a future decoder could not interpret. The
// log is append-only, so there is no later opportunity to fix a bad entry.
func TestFieldMaskValidation(t *testing.T) {
	tests := []struct {
		name string
		kind OpKind
		mask FieldMask
		ok   bool
	}{
		{"valid edit", OpSlotEdit, NewFieldMask(FieldTitle, FieldNotes), true},
		{"empty mask", OpSlotEdit, NewFieldMask(), false},
		{"unknown kind", OpKind("slot.explode.v1"), NewFieldMask(FieldTitle), false},
		{"field not allowed for kind", OpSlotEdit, NewFieldMask(FieldPosition), false},
		{"move may not edit content", OpSlotMove, NewFieldMask(FieldTitle), false},
		{"delete may only tombstone", OpSlotDelete, NewFieldMask(FieldDeletedAt), true},
		{"vote names its identity keys", OpVoteSet,
			NewFieldMask(FieldSlotID, FieldUserID, FieldOptionID), true},

		// The total-mask rule (D83/D84). A partial budget mask is the exact shape of the
		// merge that would break the sum invariant, so it is refused at the point where the
		// operation would be constructed rather than detected after the fact.
		{"budget set names every field", OpBudgetSet,
			NewFieldMask(OpBudgetSet.AllowedFields()...), true},
		{"budget set may not name a subset", OpBudgetSet,
			NewFieldMask(FieldAmountMinor), false},
		{"budget set may not omit the splits", OpBudgetSet,
			NewFieldMask(FieldLabel, FieldCategory, FieldAmountMinor, FieldSlotOptionID,
				FieldPaidBy, FieldIncurredOn), false},
		{"budget delete may only tombstone", OpBudgetDelete, NewFieldMask(FieldDeletedAt), true},

		{"attachment add names every field", OpAttachmentAdd,
			NewFieldMask(OpAttachmentAdd.AllowedFields()...), true},
		{"attachment add may not name a subset", OpAttachmentAdd,
			NewFieldMask(FieldStatus), false},
		{"attachment remove may only tombstone", OpAttachmentRemove,
			NewFieldMask(FieldDeletedAt), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.mask.Validate(tc.kind)
			if tc.ok && err != nil {
				t.Errorf("expected the mask to be accepted, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Error("expected the mask to be rejected")
			}
		})
	}
}

// TestFieldMaskIsCanonical means two logically identical operations serialise identically,
// which is what lets tests and audits compare log entries by value.
func TestFieldMaskIsCanonical(t *testing.T) {
	a := NewFieldMask(FieldNotes, FieldTitle, FieldNotes)
	b := NewFieldMask(FieldTitle, FieldNotes)
	if !slices.Equal(a, b) {
		t.Errorf("masks did not canonicalise: %v vs %v", a, b)
	}
	if len(a) != 2 {
		t.Errorf("duplicate field was not removed: %v", a)
	}
}

// TestEveryOpKindHasAllowedFields catches a kind added to the vocabulary without declaring
// what it may change — which would let it write an arbitrary field mask into the log.
func TestEveryOpKindHasAllowedFields(t *testing.T) {
	kinds := []OpKind{
		OpDayCreate, OpDayEdit, OpDayMove, OpDayDelete,
		OpSlotCreate, OpSlotEdit, OpSlotMove, OpSlotSelectOption, OpSlotSetStatus, OpSlotDelete,
		OpOptionCreate, OpOptionEdit, OpOptionDelete,
		OpVoteSet,
		OpBudgetSet, OpBudgetDelete,
		OpAttachmentAdd, OpAttachmentRemove,
		OpCommentCreate, OpCommentDelete,
	}
	if len(kinds) != len(opKinds) {
		t.Errorf("this test lists %d kinds but the vocabulary has %d; a kind was added "+
			"without being covered here", len(kinds), len(opKinds))
	}
	for _, k := range kinds {
		if !k.Valid() {
			t.Errorf("%s is not in the vocabulary", k)
		}
		if len(k.AllowedFields()) == 0 {
			t.Errorf("%s declares no allowed fields", k)
		}
		if k.Entity() == "" {
			t.Errorf("%s maps to no entity class, so a fold cannot dispatch it", k)
		}
	}
}
