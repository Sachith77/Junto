package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/junto/junto/internal/domain"
)

// Tests for the slot/option/vote model — the part of the schema the Stage 2 sync engine
// operates on.

// TestOptionsHangOffASlot covers the shape that replaced a flat item model: one decision,
// several candidates, added by different members.
func TestOptionsHangOffASlot(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	slot := makeSlot(t, ctx, r, trip, nil, "Where are we staying in Goa")

	taj := makeOption(t, ctx, r, slot, "Taj Exotica")
	airbnb := makeOption(t, ctx, r, slot, "Airbnb in Anjuna")

	options, err := r.options.ListForSlot(ctx, slot.ID)
	if err != nil {
		t.Fatalf("listing options: %v", err)
	}
	if len(options) != 2 {
		t.Fatalf("expected 2 options, got %v", optionTitles(options))
	}
	// Ordered by creation, so the first proposal stays first.
	if options[0].ID != taj.ID || options[1].ID != airbnb.ID {
		t.Errorf("options out of order: %v", optionTitles(options))
	}

	// The denormalised trip_id is what lets the Stage 2 resync load every option in a room
	// with one indexed lookup and no join.
	forTrip, err := r.options.ListForTrip(ctx, trip.ID)
	if err != nil {
		t.Fatalf("listing trip options: %v", err)
	}
	if len(forTrip) != 2 {
		t.Errorf("expected 2 options for the trip, got %d", len(forTrip))
	}
	for _, o := range forTrip {
		if o.TripID != trip.ID {
			t.Errorf("option %s carries the wrong trip id", o.ID)
		}
	}

	// Field-level content edit.
	cost := int64(850000)
	taj.Notes = "Sea view, breakfast included"
	taj.EstimatedCostMinor = &cost
	taj.Place = domain.Place{Name: "Taj Exotica", Address: "Benaulim, Goa"}
	if err := r.options.Update(ctx, taj); err != nil {
		t.Fatalf("updating option: %v", err)
	}
	if taj.Version != 2 {
		t.Errorf("version = %d, want 2", taj.Version)
	}

	got, err := r.options.GetByID(ctx, taj.ID)
	if err != nil {
		t.Fatalf("reading option: %v", err)
	}
	if got.Notes != "Sea view, breakfast included" || got.Place.Address != "Benaulim, Goa" {
		t.Errorf("option content did not persist: %+v", got)
	}
	if got.EstimatedCostMinor == nil || *got.EstimatedCostMinor != 850000 {
		t.Errorf("estimate did not persist: %v", got.EstimatedCostMinor)
	}

	// Tombstone one candidate; the other survives.
	if err := r.options.SoftDelete(ctx, airbnb.ID, time.Now().UTC(), airbnb.Version); err != nil {
		t.Fatalf("deleting option: %v", err)
	}
	if _, err := r.options.GetByID(ctx, airbnb.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a tombstoned option must read as not-found, got %v", err)
	}
	live, err := r.options.CountLive(ctx, slot.ID)
	if err != nil {
		t.Fatalf("counting: %v", err)
	}
	if live != 1 {
		t.Errorf("expected 1 live option, got %d", live)
	}
}

