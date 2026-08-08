package service

import (
	"context"

	"github.com/junto/junto/internal/domain"
)

// SlotOptionService manages the candidate proposals under a slot.
type SlotOptionService struct {
	authz
	oplog
	options domain.SlotOptionRepository
	slots   domain.SlotRepository
	clock   domain.Clock
}

// SlotOptionDeps collects SlotOptionService's dependencies.
type SlotOptionDeps struct {
	Options domain.SlotOptionRepository
	Slots   domain.SlotRepository
	Members domain.MembershipRepository
	Trips   domain.TripRepository
	Ops     domain.OpLogRepository
	Tx      domain.TxManager
	Pub     domain.OpPublisher
	Clock   domain.Clock
}

// NewSlotOptionService builds a SlotOptionService.
func NewSlotOptionService(deps SlotOptionDeps) *SlotOptionService {
	if deps.Clock == nil {
		deps.Clock = domain.SystemClock{}
	}
	return &SlotOptionService{
		authz:   authz{members: deps.Members},
		oplog:   newOplog(deps.Trips, deps.Ops, deps.Tx, deps.Pub),
		options: deps.Options,
		slots:   deps.Slots,
		clock:   deps.Clock,
	}
}

// ListForSlot returns a slot's candidates in proposal order.
func (s *SlotOptionService) ListForSlot(ctx context.Context, tripID, userID, slotID domain.ID) ([]*domain.SlotOption, error) {
	if _, err := s.actor(ctx, tripID, userID); err != nil {
		return nil, err
	}
	if err := s.checkSlotInTrip(ctx, tripID, slotID); err != nil {
		return nil, err
	}
	return s.options.ListForSlot(ctx, slotID)
}

// CreateOptionInput is the input to Create.
type CreateOptionInput struct {
	Title              string
	Notes              string
	ExternalURL        string
	EstimatedCostMinor *int64
	Place              domain.Place

	// ID lets a client name the option before the server has seen it (D4).
	ID domain.ID
}

// Create proposes a new candidate under a slot.
func (s *SlotOptionService) Create(ctx context.Context, tripID, userID, slotID domain.ID, in CreateOptionInput) (*domain.SlotOption, error) {
	actor, err := s.require(ctx, tripID, userID, domain.CapProposeOptions)
	if err != nil {
		return nil, err
	}

	id := in.ID
	if id == domain.NilID {
		id = domain.NewID()
	}
	option := &domain.SlotOption{
		ID: id, SlotID: slotID, TripID: tripID,
		Title: in.Title, Notes: in.Notes, ExternalURL: in.ExternalURL,
		EstimatedCostMinor: in.EstimatedCostMinor, Place: in.Place,
		ProposedBy: &actor.UserID,
	}

	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		if err := s.checkSlotInTrip(ctx, tripID, slotID); err != nil {
			return err
		}
		if err := option.Validate(); err != nil {
			return err
		}
		if err := s.options.Create(ctx, option); err != nil {
			return err
		}
		return rec.option(ctx, domain.OpOptionCreate, option)
	})
	if err != nil {
		return nil, err
	}
	return option, nil
}

// UpdateOptionInput is the input to Update.
//
// Fields is the D64 mask: empty replaces every editable field (whole-object REST semantics),
// non-empty names exactly what changes. This is what makes two members editing an option's
// title and its address concurrently both succeed — the reason D6 kept place data in discrete
// columns rather than a JSONB blob in the first place.
type UpdateOptionInput struct {
	Fields             domain.FieldMask
	Title              string
	Notes              string
	ExternalURL        string
	EstimatedCostMinor *int64
	Place              domain.Place

	// Version nil means merge semantics, no optimistic-concurrency precondition (D69).
	Version *int
}

