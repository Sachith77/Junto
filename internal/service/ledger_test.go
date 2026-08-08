package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/junto/junto/internal/domain"
)

// Service-level tests for the two Slice 3 conflict classes.
//
// The same boundary as oplog_test.go applies: these prove the SHAPE of what a mutation
// produces — which operations, with which masks, carrying which values — and the authorization
// and validation around it. They do not prove convergence, which needs the trip row lock and
// therefore a real database and a real race.

func budgetHarness(t *testing.T) (*planningHarness, *domain.User, *domain.Trip) {
	t.Helper()
	h := newPlanningHarness(t)
	owner := h.makeUser(t, "ledger-owner@example.com")
	trip := h.makeTrip(t, owner)
	return h, owner, trip
}

// TestBudgetWriteLogsOneTotalOperation is the atomic grain, observed from the log.
//
// A budget write must appear as exactly ONE operation naming EVERY field. If it ever appeared
// as several partial operations, a client folding them would pass through a state where the
// total had changed but the splits had not — an unbalanced ledger visible on screen, even
// though the database never held one.
func TestBudgetWriteLogsOneTotalOperation(t *testing.T) {
	h, owner, trip := budgetHarness(t)
	ctx := context.Background()

	entry, err := h.budget.Create(ctx, trip.ID, owner.ID, BudgetEntryInput{
		Label: "Beach house", Category: domain.BudgetCategoryLodging, AmountMinor: 45000,
		Splits: []domain.BudgetSplit{{UserID: owner.ID, AmountMinor: 45000}},
	})
	if err != nil {
		t.Fatalf("creating the entry: %v", err)
	}

	ops := opsFor(t, h, domain.OpBudgetSet)
	if len(ops) != 1 {
		t.Fatalf("a budget create produced %d operations, want exactly 1", len(ops))
	}

	op := ops[0]
	if op.EntityID != entry.ID {
		t.Errorf("operation targets %s, want the entry %s", op.EntityID, entry.ID)
	}
	want := domain.NewFieldMask(domain.OpBudgetSet.AllowedFields()...)
	if len(op.Fields) != len(want) {
		t.Errorf("mask is %v, want the total mask %v", op.Fields, want)
	}
	for _, f := range want {
		if !op.Fields.Has(f) {
			t.Errorf("mask is missing %q; a budget operation is applied whole", f)
		}
	}
	// And the splits travel inside it, as one value.
	if got := payloadField(t, op, domain.FieldSplits); got == "null" || got == "[]" {
		t.Errorf("the operation carries no splits: %s", got)
	}
}

// TestBudgetEditWithoutAVersionIsRefused is D85 at the service boundary.
//
// Everywhere else in this system a nil version means "merge me", and that is a coherent
// instruction. For an entry replaced whole it is not: honouring it would mean overwriting
// numbers the caller never read, which is the outcome the coarse grain was chosen to prevent.
// So it is refused, and refused as a VALIDATION error naming the field, so a client is told
// what to send rather than being handed a 500.
func TestBudgetEditWithoutAVersionIsRefused(t *testing.T) {
	h, owner, trip := budgetHarness(t)
	ctx := context.Background()

	entry, err := h.budget.Create(ctx, trip.ID, owner.ID, BudgetEntryInput{
		Label: "Dinner", Category: domain.BudgetCategoryFood, AmountMinor: 5000,
	})
	if err != nil {
		t.Fatalf("creating the entry: %v", err)
	}

	_, err = h.budget.Update(ctx, trip.ID, owner.ID, entry.ID, BudgetEntryInput{
		Label: "Dinner", Category: domain.BudgetCategoryFood, AmountMinor: 9000,
		Version: nil,
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a versionless budget edit returned %v, want a validation error", err)
	}

	if err := h.budget.Delete(ctx, trip.ID, owner.ID, entry.ID, nil); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a versionless budget delete returned %v, want a validation error", err)
	}

	// Nothing was written, and nothing was logged. A refused write that still emitted an
	// operation would be strictly worse than one that succeeded.
	fresh, err := h.budget.Get(ctx, trip.ID, owner.ID, entry.ID)
	if err != nil {
		t.Fatalf("re-reading the entry: %v", err)
	}
	if fresh.AmountMinor != 5000 {
		t.Errorf("the refused edit changed the amount to %d", fresh.AmountMinor)
	}
	if n := len(opsFor(t, h, domain.OpBudgetSet)); n != 1 {
		t.Errorf("%d budget.set operations in the log, want 1 (the create)", n)
	}
}

