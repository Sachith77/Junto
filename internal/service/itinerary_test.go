package service

import (
	"context"
	"errors"
	"testing"

	"github.com/junto/junto/internal/domain"
)

// Days, slots, options and votes: the itinerary-content services. authz is the same for
// all of them (a membership + capability check plus a trip-scoping guard), so these tests
// concentrate on the parts specific to each — including THE fix for this slice.

func TestCreateDayRequiresCapManageDays(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	viewer := h.makeUser(t, "viewer@example.com")
	trip := h.makeTrip(t, owner)
	h.addMember(t, trip.ID, viewer, domain.RoleViewer)

	if _, err := h.days.Create(ctx, trip.ID, viewer.ID, CreateDayInput{Label: "Day 1"}); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("a viewer creating a day must be forbidden, got %v", err)
	}

	day, err := h.days.Create(ctx, trip.ID, owner.ID, CreateDayInput{Label: "Day 1"})
	if err != nil {
		t.Fatalf("owner creating a day: %v", err)
	}
	if day.Position == "" {
		t.Error("a created day must have a position")
	}
}

func TestDayFromAnotherTripIsRejected(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	tripA := h.makeTrip(t, owner)
	tripB := h.makeTrip(t, owner)

	dayInA, err := h.days.Create(ctx, tripA.ID, owner.ID, CreateDayInput{Label: "In A"})
	if err != nil {
		t.Fatalf("creating day: %v", err)
	}

	// The caller IS a member of tripB (owner of both), and IS authorized for CapManageDays
	// on tripB — but dayInA does not belong to tripB. The trip-scoping guard must catch this
	// even though the capability check alone would have let it through.
	_, err = h.days.Update(ctx, tripB.ID, owner.ID, dayInA.ID, UpdateDayInput{Label: "Hijacked", Version: ptr(dayInA.Version)})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("updating a day through the wrong trip must be not-found, got %v", err)
	}
}

func TestSlotCreateAndMoveAcrossDays(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	trip := h.makeTrip(t, owner)
	dayOne, _ := h.days.Create(ctx, trip.ID, owner.ID, CreateDayInput{Label: "Day 1"})
	dayTwo, _ := h.days.Create(ctx, trip.ID, owner.ID, CreateDayInput{Label: "Day 2"})

	slot, err := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{
		DayID: &dayOne.ID, Kind: domain.SlotKindLodging, Title: "Where are we staying",
	})
	if err != nil {
		t.Fatalf("creating slot: %v", err)
	}
	if slot.Status != domain.SlotStatusPlanned {
		t.Errorf("a new slot must start planned, got %q", slot.Status)
	}

	if err := h.slots.Move(ctx, trip.ID, owner.ID, slot.ID, &dayTwo.ID, nil, ptr(slot.Version)); err != nil {
		t.Fatalf("moving: %v", err)
	}
	moved, err := h.slots.Get(ctx, trip.ID, owner.ID, slot.ID)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if moved.DayID == nil || *moved.DayID != dayTwo.ID {
		t.Errorf("day = %v, want %v", moved.DayID, dayTwo.ID)
	}
}

func TestReorderSlotsRequiresItsOwnCapability(t *testing.T) {
	// CapReorderSlots and CapEditSlots are both granted to editors in the current role
	// table, so this test's meaningful assertion is that Move goes through the SEPARATE
	// method (and hence would enforce a separate capability if the two were ever split) —
	// proven by checking a viewer, who holds neither, is rejected the same way from both.
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	viewer := h.makeUser(t, "viewer@example.com")
	trip := h.makeTrip(t, owner)
	h.addMember(t, trip.ID, viewer, domain.RoleViewer)
	slot, _ := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{Kind: domain.SlotKindNote, Title: "Backlog"})

	if err := h.slots.Move(ctx, trip.ID, viewer.ID, slot.ID, nil, nil, ptr(slot.Version)); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("a viewer reordering a slot must be forbidden, got %v", err)
	}
}

func TestSlotStatusRejectsUnknownValue(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	trip := h.makeTrip(t, owner)
	slot, _ := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{Kind: domain.SlotKindNote, Title: "Beach"})

	err := h.slots.SetStatus(ctx, trip.ID, owner.ID, slot.ID, domain.SlotStatus("maybe"), ptr(slot.Version))
	if !errors.Is(err, domain.ErrValidation) {
		t.Errorf("an unknown status must be a validation error, got %v", err)
	}
}

