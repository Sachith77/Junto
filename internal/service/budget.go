package service

import (
	"context"
	"time"

	"github.com/junto/junto/internal/domain"
)

// BudgetService manages the trip ledger.
//
// # The one service in the system with a COARSER conflict grain than field-level
//
// Everything else here merges: two members editing different fields of a slot both win. A
// budget entry does not, because it carries a cross-field invariant — its splits must sum to
// its total — and text has no such thing (D44). Each half of a field-level merge would be
// locally plausible and the pair jointly wrong, and repairing that means choosing whose number
// to discard, which is the decision merging exists to avoid.
//
// That coarseness is expressed in three places, deliberately, at three different distances from
// the data:
//
//   - internal/domain: OpBudgetSet's mask must be total, so a partial edit cannot be encoded.
//   - here: an edit must state its expected version, so a conflict is reported rather than
//     resolved by silently overwriting (D85).
//   - migration 000003: a deferred constraint trigger, so no writer at all — including one
//     that bypasses this service entirely — can commit a ledger that does not add up.
//
// Any one of them alone would be a convention. Together they make the invariant unreachable
// rather than merely unbroken.
type BudgetService struct {
	authz
	oplog
	budget  domain.BudgetRepository
	members domain.MembershipRepository
	clock   domain.Clock
}

// BudgetDeps collects BudgetService's dependencies.
type BudgetDeps struct {
	Budget  domain.BudgetRepository
	Members domain.MembershipRepository
	Trips   domain.TripRepository
	Ops     domain.OpLogRepository
	Tx      domain.TxManager
	Pub     domain.OpPublisher
	Clock   domain.Clock
}

// NewBudgetService builds a BudgetService.
func NewBudgetService(deps BudgetDeps) *BudgetService {
	if deps.Clock == nil {
		deps.Clock = domain.SystemClock{}
	}
	return &BudgetService{
		authz:   authz{members: deps.Members},
		oplog:   newOplog(deps.Trips, deps.Ops, deps.Tx, deps.Pub),
		budget:  deps.Budget,
		members: deps.Members,
		clock:   deps.Clock,
	}
}

// List returns a trip's ledger.
func (s *BudgetService) List(ctx context.Context, tripID, userID domain.ID) ([]*domain.BudgetEntry, error) {
	if _, err := s.actor(ctx, tripID, userID); err != nil {
		return nil, err
	}
	return s.budget.ListForTrip(ctx, tripID)
}

// Get returns one entry with its splits.
func (s *BudgetService) Get(ctx context.Context, tripID, userID, entryID domain.ID) (*domain.BudgetEntry, error) {
	if _, err := s.actor(ctx, tripID, userID); err != nil {
		return nil, err
	}
	return s.getInTrip(ctx, tripID, entryID)
}

// BudgetEntryInput is the complete content of a ledger entry.
//
// There is no field mask, and that absence is the design (D44/D83). Every write carries the
// whole entry INCLUDING the complete split set: "change just the total" is not an operation
// this service offers, because it is not an operation that can be performed safely.
type BudgetEntryInput struct {
	Label        string
	Category     domain.BudgetCategory
	AmountMinor  int64
	SlotOptionID *domain.ID
	PaidBy       *domain.ID
	IncurredOn   *time.Time

	// Splits is the complete set. Empty means "not split yet", which is a legitimate state
	// distinct from "split wrongly" — the database permits zero splits and rejects a non-empty
	// set that does not sum.
	Splits []domain.BudgetSplit

	// ID lets a client name the entry before the server has seen it (D4).
	ID domain.ID

	// Version is the optimistic-concurrency precondition. REQUIRED on update (D85); ignored on
	// create, which has nothing to conflict with.
	Version *int
}

// Create adds a ledger entry and its splits in one transaction.
func (s *BudgetService) Create(ctx context.Context, tripID, userID domain.ID, in BudgetEntryInput) (*domain.BudgetEntry, error) {
	actor, err := s.require(ctx, tripID, userID, domain.CapManageBudget)
	if err != nil {
		return nil, err
	}

	id := in.ID
	if id == domain.NilID {
		id = domain.NewID()
	}
	entry := &domain.BudgetEntry{
		ID: id, TripID: tripID,
		Label: in.Label, Category: in.Category, AmountMinor: in.AmountMinor,
		SlotOptionID: in.SlotOptionID, PaidBy: in.PaidBy, IncurredOn: in.IncurredOn,
		Splits:    in.Splits,
		CreatedBy: &actor.UserID,
		// Zero means "not yet persisted" to the repository, which inserts rather than updates.
		// The database's version > 0 CHECK is what makes that sentinel unambiguous.
		Version: 0,
	}

	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		if err := s.validate(ctx, tripID, entry); err != nil {
			return err
		}
		if err := s.budget.Save(ctx, entry); err != nil {
			return err
		}
		return rec.budget(ctx, domain.OpBudgetSet, entry)
	})
	if err != nil {
		return nil, err
	}
	return entry, nil
}

