package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/repository/sqlcgen"
)

// MembershipRepository is the Postgres implementation of domain.MembershipRepository.
//
// This is the core/domain seam in concrete form. The interface it satisfies is
// module-agnostic; this implementation is bound to trip_members. A second module would ship
// its own implementation over its own table and satisfy the same interface — sharing the
// contract without giving up per-table referential integrity.
type MembershipRepository struct{ base }

// NewMembershipRepository builds a MembershipRepository.
func NewMembershipRepository(pool *pgxpool.Pool) *MembershipRepository {
	return &MembershipRepository{base{pool: pool}}
}

var _ domain.MembershipRepository = (*MembershipRepository)(nil)

func (r *MembershipRepository) Add(ctx context.Context, m *domain.Member) error {
	row, err := r.q(ctx).AddMember(ctx, sqlcgen.AddMemberParams{
		ID:        m.ID,
		TripID:    m.TripID,
		UserID:    m.UserID,
		Role:      string(m.Role),
		InvitedBy: m.InvitedBy,
	})
	if err != nil {
		// A second owner surfaces here as a trip_members_owner_uq violation, mapped to a
		// field-level "this trip already has an owner". The database is the enforcement
		// point, not a prior read: a check-then-insert in Go races with itself.
		return mapError("membership", err)
	}
	*m = *toDomainMember(row)
	return nil
}

func (r *MembershipRepository) Get(ctx context.Context, tripID, userID domain.ID) (*domain.Member, error) {
	row, err := r.q(ctx).GetMember(ctx, sqlcgen.GetMemberParams{TripID: tripID, UserID: userID})
	if err != nil {
		return nil, mapError("membership", err)
	}
	return toDomainMember(row), nil
}

func (r *MembershipRepository) List(ctx context.Context, tripID domain.ID) ([]*domain.Member, error) {
	rows, err := r.q(ctx).ListMembers(ctx, tripID)
	if err != nil {
		return nil, mapError("membership", err)
	}
	out := make([]*domain.Member, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDomainMember(row))
	}
	return out, nil
}

func (r *MembershipRepository) ListProfiles(ctx context.Context, tripID domain.ID) ([]*domain.MemberProfile, error) {
	rows, err := r.q(ctx).ListMemberProfiles(ctx, tripID)
	if err != nil {
		return nil, mapError("membership", err)
	}
	out := make([]*domain.MemberProfile, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDomainMemberProfile(row))
	}
	return out, nil
}

func (r *MembershipRepository) UpdateRole(ctx context.Context, m *domain.Member) error {
	q := r.q(ctx)
	row, err := q.UpdateMemberRole(ctx, sqlcgen.UpdateMemberRoleParams{
		Role:      string(m.Role),
		UpdatedAt: time.Now().UTC(),
		TripID:    m.TripID,
		UserID:    m.UserID,
		Version:   versionArg(m.Version),
	})
	if err != nil {
		if isNoRows(err) {
			exists, existsErr := q.MemberExists(ctx, sqlcgen.MemberExistsParams{
				TripID: m.TripID, UserID: m.UserID,
			})
			if existsErr != nil {
				return mapError("membership", existsErr)
			}
			return resolveWriteMiss("membership", exists)
		}
		return mapError("membership", err)
	}
	*m = *toDomainMember(row)
	return nil
}

func (r *MembershipRepository) Remove(ctx context.Context, tripID, userID domain.ID, at time.Time) error {
	n, err := r.q(ctx).RemoveMember(ctx, sqlcgen.RemoveMemberParams{
		DeletedAt: &at,
		TripID:    tripID,
		UserID:    userID,
	})
	if err != nil {
		return mapError("membership", err)
	}
	if n == 0 {
		return resolveWriteMiss("membership", false)
	}
	return nil
}

func (r *MembershipRepository) CountByRole(ctx context.Context, tripID domain.ID, role domain.Role) (int, error) {
	n, err := r.q(ctx).CountMembersByRole(ctx, sqlcgen.CountMembersByRoleParams{
		TripID: tripID,
		Role:   string(role),
	})
	if err != nil {
		return 0, mapError("membership", err)
	}
	return int(n), nil
}