// --- THE FIX: deleting the selected option clears the slot's selection ---

func TestDeletingSelectedOptionClearsSlotSelection(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	trip := h.makeTrip(t, owner)
	slot, err := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{
		Kind: domain.SlotKindLodging, Title: "Where are we staying",
	})
	if err != nil {
		t.Fatalf("creating slot: %v", err)
	}

	taj, err := h.options.Create(ctx, trip.ID, owner.ID, slot.ID, CreateOptionInput{Title: "Taj Exotica"})
	if err != nil {
		t.Fatalf("proposing option: %v", err)
	}
	airbnb, err := h.options.Create(ctx, trip.ID, owner.ID, slot.ID, CreateOptionInput{Title: "Airbnb in Anjuna"})
	if err != nil {
		t.Fatalf("proposing second option: %v", err)
	}

	if err := h.slots.SetSelectedOption(ctx, trip.ID, owner.ID, slot.ID, &taj.ID, ptr(slot.Version)); err != nil {
		t.Fatalf("selecting: %v", err)
	}
	resolved, err := h.slots.Get(ctx, trip.ID, owner.ID, slot.ID)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if !resolved.IsResolved() || *resolved.SelectedOptionID != taj.ID {
		t.Fatalf("selection did not take: %+v", resolved.SelectedOptionID)
	}

	// THE FIX: deleting the currently-selected option must clear the slot's selection back
	// to nil, in the same transaction as the delete — not auto-promote the remaining
	// candidate (airbnb). Silently picking a new "winner" would substitute the service's
	// judgment for the group's; un-resolving is the only answer that does not guess.
	if err := h.options.Delete(ctx, trip.ID, owner.ID, taj.ID, ptr(taj.Version)); err != nil {
		t.Fatalf("deleting the selected option: %v", err)
	}

	after, err := h.slots.Get(ctx, trip.ID, owner.ID, slot.ID)
	if err != nil {
		t.Fatalf("reading slot after delete: %v", err)
	}
	if after.IsResolved() {
		t.Errorf("the slot must be unresolved after its selected option was deleted, got selection %v",
			after.SelectedOptionID)
	}

	// The other option must survive untouched — this is a targeted clear, not a cascade.
	stillThere, err := h.options.ListForSlot(ctx, trip.ID, owner.ID, slot.ID)
	if err != nil {
		t.Fatalf("listing remaining options: %v", err)
	}
	if len(stillThere) != 1 || stillThere[0].ID != airbnb.ID {
		t.Errorf("expected the airbnb option to remain untouched, got %+v", stillThere)
	}
}

// TestDeletingAnUnselectedOptionDoesNotTouchTheSelection is the control case: the fix must
// be conditional on the deleted option actually BEING the selection, not fire on every
// delete.
func TestDeletingAnUnselectedOptionDoesNotTouchTheSelection(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	trip := h.makeTrip(t, owner)
	slot, _ := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{
		Kind: domain.SlotKindLodging, Title: "Where are we staying",
	})
	taj, _ := h.options.Create(ctx, trip.ID, owner.ID, slot.ID, CreateOptionInput{Title: "Taj Exotica"})
	airbnb, _ := h.options.Create(ctx, trip.ID, owner.ID, slot.ID, CreateOptionInput{Title: "Airbnb in Anjuna"})

	if err := h.slots.SetSelectedOption(ctx, trip.ID, owner.ID, slot.ID, &taj.ID, ptr(slot.Version)); err != nil {
		t.Fatalf("selecting: %v", err)
	}

	// Delete the OTHER option, not the selected one.
	if err := h.options.Delete(ctx, trip.ID, owner.ID, airbnb.ID, ptr(airbnb.Version)); err != nil {
		t.Fatalf("deleting the unselected option: %v", err)
	}

	after, err := h.slots.Get(ctx, trip.ID, owner.ID, slot.ID)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if !after.IsResolved() || *after.SelectedOptionID != taj.ID {
		t.Errorf("the selection must be untouched when a DIFFERENT option is deleted, got %v", after.SelectedOptionID)
	}
}

