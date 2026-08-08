package service

import (
	"context"
	"time"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/pkg/fracdex"
)

// DayService manages the days within a trip.
//
// Days are logged to the operation log like every other itinerary entity (D72), even though
// they were a candidate to cut from the Slice 1 vocabulary. Omitting them would leave a
// REST-originated day change absent from the log, so a client asking "everything since seq N"
// would silently miss it — the precise hole Rule 3 exists to close, and not a trade worth
// making to save four operation kinds.
type DayService struct {
	authz
	oplog
	days  domain.DayRepository
	clock domain.Clock
}

// DayDeps collects DayService's dependencies.
type DayDeps struct {
	Days    domain.DayRepository
	Members domain.MembershipRepository
	Trips   domain.TripRepository
	Ops     domain.OpLogRepository
	Tx      domain.TxManager
	Pub     domain.OpPublisher
	Clock   domain.Clock
}

// NewDayService builds a DayService.
func NewDayService(deps DayDeps) *DayService {
	if deps.Clock == nil {
		deps.Clock = domain.SystemClock{}
	}
	return &DayService{
		authz: authz{members: deps.Members},
		oplog: newOplog(deps.Trips, deps.Ops, deps.Tx, deps.Pub),
		days:  deps.Days,
		clock: deps.Clock,
	}
}

// ListForTrip returns a trip's days in order. Membership is the read gate.
func (s *DayService) ListForTrip(ctx context.Context, tripID, userID domain.ID) ([]*domain.Day, error) {
	if _, err := s.actor(ctx, tripID, userID); err != nil {
		return nil, err
	}
	return s.days.ListForTrip(ctx, tripID)
}

// CreateDayInput is the input to Create. AfterDayID nil means "insert first".
type CreateDayInput struct {
	Date       *time.Time
	Label      string
	AfterDayID *domain.ID

	// ID lets a client name the day before the server has seen it (D4).
	ID domain.ID
}

// Create appends (or inserts) a day, computing its fractional-index position from the
// requested neighbour, inside the transaction that holds the trip's sequencer.
func (s *DayService) Create(ctx context.Context, tripID, userID domain.ID, in CreateDayInput) (*domain.Day, error) {
	actor, err := s.require(ctx, tripID, userID, domain.CapManageDays)
	if err != nil {
		return nil, err
	}

	id := in.ID
	if id == domain.NilID {
		id = domain.NewID()
	}
	day := &domain.Day{ID: id, TripID: tripID, Date: in.Date, Label: in.Label}

	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		prev, next, err := s.days.NeighbourPositions(ctx, tripID, in.AfterDayID)
		if err != nil {
			return err
		}
		position, err := fracdex.KeyBetween(prev, next)
		if err != nil {
			return err
		}
		day.Position = position

		if err := day.Validate(); err != nil {
			return err
		}
		if err := s.days.Create(ctx, day); err != nil {
			return err
		}
		// The resolved position, never the anchor that produced it (D62).
		return rec.day(ctx, domain.OpDayCreate, day)
	})
	if err != nil {
		return nil, err
	}
	return day, nil
}

// UpdateDayInput is the input to Update.
type UpdateDayInput struct {
	Fields domain.FieldMask
	Date   *time.Time
	Label  string

	// Version nil means merge semantics, no optimistic-concurrency precondition (D69).
	Version *int
}

// Update edits a day's date and label. Reordering is a separate operation (Move).
func (s *DayService) Update(ctx context.Context, tripID, userID, dayID domain.ID, in UpdateDayInput) (*domain.Day, error) {
	actor, err := s.require(ctx, tripID, userID, domain.CapManageDays)
	if err != nil {
		return nil, err
	}

	mask := maskFor(domain.OpDayEdit, in.Fields)
	if err := mask.Validate(domain.OpDayEdit); err != nil {
		return nil, err
	}

	var day *domain.Day
	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		existing, err := s.getInTrip(ctx, tripID, dayID)
		if err != nil {
			return err
		}

		day = &domain.Day{
			ID: dayID, TripID: tripID,
			Date: existing.Date, Label: existing.Label, Position: existing.Position,
			Version: versionOrCurrent(in.Version, existing.Version),
		}
		if mask.Has(domain.FieldDate) {
			day.Date = in.Date
		}
		if mask.Has(domain.FieldLabel) {
			day.Label = in.Label
		}

		if err := day.Validate(); err != nil {
			return err
		}
		if err := s.days.Update(ctx, day); err != nil {
			return err
		}
		return rec.day(ctx, domain.OpDayEdit, day, mask...)
	})
	if err != nil {
		return nil, err
	}
	return day, nil
}

// Move reorders a day to sit immediately after afterDayID (nil means "move to the start").
func (s *DayService) Move(ctx context.Context, tripID, userID, dayID domain.ID, afterDayID *domain.ID, expectedVersion *int) error {
	actor, err := s.require(ctx, tripID, userID, domain.CapManageDays)
	if err != nil {
		return err
	}

	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		existing, err := s.getInTrip(ctx, tripID, dayID)
		if err != nil {
			return err
		}
		prev, next, err := s.days.NeighbourPositions(ctx, tripID, afterDayID)
		if err != nil {
			return err
		}
		position, err := fracdex.KeyBetween(prev, next)
		if err != nil {
			return err
		}

		day := &domain.Day{
			ID: dayID, TripID: tripID, Date: existing.Date, Label: existing.Label,
			Position: position, Version: versionOrCurrent(expectedVersion, existing.Version),
		}
		if err := s.days.Update(ctx, day); err != nil {
			return err
		}
		return rec.day(ctx, domain.OpDayMove, day, domain.FieldPosition)
	})
	return err
}

// Delete soft-deletes a day.
//
// Known gap, carried over rather than solved silently: soft-deleting a day does not touch its
// slots. A slot pointing at a deleted day keeps rendering in that day's bucket until a client
// reconciles it, because ON DELETE SET NULL only fires on a HARD delete. Flagged for a product
// decision rather than guessed at here.
func (s *DayService) Delete(ctx context.Context, tripID, userID, dayID domain.ID, expectedVersion *int) error {
	actor, err := s.require(ctx, tripID, userID, domain.CapManageDays)
	if err != nil {
		return err
	}

	now := s.clock.Now()
	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		existing, err := s.getInTrip(ctx, tripID, dayID)
		if err != nil {
			return err
		}
		if err := s.days.SoftDelete(ctx, dayID, now,
			versionOrCurrent(expectedVersion, existing.Version)); err != nil {
			return err
		}
		tombstone := *existing
		tombstone.DeletedAt = &now
		tombstone.UpdatedAt = now
		tombstone.Version = existing.Version + 1
		return rec.day(ctx, domain.OpDayDelete, &tombstone, domain.FieldDeletedAt)
	})
	return err
}

func (s *DayService) getInTrip(ctx context.Context, tripID, dayID domain.ID) (*domain.Day, error) {
	day, err := s.days.GetByID(ctx, dayID)
	if err != nil {
		return nil, err
	}
	if err := checkTrip(day.TripID, tripID); err != nil {
		return nil, err
	}
	return day, nil
}
