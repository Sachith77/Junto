package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/junto/junto/internal/domain"
)

// What a fake-backed test can prove about the operation log, and what it cannot.
//
// It CAN prove the SHAPE of the log a mutation produces: that an edit's mask names only what
// changed, that a cascade emits two consecutively numbered operations linked by cause, that
// the payload is built from the persisted entity rather than the request. Those are
// properties of the service layer's own logic, and a real database adds nothing to them.
//
// It CANNOT prove convergence, and does not try. Convergence depends on the trip row lock
// serializing concurrent writers, which is a Postgres behaviour a fake cannot model — that is
// the racing WebSocket test's job.

func opsFor(t *testing.T, h *planningHarness, kind domain.OpKind) []domain.Op {
	t.Helper()
	var out []domain.Op
	for _, op := range h.opsFake.all() {
		if op.Kind == kind {
			out = append(out, op)
		}
	}
	return out
}

func payloadField(t *testing.T, op domain.Op, field string) string {
	t.Helper()
	fields, err := op.PayloadFields()
	if err != nil {
		t.Fatalf("decoding payload: %v", err)
	}
	raw, ok := fields[field]
	if !ok {
		t.Fatalf("payload has no field %q (mask: %v)", field, op.Fields)
	}
	return string(raw)
}

// TestEveryPlanningMutationIsLogged is the Rule 3 guarantee at the service level.
//
// If a mutation could commit without appending to the log, a client asking "everything since
// seq N" would silently miss it and resync would quietly degrade into a full re-fetch — with
// every other test still green. That is the specific failure this whole design is arranged to
// prevent, so it gets a direct test rather than being inferred from the others passing.
func TestEveryPlanningMutationIsLogged(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "log-owner@example.com")
	trip := h.makeTrip(t, owner)

	day, err := h.days.Create(ctx, trip.ID, owner.ID, CreateDayInput{Label: "Day 1"})
	if err != nil {
		t.Fatalf("creating day: %v", err)
	}
	slot, err := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{
		DayID: &day.ID, Kind: domain.SlotKindLodging, Title: "Where are we staying",
	})
	if err != nil {
		t.Fatalf("creating slot: %v", err)
	}
	option, err := h.options.Create(ctx, trip.ID, owner.ID, slot.ID, CreateOptionInput{
		Title: "Taj Exotica",
	})
	if err != nil {
		t.Fatalf("creating option: %v", err)
	}
	if _, err := h.votes.Cast(ctx, trip.ID, owner.ID, slot.ID, &option.ID); err != nil {
		t.Fatalf("casting vote: %v", err)
	}

	for _, kind := range []domain.OpKind{
		domain.OpDayCreate, domain.OpSlotCreate, domain.OpOptionCreate, domain.OpVoteSet,
	} {
		if len(opsFor(t, h, kind)) == 0 {
			t.Errorf("no %s operation was logged; a resyncing client would never see it", kind)
		}
	}

	// Sequence numbers are per-trip, consecutive, and start at 1 — the gaplessness a client
	// relies on to detect that it is stale.
	ops := h.opsFake.all()
	for i, op := range ops {
		if op.Seq != int64(i+1) {
			t.Errorf("operation %d has seq %d, want %d", i, op.Seq, i+1)
		}
		if op.TripID != trip.ID {
			t.Errorf("operation %d belongs to the wrong trip", i)
		}
		if op.ActorID == nil || *op.ActorID != owner.ID {
			t.Errorf("operation %d has no actor attribution", i)
		}
	}
}