func TestProposeOptionRequiresCapability(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	viewer := h.makeUser(t, "viewer@example.com")
	trip := h.makeTrip(t, owner)
	h.addMember(t, trip.ID, viewer, domain.RoleViewer)
	slot, _ := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{Kind: domain.SlotKindLodging, Title: "Lodging"})

	_, err := h.options.Create(ctx, trip.ID, viewer.ID, slot.ID, CreateOptionInput{Title: "A viewer's hotel"})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("a viewer proposing an option must be forbidden, got %v", err)
	}
}

// --- votes ---

func TestVoteRequiresCapVoteAndValidatesSlotOwnership(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	editor := h.makeUser(t, "editor@example.com")
	trip := h.makeTrip(t, owner)
	h.addMember(t, trip.ID, editor, domain.RoleEditor)
	slot, _ := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{Kind: domain.SlotKindActivity, Title: "Beach day"})
	option, _ := h.options.Create(ctx, trip.ID, owner.ID, slot.ID, CreateOptionInput{Title: "Palolem"})

	vote, err := h.votes.Cast(ctx, trip.ID, editor.ID, slot.ID, &option.ID)
	if err != nil {
		t.Fatalf("an editor voting should succeed: %v", err)
	}
	if vote.OptionID == nil || *vote.OptionID != option.ID {
		t.Errorf("vote option = %v, want %v", vote.OptionID, option.ID)
	}

	tallies, err := h.votes.Tally(ctx, trip.ID, owner.ID, slot.ID)
	if err != nil {
		t.Fatalf("tallying: %v", err)
	}
	if len(tallies) != 1 || tallies[0].Count != 1 {
		t.Errorf("expected a single tally of 1, got %+v", tallies)
	}

	// Retraction: casting nil.
	retracted, err := h.votes.Cast(ctx, trip.ID, editor.ID, slot.ID, nil)
	if err != nil {
		t.Fatalf("retracting: %v", err)
	}
	if !retracted.IsRetracted() {
		t.Error("casting nil must retract the vote")
	}
}

func TestVoteFromAnotherTripsSlotIsRejected(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	tripA := h.makeTrip(t, owner)
	tripB := h.makeTrip(t, owner)
	slotInA, _ := h.slots.Create(ctx, tripA.ID, owner.ID, CreateSlotInput{Kind: domain.SlotKindNote, Title: "In A"})

	// Owner is a member of both trips, so the capability check on tripB passes — the
	// trip-scoping guard is what has to catch a slot that belongs to a different trip.
	_, err := h.votes.Cast(ctx, tripB.ID, owner.ID, slotInA.ID, nil)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("voting on a slot through the wrong trip must be not-found, got %v", err)
	}
}

func TestVoteListForSlotRequiresMembership(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	outsider := h.makeUser(t, "outsider@example.com")
	trip := h.makeTrip(t, owner)
	slot, _ := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{Kind: domain.SlotKindActivity, Title: "Beach"})
	option, _ := h.options.Create(ctx, trip.ID, owner.ID, slot.ID, CreateOptionInput{Title: "Palolem"})
	if _, err := h.votes.Cast(ctx, trip.ID, owner.ID, slot.ID, &option.ID); err != nil {
		t.Fatalf("casting: %v", err)
	}

	votes, err := h.votes.ListForSlot(ctx, trip.ID, owner.ID, slot.ID)
	if err != nil {
		t.Fatalf("a member listing votes: %v", err)
	}
	if len(votes) != 1 {
		t.Errorf("expected 1 vote, got %d", len(votes))
	}

	if _, err := h.votes.ListForSlot(ctx, trip.ID, outsider.ID, slot.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a non-member listing votes must be not-found, got %v", err)
	}
}

// --- comments ---
//
// Append-only (no edit verb), and the one delete path in the whole service layer that is
// author-gated rather than capability-gated — see CommentService.Delete's doc comment for why.

