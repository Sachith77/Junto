package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/junto/junto/internal/domain"
)

// Attachment repository tests.
//
// Two things carry real risk here and both are the database's job: the exclusive arc (exactly
// one owner, expressed as three real FKs plus num_nonnulls = 1) and the two-phase upload's
// pending -> ready transition. Everything else about this entity is deliberately trivial,
// because attachments have no conflict resolution at all (D46).

func makeFileAttachment(t *testing.T, ctx context.Context, r repos, trip *domain.Trip, owner domain.AttachmentOwner, key string) *domain.Attachment {
	t.Helper()
	a := &domain.Attachment{
		ID:            domain.NewID(),
		TripID:        trip.ID,
		SlotOptionID:  owner.SlotOptionID,
		BudgetEntryID: owner.BudgetEntryID,
		SlotID:        owner.SlotID,
		Kind:          domain.AttachmentKindFile,
		Status:        domain.AttachmentStatusPending,
		StorageKey:    key,
		ContentType:   "image/png",
		OriginalName:  "booking.png",
	}
	if err := r.attachments.Create(ctx, a); err != nil {
		t.Fatalf("creating attachment %q: %v", key, err)
	}
	return a
}

// TestAttachmentExclusiveArcIsEnforced is the reason the schema uses three real foreign keys
// rather than a polymorphic (owner_type, owner_id) pair (D47).
//
// A polymorphic reference cannot be enforced by Postgres at all — it would be two columns and a
// promise. This proves the promise is instead a constraint: neither zero owners nor two owners
// can be stored.
func TestAttachmentExclusiveArcIsEnforced(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	slot := makeSlot(t, ctx, r, trip, nil, "Hotel")
	option := makeOption(t, ctx, r, slot, "Taj")

	t.Run("no owner", func(t *testing.T) {
		mustViolate(t, ctx, r, "an attachment with no owner", func(ctx context.Context) error {
			return r.attachments.Create(ctx, &domain.Attachment{
				ID: domain.NewID(), TripID: trip.ID,
				Kind: domain.AttachmentKindFile, Status: domain.AttachmentStatusPending,
				StorageKey: "orphan-" + domain.NewID().String(),
			})
		})
	})

	t.Run("two owners", func(t *testing.T) {
		mustViolate(t, ctx, r, "an attachment with two owners", func(ctx context.Context) error {
			return r.attachments.Create(ctx, &domain.Attachment{
				ID: domain.NewID(), TripID: trip.ID,
				SlotOptionID: &option.ID, SlotID: &slot.ID,
				Kind: domain.AttachmentKindFile, Status: domain.AttachmentStatusPending,
				StorageKey: "two-owners-" + domain.NewID().String(),
			})
		})
	})

	t.Run("exactly one owner", func(t *testing.T) {
		a := makeFileAttachment(t, ctx, r, trip,
			domain.AttachmentOwner{SlotOptionID: &option.ID}, "ok-"+domain.NewID().String())
		if a.OwnerCount() != 1 {
			t.Errorf("stored attachment reports %d owners", a.OwnerCount())
		}
	})
}

// TestAttachmentShapeMatchesItsKind pins attachments_shape: a file has an object key and no
// URL, a link has a URL, no key, and is ready on arrival because there is nothing to upload.
func TestAttachmentShapeMatchesItsKind(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	slot := makeSlot(t, ctx, r, trip, nil, "Hotel")

	t.Run("a link is ready immediately", func(t *testing.T) {
		a := &domain.Attachment{
			ID: domain.NewID(), TripID: trip.ID, SlotID: &slot.ID,
			Kind: domain.AttachmentKindLink, Status: domain.AttachmentStatusReady,
			ExternalURL: "https://example.test/booking",
		}
		if err := r.attachments.Create(ctx, a); err != nil {
			t.Fatalf("creating a link attachment: %v", err)
		}
		if !a.IsReady() {
			t.Error("a link attachment is not ready on creation")
		}
	})

	// Both violations run in their own savepoint. Without that the first one aborts the test
	// transaction and the second gets SQLSTATE 25P02 — an error, so the assertion still passes,
	// but for a reason that has nothing to do with attachments_shape. That is the shape of a
	// test that passes without testing the thing it names.
	t.Run("a pending link is refused", func(t *testing.T) {
		mustViolate(t, ctx, r, "a link stored as pending", func(ctx context.Context) error {
			return r.attachments.Create(ctx, &domain.Attachment{
				ID: domain.NewID(), TripID: trip.ID, SlotID: &slot.ID,
				Kind: domain.AttachmentKindLink, Status: domain.AttachmentStatusPending,
				ExternalURL: "https://example.test/pending",
			})
		})
	})

	t.Run("a file carrying a URL is refused", func(t *testing.T) {
		mustViolate(t, ctx, r, "a file attachment with an external URL", func(ctx context.Context) error {
			return r.attachments.Create(ctx, &domain.Attachment{
				ID: domain.NewID(), TripID: trip.ID, SlotID: &slot.ID,
				Kind: domain.AttachmentKindFile, Status: domain.AttachmentStatusPending,
				StorageKey: "key-" + domain.NewID().String(), ExternalURL: "https://example.test/x",
			})
		})
	})
}