// TestCrossSlotAndCrossTripReferencesAreRejected proves the composite FKs work from Go, one
// level deeper than the day/trip pair they extend.
func TestCrossSlotAndCrossTripReferencesAreRejected(t *testing.T) {
	r := newRepos()

	t.Run("an option cannot claim another trip", func(t *testing.T) {
		ctx := txContext(t)
		owner := makeUser(t, ctx, r)
		tripA := makeTrip(t, ctx, r, owner)
		tripB := makeTrip(t, ctx, r, owner)
		slotInA := makeSlot(t, ctx, r, tripA, nil, "In A")

		err := r.options.Create(ctx, &domain.SlotOption{
			ID: domain.NewID(), SlotID: slotInA.ID, TripID: tripB.ID, Title: "Smuggled",
		})
		assertFieldViolation(t, err, "slot_id", "slot_not_in_trip")
	})

	t.Run("a slot cannot select another slot's option", func(t *testing.T) {
		ctx := txContext(t)
		owner := makeUser(t, ctx, r)
		trip := makeTrip(t, ctx, r, owner)
		lodging := makeSlot(t, ctx, r, trip, nil, "Lodging")
		activity := makeSlot(t, ctx, r, trip, nil, "Activity")
		beachDay := makeOption(t, ctx, r, activity, "Beach day")

		err := r.slots.SetSelectedOption(ctx, lodging.ID, &beachDay.ID, lodging.Version, time.Now().UTC())
		assertFieldViolation(t, err, "selected_option_id", "option_not_in_slot")
	})

	t.Run("a vote cannot name another slot's option", func(t *testing.T) {
		ctx := txContext(t)
		owner := makeUser(t, ctx, r)
		trip := makeTrip(t, ctx, r, owner)
		lodging := makeSlot(t, ctx, r, trip, nil, "Lodging")
		activity := makeSlot(t, ctx, r, trip, nil, "Activity")
		beachDay := makeOption(t, ctx, r, activity, "Beach day")

		err := r.votes.Cast(ctx, &domain.Vote{
			ID: domain.NewID(), SlotID: lodging.ID, TripID: trip.ID,
			UserID: owner.ID, OptionID: &beachDay.ID,
		})
		assertFieldViolation(t, err, "option_id", "option_not_in_slot")
	})
}

// TestSlotResolution covers selecting and clearing the group's decision.
func TestSlotResolution(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	slot := makeSlot(t, ctx, r, trip, nil, "Where are we staying")
	taj := makeOption(t, ctx, r, slot, "Taj Exotica")

	fresh, err := r.slots.GetByID(ctx, slot.ID)
	if err != nil {
		t.Fatalf("reading slot: %v", err)
	}
	if fresh.IsResolved() {
		t.Fatal("a new slot is unresolved")
	}

	if err := r.slots.SetSelectedOption(ctx, slot.ID, &taj.ID, fresh.Version, time.Now().UTC()); err != nil {
		t.Fatalf("selecting: %v", err)
	}

	resolved, err := r.slots.GetByID(ctx, slot.ID)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if !resolved.IsResolved() || *resolved.SelectedOptionID != taj.ID {
		t.Errorf("selection did not persist: %+v", resolved.SelectedOptionID)
	}

	// Clearing is a nil selection, not a delete.
	if err := r.slots.SetSelectedOption(ctx, slot.ID, nil, resolved.Version, time.Now().UTC()); err != nil {
		t.Fatalf("clearing selection: %v", err)
	}
	cleared, err := r.slots.GetByID(ctx, slot.ID)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if cleared.IsResolved() {
		t.Error("clearing must leave the slot unresolved")
	}

	// Stale version.
	err = r.slots.SetSelectedOption(ctx, slot.ID, &taj.ID, 1, time.Now().UTC())
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Errorf("a stale selection should conflict, got %v", err)
	}
}

// TestRepositoryLayerLeavesSelectionAfterOptionSoftDelete documents the repository's half of
// a two-layer decision.
//
// A soft delete leaves the row present, so the FK stays satisfied and Postgres has no
// opinion — the repository never clears slots.selected_option_id on its own. That is
// intentional: the decision (clear it? auto-promote another candidate? leave it?) is the
// SERVICE's to make explicitly, not a side effect of a repository call. See
// service.SlotOptionService.Delete and TestDeletingSelectedOptionClearsSlotSelection in
// internal/service for the layer that actually resolves this — the fix lives there, and this
// test exists so a future change to the repository's behaviour is visible rather than
// silently changing what the service builds on.
func TestRepositoryLayerLeavesSelectionAfterOptionSoftDelete(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	slot := makeSlot(t, ctx, r, trip, nil, "Where are we staying")
	taj := makeOption(t, ctx, r, slot, "Taj Exotica")

	fresh, _ := r.slots.GetByID(ctx, slot.ID)
	if err := r.slots.SetSelectedOption(ctx, slot.ID, &taj.ID, fresh.Version, time.Now().UTC()); err != nil {
		t.Fatalf("selecting: %v", err)
	}

	if err := r.options.SoftDelete(ctx, taj.ID, time.Now().UTC(), taj.Version); err != nil {
		t.Fatalf("deleting the selected option: %v", err)
	}

	after, err := r.slots.GetByID(ctx, slot.ID)
	if err != nil {
		t.Fatalf("reading slot: %v", err)
	}
	if !after.IsResolved() {
		t.Error("a soft delete does not clear the selection; the service layer decides whether to")
	}
	// And the option is invisible to reads, so a client rendering this slot sees a selection
	// pointing at nothing. That is exactly why the service must handle it.
	if _, err := r.options.GetByID(ctx, taj.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("the tombstoned option should not be readable, got %v", err)
	}
}

