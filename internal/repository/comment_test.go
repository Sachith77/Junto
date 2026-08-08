package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/junto/junto/internal/domain"
)

// Comment repository tests.
//
// Comments have no exotic constraints of their own beyond the composite slot_id/trip_id FK
// (already proven generically by slot_options — see TestCrossTripReferencesAreRejected-style
// coverage there) and the body length CHECK. What is worth pinning here is the shape that
// makes comments append-only: no update method exists at all, and a second delete is reported
// the same idempotent way attachments report one (D46-style — no version, nothing to conflict
// over).

func TestCommentCreateGetList(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	slot := makeSlot(t, ctx, r, trip, nil, "Dinner")

	c := makeComment(t, ctx, r, slot, owner, "Should we book the earlier flight?")
	if c.CreatedAt.IsZero() || c.UpdatedAt.IsZero() {
		t.Error("created comment is missing timestamps")
	}
	if c.AuthorID == nil || *c.AuthorID != owner.ID {
		t.Errorf("comment author = %v, want %v", c.AuthorID, owner.ID)
	}

	got, err := r.comments.GetByID(ctx, c.ID)
	if err != nil {
		t.Fatalf("getting comment: %v", err)
	}
	if got.Body != c.Body {
		t.Errorf("got body %q, want %q", got.Body, c.Body)
	}

	second := makeComment(t, ctx, r, slot, owner, "Or the direct one?")
	list, err := r.comments.ListForSlot(ctx, slot.ID)
	if err != nil {
		t.Fatalf("listing comments: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("listed %d comments, want 2", len(list))
	}
	// ORDER BY created_at, id — the first comment posted must come first.
	if list[0].ID != c.ID || list[1].ID != second.ID {
		t.Errorf("comments are not in chronological order: %+v", list)
	}
}

// TestCommentCrossTripSlotIsRejected mirrors slot_options_slot_fk's guarantee for the new
// composite FK: a comment cannot claim a trip its slot does not belong to.
func TestCommentCrossTripSlotIsRejected(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	tripA := makeTrip(t, ctx, r, owner)
	tripB := makeTrip(t, ctx, r, owner)
	slot := makeSlot(t, ctx, r, tripA, nil, "Dinner")

	mustViolate(t, ctx, r, "a comment claiming the wrong trip", func(ctx context.Context) error {
		return r.comments.Create(ctx, &domain.Comment{
			ID: domain.NewID(), SlotID: slot.ID, TripID: tripB.ID, Body: "Wrong trip",
		})
	})
}

// TestCommentBodyLengthIsEnforced pins comments_body_len from the Go side, complementing the
// adversarial SQL check in tests/schema_verify.sql.
func TestCommentBodyLengthIsEnforced(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	slot := makeSlot(t, ctx, r, trip, nil, "Dinner")

	mustViolate(t, ctx, r, "an empty comment body", func(ctx context.Context) error {
		return r.comments.Create(ctx, &domain.Comment{
			ID: domain.NewID(), SlotID: slot.ID, TripID: trip.ID, Body: "",
		})
	})
}

// TestDeletingACommentIsIdempotentlyReported mirrors
// TestDeletingAnAttachmentIsIdempotentlyReported: there is no version, so a second delete is
// not a conflict, it is simply already gone.
func TestDeletingACommentIsIdempotentlyReported(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	slot := makeSlot(t, ctx, r, trip, nil, "Dinner")
	c := makeComment(t, ctx, r, slot, owner, "Delete me")

	if err := r.comments.SoftDelete(ctx, c.ID, time.Now().UTC()); err != nil {
		t.Fatalf("deleting: %v", err)
	}
	if _, err := r.comments.GetByID(ctx, c.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a deleted comment is still readable (err = %v)", err)
	}
	if err := r.comments.SoftDelete(ctx, c.ID, time.Now().UTC()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("deleting twice returned %v, want ErrNotFound", err)
	}

	// A deleted comment must also disappear from the slot's list, not just direct lookup.
	list, err := r.comments.ListForSlot(ctx, slot.ID)
	if err != nil {
		t.Fatalf("listing comments: %v", err)
	}
	for _, l := range list {
		if l.ID == c.ID {
			t.Error("a deleted comment is still returned by ListForSlot")
		}
	}
}

// TestCommentAuthorSurvivesUserDeletion mirrors slot_options.proposed_by's ON DELETE SET NULL:
// history outlives accounts (D18), so a comment from a deleted user still renders, just with
// no attributable author.
func TestCommentAuthorSurvivesUserDeletion(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	author := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	slot := makeSlot(t, ctx, r, trip, nil, "Dinner")
	c := makeComment(t, ctx, r, slot, author, "I'll bring snacks")

	tx, ok := txFromContext(ctx)
	if !ok {
		t.Fatal("expected an ambient transaction from txContext(t)")
	}
	if _, err := tx.Exec(ctx, "DELETE FROM users WHERE id = $1", author.ID); err != nil {
		t.Fatalf("hard-deleting the author: %v", err)
	}

	got, err := r.comments.GetByID(ctx, c.ID)
	if err != nil {
		t.Fatalf("getting comment after author deletion: %v", err)
	}
	if got.AuthorID != nil {
		t.Errorf("author_id survived a hard user delete: %v", *got.AuthorID)
	}
	if got.Body != c.Body {
		t.Error("the comment's own content did not survive its author's deletion")
	}
}