// TestStaleBudgetEditIsAConflict — the version is not decorative. A second writer holding
// pre-edit state is told to re-read rather than silently winning.
func TestStaleBudgetEditIsAConflict(t *testing.T) {
	h, owner, trip := budgetHarness(t)
	ctx := context.Background()

	entry, err := h.budget.Create(ctx, trip.ID, owner.ID, BudgetEntryInput{
		Label: "Taxi", Category: domain.BudgetCategoryTransport, AmountMinor: 1200,
	})
	if err != nil {
		t.Fatalf("creating the entry: %v", err)
	}
	staleVersion := entry.Version

	if _, err := h.budget.Update(ctx, trip.ID, owner.ID, entry.ID, BudgetEntryInput{
		Label: "Taxi to airport", Category: domain.BudgetCategoryTransport, AmountMinor: 1500,
		Version: ptr(staleVersion),
	}); err != nil {
		t.Fatalf("the first edit should have succeeded: %v", err)
	}

	_, err = h.budget.Update(ctx, trip.ID, owner.ID, entry.ID, BudgetEntryInput{
		Label: "Taxi", Category: domain.BudgetCategoryTransport, AmountMinor: 9000,
		Version: ptr(staleVersion),
	})
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("a stale budget edit returned %v, want ErrVersionConflict", err)
	}
}