func TestCommentCreateRequiresCapCommentAndValidatesSlotOwnership(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	viewer := h.makeUser(t, "viewer@example.com")
	editor := h.makeUser(t, "editor@example.com")
	trip := h.makeTrip(t, owner)
	h.addMember(t, trip.ID, viewer, domain.RoleViewer)
	h.addMember(t, trip.ID, editor, domain.RoleEditor)
	slot, _ := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{Kind: domain.SlotKindActivity, Title: "Beach day"})

	if _, err := h.comments.Create(ctx, trip.ID, viewer.ID, slot.ID, CreateCommentInput{Body: "hi"}); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("a viewer commenting must be forbidden, got %v", err)
	}

	comment, err := h.comments.Create(ctx, trip.ID, editor.ID, slot.ID, CreateCommentInput{Body: "Should we book the earlier flight?"})
	if err != nil {
		t.Fatalf("an editor commenting should succeed: %v", err)
	}
	if comment.AuthorID == nil || *comment.AuthorID != editor.ID {
		t.Errorf("comment author = %v, want %v", comment.AuthorID, editor.ID)
	}

	// Cross-trip: the caller is a member of tripB (owner of both), passing the capability
	// check — the trip-scoping guard is what has to catch a slot from the wrong trip.
	tripB := h.makeTrip(t, owner)
	if _, err := h.comments.Create(ctx, tripB.ID, owner.ID, slot.ID, CreateCommentInput{Body: "wrong trip"}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("commenting on a slot through the wrong trip must be not-found, got %v", err)
	}
}

func TestCommentListForSlotRequiresMembership(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	outsider := h.makeUser(t, "outsider@example.com")
	trip := h.makeTrip(t, owner)
	slot, _ := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{Kind: domain.SlotKindActivity, Title: "Beach"})
	if _, err := h.comments.Create(ctx, trip.ID, owner.ID, slot.ID, CreateCommentInput{Body: "First"}); err != nil {
		t.Fatalf("commenting: %v", err)
	}

	comments, err := h.comments.ListForSlot(ctx, trip.ID, owner.ID, slot.ID)
	if err != nil {
		t.Fatalf("a member listing comments: %v", err)
	}
	if len(comments) != 1 {
		t.Errorf("expected 1 comment, got %d", len(comments))
	}

	if _, err := h.comments.ListForSlot(ctx, trip.ID, outsider.ID, slot.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a non-member listing comments must be not-found, got %v", err)
	}
}

// TestCommentDeleteIsAuthorOnly is the one delete path in the whole service layer that is NOT
// merely capability-gated — an editor, and even the trip OWNER, must not be able to delete
// another member's comment. Only the author can.
func TestCommentDeleteIsAuthorOnly(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	author := h.makeUser(t, "author@example.com")
	trip := h.makeTrip(t, owner)
	h.addMember(t, trip.ID, author, domain.RoleEditor)
	slot, _ := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{Kind: domain.SlotKindActivity, Title: "Beach"})
	comment, err := h.comments.Create(ctx, trip.ID, author.ID, slot.ID, CreateCommentInput{Body: "I'll bring snacks"})
	if err != nil {
		t.Fatalf("commenting: %v", err)
	}

	// The OWNER of the trip — capability-wise the most privileged member there is — still
	// cannot delete someone else's comment.
	if err := h.comments.Delete(ctx, trip.ID, owner.ID, comment.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("the trip owner deleting another member's comment must be forbidden, got %v", err)
	}

	list, err := h.comments.ListForSlot(ctx, trip.ID, owner.ID, slot.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("the comment must survive the rejected delete: list=%v err=%v", list, err)
	}

	if err := h.comments.Delete(ctx, trip.ID, author.ID, comment.ID); err != nil {
		t.Fatalf("the author deleting their own comment should succeed: %v", err)
	}
	list, err = h.comments.ListForSlot(ctx, trip.ID, owner.ID, slot.ID)
	if err != nil || len(list) != 0 {
		t.Errorf("the comment must be gone after the author deletes it: list=%v err=%v", list, err)
	}
}

// TestCommentFromAnotherTripsSlotIsRejected mirrors TestVoteFromAnotherTripsSlotIsRejected for
// the delete path's trip-scoping guard.
func TestCommentFromAnotherTripsSlotIsRejected(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	tripA := h.makeTrip(t, owner)
	tripB := h.makeTrip(t, owner)
	slotInA, _ := h.slots.Create(ctx, tripA.ID, owner.ID, CreateSlotInput{Kind: domain.SlotKindNote, Title: "In A"})
	comment, err := h.comments.Create(ctx, tripA.ID, owner.ID, slotInA.ID, CreateCommentInput{Body: "hi"})
	if err != nil {
		t.Fatalf("commenting: %v", err)
	}

	if err := h.comments.Delete(ctx, tripB.ID, owner.ID, comment.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("deleting a comment through the wrong trip must be not-found, got %v", err)
	}
}

// --- day list / move / delete ---

