package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/junto/junto/internal/domain"
)

type fakeTripCovers struct {
	mu          sync.Mutex
	trips       *fakeTrips
	suggestions map[domain.ID]*domain.TripCoverSuggestion
	byKey       map[string]domain.ID
	uploads     map[domain.ID]*domain.TripCoverUpload
}

func newFakeTripCovers(trips *fakeTrips) *fakeTripCovers {
	return &fakeTripCovers{trips: trips, suggestions: map[domain.ID]*domain.TripCoverSuggestion{}, byKey: map[string]domain.ID{}, uploads: map[domain.ID]*domain.TripCoverUpload{}}
}

func (f *fakeTripCovers) GetSuggestionByQueryKey(_ context.Context, key string, now time.Time) (*domain.TripCoverSuggestion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.byKey[key]
	if !ok || !f.suggestions[id].ExpiresAt.After(now) {
		return nil, domain.ErrNotFound
	}
	copy := *f.suggestions[id]
	return &copy, nil
}
func (f *fakeTripCovers) GetSuggestionByID(_ context.Context, id domain.ID) (*domain.TripCoverSuggestion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.suggestions[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	copy := *s
	return &copy, nil
}
func (f *fakeTripCovers) UpsertSuggestion(_ context.Context, s *domain.TripCoverSuggestion) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.byKey[s.QueryKey]; ok {
		s.ID = id
	}
	copy := *s
	f.suggestions[s.ID] = &copy
	f.byKey[s.QueryKey] = s.ID
	return nil
}
func (f *fakeTripCovers) CreateUpload(_ context.Context, u *domain.TripCoverUpload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now().UTC()
	u.CreatedAt, u.UpdatedAt = now, now
	copy := *u
	f.uploads[u.ID] = &copy
	return nil
}
func (f *fakeTripCovers) GetUploadByID(_ context.Context, id domain.ID) (*domain.TripCoverUpload, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.uploads[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	copy := *u
	return &copy, nil
}
func (f *fakeTripCovers) ConfirmUpload(_ context.Context, id domain.ID, size int64, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.uploads[id]
	if !ok {
		return domain.ErrNotFound
	}
	u.Status = domain.AttachmentStatusReady
	u.SizeBytes = &size
	u.UpdatedAt = at
	return nil
}
func (f *fakeTripCovers) MarkUploadFailed(_ context.Context, id domain.ID, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.uploads[id]
	if !ok {
		return domain.ErrNotFound
	}
	u.Status = domain.AttachmentStatusFailed
	u.UpdatedAt = at
	return nil
}
func (f *fakeTripCovers) updateTrip(id domain.ID, fn func(*domain.Trip)) error {
	f.trips.mu.Lock()
	defer f.trips.mu.Unlock()
	trip, ok := f.trips.byID[id]
	if !ok {
		return domain.ErrNotFound
	}
	fn(trip)
	trip.Version++
	return nil
}
func (f *fakeTripCovers) SetSuggested(_ context.Context, id domain.ID, s *domain.TripCoverSuggestion, _ time.Time) error {
	return f.updateTrip(id, func(t *domain.Trip) {
		t.CoverSource = domain.CoverSourceSuggested
		t.CoverStorageKey = ""
		t.CoverImageURL = s.ImageURL
		t.CoverPhotographerName = s.PhotographerName
		t.CoverPhotographerURL = s.PhotographerURL
		t.CoverPhotoURL = s.PhotoURL
	})
}
func (f *fakeTripCovers) SetNeutral(_ context.Context, id domain.ID, _ time.Time) error {
	return f.updateTrip(id, func(t *domain.Trip) {
		t.CoverSource = domain.CoverSourceNeutral
		t.CoverStorageKey = ""
		t.CoverImageURL = ""
	})
}
func (f *fakeTripCovers) SetUploaded(_ context.Context, id domain.ID, u *domain.TripCoverUpload, _ time.Time) error {
	return f.updateTrip(id, func(t *domain.Trip) {
		t.CoverSource = domain.CoverSourceUploaded
		t.CoverStorageKey = u.StorageKey
		t.CoverContentType = u.ContentType
		t.CoverImageURL = ""
	})
}

type fakeCoverProvider struct {
	searches int
	result   *domain.TripCoverSuggestion
}

func (f *fakeCoverProvider) Search(_ context.Context, _ string) (*domain.TripCoverSuggestion, error) {
	f.searches++
	copy := *f.result
	return &copy, nil
}
func (f *fakeCoverProvider) TrackSelection(context.Context, string) error { return nil }

func TestTripCoverSuggestionIsCached(t *testing.T) {
	members := newFakeMembers()
	trips := newFakeTrips(members)
	covers := newFakeTripCovers(trips)
	provider := &fakeCoverProvider{result: &domain.TripCoverSuggestion{Found: true, ImageURL: "https://images.unsplash.com/kyoto", PhotographerName: "A", PhotographerURL: "https://unsplash.com/@a", PhotoURL: "https://unsplash.com/photos/a"}}
	s := NewTripCoverService(TripCoverDeps{Covers: covers, Trips: trips, Members: members, Provider: provider, Tx: &fakeTx{}})
	first, err := s.Suggest(context.Background(), "  Kyoto  ")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Suggest(context.Background(), "kyoto")
	if err != nil {
		t.Fatal(err)
	}
	if provider.searches != 1 {
		t.Fatalf("provider searched %d times, want 1", provider.searches)
	}
	if first.ID != second.ID {
		t.Fatal("cache returned a different suggestion")
	}
}

func TestTripCoverUploadBecomesActiveOnlyAfterConfirmation(t *testing.T) {
	ctx := context.Background()
	members := newFakeMembers()
	trips := newFakeTrips(members)
	covers := newFakeTripCovers(trips)
	storage := newFakeStorage()
	ownerID := domain.NewID()
	tripService := NewTripService(TripDeps{Trips: trips, Members: members, Tx: &fakeTx{}})
	trip, err := tripService.Create(ctx, ownerID, CreateTripInput{Name: "Kyoto", TimeZone: "Asia/Tokyo"})
	if err != nil {
		t.Fatal(err)
	}
	s := NewTripCoverService(TripCoverDeps{Covers: covers, Trips: trips, Members: members, Storage: storage, Provider: &fakeCoverProvider{result: &domain.TripCoverSuggestion{}}, Tx: &fakeTx{}})
	ticket, err := s.RequestUpload(ctx, trip.ID, ownerID, "image/jpeg", "kyoto.jpg")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := trips.GetByID(ctx, trip.ID)
	if before.CoverSource == domain.CoverSourceUploaded {
		t.Fatal("pending upload became visible")
	}
	storage.putObject(ticket.Upload.StorageKey, 2048, "image/jpeg")
	confirmed, err := s.ConfirmUpload(ctx, trip.ID, ownerID, ticket.Upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.CoverSource != domain.CoverSourceUploaded || confirmed.CoverURL == "" {
		t.Fatalf("cover was not activated: %+v", confirmed)
	}
}
