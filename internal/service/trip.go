package service

import (
	"context"
	"time"

	"github.com/junto/junto/internal/domain"
)

// TripService owns trip lifecycle: creation, editing, deletion, and listing "my trips".
// Membership and invitation management live in MembershipService — a trip and its member
// list are one aggregate for authorization purposes, but they are different enough concerns
// to keep in separate files.
type TripService struct {
	authz
	trips domain.TripRepository
	tx    domain.TxManager
	clock domain.Clock
}

// TripDeps collects TripService's dependencies.
type TripDeps struct {
	Trips   domain.TripRepository
	Members domain.MembershipRepository
	Tx      domain.TxManager
	Clock   domain.Clock
}

// NewTripService builds a TripService.
func NewTripService(deps TripDeps) *TripService {
	if deps.Clock == nil {
		deps.Clock = domain.SystemClock{}
	}
	return &TripService{
		authz: authz{members: deps.Members},
		trips: deps.Trips,
		tx:    deps.Tx,
		clock: deps.Clock,
	}
}

// CreateTripInput is the input to Create.
type CreateTripInput struct {
	Name        string
	Description string
	TimeZone    string
	StartDate   *time.Time
	EndDate     *time.Time
}

// Create makes a trip and adds the caller as its owner in one transaction.
//
// The two writes cannot be split: a trip that briefly exists with no owner is a trip nobody
// can administer (the owner-only capabilities have no actor to grant them to), and a crash
// between the two writes would leave exactly that state.
func (s *TripService) Create(ctx context.Context, userID domain.ID, in CreateTripInput) (*domain.Trip, error) {
	trip := &domain.Trip{
		ID:          domain.NewID(),
		Name:        in.Name,
		Description: in.Description,
		TimeZone:    in.TimeZone,
		StartDate:   in.StartDate,
		EndDate:     in.EndDate,
	}
	if err := trip.Validate(); err != nil {
		return nil, err
	}

	err := s.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := s.trips.Create(ctx, trip); err != nil {
			return err
		}
		return s.members.Add(ctx, &domain.Member{
			ID:     domain.NewID(),
			TripID: trip.ID,
			UserID: userID,
			Role:   domain.RoleOwner,
		})
	})
	if err != nil {
		return nil, err
	}
	return trip, nil
}

// Get returns a trip, provided the caller is a member.
//
// Membership itself is the read gate — every role including viewer carries CapViewTrip — so
// this is a membership check rather than a capability check, and a non-member gets the same
// ErrNotFound as a nonexistent trip.
func (s *TripService) Get(ctx context.Context, tripID, userID domain.ID) (*domain.Trip, error) {
	if _, err := s.actor(ctx, tripID, userID); err != nil {
		return nil, err
	}
	return s.trips.GetByID(ctx, tripID)
}

// UpdateTripInput is the input to Update.
type UpdateTripInput struct {
	Name        string
	Description string
	TimeZone    string
	StartDate   *time.Time
	EndDate     *time.Time
	Version     int
}

// Update edits trip content. Owner or editor only — CapEditTrip is not granted to viewers.
func (s *TripService) Update(ctx context.Context, tripID, userID domain.ID, in UpdateTripInput) (*domain.Trip, error) {
	if _, err := s.require(ctx, tripID, userID, domain.CapEditTrip); err != nil {
		return nil, err
	}

	trip := &domain.Trip{
		ID:          tripID,
		Name:        in.Name,
		Description: in.Description,
		TimeZone:    in.TimeZone,
		StartDate:   in.StartDate,
		EndDate:     in.EndDate,
		Version:     in.Version,
	}
	if err := trip.Validate(); err != nil {
		return nil, err
	}
	if err := s.trips.Update(ctx, trip); err != nil {
		return nil, err
	}
	return trip, nil
}

// Delete soft-deletes a trip. Owner only.
func (s *TripService) Delete(ctx context.Context, tripID, userID domain.ID, expectedVersion int) error {
	if _, err := s.require(ctx, tripID, userID, domain.CapDeleteTrip); err != nil {
		return err
	}
	return s.trips.SoftDelete(ctx, tripID, s.clock.Now(), expectedVersion)
}

// ListForUser returns the trips the caller belongs to, keyset paginated.
func (s *TripService) ListForUser(ctx context.Context, userID domain.ID, page domain.PageRequest) (domain.Page[*domain.Trip], error) {
	return s.trips.ListForUser(ctx, userID, page.Normalize())
}
