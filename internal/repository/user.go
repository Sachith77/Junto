package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/repository/sqlcgen"
)

// UserRepository is the Postgres implementation of domain.UserRepository.
type UserRepository struct{ base }

// NewUserRepository builds a UserRepository.
func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
	return &UserRepository{base{pool: pool}}
}

// Compile-time proof that the implementation still satisfies the port. Cheap, and it turns
// an interface drift into a build failure instead of a wiring failure at startup.
var _ domain.UserRepository = (*UserRepository)(nil)

func (r *UserRepository) Create(ctx context.Context, u *domain.User) error {
	row, err := r.q(ctx).CreateUser(ctx, sqlcgen.CreateUserParams{
		ID:              u.ID,
		Email:           u.Email,
		PasswordHash:    u.PasswordHash,
		DisplayName:     u.DisplayName,
		EmailVerifiedAt: u.EmailVerifiedAt,
	})
	if err != nil {
		return mapError("user", err)
	}
	// Write the persisted row back over the caller's struct so database-assigned values
	// (created_at, updated_at, version) are correct without a follow-up read. Every Create
	// and Update in this package does the same, which is why they all use RETURNING.
	*u = *toDomainUser(row)
	return nil
}

func (r *UserRepository) GetByID(ctx context.Context, id domain.ID) (*domain.User, error) {
	row, err := r.q(ctx).GetUserByID(ctx, id)
	if err != nil {
		return nil, mapError("user", err)
	}
	return toDomainUser(row), nil
}

func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	// Normalising here rather than trusting the caller keeps this in lockstep with the
	// users_email_lower_uq index. If lookup and uniqueness ever disagreed, duplicate
	// accounts would become possible.
	row, err := r.q(ctx).GetUserByEmail(ctx, domain.NormalizeEmail(email))
	if err != nil {
		return nil, mapError("user", err)
	}
	return toDomainUser(row), nil
}

func (r *UserRepository) Update(ctx context.Context, u *domain.User) error {
	q := r.q(ctx)
	row, err := q.UpdateUser(ctx, sqlcgen.UpdateUserParams{
		Email:           u.Email,
		DisplayName:     u.DisplayName,
		PasswordHash:    u.PasswordHash,
		EmailVerifiedAt: u.EmailVerifiedAt,
		UpdatedAt:       time.Now().UTC(),
		ID:              u.ID,
		Version:         versionArg(u.Version),
	})
	if err != nil {
		if isNoRows(err) {
			exists, existsErr := q.UserExists(ctx, u.ID)
			if existsErr != nil {
				return mapError("user", existsErr)
			}
			return resolveWriteMiss("user", exists)
		}
		return mapError("user", err)
	}
	*u = *toDomainUser(row)
	return nil
}

func (r *UserRepository) SoftDelete(ctx context.Context, id domain.ID, at time.Time) error {
	n, err := r.q(ctx).SoftDeleteUser(ctx, sqlcgen.SoftDeleteUserParams{DeletedAt: &at, ID: id})
	if err != nil {
		return mapError("user", err)
	}
	if n == 0 {
		return resolveWriteMiss("user", false)
	}
	return nil
}