// TestAMaskedEditLogsOnlyTheChangedField is the field-level-merge guarantee at its source.
//
// If the log recorded every editable field on every edit, two members editing different
// fields would each publish a full overwrite, and a client folding the log would see the
// later one clobber the earlier — the exact loss the mask exists to prevent.
func TestAMaskedEditLogsOnlyTheChangedField(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "mask-owner@example.com")
	trip := h.makeTrip(t, owner)

	slot, err := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{
		Kind: domain.SlotKindLodging, Title: "Original", Notes: "original notes",
	})
	if err != nil {
		t.Fatalf("creating slot: %v", err)
	}

	if _, err := h.slots.Update(ctx, trip.ID, owner.ID, slot.ID, UpdateSlotInput{
		Fields: domain.NewFieldMask(domain.FieldTitle),
		Title:  "Renamed",
		// Deliberately supplying values for unmasked fields. They must be ignored: a client
		// sending a whole object with a narrow mask means what the MASK says.
		Notes: "THIS MUST NOT BE APPLIED",
		Kind:  domain.SlotKindNote,
	}); err != nil {
		t.Fatalf("updating slot: %v", err)
	}

	edits := opsFor(t, h, domain.OpSlotEdit)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit operation, got %d", len(edits))
	}
	if len(edits[0].Fields) != 1 || !edits[0].Fields.Has(domain.FieldTitle) {
		t.Errorf("edit mask = %v, want exactly [title]", edits[0].Fields)
	}
	if got := payloadField(t, edits[0], domain.FieldTitle); got != `"Renamed"` {
		t.Errorf("logged title = %s, want \"Renamed\"", got)
	}

	reloaded, err := h.slots.Get(ctx, trip.ID, owner.ID, slot.ID)
	if err != nil {
		t.Fatalf("reloading slot: %v", err)
	}
	if reloaded.Notes != "original notes" {
		t.Errorf("notes = %q, want the original — an unmasked field was written", reloaded.Notes)
	}
	if reloaded.Kind != domain.SlotKindLodging {
		t.Errorf("kind = %q, want the original", reloaded.Kind)
	}
}

// TestAnUnmaskedEditLogsEveryEditableField covers the other half: a whole-object REST write
// genuinely does replace all five fields, and saying so in the log is accurate rather than
// lazy. Understating the mask would make a fold disagree with the database.
func TestAnUnmaskedEditLogsEveryEditableField(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "wholeobj@example.com")
	trip := h.makeTrip(t, owner)

	slot, err := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{
		Kind: domain.SlotKindLodging, Title: "Original",
	})
	if err != nil {
		t.Fatalf("creating slot: %v", err)
	}
	if _, err := h.slots.Update(ctx, trip.ID, owner.ID, slot.ID, UpdateSlotInput{
		Kind: domain.SlotKindActivity, Title: "Renamed", Notes: "new notes",
	}); err != nil {
		t.Fatalf("updating slot: %v", err)
	}

	edits := opsFor(t, h, domain.OpSlotEdit)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit operation, got %d", len(edits))
	}
	want := domain.NewFieldMask(domain.OpSlotEdit.AllowedFields()...)
	if len(edits[0].Fields) != len(want) {
		t.Errorf("unmasked edit logged %v, want every editable field %v", edits[0].Fields, want)
	}
}

// TestTheSelectionCascadeIsTwoLinkedOperations pins D63 at the layer that produces it.
func TestTheSelectionCascadeIsTwoLinkedOperations(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "cascade@example.com")
	trip := h.makeTrip(t, owner)

	slot, err := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{
		Kind: domain.SlotKindLodging, Title: "Where are we staying",
	})
	if err != nil {
		t.Fatalf("creating slot: %v", err)
	}
	option, err := h.options.Create(ctx, trip.ID, owner.ID, slot.ID, CreateOptionInput{Title: "Taj"})
	if err != nil {
		t.Fatalf("creating option: %v", err)
	}
	if err := h.slots.SetSelectedOption(ctx, trip.ID, owner.ID, slot.ID, &option.ID, nil); err != nil {
		t.Fatalf("selecting: %v", err)
	}

	before := len(h.opsFake.all())
	if err := h.options.Delete(ctx, trip.ID, owner.ID, option.ID, nil); err != nil {
		t.Fatalf("deleting option: %v", err)
	}

	ops := h.opsFake.all()[before:]
	if len(ops) != 2 {
		t.Fatalf("one intent produced %d operations, want 2 (the delete and its cascade)", len(ops))
	}
	if ops[0].Kind != domain.OpOptionDelete || ops[1].Kind != domain.OpSlotSelectOption {
		t.Errorf("cascade shape = %s then %s", ops[0].Kind, ops[1].Kind)
	}
	if ops[1].Seq != ops[0].Seq+1 {
		t.Errorf("cascade operations are not consecutive: %d then %d", ops[0].Seq, ops[1].Seq)
	}

	// The cleared selection must be logged as a VALUE, so a client assigns it rather than
	// re-deriving the rule and getting a chance to disagree with the server.
	if got := payloadField(t, ops[1], domain.FieldSelectedOptionID); got != "null" {
		t.Errorf("logged selected_option_id = %s, want null", got)
	}
}