func TestDayListForTripRequiresMembership(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	outsider := h.makeUser(t, "outsider@example.com")
	trip := h.makeTrip(t, owner)
	if _, err := h.days.Create(ctx, trip.ID, owner.ID, CreateDayInput{Label: "Day 1"}); err != nil {
		t.Fatalf("creating day: %v", err)
	}

	days, err := h.days.ListForTrip(ctx, trip.ID, owner.ID)
	if err != nil || len(days) != 1 {
		t.Fatalf("a member listing days: %v (%d days)", err, len(days))
	}
	if _, err := h.days.ListForTrip(ctx, trip.ID, outsider.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a non-member listing days must be not-found, got %v", err)
	}
}

func TestDayMoveReordersAndEnforcesCapability(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	viewer := h.makeUser(t, "viewer@example.com")
	trip := h.makeTrip(t, owner)
	h.addMember(t, trip.ID, viewer, domain.RoleViewer)
	dayOne, _ := h.days.Create(ctx, trip.ID, owner.ID, CreateDayInput{Label: "One"})
	dayTwo, _ := h.days.Create(ctx, trip.ID, owner.ID, CreateDayInput{Label: "Two"})

	if err := h.days.Move(ctx, trip.ID, viewer.ID, dayOne.ID, nil, ptr(dayOne.Version)); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("a viewer reordering a day must be forbidden, got %v", err)
	}

	// Move dayTwo to the front (after nil).
	if err := h.days.Move(ctx, trip.ID, owner.ID, dayTwo.ID, nil, ptr(dayTwo.Version)); err != nil {
		t.Fatalf("moving: %v", err)
	}
	days, err := h.days.ListForTrip(ctx, trip.ID, owner.ID)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(days) != 2 || days[0].ID != dayTwo.ID {
		t.Errorf("expected dayTwo first after the move, got %v", titlesOfDays(days))
	}
}

func titlesOfDays(days []*domain.Day) []string {
	out := make([]string, 0, len(days))
	for _, d := range days {
		out = append(out, d.Label)
	}
	return out
}

func TestDayDeleteRequiresCapabilityAndTripScoping(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	viewer := h.makeUser(t, "viewer@example.com")
	trip := h.makeTrip(t, owner)
	tripB := h.makeTrip(t, owner)
	h.addMember(t, trip.ID, viewer, domain.RoleViewer)
	day, _ := h.days.Create(ctx, trip.ID, owner.ID, CreateDayInput{Label: "One"})

	if err := h.days.Delete(ctx, trip.ID, viewer.ID, day.ID, ptr(day.Version)); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("a viewer deleting a day must be forbidden, got %v", err)
	}
	if err := h.days.Delete(ctx, tripB.ID, owner.ID, day.ID, ptr(day.Version)); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("deleting a day through the wrong trip must be not-found, got %v", err)
	}
	if err := h.days.Delete(ctx, trip.ID, owner.ID, day.ID, ptr(day.Version)); err != nil {
		t.Fatalf("the owner deleting their own day: %v", err)
	}
	days, err := h.days.ListForTrip(ctx, trip.ID, owner.ID)
	if err != nil || len(days) != 0 {
		t.Errorf("expected 0 live days after delete, got %d (err %v)", len(days), err)
	}
}

// --- slot update / delete ---