// TestTwoPhaseUploadConfirmation walks the lifecycle the presigned-PUT design forces on us: the
// API never sees the bytes, so size and checksum can only be recorded after the object lands
// and the server has stat'd it (D48).
func TestTwoPhaseUploadConfirmation(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	slot := makeSlot(t, ctx, r, trip, nil, "Hotel")

	a := makeFileAttachment(t, ctx, r, trip,
		domain.AttachmentOwner{SlotID: &slot.ID}, "trips/x/"+domain.NewID().String())
	if a.IsReady() {
		t.Fatal("a freshly created file attachment is already ready")
	}

	checksum := randomHash()
	confirmed, err := r.attachments.ConfirmReturning(ctx, a.ID, 4096, checksum, time.Now().UTC())
	if err != nil {
		t.Fatalf("confirming: %v", err)
	}
	if !confirmed.IsReady() {
		t.Errorf("status after confirmation is %q, want ready", confirmed.Status)
	}
	if confirmed.SizeBytes == nil || *confirmed.SizeBytes != 4096 {
		t.Errorf("size after confirmation is %v, want 4096", confirmed.SizeBytes)
	}
	if len(confirmed.ChecksumSHA256) != 32 {
		t.Errorf("checksum is %d bytes, want 32", len(confirmed.ChecksumSHA256))
	}

	// A second confirmation matches no pending row. That is what makes a retried confirm safe:
	// it cannot overwrite the recorded size with a later, different observation.
	if _, err := r.attachments.ConfirmReturning(ctx, a.ID, 999, checksum, time.Now().UTC()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("re-confirming returned %v, want ErrNotFound", err)
	}
	fresh, err := r.attachments.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if fresh.SizeBytes == nil || *fresh.SizeBytes != 4096 {
		t.Errorf("a re-confirmation changed the recorded size to %v", fresh.SizeBytes)
	}
}

// TestOneStorageObjectHasOneAttachment pins attachments_storage_key_uq. Two rows pointing at
// one stored object would make deletion ambiguous — removing either would break the other.
func TestOneStorageObjectHasOneAttachment(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	slot := makeSlot(t, ctx, r, trip, nil, "Hotel")

	key := "trips/shared/" + domain.NewID().String()
	makeFileAttachment(t, ctx, r, trip, domain.AttachmentOwner{SlotID: &slot.ID}, key)

	mustViolate(t, ctx, r, "a second attachment on one object key", func(ctx context.Context) error {
		return r.attachments.Create(ctx, &domain.Attachment{
			ID: domain.NewID(), TripID: trip.ID, SlotID: &slot.ID,
			Kind: domain.AttachmentKindFile, Status: domain.AttachmentStatusPending,
			StorageKey: key,
		})
	})
}

// TestManyLinksCoexistDespiteTheEmptyStorageKey checks the uniqueness index stayed PARTIAL.
//
// Links have no storage key, so every one of them carries the empty string. A non-partial
// unique index would allow exactly one link per trip and nobody would notice until the second
// one failed in production.
func TestManyLinksCoexistDespiteTheEmptyStorageKey(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	slot := makeSlot(t, ctx, r, trip, nil, "Hotel")

	for i := 0; i < 3; i++ {
		a := &domain.Attachment{
			ID: domain.NewID(), TripID: trip.ID, SlotID: &slot.ID,
			Kind: domain.AttachmentKindLink, Status: domain.AttachmentStatusReady,
			ExternalURL: "https://example.test/" + domain.NewID().String(),
		}
		if err := r.attachments.Create(ctx, a); err != nil {
			t.Fatalf("creating link %d: %v", i, err)
		}
	}
}