// Update edits a candidate's content.
func (s *SlotOptionService) Update(ctx context.Context, tripID, userID, optionID domain.ID, in UpdateOptionInput) (*domain.SlotOption, error) {
	actor, err := s.require(ctx, tripID, userID, domain.CapProposeOptions)
	if err != nil {
		return nil, err
	}

	mask := maskFor(domain.OpOptionEdit, in.Fields)
	if err := mask.Validate(domain.OpOptionEdit); err != nil {
		return nil, err
	}

	var option *domain.SlotOption
	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		existing, err := s.getInTrip(ctx, tripID, optionID)
		if err != nil {
			return err
		}

		option = &domain.SlotOption{
			ID: optionID, SlotID: existing.SlotID, TripID: tripID,
			Title: existing.Title, Notes: existing.Notes, ExternalURL: existing.ExternalURL,
			EstimatedCostMinor: existing.EstimatedCostMinor, Place: existing.Place,
			Version: versionOrCurrent(in.Version, existing.Version),
		}
		if mask.Has(domain.FieldTitle) {
			option.Title = in.Title
		}
		if mask.Has(domain.FieldNotes) {
			option.Notes = in.Notes
		}
		if mask.Has(domain.FieldExternalURL) {
			option.ExternalURL = in.ExternalURL
		}
		if mask.Has(domain.FieldEstimatedCost) {
			option.EstimatedCostMinor = in.EstimatedCostMinor
		}
		if mask.Has(domain.FieldPlaceName) {
			option.Place.Name = in.Place.Name
		}
		if mask.Has(domain.FieldPlaceAddress) {
			option.Place.Address = in.Place.Address
		}
		// Latitude and longitude move together: the schema's slot_options_latlng CHECK makes
		// a half-set coordinate pair unrepresentable, so a mask naming one without the other
		// would be rejected by the database rather than merged.
		if mask.Has(domain.FieldPlaceLat) {
			option.Place.Lat = in.Place.Lat
		}
		if mask.Has(domain.FieldPlaceLng) {
			option.Place.Lng = in.Place.Lng
		}
		if mask.Has(domain.FieldPlaceProviderID) {
			option.Place.ProviderID = in.Place.ProviderID
		}

		if err := option.Validate(); err != nil {
			return err
		}
		if err := s.options.Update(ctx, option); err != nil {
			return err
		}
		return rec.option(ctx, domain.OpOptionEdit, option, mask...)
	})
	if err != nil {
		return nil, err
	}
	return option, nil
}

// Delete withdraws a candidate.
//
// If this option is currently its slot's selection, the slot's selected_option_id is cleared
// back to nil in the SAME transaction (D56). It clears rather than auto-promoting another
// candidate: silently picking a new "winner" when the group's actual choice disappears would
// substitute this service's judgment for theirs.
//
// # Why that cascade is a SECOND operation (D63)
//
// The clearing is logged as its own op, sharing this intent's cause id:
//
//	seq N    option.delete.v1       entity=<option>
//	seq N+1  slot.select_option.v1  entity=<slot>  fields=[selected_option_id]
//
// The alternative — one op that clients expand by re-deriving the rule — would put a piece of
// domain logic in every client and give it somewhere to drift from the server. With two ops a
// log entry is uniformly "one entity, one set of field changes", replay is a pure fold, and
// fold(log) == database state stays checkable.
func (s *SlotOptionService) Delete(ctx context.Context, tripID, userID, optionID domain.ID, expectedVersion *int) error {
	actor, err := s.require(ctx, tripID, userID, domain.CapProposeOptions)
	if err != nil {
		return err
	}

	now := s.clock.Now()
	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		existing, err := s.getInTrip(ctx, tripID, optionID)
		if err != nil {
			return err
		}
		if err := s.options.SoftDelete(ctx, optionID, now,
			versionOrCurrent(expectedVersion, existing.Version)); err != nil {
			return err
		}

		tombstone := *existing
		tombstone.DeletedAt = &now
		tombstone.UpdatedAt = now
		tombstone.Version = existing.Version + 1
		if err := rec.option(ctx, domain.OpOptionDelete, &tombstone, domain.FieldDeletedAt); err != nil {
			return err
		}

		slot, err := s.slots.GetByID(ctx, existing.SlotID)
		if err != nil {
			return err
		}
		if slot.SelectedOptionID == nil || *slot.SelectedOptionID != optionID {
			return nil
		}
		if err := s.slots.SetSelectedOption(ctx, slot.ID, nil, slot.Version, now); err != nil {
			return err
		}
		cleared, err := s.slots.GetByID(ctx, slot.ID)
		if err != nil {
			return err
		}
		return rec.slot(ctx, domain.OpSlotSelectOption, cleared, domain.FieldSelectedOptionID)
	})
	return err
}

func (s *SlotOptionService) checkSlotInTrip(ctx context.Context, tripID, slotID domain.ID) error {
	slot, err := s.slots.GetByID(ctx, slotID)
	if err != nil {
		return err
	}
	return checkTrip(slot.TripID, tripID)
}

func (s *SlotOptionService) getInTrip(ctx context.Context, tripID, optionID domain.ID) (*domain.SlotOption, error) {
	option, err := s.options.GetByID(ctx, optionID)
	if err != nil {
		return nil, err
	}
	if err := checkTrip(option.TripID, tripID); err != nil {
		return nil, err
	}
	return option, nil
}