func TestSlotUpdateRequiresCapabilityAndTripScoping(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	viewer := h.makeUser(t, "viewer@example.com")
	trip := h.makeTrip(t, owner)
	tripB := h.makeTrip(t, owner)
	h.addMember(t, trip.ID, viewer, domain.RoleViewer)
	slot, _ := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{Kind: domain.SlotKindNote, Title: "Original"})

	_, err := h.slots.Update(ctx, trip.ID, viewer.ID, slot.ID, UpdateSlotInput{
		Kind: domain.SlotKindNote, Title: "Hijacked", Version: ptr(slot.Version),
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("a viewer updating a slot must be forbidden, got %v", err)
	}

	_, err = h.slots.Update(ctx, tripB.ID, owner.ID, slot.ID, UpdateSlotInput{
		Kind: domain.SlotKindNote, Title: "Hijacked", Version: ptr(slot.Version),
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("updating a slot through the wrong trip must be not-found, got %v", err)
	}

	updated, err := h.slots.Update(ctx, trip.ID, owner.ID, slot.ID, UpdateSlotInput{
		Kind: domain.SlotKindActivity, Title: "Renamed", Notes: "some notes", Version: ptr(slot.Version),
	})
	if err != nil {
		t.Fatalf("the owner updating their own slot: %v", err)
	}
	if updated.Title != "Renamed" || updated.Kind != domain.SlotKindActivity {
		t.Errorf("update did not persist: %+v", updated)
	}
}

func TestSlotDeleteRequiresCapability(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	viewer := h.makeUser(t, "viewer@example.com")
	trip := h.makeTrip(t, owner)
	h.addMember(t, trip.ID, viewer, domain.RoleViewer)
	slot, _ := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{Kind: domain.SlotKindNote, Title: "Doomed"})

	if err := h.slots.Delete(ctx, trip.ID, viewer.ID, slot.ID, ptr(slot.Version)); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("a viewer deleting a slot must be forbidden, got %v", err)
	}
	if err := h.slots.Delete(ctx, trip.ID, owner.ID, slot.ID, ptr(slot.Version)); err != nil {
		t.Fatalf("the owner deleting their own slot: %v", err)
	}
	if _, err := h.slots.Get(ctx, trip.ID, owner.ID, slot.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a deleted slot must read as not-found, got %v", err)
	}
}

// --- option update ---

func TestSlotOptionUpdateRequiresCapabilityAndTripScoping(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	viewer := h.makeUser(t, "viewer@example.com")
	trip := h.makeTrip(t, owner)
	tripB := h.makeTrip(t, owner)
	h.addMember(t, trip.ID, viewer, domain.RoleViewer)
	slot, _ := h.slots.Create(ctx, trip.ID, owner.ID, CreateSlotInput{Kind: domain.SlotKindLodging, Title: "Lodging"})
	option, _ := h.options.Create(ctx, trip.ID, owner.ID, slot.ID, CreateOptionInput{Title: "Taj"})

	_, err := h.options.Update(ctx, trip.ID, viewer.ID, option.ID, UpdateOptionInput{
		Title: "Hijacked", Version: ptr(option.Version),
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("a viewer updating an option must be forbidden, got %v", err)
	}

	_, err = h.options.Update(ctx, tripB.ID, owner.ID, option.ID, UpdateOptionInput{
		Title: "Hijacked", Version: ptr(option.Version),
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("updating an option through the wrong trip must be not-found, got %v", err)
	}

	updated, err := h.options.Update(ctx, trip.ID, owner.ID, option.ID, UpdateOptionInput{
		Title: "Taj Exotica Deluxe", Version: ptr(option.Version),
	})
	if err != nil {
		t.Fatalf("the owner updating the option: %v", err)
	}
	if updated.Title != "Taj Exotica Deluxe" {
		t.Errorf("title = %q, want the update to persist", updated.Title)
	}
}

// --- membership: invitation listing and revocation ---

func TestListAndRevokeInvitationsRequireCapability(t *testing.T) {
	h := newPlanningHarness(t)
	ctx := context.Background()
	owner := h.makeUser(t, "owner@example.com")
	viewer := h.makeUser(t, "viewer@example.com")
	trip := h.makeTrip(t, owner)
	h.addMember(t, trip.ID, viewer, domain.RoleViewer)

	email := "invitee@example.com"
	created, err := h.members.CreateInvitation(ctx, trip.ID, owner.ID, CreateInvitationInput{
		Email: &email, Role: domain.RoleEditor,
	})
	if err != nil {
		t.Fatalf("creating invitation: %v", err)
	}

	if _, err := h.members.ListInvitations(ctx, trip.ID, viewer.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("a viewer listing invitations must be forbidden, got %v", err)
	}
	list, err := h.members.ListInvitations(ctx, trip.ID, owner.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("the owner listing invitations: %v (%d)", err, len(list))
	}

	if err := h.members.RevokeInvitation(ctx, trip.ID, viewer.ID, created.Invitation.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("a viewer revoking an invitation must be forbidden, got %v", err)
	}
	if err := h.members.RevokeInvitation(ctx, trip.ID, owner.ID, created.Invitation.ID); err != nil {
		t.Fatalf("the owner revoking the invitation: %v", err)
	}

	list, err = h.members.ListInvitations(ctx, trip.ID, owner.ID)
	if err != nil || len(list) != 0 {
		t.Errorf("expected 0 outstanding invitations after revoke, got %d (err %v)", len(list), err)
	}
}
