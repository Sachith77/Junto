package service

import (
	"context"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/pkg/fracdex"
)

// SlotService manages slots — the decisions in an itinerary.
//
// Update, Move, SetSelectedOption and SetStatus are deliberately separate methods rather than
// one generic PATCH. Each is a distinct capability check and a distinct sync operation;
// collapsing them would make a content edit indistinguishable from a reorder at the one layer
// (the service) that is supposed to keep that distinction meaningful.
//
// Every mutation here runs through oplog.write, which holds the trip's sequencer for the
// duration of the transaction and appends to the immutable log before committing. That is the
// only reason a resyncing client can trust "everything since seq N" regardless of whether a
// change arrived over REST or over a WebSocket.
type SlotService struct {
	authz
	oplog
	slots domain.SlotRepository
	clock domain.Clock
}

// SlotDeps collects SlotService's dependencies.
type SlotDeps struct {
	Slots   domain.SlotRepository
	Members domain.MembershipRepository
	Trips   domain.TripRepository
	Ops     domain.OpLogRepository
	Tx      domain.TxManager
	Pub     domain.OpPublisher
	Clock   domain.Clock
}

// NewSlotService builds a SlotService.
func NewSlotService(deps SlotDeps) *SlotService {
	if deps.Clock == nil {
		deps.Clock = domain.SystemClock{}
	}
	return &SlotService{
		authz: authz{members: deps.Members},
		oplog: newOplog(deps.Trips, deps.Ops, deps.Tx, deps.Pub),
		slots: deps.Slots,
		clock: deps.Clock,
	}
}

// Get returns a slot, provided the caller is a member and the slot belongs to the named trip.
func (s *SlotService) Get(ctx context.Context, tripID, userID, slotID domain.ID) (*domain.Slot, error) {
	if _, err := s.actor(ctx, tripID, userID); err != nil {
		return nil, err
	}
	slot, err := s.slots.GetByID(ctx, slotID)
	if err != nil {
		return nil, err
	}
	if err := checkTrip(slot.TripID, tripID); err != nil {
		return nil, err
	}
	return slot, nil
}

// ListForTrip returns every live slot in a trip (scheduled and backlog).
//
// Ordering is per-bucket: `position` only orders slots within one day or the backlog, not
// across the whole trip. Callers group by DayID and sort by Position within each group.
func (s *SlotService) ListForTrip(ctx context.Context, tripID, userID domain.ID) ([]*domain.Slot, error) {
	if _, err := s.actor(ctx, tripID, userID); err != nil {
		return nil, err
	}
	return s.slots.ListForTrip(ctx, tripID)
}

// CreateSlotInput is the input to Create.
type CreateSlotInput struct {
	// DayID nil places the slot in the trip backlog.
	DayID       *domain.ID
	Kind        domain.SlotKind
	Title       string
	Notes       string
	StartTime   *domain.TimeOfDay
	EndTime     *domain.TimeOfDay
	AfterSlotID *domain.ID

	// ID lets the caller name the entity before the server has seen it, which is what
	// optimistic UI and offline editing require (D4). Zero means "assign one".
	ID domain.ID
}

// Create adds a slot to a day or the backlog, computing its position from the requested
// neighbour.
//
// The neighbour lookup and the fractional-index computation both happen INSIDE the
// transaction, after the sequencer is held. That ordering is not incidental: computing a
// position against neighbours read outside the lock would race another writer inserting into
// the same gap, and the resulting key would be resolved against state that no longer exists.
func (s *SlotService) Create(ctx context.Context, tripID, userID domain.ID, in CreateSlotInput) (*domain.Slot, error) {
	actor, err := s.require(ctx, tripID, userID, domain.CapCreateSlots)
	if err != nil {
		return nil, err
	}

	id := in.ID
	if id == domain.NilID {
		id = domain.NewID()
	}
	slot := &domain.Slot{
		ID: id, TripID: tripID, DayID: in.DayID,
		Kind: in.Kind, Title: in.Title, Notes: in.Notes,
		StartTime: in.StartTime, EndTime: in.EndTime,
		Status: domain.SlotStatusPlanned, CreatedBy: &actor.UserID,
	}

	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		prev, next, err := s.slots.NeighbourPositions(ctx, tripID, in.DayID, in.AfterSlotID)
		if err != nil {
			return err
		}
		position, err := fracdex.KeyBetween(prev, next)
		if err != nil {
			return err
		}
		slot.Position = position

		if err := slot.Validate(); err != nil {
			return err
		}
		if err := s.slots.Create(ctx, slot); err != nil {
			return err
		}
		// The log records the RESOLVED position, never the "after_slot_id" that produced it
		// (D62). Replaying the derivation later, against a list whose neighbours have all
		// changed, would produce a different key and convergence would die silently.
		return rec.slot(ctx, domain.OpSlotCreate, slot)
	})
	if err != nil {
		return nil, err
	}
	return slot, nil
}

