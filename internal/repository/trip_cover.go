package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/repository/sqlcgen"
)

type TripCoverRepository struct{ base }

func NewTripCoverRepository(pool *pgxpool.Pool) *TripCoverRepository {
	return &TripCoverRepository{base{pool: pool}}
}

var _ domain.TripCoverRepository = (*TripCoverRepository)(nil)

func toDomainCoverSuggestion(row sqlcgen.TripCoverSuggestion) *domain.TripCoverSuggestion {
	return &domain.TripCoverSuggestion{
		ID: row.ID, QueryKey: row.QueryKey, Query: row.Query, Found: row.Found,
		ImageURL: row.ImageUrl, PhotographerName: row.PhotographerName,
		PhotographerURL: row.PhotographerUrl, PhotoURL: row.PhotoUrl,
		DownloadLocation: row.DownloadLocation, ExpiresAt: row.ExpiresAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func toDomainCoverUpload(row sqlcgen.TripCoverUpload) *domain.TripCoverUpload {
	return &domain.TripCoverUpload{
		ID: row.ID, TripID: row.TripID, StorageKey: row.StorageKey,
		ContentType: row.ContentType, OriginalName: row.OriginalName,
		SizeBytes: row.SizeBytes, Status: domain.AttachmentStatus(row.Status),
		UploadedBy: row.UploadedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func (r *TripCoverRepository) GetSuggestionByQueryKey(ctx context.Context, key string, now time.Time) (*domain.TripCoverSuggestion, error) {
	row, err := r.q(ctx).GetTripCoverSuggestionByQueryKey(ctx, sqlcgen.GetTripCoverSuggestionByQueryKeyParams{QueryKey: key, Now: now})
	if err != nil {
		return nil, mapError("trip cover suggestion", err)
	}
	return toDomainCoverSuggestion(row), nil
}

func (r *TripCoverRepository) GetSuggestionByID(ctx context.Context, id domain.ID) (*domain.TripCoverSuggestion, error) {
	row, err := r.q(ctx).GetTripCoverSuggestionByID(ctx, id)
	if err != nil {
		return nil, mapError("trip cover suggestion", err)
	}
	return toDomainCoverSuggestion(row), nil
}

func (r *TripCoverRepository) UpsertSuggestion(ctx context.Context, s *domain.TripCoverSuggestion) error {
	row, err := r.q(ctx).UpsertTripCoverSuggestion(ctx, sqlcgen.UpsertTripCoverSuggestionParams{
		ID: s.ID, QueryKey: s.QueryKey, Query: s.Query, Found: s.Found,
		ImageUrl: s.ImageURL, PhotographerName: s.PhotographerName,
		PhotographerUrl: s.PhotographerURL, PhotoUrl: s.PhotoURL,
		DownloadLocation: s.DownloadLocation, ExpiresAt: s.ExpiresAt,
	})
	if err != nil {
		return mapError("trip cover suggestion", err)
	}
	*s = *toDomainCoverSuggestion(row)
	return nil
}

func (r *TripCoverRepository) CreateUpload(ctx context.Context, u *domain.TripCoverUpload) error {
	row, err := r.q(ctx).CreateTripCoverUpload(ctx, sqlcgen.CreateTripCoverUploadParams{
		ID: u.ID, TripID: u.TripID, StorageKey: u.StorageKey, ContentType: u.ContentType,
		OriginalName: u.OriginalName, UploadedBy: u.UploadedBy,
	})
	if err != nil {
		return mapError("trip cover upload", err)
	}
	*u = *toDomainCoverUpload(row)
	return nil
}

func (r *TripCoverRepository) GetUploadByID(ctx context.Context, id domain.ID) (*domain.TripCoverUpload, error) {
	row, err := r.q(ctx).GetTripCoverUploadByID(ctx, id)
	if err != nil {
		return nil, mapError("trip cover upload", err)
	}
	return toDomainCoverUpload(row), nil
}

func (r *TripCoverRepository) ConfirmUpload(ctx context.Context, id domain.ID, size int64, at time.Time) error {
	n, err := r.q(ctx).ConfirmTripCoverUpload(ctx, sqlcgen.ConfirmTripCoverUploadParams{ID: id, SizeBytes: &size, UpdatedAt: at})
	if err != nil {
		return mapError("trip cover upload", err)
	}
	if n == 0 {
		return mapError("trip cover upload", domain.ErrNotFound)
	}
	return nil
}

func (r *TripCoverRepository) MarkUploadFailed(ctx context.Context, id domain.ID, at time.Time) error {
	_, err := r.q(ctx).FailTripCoverUpload(ctx, sqlcgen.FailTripCoverUploadParams{ID: id, UpdatedAt: at})
	return mapError("trip cover upload", err)
}

func (r *TripCoverRepository) SetSuggested(ctx context.Context, tripID domain.ID, s *domain.TripCoverSuggestion, at time.Time) error {
	n, err := r.q(ctx).SetTripSuggestedCover(ctx, sqlcgen.SetTripSuggestedCoverParams{
		ID: tripID, ImageUrl: s.ImageURL, PhotographerName: s.PhotographerName,
		PhotographerUrl: s.PhotographerURL, PhotoUrl: s.PhotoURL, UpdatedAt: at,
	})
	if err != nil {
		return mapError("trip", err)
	}
	if n == 0 {
		return mapError("trip", domain.ErrNotFound)
	}
	return nil
}

func (r *TripCoverRepository) SetNeutral(ctx context.Context, tripID domain.ID, at time.Time) error {
	n, err := r.q(ctx).SetTripNeutralCover(ctx, sqlcgen.SetTripNeutralCoverParams{ID: tripID, UpdatedAt: at})
	if err != nil {
		return mapError("trip", err)
	}
	if n == 0 {
		return mapError("trip", domain.ErrNotFound)
	}
	return nil
}

func (r *TripCoverRepository) SetUploaded(ctx context.Context, tripID domain.ID, u *domain.TripCoverUpload, at time.Time) error {
	n, err := r.q(ctx).SetTripUploadedCover(ctx, sqlcgen.SetTripUploadedCoverParams{
		ID: tripID, StorageKey: u.StorageKey, ContentType: u.ContentType, UpdatedAt: at,
	})
	if err != nil {
		return mapError("trip", err)
	}
	if n == 0 {
		return mapError("trip", domain.ErrNotFound)
	}
	return nil
}
