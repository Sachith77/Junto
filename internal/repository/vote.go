package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/repository/sqlcgen"
)

// VoteRepository is the Postgres implementation of domain.VoteRepository.
//
// There is no Delete. Retraction sets OptionID to nil, keeping exactly one row per
// (slot, user) — the last-writer-wins register shape that makes a vote the simplest
// convergent entity in the system. See domain.Vote.
type VoteRepository struct{ base }

// NewVoteRepository builds a VoteRepository.
func NewVoteRepository(pool *pgxpool.Pool) *VoteRepository {
	return &VoteRepository{base{pool: pool}}
}

var _ domain.VoteRepository = (*VoteRepository)(nil)

// Cast records or changes a member's choice.
//
// Upsert on (slot_id, user_id), so "change my vote" is one round trip and one row. It takes
// no expected version on purpose: a vote is last-writer-wins by design, and requiring a
// version would make a member's second click fail rather than simply win — which is not what
// anyone means by changing their mind.
func (r *VoteRepository) Cast(ctx context.Context, v *domain.Vote) error {
	if v.ID == domain.NilID {
		// Only used when the row is genuinely new; on conflict the existing id is kept.
		v.ID = domain.NewID()
	}

	row, err := r.q(ctx).CastVote(ctx, sqlcgen.CastVoteParams{
		ID:       v.ID,
		SlotID:   v.SlotID,
		TripID:   v.TripID,
		UserID:   v.UserID,
		OptionID: v.OptionID,
	})
	if err != nil {
		// Voting for an option that belongs to a different slot fails the composite FK
		// (option_id, slot_id) -> slot_options(id, slot_id) here.
		return mapError("vote", err)
	}
	*v = *toDomainVote(row)
	return nil
}

func (r *VoteRepository) Get(ctx context.Context, slotID, userID domain.ID) (*domain.Vote, error) {
	row, err := r.q(ctx).GetVote(ctx, sqlcgen.GetVoteParams{SlotID: slotID, UserID: userID})
	if err != nil {
		return nil, mapError("vote", err)
	}
	return toDomainVote(row), nil
}

func (r *VoteRepository) ListForSlot(ctx context.Context, slotID domain.ID) ([]*domain.Vote, error) {
	rows, err := r.q(ctx).ListVotesForSlot(ctx, slotID)
	if err != nil {
		return nil, mapError("vote", err)
	}
	return toDomainVotes(rows), nil
}

func (r *VoteRepository) ListForTrip(ctx context.Context, tripID domain.ID) ([]*domain.Vote, error) {
	rows, err := r.q(ctx).ListVotesForTrip(ctx, tripID)
	if err != nil {
		return nil, mapError("vote", err)
	}
	return toDomainVotes(rows), nil
}

// Tally counts votes per option, excluding retractions.
func (r *VoteRepository) Tally(ctx context.Context, slotID domain.ID) ([]domain.VoteTally, error) {
	rows, err := r.q(ctx).TallyVotesForSlot(ctx, slotID)
	if err != nil {
		return nil, mapError("vote", err)
	}

	out := make([]domain.VoteTally, 0, len(rows))
	for _, row := range rows {
		// option_id is nullable in the column definition, but the query filters retractions
		// out, so a NULL here would mean the query changed underneath us. Skipping rather
		// than dereferencing keeps a schema drift from becoming a nil panic.
		if row.OptionID == nil {
			continue
		}
		out = append(out, domain.VoteTally{OptionID: *row.OptionID, Count: int(row.VoteCount)})
	}
	return out, nil
}
