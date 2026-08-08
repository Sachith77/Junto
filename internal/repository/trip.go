package repository

import (
	"context"
	"encoding/base64"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/repository/sqlcgen"
)

// TripRepository is the Postgres implementation of domain.TripRepository.
type TripRepository struct{ base }

// NewTripRepository builds a TripRepository.
func NewTripRepository(pool *pgxpool.Pool) *TripRepository {
	return &TripRepository{base{pool: pool}}
}

var _ domain.TripRepository = (*TripRepository)(nil)

func (r *TripRepository) Create(ctx context.Context, t *domain.Trip) error {
	row, err := r.q(ctx).CreateTrip(ctx, sqlcgen.CreateTripParams{
		ID:          t.ID,
		Name:        t.Name,
		Description: t.Description,
		StartDate:   t.StartDate,
		EndDate:     t.EndDate,
		TimeZone:    t.TimeZone,
	})
	if err != nil {
		return mapError("trip", err)
	}
	*t = *toDomainTrip(row)
	return nil
}

func (r *TripRepository) GetByID(ctx context.Context, id domain.ID) (*domain.Trip, error) {
	row, err := r.q(ctx).GetTripByID(ctx, id)
	if err != nil {
		return nil, mapError("trip", err)
	}
	return toDomainTrip(row), nil
}

func (r *TripRepository) Update(ctx context.Context, t *domain.Trip) error {
	q := r.q(ctx)
	row, err := q.UpdateTrip(ctx, sqlcgen.UpdateTripParams{
		Name:        t.Name,
		Description: t.Description,
		StartDate:   t.StartDate,
		EndDate:     t.EndDate,
		TimeZone:    t.TimeZone,
		UpdatedAt:   time.Now().UTC(),
		ID:          t.ID,
		Version:     versionArg(t.Version),
	})
	if err != nil {
		if isNoRows(err) {
			exists, existsErr := q.TripExists(ctx, t.ID)
			if existsErr != nil {
				return mapError("trip", existsErr)
			}
			return resolveWriteMiss("trip", exists)
		}
		return mapError("trip", err)
	}
	*t = *toDomainTrip(row)
	return nil
}

func (r *TripRepository) SoftDelete(ctx context.Context, id domain.ID, at time.Time, expectedVersion int) error {
	q := r.q(ctx)
	n, err := q.SoftDeleteTrip(ctx, sqlcgen.SoftDeleteTripParams{
		DeletedAt: &at,
		ID:        id,
		Version:   versionArg(expectedVersion),
	})
	if err != nil {
		return mapError("trip", err)
	}
	if n == 0 {
		exists, existsErr := q.TripExists(ctx, id)
		if existsErr != nil {
			return mapError("trip", existsErr)
		}
		return resolveWriteMiss("trip", exists)
	}
	return nil
}

// LockForWrite takes the trip's row lock. It is the first statement of every write
// transaction in that trip (D60).
//
// Everything the sync engine claims rests on this call happening BEFORE any entity read: it
// serializes writers within a trip, which is what makes read-modify-write safe and lets
// field-level merge fall out of "an operation writes only the columns it names". Move it
// after the read and two transactions can read the same state, then serialize only at the
// sequence step — a lost update at field granularity, with a log that still looks correctly
// ordered.
//
// A missing or soft-deleted trip returns ErrNotFound here, before any work is done.
func (r *TripRepository) LockForWrite(ctx context.Context, tripID domain.ID) error {
	if _, err := r.q(ctx).LockTripForWrite(ctx, tripID); err != nil {
		return mapError("trip", err)
	}
	return nil
}

// NextOpSeq allocates the next operation sequence number for a trip.
//
// Called once per committed operation, so one intent producing several operations (D63) gets
// consecutive numbers. Cheap to call repeatedly: LockForWrite is already holding the row.
func (r *TripRepository) NextOpSeq(ctx context.Context, tripID domain.ID) (int64, error) {
	seq, err := r.q(ctx).NextOpSeq(ctx, tripID)
	if err != nil {
		return 0, mapError("trip", err)
	}
	return seq, nil
}

// CurrentOpSeq reads a trip's sequence without allocating or locking.
func (r *TripRepository) CurrentOpSeq(ctx context.Context, tripID domain.ID) (int64, error) {
	seq, err := r.q(ctx).CurrentOpSeq(ctx, tripID)
	if err != nil {
		return 0, mapError("trip", err)
	}
	return seq, nil
}

// ListForUser returns the trips a user belongs to, newest first, keyset paginated.
func (r *TripRepository) ListForUser(ctx context.Context, userID domain.ID, page domain.PageRequest) (domain.Page[*domain.Trip], error) {
	var empty domain.Page[*domain.Trip]

	page = page.Normalize()
	params := sqlcgen.ListTripsForUserParams{
		UserID: userID,
		// Fetch one more than asked for. The probe row is how HasMore is determined
		// without a second COUNT query, which would be both slower and wrong: the total can
		// change between the two queries.
		PageLimit: int32(page.Limit) + 1,
	}

	if page.After != "" {
		createdAt, id, err := decodeTripCursor(page.After)
		if err != nil {
			return empty, err
		}
		params.AfterCreatedAt = &createdAt
		params.AfterID = &id
	}

	rows, err := r.q(ctx).ListTripsForUser(ctx, params)
	if err != nil {
		return empty, mapError("trip", err)
	}

	trips := make([]*domain.Trip, 0, len(rows))
	for _, row := range rows {
		trips = append(trips, toDomainTrip(row))
	}
	return domain.NewPage(trips, page.Limit, func(t *domain.Trip) domain.Cursor {
		return encodeTripCursor(t.CreatedAt, t.ID)
	}), nil
}

// Cursor encoding.
//
// The cursor is opaque to clients on purpose: it encodes the sort key of the last row seen,
// and keeping it opaque means the sort key can change without invalidating anyone's saved
// pagination state. base64 is not obfuscation — it is there so the value survives being put
// in a query string unescaped.
//
// RFC3339Nano because the sort key must round-trip to the exact microsecond. Truncating to
// seconds would make the boundary comparison ambiguous for rows created in the same second,
// which is precisely when pagination correctness matters.

const cursorSeparator = "|"

func encodeTripCursor(createdAt time.Time, id domain.ID) domain.Cursor {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + cursorSeparator + id.String()
	return domain.Cursor(base64.RawURLEncoding.EncodeToString([]byte(raw)))
}

func decodeTripCursor(c domain.Cursor) (time.Time, domain.ID, error) {
	// A malformed cursor is user input, so it becomes a field-level validation error rather
	// than a 500. Clients do hand-edit query strings.
	invalid := func() (time.Time, domain.ID, error) {
		ve := &domain.ValidationError{}
		ve.Add("cursor", "invalid_cursor", "pagination cursor is malformed")
		return time.Time{}, domain.NilID, ve
	}

	decoded, err := base64.RawURLEncoding.DecodeString(string(c))
	if err != nil {
		return invalid()
	}
	tsPart, idPart, found := strings.Cut(string(decoded), cursorSeparator)
	if !found {
		return invalid()
	}
	createdAt, err := time.Parse(time.RFC3339Nano, tsPart)
	if err != nil {
		return invalid()
	}
	id, err := domain.ParseID("cursor", idPart)
	if err != nil {
		return invalid()
	}
	return createdAt, id, nil
}