// TestOnlyTheFirstOperationOfAnIntentCarriesTheClientOpID protects replay safety: the partial
// unique index on (trip_id, client_op_id) would reject a cascade that repeated the id, so the
// derived operation is linked by cause instead.
func TestOnlyTheFirstOperationOfAnIntentCarriesTheClientOpID(t *testing.T) {
	h := newPlanningHarness(t)
	owner := h.makeUser(t, "clientop@example.com")
	trip := h.makeTrip(t, owner)

	ctx := context.Background()
	slot, err := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{
		Kind: domain.SlotKindLodging, Title: "Where are we staying",
	})
	if err != nil {
		t.Fatalf("creating slot: %v", err)
	}
	option, err := h.options.Create(ctx, trip.ID, owner.ID, slot.ID, CreateOptionInput{Title: "Taj"})
	if err != nil {
		t.Fatalf("creating option: %v", err)
	}
	if err := h.slots.SetSelectedOption(ctx, trip.ID, owner.ID, slot.ID, &option.ID, nil); err != nil {
		t.Fatalf("selecting: %v", err)
	}

	clientOpID := domain.NewID()
	syncCtx := domain.ContextWithOpOrigin(ctx, domain.OpOrigin{
		ClientOpID: &clientOpID, CauseOpID: &clientOpID,
	})

	before := len(h.opsFake.all())
	if err := h.options.Delete(syncCtx, trip.ID, owner.ID, option.ID, nil); err != nil {
		t.Fatalf("deleting option: %v", err)
	}
	ops := h.opsFake.all()[before:]
	if len(ops) != 2 {
		t.Fatalf("expected 2 operations, got %d", len(ops))
	}
	if ops[0].ClientOpID == nil || *ops[0].ClientOpID != clientOpID {
		t.Error("the first operation did not carry the client op id")
	}
	if ops[1].ClientOpID != nil {
		t.Error("the derived operation carried the client op id; the uniqueness index " +
			"would reject the pair and replay protection would break")
	}
	if ops[0].CauseOpID == nil || ops[1].CauseOpID == nil ||
		*ops[0].CauseOpID != *ops[1].CauseOpID {
		t.Error("the two operations are not linked by a shared cause")
	}
}

// TestARESTOriginatedWriteHasNoClientOpID confirms the partial index's premise: REST writes
// legitimately have no client op id, which is why that index must stay partial.
func TestARESTOriginatedWriteHasNoClientOpID(t *testing.T) {
	h := newPlanningHarness(t)
	owner := h.makeUser(t, "rest@example.com")
	trip := h.makeTrip(t, owner)

	if _, err := h.slots.Create(context.Background(), trip.ID, owner.ID, CreateSlotInput{
		Kind: domain.SlotKindLodging, Title: "Created over REST",
	}); err != nil {
		t.Fatalf("creating slot: %v", err)
	}
	for _, op := range h.opsFake.all() {
		if op.ClientOpID != nil {
			t.Errorf("a REST-originated %s operation carried a client op id", op.Kind)
		}
	}
}

