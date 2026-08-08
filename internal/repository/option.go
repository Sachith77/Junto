package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/repository/sqlcgen"
)

// SlotOptionRepository is the Postgres implementation of domain.SlotOptionRepository.
type SlotOptionRepository struct{ base }

// NewSlotOptionRepository builds a SlotOptionRepository.
func NewSlotOptionRepository(pool *pgxpool.Pool) *SlotOptionRepository {
	return &SlotOptionRepository{base{pool: pool}}
}

var _ domain.SlotOptionRepository = (*SlotOptionRepository)(nil)

func (r *SlotOptionRepository) Create(ctx context.Context, o *domain.SlotOption) error {
	row, err := r.q(ctx).CreateSlotOption(ctx, sqlcgen.CreateSlotOptionParams{
		ID:                 o.ID,
		SlotID:             o.SlotID,
		TripID:             o.TripID,
		Title:              o.Title,
		Notes:              o.Notes,
		ExternalUrl:        o.ExternalURL,
		EstimatedCostMinor: o.EstimatedCostMinor,
		PlaceName:          o.Place.Name,
		PlaceAddress:       o.Place.Address,
		PlaceLat:           o.Place.Lat,
		PlaceLng:           o.Place.Lng,
		PlaceProviderID:    o.Place.ProviderID,
		ProposedBy:         o.ProposedBy,
	})
	if err != nil {
		// An option claiming a trip its slot does not belong to fails the composite FK
		// (slot_id, trip_id) -> slots(id, trip_id) here.
		return mapError("slot option", err)
	}
	*o = *toDomainSlotOption(row)
	return nil
}

func (r *SlotOptionRepository) GetByID(ctx context.Context, id domain.ID) (*domain.SlotOption, error) {
	row, err := r.q(ctx).GetSlotOptionByID(ctx, id)
	if err != nil {
		return nil, mapError("slot option", err)
	}
	return toDomainSlotOption(row), nil
}

func (r *SlotOptionRepository) ListForSlot(ctx context.Context, slotID domain.ID) ([]*domain.SlotOption, error) {
	rows, err := r.q(ctx).ListOptionsForSlot(ctx, slotID)
	if err != nil {
		return nil, mapError("slot option", err)
	}
	return toDomainSlotOptions(rows), nil
}

func (r *SlotOptionRepository) ListForTrip(ctx context.Context, tripID domain.ID) ([]*domain.SlotOption, error) {
	rows, err := r.q(ctx).ListOptionsForTrip(ctx, tripID)
	if err != nil {
		return nil, mapError("slot option", err)
	}
	return toDomainSlotOptions(rows), nil
}

func (r *SlotOptionRepository) Update(ctx context.Context, o *domain.SlotOption) error {
	q := r.q(ctx)
	row, err := q.UpdateSlotOption(ctx, sqlcgen.UpdateSlotOptionParams{
		Title:              o.Title,
		Notes:              o.Notes,
		ExternalUrl:        o.ExternalURL,
		EstimatedCostMinor: o.EstimatedCostMinor,
		PlaceName:          o.Place.Name,
		PlaceAddress:       o.Place.Address,
		PlaceLat:           o.Place.Lat,
		PlaceLng:           o.Place.Lng,
		PlaceProviderID:    o.Place.ProviderID,
		UpdatedAt:          time.Now().UTC(),
		ID:                 o.ID,
		Version:            versionArg(o.Version),
	})
	if err != nil {
		if isNoRows(err) {
			exists, existsErr := q.SlotOptionExists(ctx, o.ID)
			if existsErr != nil {
				return mapError("slot option", existsErr)
			}
			return resolveWriteMiss("slot option", exists)
		}
		return mapError("slot option", err)
	}
	*o = *toDomainSlotOption(row)
	return nil
}

// SoftDelete tombstones an option.
//
// Note what this deliberately does NOT do: it does not clear slots.selected_option_id if the
// deleted option happened to be the selection. A soft delete leaves the row present, so the
// foreign key stays satisfied and the database has no opinion. Whether a slot should lose its
// resolution when the chosen candidate is withdrawn is a product decision, and it belongs in
// the service layer where it can be made explicitly rather than as a side effect here.
func (r *SlotOptionRepository) SoftDelete(ctx context.Context, id domain.ID, at time.Time, expectedVersion int) error {
	q := r.q(ctx)
	n, err := q.SoftDeleteSlotOption(ctx, sqlcgen.SoftDeleteSlotOptionParams{
		DeletedAt: &at,
		ID:        id,
		Version:   versionArg(expectedVersion),
	})
	if err != nil {
		return mapError("slot option", err)
	}
	if n == 0 {
		exists, existsErr := q.SlotOptionExists(ctx, id)
		if existsErr != nil {
			return mapError("slot option", existsErr)
		}
		return resolveWriteMiss("slot option", exists)
	}
	return nil
}

// CountLive reports how many live options a slot has. Not part of the domain port — the
// service uses it to decide whether a slot still has anything to resolve to.
func (r *SlotOptionRepository) CountLive(ctx context.Context, slotID domain.ID) (int, error) {
	n, err := r.q(ctx).CountLiveOptionsForSlot(ctx, slotID)
	if err != nil {
		return 0, mapError("slot option", err)
	}
	return int(n), nil
}