// TestVoteIsAnUpsertRegister is the repository half of the property Stage 2 relies on.
func TestVoteIsAnUpsertRegister(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	slot := makeSlot(t, ctx, r, trip, nil, "Where are we staying")
	taj := makeOption(t, ctx, r, slot, "Taj Exotica")
	airbnb := makeOption(t, ctx, r, slot, "Airbnb in Anjuna")

	cast := func(option *domain.ID) *domain.Vote {
		t.Helper()
		v := &domain.Vote{
			ID: domain.NewID(), SlotID: slot.ID, TripID: trip.ID,
			UserID: owner.ID, OptionID: option,
		}
		if err := r.votes.Cast(ctx, v); err != nil {
			t.Fatalf("casting: %v", err)
		}
		return v
	}

	first := cast(&taj.ID)
	if first.Version != 1 {
		t.Errorf("a new vote starts at version 1, got %d", first.Version)
	}

	// Changing your mind is an UPDATE of the same row, not a second row.
	second := cast(&airbnb.ID)
	if second.ID != first.ID {
		t.Error("changing a vote must reuse the same row; the register keeps one per member")
	}
	if second.Version != 2 {
		t.Errorf("version = %d, want 2 after a change", second.Version)
	}

	votes, err := r.votes.ListForSlot(ctx, slot.ID)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(votes) != 1 {
		t.Fatalf("expected exactly 1 vote row, got %d", len(votes))
	}

	// Retraction is a VALUE, not a deletion. The row stays so the register keeps its shape.
	retracted := cast(nil)
	if !retracted.IsRetracted() {
		t.Error("a nil option is a retraction")
	}
	votes, err = r.votes.ListForSlot(ctx, slot.ID)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(votes) != 1 {
		t.Errorf("retraction must not remove the row, got %d rows", len(votes))
	}

	stored, err := r.votes.Get(ctx, slot.ID, owner.ID)
	if err != nil {
		t.Fatalf("reading vote: %v", err)
	}
	if !stored.IsRetracted() {
		t.Error("the stored vote should be retracted")
	}
}

