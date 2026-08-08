package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/repository/sqlcgen"
)

// CommentRepository is the Postgres implementation of domain.CommentRepository.
type CommentRepository struct{ base }

// NewCommentRepository builds a CommentRepository.
func NewCommentRepository(pool *pgxpool.Pool) *CommentRepository {
	return &CommentRepository{base{pool: pool}}
}

var _ domain.CommentRepository = (*CommentRepository)(nil)

func (r *CommentRepository) Create(ctx context.Context, c *domain.Comment) error {
	row, err := r.q(ctx).CreateComment(ctx, sqlcgen.CreateCommentParams{
		ID:       c.ID,
		SlotID:   c.SlotID,
		TripID:   c.TripID,
		Body:     c.Body,
		AuthorID: c.AuthorID,
	})
	if err != nil {
		// A slot from another trip fails comments_slot_fk; an empty or too-long body fails
		// comments_body_len. Both map to field-level errors by constraint name.
		return mapError("comment", err)
	}
	*c = *toDomainComment(row)
	return nil
}

func (r *CommentRepository) GetByID(ctx context.Context, id domain.ID) (*domain.Comment, error) {
	row, err := r.q(ctx).GetCommentByID(ctx, id)
	if err != nil {
		return nil, mapError("comment", err)
	}
	return toDomainComment(row), nil
}

func (r *CommentRepository) ListForSlot(ctx context.Context, slotID domain.ID) ([]*domain.Comment, error) {
	rows, err := r.q(ctx).ListCommentsForSlot(ctx, slotID)
	if err != nil {
		return nil, mapError("comment", err)
	}
	return toDomainComments(rows), nil
}

// SoftDelete tombstones a comment.
//
// No version predicate and no conflict case, for the same reason AttachmentRepository.SoftDelete
// has none (D46-style): comments carry no version column, so there is no concurrent-edit case
// a zero-row result could be mistaken for. Author authorization is checked in the service, not
// here — there is no SQL predicate that can express "the deleter must equal author_id" without
// collapsing "not the author" and "already deleted" into the same zero-row result, which would
// hide a permission error behind a 404.
func (r *CommentRepository) SoftDelete(ctx context.Context, id domain.ID, at time.Time) error {
	n, err := r.q(ctx).SoftDeleteComment(ctx, sqlcgen.SoftDeleteCommentParams{
		DeletedAt: &at,
		ID:        id,
	})
	if err != nil {
		return mapError("comment", err)
	}
	if n == 0 {
		return mapError("comment", domain.ErrNotFound)
	}
	return nil
}