// UpdateSlotInput is the input to Update.
//
// Fields is the D64 field mask. Empty means "replace every editable field", which is what a
// whole-object REST write genuinely does. A non-empty mask names exactly what changes, and
// that is the difference between a collaborative edit and an overwrite: two members editing
// title and notes concurrently both succeed only because each names one field.
type UpdateSlotInput struct {
	Fields    domain.FieldMask
	Kind      domain.SlotKind
	Title     string
	Notes     string
	StartTime *domain.TimeOfDay
	EndTime   *domain.TimeOfDay

	// Version nil means no optimistic-concurrency precondition — merge semantics (D69).
	Version *int
}

// Update edits a slot's content. Placement, resolution and coverage each have their own
// method.
func (s *SlotService) Update(ctx context.Context, tripID, userID, slotID domain.ID, in UpdateSlotInput) (*domain.Slot, error) {
	actor, err := s.require(ctx, tripID, userID, domain.CapEditSlots)
	if err != nil {
		return nil, err
	}

	mask := maskFor(domain.OpSlotEdit, in.Fields)
	if err := mask.Validate(domain.OpSlotEdit); err != nil {
		return nil, err
	}

	var slot *domain.Slot
	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		existing, err := s.getInTrip(ctx, tripID, slotID)
		if err != nil {
			return err
		}

		// Unmasked fields are carried over from the row just read. Safe without a version
		// check because the trip's sequencer is held: no other writer in this trip can
		// interleave between this read and the write below.
		slot = &domain.Slot{
			ID: slotID, TripID: tripID, DayID: existing.DayID,
			Kind: existing.Kind, Title: existing.Title, Notes: existing.Notes,
			StartTime: existing.StartTime, EndTime: existing.EndTime,
			Position: existing.Position, Status: existing.Status,
			SelectedOptionID: existing.SelectedOptionID,
			Version:          versionOrCurrent(in.Version, existing.Version),
		}
		if mask.Has(domain.FieldKind) {
			slot.Kind = in.Kind
		}
		if mask.Has(domain.FieldTitle) {
			slot.Title = in.Title
		}
		if mask.Has(domain.FieldNotes) {
			slot.Notes = in.Notes
		}
		if mask.Has(domain.FieldStartTime) {
			slot.StartTime = in.StartTime
		}
		if mask.Has(domain.FieldEndTime) {
			slot.EndTime = in.EndTime
		}

		if err := slot.Validate(); err != nil {
			return err
		}
		if err := s.slots.Update(ctx, slot); err != nil {
			return err
		}
		return rec.slot(ctx, domain.OpSlotEdit, slot, mask...)
	})
	if err != nil {
		return nil, err
	}
	return slot, nil
}

// Move reorders a slot, optionally onto a different day (nil day means the backlog).
//
// day and position move together, always: a move must never be observable as a delete
// followed by an insert, because a client that saw only half of that would render a vanished
// slot.
func (s *SlotService) Move(ctx context.Context, tripID, userID, slotID domain.ID, dayID *domain.ID, afterSlotID *domain.ID, expectedVersion *int) error {
	actor, err := s.require(ctx, tripID, userID, domain.CapReorderSlots)
	if err != nil {
		return err
	}

	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		existing, err := s.getInTrip(ctx, tripID, slotID)
		if err != nil {
			return err
		}
		prev, next, err := s.slots.NeighbourPositions(ctx, tripID, dayID, afterSlotID)
		if err != nil {
			return err
		}
		position, err := fracdex.KeyBetween(prev, next)
		if err != nil {
			return err
		}
		if err := s.slots.Move(ctx, slotID, dayID, position,
			versionOrCurrent(expectedVersion, existing.Version), s.clock.Now()); err != nil {
			return err
		}
		return s.recordSlotState(ctx, rec, domain.OpSlotMove, slotID,
			domain.FieldDayID, domain.FieldPosition)
	})
	return err
}

