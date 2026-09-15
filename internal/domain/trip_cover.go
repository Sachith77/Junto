package domain

import (
	"context"
	"time"
)

const MaxTripCoverSize int64 = 12 * 1024 * 1024

// TripCoverSuggestion is one cached provider result. Found=false is a negative cache entry.
type TripCoverSuggestion struct {
	ID               ID
	QueryKey         string
	Query            string
	Found            bool
	ImageURL         string
	PhotographerName string
	PhotographerURL  string
	PhotoURL         string
	DownloadLocation string
	ExpiresAt        time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type TripCoverUpload struct {
	ID           ID
	TripID       ID
	StorageKey   string
	ContentType  string
	OriginalName string
	SizeBytes    *int64
	Status       AttachmentStatus
	UploadedBy   *ID
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CoverPhotoProvider searches a destination-photo service and records a selected image.
type CoverPhotoProvider interface {
	Search(ctx context.Context, query string) (*TripCoverSuggestion, error)
	TrackSelection(ctx context.Context, downloadLocation string) error
}