// TestUnbalancedSplitsAreRejectedBeforeTheDatabase. The deferred trigger is the backstop, not
// the user interface: a caller should get a field-level message naming `splits`, not a
// constraint failure at commit.
func TestUnbalancedSplitsAreRejectedBeforeTheDatabase(t *testing.T) {
	h, owner, trip := budgetHarness(t)
	ctx := context.Background()

	_, err := h.budget.Create(ctx, trip.ID, owner.ID, BudgetEntryInput{
		Label: "Does not add up", Category: domain.BudgetCategoryOther, AmountMinor: 1000,
		Splits: []domain.BudgetSplit{{UserID: owner.ID, AmountMinor: 600}},
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("an unbalanced split set returned %v, want a validation error", err)
	}
	if n := len(opsFor(t, h, domain.OpBudgetSet)); n != 0 {
		t.Errorf("%d operations were logged for a rejected write, want 0", n)
	}
}

// TestSplitsMustNameTripMembers closes a gap the schema cannot.
//
// budget_splits.user_id references users, not trip_members, so the database will happily record
// that a stranger owes money on a trip they have nothing to do with. This is the check that
// makes that unrepresentable in practice.
func TestSplitsMustNameTripMembers(t *testing.T) {
	h, owner, trip := budgetHarness(t)
	ctx := context.Background()
	stranger := h.makeUser(t, "stranger@example.com")

	_, err := h.budget.Create(ctx, trip.ID, owner.ID, BudgetEntryInput{
		Label: "Who is this", Category: domain.BudgetCategoryOther, AmountMinor: 100,
		Splits: []domain.BudgetSplit{{UserID: stranger.ID, AmountMinor: 100}},
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a split naming a non-member returned %v, want a validation error", err)
	}
}

// TestViewerCannotTouchTheLedger. CapManageBudget is deliberately separate from
// CapProposeOptions: "may suggest where we stay" and "may edit who owes what" are different
// levels of trust.
func TestViewerCannotTouchTheLedger(t *testing.T) {
	h, _, trip := budgetHarness(t)
	ctx := context.Background()

	viewer := h.makeUser(t, "viewer@example.com")
	if err := h.membersFake.Add(ctx, &domain.Member{
		ID: domain.NewID(), TripID: trip.ID, UserID: viewer.ID, Role: domain.RoleViewer,
	}); err != nil {
		t.Fatalf("adding viewer: %v", err)
	}

	_, err := h.budget.Create(ctx, trip.ID, viewer.ID, BudgetEntryInput{
		Label: "Sneaky", Category: domain.BudgetCategoryOther, AmountMinor: 1,
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a viewer creating a ledger entry got %v, want ErrForbidden", err)
	}

	// But they can read it — viewers are on the trip, not locked out of it.
	if _, err := h.budget.List(ctx, trip.ID, viewer.ID); err != nil {
		t.Errorf("a viewer could not read the ledger: %v", err)
	}
}

// TestBudgetEntryFromAnotherTripIsNotFound is the D54 trip-scoping guard. The capability was
// checked against the trip in the URL; the entry id in the path could belong to another trip
// entirely.
func TestBudgetEntryFromAnotherTripIsNotFound(t *testing.T) {
	h, owner, tripA := budgetHarness(t)
	ctx := context.Background()
	tripB := h.makeTrip(t, owner)

	entryB, err := h.budget.Create(ctx, tripB.ID, owner.ID, BudgetEntryInput{
		Label: "Other trip", Category: domain.BudgetCategoryOther, AmountMinor: 100,
	})
	if err != nil {
		t.Fatalf("creating in trip B: %v", err)
	}

	// The caller owns BOTH trips, so this is purely the scoping check — not authorization.
	_, err = h.budget.Update(ctx, tripA.ID, owner.ID, entryB.ID, BudgetEntryInput{
		Label: "Hijacked", Category: domain.BudgetCategoryOther, AmountMinor: 1,
		Version: ptr(entryB.Version),
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("editing another trip's entry through trip A returned %v, want ErrNotFound", err)
	}
}

// TestBudgetDeleteLogsATombstone — the delete names only deleted_at, like every other tombstone
// in the system, so a fold learns the entry is gone without losing what it was.
func TestBudgetDeleteLogsATombstone(t *testing.T) {
	h, owner, trip := budgetHarness(t)
	ctx := context.Background()

	entry, err := h.budget.Create(ctx, trip.ID, owner.ID, BudgetEntryInput{
		Label: "Cancelled", Category: domain.BudgetCategoryActivity, AmountMinor: 2000,
	})
	if err != nil {
		t.Fatalf("creating: %v", err)
	}
	if err := h.budget.Delete(ctx, trip.ID, owner.ID, entry.ID, ptr(entry.Version)); err != nil {
		t.Fatalf("deleting: %v", err)
	}

	ops := opsFor(t, h, domain.OpBudgetDelete)
	if len(ops) != 1 {
		t.Fatalf("%d budget.delete operations, want 1", len(ops))
	}
	if len(ops[0].Fields) != 1 || !ops[0].Fields.Has(domain.FieldDeletedAt) {
		t.Errorf("delete mask is %v, want just deleted_at", ops[0].Fields)
	}
	if got := payloadField(t, ops[0], domain.FieldDeletedAt); got == "null" {
		t.Error("the tombstone operation carries a null deleted_at")
	}
}

// --- attachments ---

func attachmentFixture(t *testing.T, h *planningHarness, owner *domain.User, trip *domain.Trip) *domain.Slot {
	t.Helper()
	slot, err := h.slots.Create(context.Background(), trip.ID, owner.ID, CreateSlotInput{
		Kind: domain.SlotKindLodging, Title: "Where are we staying",
	})
	if err != nil {
		t.Fatalf("creating slot: %v", err)
	}
	return slot
}

// TestPendingUploadIsNotAnnounced is the reason attachment.add is written at confirmation
// rather than at presign.
//
// A pending row may never become anything — abandoned uploads are the normal failure mode of a
// presigned design. Announcing one would tell the room about a photo that may not arrive, and
// then require retracting it with a removal for an entity no client ever saw.
func TestPendingUploadIsNotAnnounced(t *testing.T) {
	h, owner, trip := budgetHarness(t)
	ctx := context.Background()
	slot := attachmentFixture(t, h, owner, trip)

	ticket, err := h.files.RequestUpload(ctx, trip.ID, owner.ID, RequestUploadInput{
		Owner:       domain.AttachmentOwner{SlotID: &slot.ID},
		ContentType: "image/png", OriginalName: "booking.png",
	})
	if err != nil {
		t.Fatalf("requesting an upload: %v", err)
	}
	if ticket.UploadURL == "" {
		t.Error("no upload URL was returned")
	}
	if ticket.Attachment.Status != domain.AttachmentStatusPending {
		t.Errorf("a reserved attachment is %q, want pending", ticket.Attachment.Status)
	}
	if n := len(opsFor(t, h, domain.OpAttachmentAdd)); n != 0 {
		t.Errorf("%d attachment.add operations after presigning, want 0", n)
	}

	// The browser's PUT lands, and only now is it announced.
	h.storageFake.putObject(ticket.Attachment.StorageKey, 4096, "image/png")
	confirmed, err := h.files.ConfirmUpload(ctx, trip.ID, owner.ID, ticket.Attachment.ID)
	if err != nil {
		t.Fatalf("confirming: %v", err)
	}
	if !confirmed.IsReady() {
		t.Errorf("status after confirmation is %q, want ready", confirmed.Status)
	}

	ops := opsFor(t, h, domain.OpAttachmentAdd)
	if len(ops) != 1 {
		t.Fatalf("%d attachment.add operations after confirmation, want 1", len(ops))
	}
	// Built from the persisted row (D62): the size in the log is the one the STORAGE reported.
	if got := payloadField(t, ops[0], domain.FieldSizeBytes); got != "4096" {
		t.Errorf("logged size is %s, want 4096", got)
	}
	if got := payloadField(t, ops[0], domain.FieldStatus); got != `"ready"` {
		t.Errorf("logged status is %s, want ready", got)
	}
}

// TestOversizedUploadIsRejectedAndItsObjectDeleted is the compensating control for the whole
// presigned design.
//
// A presigned PUT carries no size limit, so without this check any member holding an upload URL
// has an unbounded write primitive against the bucket. The row is marked failed AND the object
// is removed — leaving it would mean the limit was reported but not enforced.
func TestOversizedUploadIsRejectedAndItsObjectDeleted(t *testing.T) {
	h, owner, trip := budgetHarness(t)
	ctx := context.Background()
	slot := attachmentFixture(t, h, owner, trip)

	ticket, err := h.files.RequestUpload(ctx, trip.ID, owner.ID, RequestUploadInput{
		Owner:       domain.AttachmentOwner{SlotID: &slot.ID},
		ContentType: "image/png", OriginalName: "enormous.png",
	})
	if err != nil {
		t.Fatalf("requesting an upload: %v", err)
	}

	h.storageFake.putObject(ticket.Attachment.StorageKey, domain.MaxAttachmentBytes+1, "image/png")

	_, err = h.files.ConfirmUpload(ctx, trip.ID, owner.ID, ticket.Attachment.ID)
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("confirming an oversized upload returned %v, want a validation error", err)
	}
	if !h.storageFake.wasDeleted(ticket.Attachment.StorageKey) {
		t.Error("the oversized object was left in storage")
	}
	if n := len(opsFor(t, h, domain.OpAttachmentAdd)); n != 0 {
		t.Errorf("%d attachment.add operations for a rejected upload, want 0", n)
	}
}

// TestConfirmingAnUploadThatNeverArrivedFails — the server trusts the storage, not the client.
func TestConfirmingAnUploadThatNeverArrivedFails(t *testing.T) {
	h, owner, trip := budgetHarness(t)
	ctx := context.Background()
	slot := attachmentFixture(t, h, owner, trip)

	ticket, err := h.files.RequestUpload(ctx, trip.ID, owner.ID, RequestUploadInput{
		Owner:       domain.AttachmentOwner{SlotID: &slot.ID},
		ContentType: "image/png", OriginalName: "never-sent.png",
	})
	if err != nil {
		t.Fatalf("requesting an upload: %v", err)
	}

	// No putObject: the client asked for a URL and never used it.
	if _, err := h.files.ConfirmUpload(ctx, trip.ID, owner.ID, ticket.Attachment.ID); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("confirming a non-existent object returned %v, want a validation error", err)
	}
}

// TestConfirmationIsIdempotent. A client that lost the response and retried must get the same
// answer, not an error about a state it already reached — and the recorded size must not change.
func TestConfirmationIsIdempotent(t *testing.T) {
	h, owner, trip := budgetHarness(t)
	ctx := context.Background()
	slot := attachmentFixture(t, h, owner, trip)

	ticket, err := h.files.RequestUpload(ctx, trip.ID, owner.ID, RequestUploadInput{
		Owner:       domain.AttachmentOwner{SlotID: &slot.ID},
		ContentType: "image/png", OriginalName: "retry.png",
	})
	if err != nil {
		t.Fatalf("requesting an upload: %v", err)
	}
	h.storageFake.putObject(ticket.Attachment.StorageKey, 1024, "image/png")

	first, err := h.files.ConfirmUpload(ctx, trip.ID, owner.ID, ticket.Attachment.ID)
	if err != nil {
		t.Fatalf("first confirmation: %v", err)
	}
	second, err := h.files.ConfirmUpload(ctx, trip.ID, owner.ID, ticket.Attachment.ID)
	if err != nil {
		t.Fatalf("second confirmation: %v", err)
	}

	if *first.SizeBytes != *second.SizeBytes {
		t.Errorf("a repeated confirmation changed the size from %d to %d", *first.SizeBytes, *second.SizeBytes)
	}
	// And it did not announce the same upload twice.
	if n := len(opsFor(t, h, domain.OpAttachmentAdd)); n != 1 {
		t.Errorf("%d attachment.add operations after two confirmations, want 1", n)
	}
}

// TestTwoConcurrentUploadsBothSurvive is the entire attachment conflict story, at the service
// level: there is no merge, so both simply exist.
func TestTwoConcurrentUploadsBothSurvive(t *testing.T) {
	h, owner, trip := budgetHarness(t)
	ctx := context.Background()
	slot := attachmentFixture(t, h, owner, trip)

	for _, name := range []string{"first.png", "second.png"} {
		ticket, err := h.files.RequestUpload(ctx, trip.ID, owner.ID, RequestUploadInput{
			Owner:       domain.AttachmentOwner{SlotID: &slot.ID},
			ContentType: "image/png", OriginalName: name,
		})
		if err != nil {
			t.Fatalf("requesting %s: %v", name, err)
		}
		h.storageFake.putObject(ticket.Attachment.StorageKey, 2048, "image/png")
		if _, err := h.files.ConfirmUpload(ctx, trip.ID, owner.ID, ticket.Attachment.ID); err != nil {
			t.Fatalf("confirming %s: %v", name, err)
		}
	}

	live, err := h.files.ListForOwner(ctx, trip.ID, owner.ID, domain.AttachmentOwner{SlotID: &slot.ID})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(live) != 2 {
		t.Fatalf("%d attachments on the slot, want 2 — an upload must not displace another", len(live))
	}
	// Distinct object keys, derived from the attachment id rather than the filename, so two
	// uploads of the same file name cannot collide.
	if live[0].StorageKey == live[1].StorageKey {
		t.Error("two attachments share one storage key")
	}
}

// TestStorageKeyIgnoresTheClientsFilename. A key built from user input is a path-traversal and
// collision surface; the original name belongs in a column, where it is data rather than a path.
func TestStorageKeyIgnoresTheClientsFilename(t *testing.T) {
	h, owner, trip := budgetHarness(t)
	ctx := context.Background()
	slot := attachmentFixture(t, h, owner, trip)

	ticket, err := h.files.RequestUpload(ctx, trip.ID, owner.ID, RequestUploadInput{
		Owner:       domain.AttachmentOwner{SlotID: &slot.ID},
		ContentType: "image/png",
		// The classic payload. If any of it reaches the key, the key is built from user input.
		OriginalName: "../../../etc/passwd",
	})
	if err != nil {
		t.Fatalf("requesting an upload: %v", err)
	}
	key := ticket.Attachment.StorageKey
	for _, bad := range []string{"..", "etc", "passwd"} {
		if strings.Contains(key, bad) {
			t.Errorf("storage key %q contains %q from the client's filename", key, bad)
		}
	}
	if ticket.Attachment.OriginalName != "../../../etc/passwd" {
		t.Error("the original name was not preserved as data")
	}
}

// TestAttachmentOwnerMustBelongToTheTrip is the D54 guard for the exclusive arc. The arc
// guarantees exactly one owner; it guarantees nothing about whose trip that owner is in.
func TestAttachmentOwnerMustBelongToTheTrip(t *testing.T) {
	h, owner, tripA := budgetHarness(t)
	ctx := context.Background()
	tripB := h.makeTrip(t, owner)
	slotB := attachmentFixture(t, h, owner, tripB)

	_, err := h.files.RequestUpload(ctx, tripA.ID, owner.ID, RequestUploadInput{
		Owner:       domain.AttachmentOwner{SlotID: &slotB.ID},
		ContentType: "image/png", OriginalName: "wrong-trip.png",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("attaching to another trip's slot returned %v, want ErrNotFound", err)
	}
}

// TestLinkAttachmentIsReadyAndAnnouncedImmediately — there is nothing to upload, so the
// two-phase lifecycle does not apply and the announcement happens at creation.
func TestLinkAttachmentIsReadyAndAnnouncedImmediately(t *testing.T) {
	h, owner, trip := budgetHarness(t)
	ctx := context.Background()
	slot := attachmentFixture(t, h, owner, trip)

	link, err := h.files.CreateLink(ctx, trip.ID, owner.ID, CreateLinkInput{
		Owner: domain.AttachmentOwner{SlotID: &slot.ID},
		URL:   "https://example.test/booking", Title: "Booking confirmation",
	})
	if err != nil {
		t.Fatalf("creating a link: %v", err)
	}
	if !link.IsReady() {
		t.Errorf("a link is %q, want ready on creation", link.Status)
	}
	if n := len(opsFor(t, h, domain.OpAttachmentAdd)); n != 1 {
		t.Fatalf("%d attachment.add operations for a link, want 1", n)
	}

	url, err := h.files.DownloadURL(ctx, trip.ID, owner.ID, link.ID)
	if err != nil {
		t.Fatalf("resolving a link's URL: %v", err)
	}
	if url != "https://example.test/booking" {
		t.Errorf("a link resolved to %q, want its own URL", url)
	}
}

// TestDeletingAnAttachmentKeepsTheObject.
//
// A soft delete is reversible everywhere else in this system; deleting the bytes would make
// this the one tombstone that destroys data, and would break any download already in flight.
// Reclaiming objects is the sweeper's job, on its own schedule.
func TestDeletingAnAttachmentKeepsTheObject(t *testing.T) {
	h, owner, trip := budgetHarness(t)
	ctx := context.Background()
	slot := attachmentFixture(t, h, owner, trip)

	ticket, err := h.files.RequestUpload(ctx, trip.ID, owner.ID, RequestUploadInput{
		Owner:       domain.AttachmentOwner{SlotID: &slot.ID},
		ContentType: "image/png", OriginalName: "keepme.png",
	})
	if err != nil {
		t.Fatalf("requesting an upload: %v", err)
	}
	h.storageFake.putObject(ticket.Attachment.StorageKey, 1024, "image/png")
	if _, err := h.files.ConfirmUpload(ctx, trip.ID, owner.ID, ticket.Attachment.ID); err != nil {
		t.Fatalf("confirming: %v", err)
	}

	if err := h.files.Delete(ctx, trip.ID, owner.ID, ticket.Attachment.ID); err != nil {
		t.Fatalf("deleting: %v", err)
	}

	if !h.storageFake.has(ticket.Attachment.StorageKey) {
		t.Error("a soft delete destroyed the stored object")
	}
	ops := opsFor(t, h, domain.OpAttachmentRemove)
	if len(ops) != 1 {
		t.Fatalf("%d attachment.remove operations, want 1", len(ops))
	}
	if len(ops[0].Fields) != 1 || !ops[0].Fields.Has(domain.FieldDeletedAt) {
		t.Errorf("removal mask is %v, want just deleted_at", ops[0].Fields)
	}
}

// TestDeletingAPendingUploadAnnouncesNothing.
//
// It was never announced, so there is nothing to retract — and a removal operation targeting an
// entity no client has ever seen is exactly what a fold rejects as an unknown entity. This is
// the asymmetry that makes "announce on becoming visible" a complete rule rather than a partial
// one.
func TestDeletingAPendingUploadAnnouncesNothing(t *testing.T) {
	h, owner, trip := budgetHarness(t)
	ctx := context.Background()
	slot := attachmentFixture(t, h, owner, trip)

	ticket, err := h.files.RequestUpload(ctx, trip.ID, owner.ID, RequestUploadInput{
		Owner:       domain.AttachmentOwner{SlotID: &slot.ID},
		ContentType: "image/png", OriginalName: "abandoned.png",
	})
	if err != nil {
		t.Fatalf("requesting an upload: %v", err)
	}

	if err := h.files.Delete(ctx, trip.ID, owner.ID, ticket.Attachment.ID); err != nil {
		t.Fatalf("deleting a pending attachment: %v", err)
	}
	if n := len(opsFor(t, h, domain.OpAttachmentRemove)); n != 0 {
		t.Errorf("%d attachment.remove operations for a never-announced upload, want 0", n)
	}
}

// TestViewerCannotUpload — CapUploadAttachments, like CapManageBudget, is not granted to
// viewers.
func TestViewerCannotUpload(t *testing.T) {
	h, owner, trip := budgetHarness(t)
	ctx := context.Background()
	slot := attachmentFixture(t, h, owner, trip)

	viewer := h.makeUser(t, "viewer-upload@example.com")
	if err := h.membersFake.Add(ctx, &domain.Member{
		ID: domain.NewID(), TripID: trip.ID, UserID: viewer.ID, Role: domain.RoleViewer,
	}); err != nil {
		t.Fatalf("adding viewer: %v", err)
	}

	_, err := h.files.RequestUpload(ctx, trip.ID, viewer.ID, RequestUploadInput{
		Owner:       domain.AttachmentOwner{SlotID: &slot.ID},
		ContentType: "image/png", OriginalName: "nope.png",
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a viewer requesting an upload got %v, want ErrForbidden", err)
	}
}