// SetSelectedOption records the group's resolution for a slot, or clears it when optionID is
// nil. The database's composite FK already rejects an option belonging to a different slot;
// this method's job is authorization and trip scoping, not that check.
func (s *SlotService) SetSelectedOption(ctx context.Context, tripID, userID, slotID domain.ID, optionID *domain.ID, expectedVersion *int) error {
	actor, err := s.require(ctx, tripID, userID, domain.CapEditSlots)
	if err != nil {
		return err
	}

	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		existing, err := s.getInTrip(ctx, tripID, slotID)
		if err != nil {
			return err
		}
		if err := s.slots.SetSelectedOption(ctx, slotID, optionID,
			versionOrCurrent(expectedVersion, existing.Version), s.clock.Now()); err != nil {
			return err
		}
		return s.recordSlotState(ctx, rec, domain.OpSlotSelectOption, slotID,
			domain.FieldSelectedOptionID)
	})
	return err
}

// SetStatus records Live-mode coverage.
func (s *SlotService) SetStatus(ctx context.Context, tripID, userID, slotID domain.ID, status domain.SlotStatus, expectedVersion *int) error {
	actor, err := s.require(ctx, tripID, userID, domain.CapEditSlots)
	if err != nil {
		return err
	}
	if !status.Valid() {
		ve := &domain.ValidationError{}
		ve.Add("status", "invalid_status", "must be one of: planned, covered, skipped")
		return ve
	}

	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		existing, err := s.getInTrip(ctx, tripID, slotID)
		if err != nil {
			return err
		}
		if err := s.slots.SetStatus(ctx, slotID, status, actor.UserID,
			versionOrCurrent(expectedVersion, existing.Version), s.clock.Now()); err != nil {
			return err
		}
		return s.recordSlotState(ctx, rec, domain.OpSlotSetStatus, slotID,
			domain.FieldStatus, domain.FieldStatusChangedAt, domain.FieldStatusChangedBy)
	})
	return err
}

// Delete soft-deletes a slot.
//
// The tombstone is load-bearing rather than stylistic (D3): concurrent delete-versus-edit is
// one of the required conflict cases, and you cannot converge on a row that is gone. An edit
// arriving after this applies to its own fields and leaves the slot deleted — the editor's
// change is recorded rather than silently dropped, which is what keeps fold(log) equal to the
// database state.
//
// Known gap, carried over rather than solved silently: its slot_options are not touched. A
// hard delete would cascade; a soft delete does not, so a deleted slot's options remain live
// rows, invisible only because nothing lists options for a deleted slot's id anymore.
func (s *SlotService) Delete(ctx context.Context, tripID, userID, slotID domain.ID, expectedVersion *int) error {
	actor, err := s.require(ctx, tripID, userID, domain.CapDeleteSlots)
	if err != nil {
		return err
	}

	now := s.clock.Now()
	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		existing, err := s.getInTrip(ctx, tripID, slotID)
		if err != nil {
			return err
		}
		if err := s.slots.SoftDelete(ctx, slotID, now,
			versionOrCurrent(expectedVersion, existing.Version)); err != nil {
			return err
		}
		// A tombstone is the one case that cannot be logged by re-reading: GetByID filters
		// soft-deleted rows out, by design. The resulting state is fully determined instead
		// — the soft-delete statement sets deleted_at and updated_at to the same instant and
		// bumps the version by one — so this reconstruction is exact rather than a guess.
		tombstone := *existing
		tombstone.DeletedAt = &now
		tombstone.UpdatedAt = now
		tombstone.Version = existing.Version + 1
		return rec.slot(ctx, domain.OpSlotDelete, &tombstone, domain.FieldDeletedAt)
	})
	return err
}

// recordSlotState re-reads the slot and logs the named fields from the PERSISTED row.
//
// Move, SetSelectedOption, SetStatus and SoftDelete do not return the updated row, so the
// alternative would be reconstructing the expected result in Go and logging that. Re-reading
// costs one primary-key lookup inside a transaction already held, and buys the guarantee that
// the log can never disagree with the database — which is the invariant the convergence test
// asserts. Correctness over a saved round trip, per this project's stated priority order.
func (s *SlotService) recordSlotState(ctx context.Context, rec *recorder, kind domain.OpKind, slotID domain.ID, fields ...string) error {
	slot, err := s.slots.GetByID(ctx, slotID)
	if err != nil {
		return err
	}
	return rec.slot(ctx, kind, slot, fields...)
}

// getInTrip loads a slot and verifies it belongs to tripID — the defense-in-depth check
// described on checkTrip.
func (s *SlotService) getInTrip(ctx context.Context, tripID, slotID domain.ID) (*domain.Slot, error) {
	slot, err := s.slots.GetByID(ctx, slotID)
	if err != nil {
		return nil, err
	}
	if err := checkTrip(slot.TripID, tripID); err != nil {
		return nil, err
	}
	return slot, nil
}
