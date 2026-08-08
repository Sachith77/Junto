package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/repository/sqlcgen"
)

// DayRepository is the Postgres implementation of domain.DayRepository.
type DayRepository struct{ base }

// NewDayRepository builds a DayRepository.
func NewDayRepository(pool *pgxpool.Pool) *DayRepository {
	return &DayRepository{base{pool: pool}}
}

var _ domain.DayRepository = (*DayRepository)(nil)

func (r *DayRepository) Create(ctx context.Context, d *domain.Day) error {
	row, err := r.q(ctx).CreateDay(ctx, sqlcgen.CreateDayParams{
		ID:       d.ID,
		TripID:   d.TripID,
		Date:     d.Date,
		Label:    d.Label,
		Position: d.Position,
	})
	if err != nil {
		return mapError("day", err)
	}
	*d = *toDomainDay(row)
	return nil
}

func (r *DayRepository) GetByID(ctx context.Context, id domain.ID) (*domain.Day, error) {
	row, err := r.q(ctx).GetDayByID(ctx, id)
	if err != nil {
		return nil, mapError("day", err)
	}
	return toDomainDay(row), nil
}

func (r *DayRepository) ListForTrip(ctx context.Context, tripID domain.ID) ([]*domain.Day, error) {
	rows, err := r.q(ctx).ListDaysForTrip(ctx, tripID)
	if err != nil {
		return nil, mapError("day", err)
	}
	out := make([]*domain.Day, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDomainDay(row))
	}
	return out, nil
}

func (r *DayRepository) Update(ctx context.Context, d *domain.Day) error {
	q := r.q(ctx)
	row, err := q.UpdateDay(ctx, sqlcgen.UpdateDayParams{
		Date:      d.Date,
		Label:     d.Label,
		Position:  d.Position,
		UpdatedAt: time.Now().UTC(),
		ID:        d.ID,
		Version:   versionArg(d.Version),
	})
	if err != nil {
		if isNoRows(err) {
			exists, existsErr := q.DayExists(ctx, d.ID)
			if existsErr != nil {
				return mapError("day", existsErr)
			}
			return resolveWriteMiss("day", exists)
		}
		return mapError("day", err)
	}
	*d = *toDomainDay(row)
	return nil
}

func (r *DayRepository) SoftDelete(ctx context.Context, id domain.ID, at time.Time, expectedVersion int) error {
	q := r.q(ctx)
	n, err := q.SoftDeleteDay(ctx, sqlcgen.SoftDeleteDayParams{
		DeletedAt: &at,
		ID:        id,
		Version:   versionArg(expectedVersion),
	})
	if err != nil {
		return mapError("day", err)
	}
	if n == 0 {
		exists, existsErr := q.DayExists(ctx, id)
		if existsErr != nil {
			return mapError("day", existsErr)
		}
		return resolveWriteMiss("day", exists)
	}
	return nil
}

// NeighbourPositions returns the position keys bracketing the slot after afterDayID.
//
// An empty return value means "unbounded on that side", which is exactly what
// fracdex.KeyBetween expects. Both a missing anchor and an empty list are normal, so
// pgx.ErrNoRows is translated to "" rather than treated as a failure.
func (r *DayRepository) NeighbourPositions(ctx context.Context, tripID domain.ID, afterDayID *domain.ID) (string, string, error) {
	q := r.q(ctx)

	if afterDayID == nil {
		// Inserting at the start: there is no predecessor, and the successor is whatever
		// currently sorts first.
		next, err := q.FirstDayPosition(ctx, tripID)
		if err != nil {
			if isNoRows(err) {
				return "", "", nil // empty trip: unbounded on both sides
			}
			return "", "", mapError("day", err)
		}
		return "", next, nil
	}

	prev, err := q.GetDayPosition(ctx, *afterDayID)
	if err != nil {
		// A missing anchor is a real error: the caller asked to insert after a specific day
		// that does not exist, and silently appending to the end would put the row somewhere
		// the user did not ask for.
		return "", "", mapError("day", err)
	}

	next, err := q.DayPositionAfter(ctx, sqlcgen.DayPositionAfterParams{
		TripID:        tripID,
		AfterPosition: prev,
		AfterID:       *afterDayID,
	})
	if err != nil {
		if isNoRows(err) {
			return prev, "", nil // anchor is last: unbounded above
		}
		return "", "", mapError("day", err)
	}
	return prev, next, nil
}
