package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/junto/junto/internal/domain"
)

const (
	coverUploadURLTTL = 15 * time.Minute
	coverReadURLTTL   = 6 * time.Hour
	coverCacheTTL     = 30 * 24 * time.Hour
	coverMissTTL      = 24 * time.Hour
)

type TripCoverService struct {
	authz
	covers   domain.TripCoverRepository
	trips    domain.TripRepository
	storage  domain.FileStorage
	provider domain.CoverPhotoProvider
	tx       domain.TxManager
	clock    domain.Clock
	log      *slog.Logger
}

type TripCoverDeps struct {
	Covers   domain.TripCoverRepository
	Trips    domain.TripRepository
	Members  domain.MembershipRepository
	Storage  domain.FileStorage
	Provider domain.CoverPhotoProvider
	Tx       domain.TxManager
	Clock    domain.Clock
	Logger   *slog.Logger
}

func NewTripCoverService(deps TripCoverDeps) *TripCoverService {
	if deps.Clock == nil {
		deps.Clock = domain.SystemClock{}
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &TripCoverService{authz: authz{members: deps.Members}, covers: deps.Covers,
		trips: deps.Trips, storage: deps.Storage, provider: deps.Provider, tx: deps.Tx,
		clock: deps.Clock, log: deps.Logger}
}

// Suggest returns a cached destination result, fetching it once when needed.
func (s *TripCoverService) Suggest(ctx context.Context, query string) (*domain.TripCoverSuggestion, error) {
	query = strings.Join(strings.Fields(strings.TrimSpace(query)), " ")
	if query == "" {
		return neutralSuggestion(""), nil
	}
	if utf8.RuneCountInString(query) > 200 {
		ve := &domain.ValidationError{}
		ve.Add("query", "too_long", "destination must be at most 200 characters")
		return nil, ve
	}
	key := strings.ToLower(query)
	if cached, err := s.covers.GetSuggestionByQueryKey(ctx, key, s.clock.Now()); err == nil {
		return cached, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}

	if s.provider == nil {
		result := neutralSuggestion(query)
		result.ID = domain.NewID()
		result.QueryKey = key
		result.ExpiresAt = s.clock.Now().Add(coverMissTTL)
		if err := s.covers.UpsertSuggestion(ctx, result); err != nil {
			return nil, err
		}
		return result, nil
	}
	result, err := s.provider.Search(ctx, query)
	if err != nil {
		s.log.Warn("destination cover search failed", "query", query, "error", err)
		return neutralSuggestion(query), nil
	}
	if result == nil {
		result = neutralSuggestion(query)
	}
	result.ID = domain.NewID()
	result.QueryKey = key
	result.Query = query
	ttl := coverCacheTTL
	if !result.Found {
		ttl = coverMissTTL
	}
	result.ExpiresAt = s.clock.Now().Add(ttl)
	if err := s.covers.UpsertSuggestion(ctx, result); err != nil {
		return nil, err
	}
	return result, nil
}

func neutralSuggestion(query string) *domain.TripCoverSuggestion {
	return &domain.TripCoverSuggestion{Query: query, Found: false}
}

// ValidateSuggestion lets a trip write reject an expired or invented suggestion before it
// changes the trip itself.
func (s *TripCoverService) ValidateSuggestion(ctx context.Context, id domain.ID) error {
	suggestion, err := s.covers.GetSuggestionByID(ctx, id)
	if err != nil {
		return err
	}
	if suggestion.ExpiresAt.Before(s.clock.Now()) {
		return domain.ErrNotFound
	}
	return nil
}

// ApplySuggestion sets a searched image, or the neutral cover for a negative result.
func (s *TripCoverService) ApplySuggestion(ctx context.Context, tripID, userID, suggestionID domain.ID) (*domain.Trip, error) {
	if _, err := s.require(ctx, tripID, userID, domain.CapEditTrip); err != nil {
		return nil, err
	}
	suggestion, err := s.covers.GetSuggestionByID(ctx, suggestionID)
	if err != nil {
		return nil, err
	}
	if suggestion.ExpiresAt.Before(s.clock.Now()) {
		return nil, domain.ErrNotFound
	}
	return s.apply(ctx, tripID, suggestion)
}

// AutoAssign searches from the trip name. It never replaces a user upload.
func (s *TripCoverService) AutoAssign(ctx context.Context, tripID, userID domain.ID, query string) (*domain.Trip, error) {
	if _, err := s.require(ctx, tripID, userID, domain.CapEditTrip); err != nil {
		return nil, err
	}
	current, err := s.trips.GetByID(ctx, tripID)
	if err != nil {
		return nil, err
	}
	if current.CoverSource == domain.CoverSourceUploaded {
		return current, nil
	}
	suggestion, err := s.Suggest(ctx, query)
	if err != nil {
		return nil, err
	}
	return s.apply(ctx, tripID, suggestion)
}

func (s *TripCoverService) apply(ctx context.Context, tripID domain.ID, suggestion *domain.TripCoverSuggestion) (*domain.Trip, error) {
	err := s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.trips.LockForWrite(ctx, tripID); err != nil {
			return err
		}
		if suggestion.Found {
			return s.covers.SetSuggested(ctx, tripID, suggestion, s.clock.Now())
		}
		return s.covers.SetNeutral(ctx, tripID, s.clock.Now())
	})
	if err != nil {
		return nil, err
	}
	if suggestion.Found && suggestion.DownloadLocation != "" {
		if err := s.provider.TrackSelection(ctx, suggestion.DownloadLocation); err != nil {
			s.log.Warn("destination cover selection tracking failed", "error", err)
		}
	}
	return s.trips.GetByID(ctx, tripID)
}

