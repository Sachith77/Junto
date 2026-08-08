package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/repository/sqlcgen"
)

// InvitationRepository is the Postgres implementation of domain.InvitationRepository.
type InvitationRepository struct{ base }

// NewInvitationRepository builds an InvitationRepository.
func NewInvitationRepository(pool *pgxpool.Pool) *InvitationRepository {
	return &InvitationRepository{base{pool: pool}}
}

var _ domain.InvitationRepository = (*InvitationRepository)(nil)

func (r *InvitationRepository) Create(ctx context.Context, inv *domain.Invitation) error {
	params := sqlcgen.CreateInvitationParams{
		ID:        inv.ID,
		TripID:    inv.TripID,
		Email:     inv.Email,
		Role:      string(inv.Role),
		TokenHash: inv.TokenHash,
		CreatedBy: inv.CreatedBy,
		ExpiresAt: inv.ExpiresAt,
	}
	if inv.MaxUses != nil {
		n := int32(*inv.MaxUses)
		params.MaxUses = &n
	}

	row, err := r.q(ctx).CreateInvitation(ctx, params)
	if err != nil {
		return mapError("invitation", err)
	}
	*inv = *toDomainInvitation(row)
	return nil
}

func (r *InvitationRepository) GetByHash(ctx context.Context, hash []byte) (*domain.Invitation, error) {
	row, err := r.q(ctx).GetInvitationByHash(ctx, hash)
	if err != nil {
		return nil, mapError("invitation", err)
	}
	return toDomainInvitation(row), nil
}

func (r *InvitationRepository) ListForTrip(ctx context.Context, tripID domain.ID) ([]*domain.Invitation, error) {
	rows, err := r.q(ctx).ListInvitationsForTrip(ctx, tripID)
	if err != nil {
		return nil, mapError("invitation", err)
	}
	out := make([]*domain.Invitation, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDomainInvitation(row))
	}
	return out, nil
}

// IncrementUseCount atomically redeems one use of an invitation.
//
// Every condition that makes an invitation usable — not revoked, not expired, uses
// remaining — is evaluated inside the same UPDATE that consumes the use. Callers must NOT
// check redeemability first and then call this: that read-then-write is exactly the race
// this design exists to close, where two concurrent requests both observe use_count = 0 and
// both succeed on a single-use link.
//
// A zero-row result IS the rejection, and is reported as ErrTokenInvalid.
func (r *InvitationRepository) IncrementUseCount(ctx context.Context, id domain.ID) error {
	_, err := r.q(ctx).RedeemInvitation(ctx, sqlcgen.RedeemInvitationParams{
		ID:  id,
		Now: time.Now().UTC(),
	})
	if err != nil {
		if isNoRows(err) {
			// Deliberately does not distinguish revoked from expired from exhausted. To an
			// unauthenticated redeemer they are the same answer, and separating them would
			// let someone probe which invite tokens ever existed.
			return domain.ErrTokenInvalid
		}
		return mapError("invitation", err)
	}
	return nil
}

func (r *InvitationRepository) Revoke(ctx context.Context, id domain.ID, at time.Time) error {
	n, err := r.q(ctx).RevokeInvitation(ctx, sqlcgen.RevokeInvitationParams{RevokedAt: &at, ID: id})
	if err != nil {
		return mapError("invitation", err)
	}
	if n == 0 {
		// Either it does not exist or it was already revoked; both mean "there is nothing
		// live here to revoke", and the caller's intent is satisfied either way.
		return resolveWriteMiss("invitation", false)
	}
	return nil
}
