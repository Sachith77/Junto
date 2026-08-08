package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/repository/sqlcgen"
)

// SlotRepository is the Postgres implementation of domain.SlotRepository.
//
// Slots are the entity the Stage 2 sync engine operates on, so the granularity of the methods
// here matters more than elsewhere: Update changes content, Move changes placement,
// SetSelectedOption resolves, SetStatus records coverage — and none of them does another's
// job. A sync operation should map to exactly one of them.
type SlotRepository struct{ base }

// NewSlotRepository builds a SlotRepository.
func NewSlotRepository(pool *pgxpool.Pool) *SlotRepository {
	return &SlotRepository{base{pool: pool}}
}

var _ domain.SlotRepository = (*SlotRepository)(nil)

func (r *SlotRepository) Create(ctx context.Context, s *domain.Slot) error {
	row, err := r.q(ctx).CreateSlot(ctx, sqlcgen.CreateSlotParams{
		ID:        s.ID,
		TripID:    s.TripID,
		DayID:     s.DayID,
		Kind:      string(s.Kind),
		Title:     s.Title,
		Notes:     s.Notes,
		StartTime: fromDomainTimeOfDay(s.StartTime),
		EndTime:   fromDomainTimeOfDay(s.EndTime),
		Position:  s.Position,
		Status:    string(s.Status),
		CreatedBy: s.CreatedBy,
	})
	if err != nil {
		// A slot pointing at another trip's day fails the composite FK here and is mapped to
		// a field-level "the day does not belong to this trip". The database makes that state
		// unrepresentable; this just turns the rejection into a usable message.
		return mapError("slot", err)
	}
	*s = *toDomainSlot(row)
	return nil
}

func (r *SlotRepository) GetByID(ctx context.Context, id domain.ID) (*domain.Slot, error) {
	row, err := r.q(ctx).GetSlotByID(ctx, id)
	if err != nil {
		return nil, mapError("slot", err)
	}
	return toDomainSlot(row), nil
}

func (r *SlotRepository) ListForTrip(ctx context.Context, tripID domain.ID) ([]*domain.Slot, error) {
	rows, err := r.q(ctx).ListSlotsForTrip(ctx, tripID)
	if err != nil {
		return nil, mapError("slot", err)
	}
	return toDomainSlots(rows), nil
}

func (r *SlotRepository) ListForDay(ctx context.Context, dayID domain.ID) ([]*domain.Slot, error) {
	rows, err := r.q(ctx).ListSlotsForDay(ctx, &dayID)
	if err != nil {
		return nil, mapError("slot", err)
	}
	return toDomainSlots(rows), nil
}

func (r *SlotRepository) ListBacklog(ctx context.Context, tripID domain.ID) ([]*domain.Slot, error) {
	rows, err := r.q(ctx).ListBacklogSlots(ctx, tripID)
	if err != nil {
		return nil, mapError("slot", err)
	}
	return toDomainSlots(rows), nil
}

// Update changes a slot's content. It deliberately cannot change day, position, selection or
// status; each of those has its own method.
func (r *SlotRepository) Update(ctx context.Context, s *domain.Slot) error {
	q := r.q(ctx)
	row, err := q.UpdateSlot(ctx, sqlcgen.UpdateSlotParams{
		Kind:      string(s.Kind),
		Title:     s.Title,
		Notes:     s.Notes,
		StartTime: fromDomainTimeOfDay(s.StartTime),
		EndTime:   fromDomainTimeOfDay(s.EndTime),
		UpdatedAt: time.Now().UTC(),
		ID:        s.ID,
		Version:   versionArg(s.Version),
	})
	if err != nil {
		return r.resolve(ctx, q, s.ID, err)
	}
	*s = *toDomainSlot(row)
	return nil
}

// Move changes a slot's day and position together, in one statement and one version bump.
//
// A move must never be observable as a delete followed by an insert: a client that saw only
// the first half would render the slot as vanished, and in Stage 2 a two-operation move would
// hand the sync engine an intermediate state that never actually existed.
func (r *SlotRepository) Move(ctx context.Context, id domain.ID, dayID *domain.ID, position string, expectedVersion int, at time.Time) error {
	q := r.q(ctx)
	_, err := q.MoveSlot(ctx, sqlcgen.MoveSlotParams{
		DayID:     dayID,
		Position:  position,
		UpdatedAt: at,
		ID:        id,
		Version:   versionArg(expectedVersion),
	})
	if err != nil {
		return r.resolve(ctx, q, id, err)
	}
	return nil
}

