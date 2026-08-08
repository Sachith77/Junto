package service

import (
	"context"

	"github.com/junto/junto/internal/domain"
)

// VoteService manages member votes on slot options.
//
// # The simplest convergent entity in the system
//
// A vote is one row per (slot, user) with one mutable field: a last-writer-wins REGISTER. It
// has no tombstone to reconcile and therefore no delete-versus-edit case, and its whole
// operation vocabulary is one verb.
//
// It uses the SAME mechanism as slots and options — the same sequencer, the same transaction
// shape, the same log — with no special-case code anywhere. That is the point: a vote is the
// degenerate case of the general rule, an entity whose field mask always names exactly one
// mutable field. It is the cleanest available proof that the machinery is right, precisely
// because everything incidental has been stripped away.
type VoteService struct {
	authz
	oplog
	votes domain.VoteRepository
	slots domain.SlotRepository
}

// VoteDeps collects VoteService's dependencies.
type VoteDeps struct {
	Votes   domain.VoteRepository
	Slots   domain.SlotRepository
	Members domain.MembershipRepository
	Trips   domain.TripRepository
	Ops     domain.OpLogRepository
	Tx      domain.TxManager
	Pub     domain.OpPublisher
}

// NewVoteService builds a VoteService.
func NewVoteService(deps VoteDeps) *VoteService {
	return &VoteService{
		authz: authz{members: deps.Members},
		oplog: newOplog(deps.Trips, deps.Ops, deps.Tx, deps.Pub),
		votes: deps.Votes,
		slots: deps.Slots,
	}
}

// Cast records or changes the caller's own vote on a slot. optionID nil retracts it.
//
// No expected version is taken, on either path: a vote is last-writer-wins by design, and
// requiring one would make a member's second click fail rather than simply win — which is not
// what changing your mind means. Retraction sets option_id to NULL rather than deleting the
// row, which is the one deliberate break from the tombstone convention in this codebase and
// exists precisely to preserve the register shape.
func (s *VoteService) Cast(ctx context.Context, tripID, userID, slotID domain.ID, optionID *domain.ID) (*domain.Vote, error) {
	actor, err := s.require(ctx, tripID, userID, domain.CapVote)
	if err != nil {
		return nil, err
	}

	vote := &domain.Vote{SlotID: slotID, TripID: tripID, UserID: userID, OptionID: optionID}
	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		slot, err := s.slots.GetByID(ctx, slotID)
		if err != nil {
			return err
		}
		if err := checkTrip(slot.TripID, tripID); err != nil {
			return err
		}
		if err := vote.Validate(); err != nil {
			return err
		}
		// The database's composite FK (option_id, slot_id) -> slot_options(id, slot_id)
		// rejects an option that does not belong to this slot; Cast does not duplicate it.
		if err := s.votes.Cast(ctx, vote); err != nil {
			return err
		}
		return rec.vote(ctx, vote)
	})
	if err != nil {
		return nil, err
	}
	return vote, nil
}

// ListForSlot returns every member's vote on a slot, for a "who voted for what" view.
func (s *VoteService) ListForSlot(ctx context.Context, tripID, userID, slotID domain.ID) ([]*domain.Vote, error) {
	if _, err := s.actor(ctx, tripID, userID); err != nil {
		return nil, err
	}
	if err := s.checkSlotInTrip(ctx, tripID, slotID); err != nil {
		return nil, err
	}
	return s.votes.ListForSlot(ctx, slotID)
}

// Tally counts live votes per option for a slot.
//
// "Live" excludes both retractions and votes for soft-deleted options (D71), filtered in the
// query rather than here so there is one definition of a visible vote rather than two that
// drift. The vote rows themselves are retained, so undeleting an option restores the group's
// expressed preferences instead of silently discarding them.
func (s *VoteService) Tally(ctx context.Context, tripID, userID, slotID domain.ID) ([]domain.VoteTally, error) {
	if _, err := s.actor(ctx, tripID, userID); err != nil {
		return nil, err
	}
	if err := s.checkSlotInTrip(ctx, tripID, slotID); err != nil {
		return nil, err
	}
	return s.votes.Tally(ctx, slotID)
}

func (s *VoteService) checkSlotInTrip(ctx context.Context, tripID, slotID domain.ID) error {
	slot, err := s.slots.GetByID(ctx, slotID)
	if err != nil {
		return err
	}
	return checkTrip(slot.TripID, tripID)
}