// TestVoteTallyExcludesRetractions covers the counting the UI reads.
func TestVoteTallyExcludesRetractions(t *testing.T) {
	ctx := txContext(t)
	r := newRepos()
	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	slot := makeSlot(t, ctx, r, trip, nil, "Where are we staying")
	taj := makeOption(t, ctx, r, slot, "Taj Exotica")
	airbnb := makeOption(t, ctx, r, slot, "Airbnb in Anjuna")

	voters := []*domain.User{owner}
	for i := 0; i < 3; i++ {
		u := makeUser(t, ctx, r)
		if err := r.members.Add(ctx, &domain.Member{
			ID: domain.NewID(), TripID: trip.ID, UserID: u.ID, Role: domain.RoleEditor,
		}); err != nil {
			t.Fatalf("adding member: %v", err)
		}
		voters = append(voters, u)
	}

	// Three for the Taj, one for the Airbnb.
	for i, u := range voters {
		option := &taj.ID
		if i == 3 {
			option = &airbnb.ID
		}
		if err := r.votes.Cast(ctx, &domain.Vote{
			ID: domain.NewID(), SlotID: slot.ID, TripID: trip.ID, UserID: u.ID, OptionID: option,
		}); err != nil {
			t.Fatalf("casting for %s: %v", u.ID, err)
		}
	}

	tallies, err := r.votes.Tally(ctx, slot.ID)
	if err != nil {
		t.Fatalf("tallying: %v", err)
	}
	counts := map[domain.ID]int{}
	for _, tl := range tallies {
		counts[tl.OptionID] = tl.Count
	}
	if counts[taj.ID] != 3 || counts[airbnb.ID] != 1 {
		t.Errorf("tally = %v, want Taj 3 / Airbnb 1", counts)
	}

	leaders, best := domain.Leaders(tallies)
	if best != 3 || len(leaders) != 1 || leaders[0] != taj.ID {
		t.Errorf("leader = %v (%d), want the Taj with 3", leaders, best)
	}

	// One voter retracts: the count drops, and the row remains.
	if err := r.votes.Cast(ctx, &domain.Vote{
		ID: domain.NewID(), SlotID: slot.ID, TripID: trip.ID, UserID: voters[0].ID, OptionID: nil,
	}); err != nil {
		t.Fatalf("retracting: %v", err)
	}

	tallies, err = r.votes.Tally(ctx, slot.ID)
	if err != nil {
		t.Fatalf("re-tallying: %v", err)
	}
	counts = map[domain.ID]int{}
	for _, tl := range tallies {
		counts[tl.OptionID] = tl.Count
	}
	if counts[taj.ID] != 2 {
		t.Errorf("after a retraction the Taj should have 2, got %d", counts[taj.ID])
	}

	all, err := r.votes.ListForSlot(ctx, slot.ID)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("all four members still have a vote row, got %d", len(all))
	}

	// Trip-wide listing is the Stage 2 resync path.
	forTrip, err := r.votes.ListForTrip(ctx, trip.ID)
	if err != nil {
		t.Fatalf("listing trip votes: %v", err)
	}
	if len(forTrip) != 4 {
		t.Errorf("expected 4 votes in the trip, got %d", len(forTrip))
	}
}

// TestConcurrentVotesFromDifferentMembersAllSucceed proves the upsert does not serialise
// members against each other — only a member against themselves.
func TestConcurrentVotesFromDifferentMembersAllSucceed(t *testing.T) {
	ctx := concurrentCtx(t)
	r := newRepos()

	owner := makeUser(t, ctx, r)
	trip := makeTrip(t, ctx, r, owner)
	t.Cleanup(func() { cleanupTrip(t, r, trip.ID, owner.ID) })

	slot := makeSlot(t, ctx, r, trip, nil, "Where are we staying")
	option := makeOption(t, ctx, r, slot, "Taj Exotica")

	const voters = 8
	users := make([]*domain.User, 0, voters)
	for i := 0; i < voters; i++ {
		u := makeUser(t, ctx, r)
		users = append(users, u)
		t.Cleanup(func() { deleteUser(t, r, u.ID) })
		if err := r.members.Add(ctx, &domain.Member{
			ID: domain.NewID(), TripID: trip.ID, UserID: u.ID, Role: domain.RoleEditor,
		}); err != nil {
			t.Fatalf("adding member: %v", err)
		}
	}

	errs := make(chan error, voters)
	start := make(chan struct{})
	for _, u := range users {
		go func(u *domain.User) {
			<-start
			errs <- r.votes.Cast(context.Background(), &domain.Vote{
				ID: domain.NewID(), SlotID: slot.ID, TripID: trip.ID,
				UserID: u.ID, OptionID: &option.ID,
			})
		}(u)
	}
	close(start)

	for i := 0; i < voters; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent vote %d failed: %v", i, err)
		}
	}

	tallies, err := r.votes.Tally(ctx, slot.ID)
	if err != nil {
		t.Fatalf("tallying: %v", err)
	}
	if len(tallies) != 1 || tallies[0].Count != voters {
		t.Errorf("tally = %+v, want a single option with %d votes", tallies, voters)
	}
}