// TestNilVersionMeansNoPrecondition is D69 from the caller's side: omitting the version asks
// for merge semantics, supplying a stale one still gets the REST 409.
func TestNilVersionMeansNoPrecondition(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "version@example.com")
	trip := h.makeTrip(t, owner)

	slot, err := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{
		Kind: domain.SlotKindLodging, Title: "First",
	})
	if err != nil {
		t.Fatalf("creating slot: %v", err)
	}
	staleVersion := slot.Version

	if _, err := h.slots.Update(ctx, trip.ID, owner.ID, slot.ID, UpdateSlotInput{
		Fields: domain.NewFieldMask(domain.FieldTitle), Title: "Second",
	}); err != nil {
		t.Fatalf("first update: %v", err)
	}

	// A now-stale version must still be rejected — REST behaviour is unchanged.
	_, err = h.slots.Update(ctx, trip.ID, owner.ID, slot.ID, UpdateSlotInput{
		Fields: domain.NewFieldMask(domain.FieldTitle), Title: "Third",
		Version: ptr(staleVersion),
	})
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Errorf("a stale version returned %v, want ErrVersionConflict", err)
	}

	// The same stale state, submitted with no precondition, merges instead of failing.
	if _, err := h.slots.Update(ctx, trip.ID, owner.ID, slot.ID, UpdateSlotInput{
		Fields: domain.NewFieldMask(domain.FieldTitle), Title: "Third",
	}); err != nil {
		t.Errorf("an update with no version precondition failed: %v", err)
	}
}

// TestAFailedMutationLeavesNoOperation is why the sequence stays gapless: a rejected write
// rolls back the counter with everything else, so seq contiguity remains a usable
// completeness check rather than a hint (D61).
func TestAFailedMutationLeavesNoOperation(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "failed@example.com")
	viewer := h.makeUser(t, "failed-viewer@example.com")
	trip := h.makeTrip(t, owner)
	h.addMember(t, trip.ID, viewer, domain.RoleViewer)

	before := len(h.opsFake.all())

	if _, err := h.slots.Create(ctx, trip.ID, viewer.ID, CreateSlotInput{
		Kind: domain.SlotKindLodging, Title: "Not allowed",
	}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
	// Validation failure, which happens INSIDE the transaction after the sequencer is held.
	if _, err := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{
		Kind: domain.SlotKindLodging, Title: "",
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected a validation error, got %v", err)
	}

	if after := len(h.opsFake.all()); after != before {
		t.Errorf("a rejected mutation appended %d operation(s) to the log", after-before)
	}
}

// TestTheLoggedPayloadMatchesThePersistedEntity is D62 checked directly: the log records what
// the database holds, not what the request asked for. A create resolves its position key
// server-side, and it is the RESOLVED key that must be logged — replaying "after X" against a
// list whose neighbours have changed would produce a different order.
func TestTheLoggedPayloadMatchesThePersistedEntity(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "resolved@example.com")
	trip := h.makeTrip(t, owner)

	first, err := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{
		Kind: domain.SlotKindLodging, Title: "First",
	})
	if err != nil {
		t.Fatalf("creating first slot: %v", err)
	}
	second, err := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{
		Kind: domain.SlotKindLodging, Title: "Second", AfterSlotID: &first.ID,
	})
	if err != nil {
		t.Fatalf("creating second slot: %v", err)
	}

	creates := opsFor(t, h, domain.OpSlotCreate)
	if len(creates) != 2 {
		t.Fatalf("expected 2 create operations, got %d", len(creates))
	}

	var logged string
	if err := json.Unmarshal(
		[]byte(payloadField(t, creates[1], domain.FieldPosition)), &logged); err != nil {
		t.Fatalf("decoding position: %v", err)
	}
	if logged != second.Position {
		t.Errorf("logged position %q does not match the stored position %q",
			logged, second.Position)
	}
	if logged == "" {
		t.Error("the log recorded an empty position; a derivation escaped into the log")
	}

	// And nothing intent-only leaked in: after_slot_id must not be in the payload at all.
	fields, err := creates[1].PayloadFields()
	if err != nil {
		t.Fatalf("decoding payload: %v", err)
	}
	if _, present := fields["after_slot_id"]; present {
		t.Error("the log recorded after_slot_id; derivations must never be replayable")
	}
}