// TestListForOwnerIsScopedToOneEntity — an attachment on a slot must not appear under one of
// that slot's options, and vice versa.
func TestListForOwnerIsScopedToOneEntity(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	slot := makeSlot(t, ctx, r, trip, nil, "Hotel")
	option := makeOption(t, ctx, r, slot, "Taj")

	onSlot := makeFileAttachment(t, ctx, r, trip,
		domain.AttachmentOwner{SlotID: &slot.ID}, "slot-"+domain.NewID().String())
	onOption := makeFileAttachment(t, ctx, r, trip,
		domain.AttachmentOwner{SlotOptionID: &option.ID}, "option-"+domain.NewID().String())

	slotAttachments, err := r.attachments.ListForOwner(ctx, domain.AttachmentOwner{SlotID: &slot.ID})
	if err != nil {
		t.Fatalf("listing by slot: %v", err)
	}
	if len(slotAttachments) != 1 || slotAttachments[0].ID != onSlot.ID {
		t.Errorf("slot listing returned %d rows, want just the slot's own", len(slotAttachments))
	}

	optionAttachments, err := r.attachments.ListForOwner(ctx, domain.AttachmentOwner{SlotOptionID: &option.ID})
	if err != nil {
		t.Fatalf("listing by option: %v", err)
	}
	if len(optionAttachments) != 1 || optionAttachments[0].ID != onOption.ID {
		t.Errorf("option listing returned %d rows, want just the option's own", len(optionAttachments))
	}

	// Two owners in the query struct is a caller bug, and it is refused rather than resolved by
	// precedence — the Go-side mirror of the exclusive arc.
	if _, err := r.attachments.ListForOwner(ctx, domain.AttachmentOwner{
		SlotID: &slot.ID, SlotOptionID: &option.ID,
	}); err == nil {
		t.Error("listing accepted two owners")
	}
}

// TestSweeperFindsOnlyAbandonedUploads. The sweeper deletes storage objects, so a bug that
// widened this query would delete files people are still using.
func TestSweeperFindsOnlyAbandonedUploads(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	slot := makeSlot(t, ctx, r, trip, nil, "Hotel")

	stale := makeFileAttachment(t, ctx, r, trip,
		domain.AttachmentOwner{SlotID: &slot.ID}, "stale-"+domain.NewID().String())
	confirmed := makeFileAttachment(t, ctx, r, trip,
		domain.AttachmentOwner{SlotID: &slot.ID}, "done-"+domain.NewID().String())
	if err := r.attachments.Confirm(ctx, confirmed.ID, 10, randomHash(), time.Now().UTC()); err != nil {
		t.Fatalf("confirming: %v", err)
	}

	found, err := r.attachments.ListStalePending(ctx, time.Now().UTC().Add(time.Minute), 100)
	if err != nil {
		t.Fatalf("listing stale pending: %v", err)
	}

	var sawStale, sawConfirmed bool
	for _, a := range found {
		switch a.ID {
		case stale.ID:
			sawStale = true
		case confirmed.ID:
			sawConfirmed = true
		}
	}
	if !sawStale {
		t.Error("the sweeper missed an abandoned pending upload")
	}
	if sawConfirmed {
		t.Error("the sweeper would delete a confirmed attachment's object")
	}

	// Nothing is stale yet if the cutoff predates everything.
	none, err := r.attachments.ListStalePending(ctx, time.Now().UTC().Add(-time.Hour), 100)
	if err != nil {
		t.Fatalf("listing with an early cutoff: %v", err)
	}
	for _, a := range none {
		if a.ID == stale.ID {
			t.Error("the cutoff was ignored")
		}
	}
}

// TestDeletingAnAttachmentIsIdempotentlyReported. There is no version, so a second delete is
// not a conflict — it is simply already gone, and the caller needs to be able to tell.
func TestDeletingAnAttachmentIsIdempotentlyReported(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	slot := makeSlot(t, ctx, r, trip, nil, "Hotel")

	a := makeFileAttachment(t, ctx, r, trip,
		domain.AttachmentOwner{SlotID: &slot.ID}, "gone-"+domain.NewID().String())

	if err := r.attachments.SoftDelete(ctx, a.ID, time.Now().UTC()); err != nil {
		t.Fatalf("deleting: %v", err)
	}
	if _, err := r.attachments.GetByID(ctx, a.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a deleted attachment is still readable (err = %v)", err)
	}
	if err := r.attachments.SoftDelete(ctx, a.ID, time.Now().UTC()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("deleting twice returned %v, want ErrNotFound", err)
	}
}
