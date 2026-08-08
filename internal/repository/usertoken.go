package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/repository/sqlcgen"
)

// UserTokenRepository is the Postgres implementation of domain.UserTokenRepository.
type UserTokenRepository struct{ base }

// NewUserTokenRepository builds a UserTokenRepository.
func NewUserTokenRepository(pool *pgxpool.Pool) *UserTokenRepository {
	return &UserTokenRepository{base{pool: pool}}
}

var _ domain.UserTokenRepository = (*UserTokenRepository)(nil)

func (r *UserTokenRepository) Create(ctx context.Context, t *domain.UserToken) error {
	row, err := r.q(ctx).CreateUserToken(ctx, sqlcgen.CreateUserTokenParams{
		ID:        t.ID,
		UserID:    t.UserID,
		Purpose:   string(t.Purpose),
		TokenHash: t.TokenHash,
		ExpiresAt: t.ExpiresAt,
	})
	if err != nil {
		return mapError("user token", err)
	}
	*t = *toDomainUserToken(row)
	return nil
}

func (r *UserTokenRepository) GetByHash(ctx context.Context, hash []byte) (*domain.UserToken, error) {
	row, err := r.q(ctx).GetUserTokenByHash(ctx, hash)
	if err != nil {
		return nil, mapError("user token", err)
	}
	return toDomainUserToken(row), nil
}

// Consume marks a single-use token as spent.
//
// Returns ErrTokenConsumed on a zero-row result. The `consumed_at IS NULL` guard makes this
// atomic, so two requests racing on the same reset link produce exactly one winner.
func (r *UserTokenRepository) Consume(ctx context.Context, id domain.ID, at time.Time) error {
	n, err := r.q(ctx).ConsumeUserToken(ctx, sqlcgen.ConsumeUserTokenParams{ConsumedAt: &at, ID: id})
	if err != nil {
		return mapError("user token", err)
	}
	if n == 0 {
		return domain.ErrTokenConsumed
	}
	return nil
}

func (r *UserTokenRepository) ConsumeAllForPurpose(ctx context.Context, userID domain.ID, purpose domain.TokenPurpose, at time.Time) error {
	// No zero-row check: having nothing outstanding to retire is the normal case on a
	// first password-reset request, not a failure.
	_, err := r.q(ctx).ConsumeAllUserTokensForPurpose(ctx, sqlcgen.ConsumeAllUserTokensForPurposeParams{
		ConsumedAt: &at,
		UserID:     userID,
		Purpose:    string(purpose),
	})
	return mapError("user token", err)
}

func (r *UserTokenRepository) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	n, err := r.q(ctx).DeleteExpiredUserTokens(ctx, before)
	if err != nil {
		return 0, mapError("user token", err)
	}
	return n, nil
}