// SetSelectedOption records the group's resolution, or clears it when optionID is nil.
func (r *SlotRepository) SetSelectedOption(ctx context.Context, slotID domain.ID, optionID *domain.ID, expectedVersion int, at time.Time) error {
	q := r.q(ctx)
	_, err := q.SetSlotSelectedOption(ctx, sqlcgen.SetSlotSelectedOptionParams{
		SelectedOptionID: optionID,
		UpdatedAt:        at,
		ID:               slotID,
		Version:          versionArg(expectedVersion),
	})
	if err != nil {
		// Selecting an option that belongs to a different slot fails the composite FK and is
		// mapped to a field-level message rather than a generic conflict.
		return r.resolve(ctx, q, slotID, err)
	}
	return nil
}

// SetStatus records Live-mode coverage together with who changed it and when.
func (r *SlotRepository) SetStatus(ctx context.Context, slotID domain.ID, status domain.SlotStatus, by domain.ID, expectedVersion int, at time.Time) error {
	q := r.q(ctx)
	_, err := q.SetSlotStatus(ctx, sqlcgen.SetSlotStatusParams{
		Status:          string(status),
		StatusChangedAt: &at,
		StatusChangedBy: &by,
		ID:              slotID,
		Version:         versionArg(expectedVersion),
	})
	if err != nil {
		return r.resolve(ctx, q, slotID, err)
	}
	return nil
}

func (r *SlotRepository) SoftDelete(ctx context.Context, id domain.ID, at time.Time, expectedVersion int) error {
	q := r.q(ctx)
	n, err := q.SoftDeleteSlot(ctx, sqlcgen.SoftDeleteSlotParams{
		DeletedAt: &at,
		ID:        id,
		Version:   versionArg(expectedVersion),
	})
	if err != nil {
		return mapError("slot", err)
	}
	if n == 0 {
		exists, existsErr := q.SlotExists(ctx, id)
		if existsErr != nil {
			return mapError("slot", existsErr)
		}
		return resolveWriteMiss("slot", exists)
	}
	return nil
}

// NeighbourPositions returns the position keys bracketing the slot after afterSlotID within
// the given bucket (a day, or the backlog when dayID is nil).
func (r *SlotRepository) NeighbourPositions(ctx context.Context, tripID domain.ID, dayID *domain.ID, afterSlotID *domain.ID) (string, string, error) {
	q := r.q(ctx)

	if afterSlotID == nil {
		next, err := q.FirstSlotPosition(ctx, sqlcgen.FirstSlotPositionParams{
			TripID: tripID,
			DayID:  dayID,
		})
		if err != nil {
			if isNoRows(err) {
				return "", "", nil // empty bucket: unbounded on both sides
			}
			return "", "", mapError("slot", err)
		}
		return "", next, nil
	}

	prev, err := q.GetSlotPosition(ctx, *afterSlotID)
	if err != nil {
		// A missing anchor is a real error: the caller asked to insert after a specific slot
		// that does not exist, and silently appending to the end would put the row somewhere
		// the user did not ask for.
		return "", "", mapError("slot", err)
	}

	next, err := q.SlotPositionAfter(ctx, sqlcgen.SlotPositionAfterParams{
		TripID:        tripID,
		DayID:         dayID,
		AfterPosition: prev,
		AfterID:       *afterSlotID,
	})
	if err != nil {
		if isNoRows(err) {
			return prev, "", nil // anchor is last in its bucket
		}
		return "", "", mapError("slot", err)
	}
	return prev, next, nil
}

// resolve turns a failed RETURNING update into the right domain error.
//
// Shared by the four write methods because they all have the same failure shape: pgx reports
// an optimistic-concurrency miss as ErrNoRows, and "row absent" must be distinguished from
// "row at a different version" — collapsing them returns 404 for a concurrent edit, and a
// client that retries a 404 abandons data that is still there.
func (r *SlotRepository) resolve(ctx context.Context, q *sqlcgen.Queries, id domain.ID, err error) error {
	if !isNoRows(err) {
		return mapError("slot", err)
	}
	exists, existsErr := q.SlotExists(ctx, id)
	if existsErr != nil {
		return mapError("slot", existsErr)
	}
	return resolveWriteMiss("slot", exists)
}