// Update replaces an entry and its complete split set.
//
// It is deliberately not called "patch": there is no partial form. A caller that wants to
// change one number sends the whole entry back with that number changed, having read it first —
// which is exactly what the required version proves it did.
func (s *BudgetService) Update(ctx context.Context, tripID, userID, entryID domain.ID, in BudgetEntryInput) (*domain.BudgetEntry, error) {
	actor, err := s.require(ctx, tripID, userID, domain.CapManageBudget)
	if err != nil {
		return nil, err
	}
	version, err := requireVersion(in.Version, "version")
	if err != nil {
		return nil, err
	}

	var entry *domain.BudgetEntry
	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		existing, err := s.getInTrip(ctx, tripID, entryID)
		if err != nil {
			return err
		}

		entry = &domain.BudgetEntry{
			ID: entryID, TripID: tripID,
			Label: in.Label, Category: in.Category, AmountMinor: in.AmountMinor,
			SlotOptionID: in.SlotOptionID, PaidBy: in.PaidBy, IncurredOn: in.IncurredOn,
			Splits: in.Splits,
			// Preserved rather than reassigned: an edit does not change who first recorded the
			// cost, and the log would otherwise show authorship silently changing hands.
			CreatedBy: existing.CreatedBy,
			CreatedAt: existing.CreatedAt,
			Version:   version,
		}
		if err := s.validate(ctx, tripID, entry); err != nil {
			return err
		}
		if err := s.budget.Save(ctx, entry); err != nil {
			return err
		}
		return rec.budget(ctx, domain.OpBudgetSet, entry)
	})
	if err != nil {
		return nil, err
	}
	return entry, nil
}

// Delete tombstones an entry.
//
// The version is required here for the same reason it is on Update: deleting a ledger line is
// not less consequential than editing one, and "I deleted the entry I was looking at" is only
// true if the entry has not changed since it was read.
func (s *BudgetService) Delete(ctx context.Context, tripID, userID, entryID domain.ID, expectedVersion *int) error {
	actor, err := s.require(ctx, tripID, userID, domain.CapManageBudget)
	if err != nil {
		return err
	}
	version, err := requireVersion(expectedVersion, "version")
	if err != nil {
		return err
	}

	now := s.clock.Now()
	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		existing, err := s.getInTrip(ctx, tripID, entryID)
		if err != nil {
			return err
		}
		if err := s.budget.SoftDelete(ctx, entryID, now, version); err != nil {
			return err
		}

		tombstone := *existing
		tombstone.DeletedAt = &now
		tombstone.UpdatedAt = now
		tombstone.Version = existing.Version + 1
		return rec.budget(ctx, domain.OpBudgetDelete, &tombstone, domain.FieldDeletedAt)
	})
	return err
}

// validate checks the entry's own invariants and that every split names a trip member.
//
// # Why membership is checked here and not by a constraint
//
// budget_splits.user_id references users, not trip_members, so the database will happily record
// that a stranger owes money on a trip they are not part of. A composite FK to trip_members
// would fix it, but trip membership is soft-deleted: a member who leaves would either break
// every historical split referencing them, or the FK would have to point at rows that are
// tombstoned, which enforces nothing. Checking at write time — while allowing existing splits
// for a departed member to stand — is the honest version of that trade.
func (s *BudgetService) validate(ctx context.Context, tripID domain.ID, e *domain.BudgetEntry) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if len(e.Splits) == 0 {
		return nil
	}

	members, err := s.members.List(ctx, tripID)
	if err != nil {
		return err
	}
	isMember := make(map[domain.ID]struct{}, len(members))
	for _, m := range members {
		isMember[m.UserID] = struct{}{}
	}

	ve := &domain.ValidationError{}
	for _, split := range e.Splits {
		if _, ok := isMember[split.UserID]; !ok {
			ve.Add("splits", "not_a_member",
				"a split may only name a member of this trip")
			break
		}
	}
	return ve.OrNil()
}

// getInTrip loads an entry and re-checks it belongs to the trip in the URL (D54).
//
// The capability was checked against the trip the caller named; the entry id in the path could
// still belong to a different trip entirely. Without this, a member of trip A who learns a
// trip-B entry id could act on it authorized against the wrong trip.
func (s *BudgetService) getInTrip(ctx context.Context, tripID, entryID domain.ID) (*domain.BudgetEntry, error) {
	entry, err := s.budget.GetByID(ctx, entryID)
	if err != nil {
		return nil, err
	}
	if err := checkTrip(entry.TripID, tripID); err != nil {
		return nil, err
	}
	return entry, nil
}