type CoverUploadTicket struct {
	Upload    *domain.TripCoverUpload
	UploadURL string
	ExpiresAt time.Time
}

func (s *TripCoverService) RequestUpload(ctx context.Context, tripID, userID domain.ID, contentType, originalName string) (*CoverUploadTicket, error) {
	if s.storage == nil {
		return nil, domain.ErrNotFound
	}
	actor, err := s.require(ctx, tripID, userID, domain.CapEditTrip)
	if err != nil {
		return nil, err
	}
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if !allowedCoverType(contentType) {
		ve := &domain.ValidationError{}
		ve.Add("content_type", "unsupported", "cover must be a JPEG, PNG, WebP, or AVIF image")
		return nil, ve
	}
	if utf8.RuneCountInString(originalName) > 255 {
		ve := &domain.ValidationError{}
		ve.Add("original_name", "too_long", "file name must be at most 255 characters")
		return nil, ve
	}
	id := domain.NewID()
	upload := &domain.TripCoverUpload{ID: id, TripID: tripID, StorageKey: fmt.Sprintf("trips/%s/covers/%s", tripID, id),
		ContentType: contentType, OriginalName: originalName, Status: domain.AttachmentStatusPending,
		UploadedBy: &actor.UserID}
	if err := s.covers.CreateUpload(ctx, upload); err != nil {
		return nil, err
	}
	uploadURL, err := s.storage.PresignUpload(ctx, upload.StorageKey, contentType, coverUploadURLTTL)
	if err != nil {
		return nil, fmt.Errorf("presigning trip cover upload: %w", err)
	}
	return &CoverUploadTicket{Upload: upload, UploadURL: uploadURL, ExpiresAt: s.clock.Now().Add(coverUploadURLTTL)}, nil
}

func (s *TripCoverService) ConfirmUpload(ctx context.Context, tripID, userID, uploadID domain.ID) (*domain.Trip, error) {
	if s.storage == nil {
		return nil, domain.ErrNotFound
	}
	if _, err := s.require(ctx, tripID, userID, domain.CapEditTrip); err != nil {
		return nil, err
	}
	upload, err := s.covers.GetUploadByID(ctx, uploadID)
	if err != nil {
		return nil, err
	}
	if upload.TripID != tripID {
		return nil, errWrongTrip
	}
	if upload.Status == domain.AttachmentStatusReady {
		trip, err := s.trips.GetByID(ctx, tripID)
		if err == nil {
			trip, err = s.Resolve(ctx, trip)
		}
		return trip, err
	}
	info, err := s.storage.Stat(ctx, upload.StorageKey)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			ve := &domain.ValidationError{}
			ve.Add("upload", "not_uploaded", "no uploaded cover was found")
			return nil, ve
		}
		return nil, err
	}
	actualType := strings.ToLower(strings.TrimSpace(strings.Split(info.ContentType, ";")[0]))
	if info.SizeBytes <= 0 || info.SizeBytes > domain.MaxTripCoverSize || actualType != upload.ContentType {
		_ = s.covers.MarkUploadFailed(ctx, upload.ID, s.clock.Now())
		_ = s.storage.Delete(ctx, upload.StorageKey)
		ve := &domain.ValidationError{}
		if info.SizeBytes > domain.MaxTripCoverSize {
			ve.Add("size_bytes", "too_large", "cover exceeds the 12 MiB limit")
		} else {
			ve.Add("upload", "invalid_image", "uploaded object does not match the selected image type")
		}
		return nil, ve
	}
	var previousKey string
	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.trips.LockForWrite(ctx, tripID); err != nil {
			return err
		}
		current, err := s.trips.GetByID(ctx, tripID)
		if err != nil {
			return err
		}
		if current.CoverSource == domain.CoverSourceUploaded {
			previousKey = current.CoverStorageKey
		}
		if err := s.covers.ConfirmUpload(ctx, upload.ID, info.SizeBytes, s.clock.Now()); err != nil {
			return err
		}
		upload.Status = domain.AttachmentStatusReady
		upload.SizeBytes = &info.SizeBytes
		return s.covers.SetUploaded(ctx, tripID, upload, s.clock.Now())
	})
	if err != nil {
		return nil, err
	}
	if previousKey != "" && previousKey != upload.StorageKey {
		if err := s.storage.Delete(ctx, previousKey); err != nil {
			s.log.Warn("deleting replaced trip cover", "error", err)
		}
	}
	trip, err := s.trips.GetByID(ctx, tripID)
	if err != nil {
		return nil, err
	}
	return s.Resolve(ctx, trip)
}

func allowedCoverType(v string) bool {
	switch v {
	case "image/jpeg", "image/png", "image/webp", "image/avif":
		return true
	}
	return false
}

// Resolve adds a short-lived read URL to an uploaded cover. Suggested URLs are already public.
func (s *TripCoverService) Resolve(ctx context.Context, trip *domain.Trip) (*domain.Trip, error) {
	if trip.CoverSource != domain.CoverSourceUploaded || trip.CoverStorageKey == "" {
		return trip, nil
	}
	if s.storage == nil {
		return trip, nil
	}
	u, err := s.storage.PresignDownload(ctx, trip.CoverStorageKey, coverReadURLTTL)
	if err != nil {
		return nil, err
	}
	trip.CoverURL = u
	return trip, nil
}
