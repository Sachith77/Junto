package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/repository/sqlcgen"
)

// AttachmentRepository is the Postgres implementation of domain.AttachmentRepository.
type AttachmentRepository struct{ base }

// NewAttachmentRepository builds an AttachmentRepository.
func NewAttachmentRepository(pool *pgxpool.Pool) *AttachmentRepository {
	return &AttachmentRepository{base{pool: pool}}
}

var _ domain.AttachmentRepository = (*AttachmentRepository)(nil)

func (r *AttachmentRepository) Create(ctx context.Context, a *domain.Attachment) error {
	row, err := r.q(ctx).CreateAttachment(ctx, sqlcgen.CreateAttachmentParams{
		ID:            a.ID,
		TripID:        a.TripID,
		SlotOptionID:  a.SlotOptionID,
		BudgetEntryID: a.BudgetEntryID,
		SlotID:        a.SlotID,
		Kind:          string(a.Kind),
		Status:        string(a.Status),
		StorageKey:    a.StorageKey,
		ExternalUrl:   a.ExternalURL,
		ContentType:   a.ContentType,
		OriginalName:  a.OriginalName,
		UploadedBy:    a.UploadedBy,
	})
	if err != nil {
		// Two or zero owners fails attachments_one_owner; a duplicate object key fails
		// attachments_storage_key_uq. Both map to field-level errors by constraint name.
		return mapError("attachment", err)
	}
	*a = *toDomainAttachment(row)
	return nil
}

func (r *AttachmentRepository) GetByID(ctx context.Context, id domain.ID) (*domain.Attachment, error) {
	row, err := r.q(ctx).GetAttachmentByID(ctx, id)
	if err != nil {
		return nil, mapError("attachment", err)
	}
	return toDomainAttachment(row), nil
}

// ListForOwner returns the live attachments hanging off one entity.
//
// Three queries rather than one with three nullable predicates. A single query would either
// need OR-ed conditions that no single index can serve, or a coalesce trick that reads as
// cleverness; three narrow statements each hit their own index and each say what they mean.
func (r *AttachmentRepository) ListForOwner(ctx context.Context, owner domain.AttachmentOwner) ([]*domain.Attachment, error) {
	if !owner.Valid() {
		ve := &domain.ValidationError{}
		ve.Add("owner", "exactly_one_required",
			"an attachment owner must be exactly one of: slot option, budget entry, slot")
		return nil, ve
	}

	q := r.q(ctx)
	var (
		rows []sqlcgen.Attachment
		err  error
	)
	switch {
	case owner.SlotOptionID != nil:
		rows, err = q.ListAttachmentsForOption(ctx, owner.SlotOptionID)
	case owner.BudgetEntryID != nil:
		rows, err = q.ListAttachmentsForBudgetEntry(ctx, owner.BudgetEntryID)
	default:
		rows, err = q.ListAttachmentsForSlot(ctx, owner.SlotID)
	}
	if err != nil {
		return nil, mapError("attachment", err)
	}
	return toDomainAttachments(rows), nil
}

// ListForTrip returns every live attachment in a trip.
//
// Not part of the domain port: it exists for the resync-comparison path, where a test asserts
// that folding the log reproduces the database. Keeping it off the interface means no service
// can reach for it as a shortcut around the owner-scoped reads.
func (r *AttachmentRepository) ListForTrip(ctx context.Context, tripID domain.ID) ([]*domain.Attachment, error) {
	rows, err := r.q(ctx).ListAttachmentsForTrip(ctx, tripID)
	if err != nil {
		return nil, mapError("attachment", err)
	}
	return toDomainAttachments(rows), nil
}

// Confirm completes the two-phase upload, recording the size and checksum the server observed.
//
// Returns ErrNotFound when no pending row matched. That collapses several situations — never
// existed, already confirmed, already failed, already deleted — and the collapse is correct
// here in a way it would not be for a versioned entity: there is no "different version" case to
// distinguish, so the resolveWriteMiss dance that Update and SoftDelete perform elsewhere would
// be answering a question nobody asked.
func (r *AttachmentRepository) Confirm(ctx context.Context, id domain.ID, sizeBytes int64, checksum []byte, at time.Time) error {
	_, err := r.ConfirmReturning(ctx, id, sizeBytes, checksum, at)
	return err
}

// ConfirmReturning is Confirm plus the resulting row.
//
// The service needs the persisted attachment to build its operation payload from, because the
// log stores resolved effects and never the request that caused them (D62) — the size and
// checksum in that payload have to be the ones the database now holds, not the ones the caller
// intended to write. Kept off the domain port because only the op-log path needs it.
func (r *AttachmentRepository) ConfirmReturning(ctx context.Context, id domain.ID, sizeBytes int64, checksum []byte, at time.Time) (*domain.Attachment, error) {
	row, err := r.q(ctx).ConfirmAttachment(ctx, sqlcgen.ConfirmAttachmentParams{
		SizeBytes:      &sizeBytes,
		ChecksumSha256: checksum,
		UpdatedAt:      at,
		ID:             id,
	})
	if err != nil {
		return nil, mapError("attachment", err)
	}
	return toDomainAttachment(row), nil
}

func (r *AttachmentRepository) MarkFailed(ctx context.Context, id domain.ID, at time.Time) error {
	n, err := r.q(ctx).MarkAttachmentFailed(ctx, sqlcgen.MarkAttachmentFailedParams{
		UpdatedAt: at,
		ID:        id,
	})
	if err != nil {
		return mapError("attachment", err)
	}
	if n == 0 {
		return mapError("attachment", domain.ErrNotFound)
	}
	return nil
}

// SoftDelete tombstones an attachment.
//
// No version predicate and no conflict case: attachments carry no version because they are
// immutable once ready (D46). Deleting one that is already deleted affects zero rows and is
// reported as not-found, which is the only honest answer — there is no concurrent edit it could
// be mistaken for.
func (r *AttachmentRepository) SoftDelete(ctx context.Context, id domain.ID, at time.Time) error {
	q := r.q(ctx)
	n, err := q.SoftDeleteAttachment(ctx, sqlcgen.SoftDeleteAttachmentParams{
		DeletedAt: &at,
		ID:        id,
	})
	if err != nil {
		return mapError("attachment", err)
	}
	if n == 0 {
		return mapError("attachment", domain.ErrNotFound)
	}
	return nil
}

func (r *AttachmentRepository) ListStalePending(ctx context.Context, before time.Time, limit int) ([]*domain.Attachment, error) {
	rows, err := r.q(ctx).ListStalePendingAttachments(ctx, sqlcgen.ListStalePendingAttachmentsParams{
		Before:   before,
		RowLimit: int32(limit),
	})
	if err != nil {
		return nil, mapError("attachment", err)
	}
	return toDomainAttachments(rows), nil
}
