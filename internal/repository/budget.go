package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/repository/sqlcgen"
)

// BudgetRepository is the Postgres implementation of domain.BudgetRepository.
type BudgetRepository struct{ base }

// NewBudgetRepository builds a BudgetRepository.
func NewBudgetRepository(pool *pgxpool.Pool) *BudgetRepository {
	return &BudgetRepository{base{pool: pool}}
}

var _ domain.BudgetRepository = (*BudgetRepository)(nil)

// errSaveNeedsTx is returned when Save is called outside a transaction.
//
// Not a defensive nicety — it is the enforcement of the atomic unit. Save issues three or more
// statements (write the entry, delete the split set, reinsert it), and outside a transaction
// each one commits on its own. The deferred sum trigger would then fire per statement and pass
// each time: the DELETE commits an entry with zero splits, which is a legal state, and the
// INSERTs commit the new set. Nothing would be rejected, and a crash between them would leave a
// permanently wrong ledger. Refusing here means the only way to get it wrong is deleted rather
// than documented.
//
// A plain error, not a domain sentinel: no client input can provoke it, so it is a programming
// error and belongs in a 500 with a stack, not in a field-level validation message.
var errSaveNeedsTx = errors.New(
	"repository: a budget entry and its splits must be saved inside a transaction")

// Save writes an entry and its COMPLETE split set as one atomic unit (D44).
//
// # Insert versus update
//
// Chosen by e.Version: zero means "not yet persisted". That is not a convention this code hopes
// callers respect — budget_entries_version CHECK (version > 0) means the database can never
// produce a zero, so a zero unambiguously came from a caller constructing a new entry. An
// update carries the caller's expected version and fails with ErrVersionConflict if the stored
// row has moved on, which for the budget is the ONLY behaviour: there is no merge path to fall
// back to (D85).
//
// # Why the splits are deleted and reinserted rather than diffed
//
// A diff would be three statement kinds instead of two, would need to reason about a member
// whose share went to zero versus one who left the split entirely, and would produce exactly
// the same rows. The set is small (trip members), it is rewritten as a unit by definition, and
// the deferred trigger checks the result either way.
func (r *BudgetRepository) Save(ctx context.Context, e *domain.BudgetEntry) error {
	if _, ok := txFromContext(ctx); !ok {
		return errSaveNeedsTx
	}
	q := r.q(ctx)

	var row sqlcgen.BudgetEntry
	var err error
	if e.Version == 0 {
		row, err = q.CreateBudgetEntry(ctx, sqlcgen.CreateBudgetEntryParams{
			ID:           e.ID,
			TripID:       e.TripID,
			SlotOptionID: e.SlotOptionID,
			Label:        e.Label,
			Category:     string(e.Category),
			AmountMinor:  e.AmountMinor,
			PaidBy:       e.PaidBy,
			IncurredOn:   e.IncurredOn,
			CreatedBy:    e.CreatedBy,
		})
		if err != nil {
			return mapError("budget entry", err)
		}
	} else {
		row, err = q.UpdateBudgetEntry(ctx, sqlcgen.UpdateBudgetEntryParams{
			SlotOptionID: e.SlotOptionID,
			Label:        e.Label,
			Category:     string(e.Category),
			AmountMinor:  e.AmountMinor,
			PaidBy:       e.PaidBy,
			IncurredOn:   e.IncurredOn,
			UpdatedAt:    time.Now().UTC(),
			ID:           e.ID,
			Version:      versionArg(e.Version),
		})
		if err != nil {
			if isNoRows(err) {
				exists, existsErr := q.BudgetEntryExists(ctx, e.ID)
				if existsErr != nil {
					return mapError("budget entry", existsErr)
				}
				return resolveWriteMiss("budget entry", exists)
			}
			return mapError("budget entry", err)
		}
	}

	if err := q.DeleteSplitsForEntry(ctx, row.ID); err != nil {
		return mapError("budget split", err)
	}

	saved := make([]domain.BudgetSplit, 0, len(e.Splits))
	for _, s := range e.Splits {
		id := s.ID
		if id == domain.NilID {
			id = domain.NewID()
		}
		splitRow, err := q.InsertBudgetSplit(ctx, sqlcgen.InsertBudgetSplitParams{
			ID:            id,
			BudgetEntryID: row.ID,
			UserID:        s.UserID,
			AmountMinor:   s.AmountMinor,
		})
		if err != nil {
			// A member who is not on this trip fails budget_splits' user FK here. The sum
			// itself is NOT checked yet — that trigger is deferred to COMMIT, which is the
			// whole reason this rewrite is legal.
			return mapError("budget split", err)
		}
		saved = append(saved, toDomainBudgetSplit(splitRow))
	}

	out := toDomainBudgetEntry(row)
	out.Splits = saved
	*e = *out
	return nil
}

func (r *BudgetRepository) GetByID(ctx context.Context, id domain.ID) (*domain.BudgetEntry, error) {
	q := r.q(ctx)
	row, err := q.GetBudgetEntryByID(ctx, id)
	if err != nil {
		return nil, mapError("budget entry", err)
	}
	splits, err := q.ListSplitsForEntry(ctx, id)
	if err != nil {
		return nil, mapError("budget split", err)
	}
	entry := toDomainBudgetEntry(row)
	entry.Splits = toDomainBudgetSplits(splits)
	return entry, nil
}

// ListForTrip returns a trip's ledger with every entry's splits attached.
//
// Two queries, not one per entry: the splits arrive in a single trip-scoped read and are
// bucketed by entry id in memory. The N+1 version of this is the kind of thing that is
// invisible until a trip has a hundred entries and then is the slowest endpoint in the app.
func (r *BudgetRepository) ListForTrip(ctx context.Context, tripID domain.ID) ([]*domain.BudgetEntry, error) {
	q := r.q(ctx)
	rows, err := q.ListBudgetEntriesForTrip(ctx, tripID)
	if err != nil {
		return nil, mapError("budget entry", err)
	}
	splitRows, err := q.ListSplitsForTrip(ctx, tripID)
	if err != nil {
		return nil, mapError("budget split", err)
	}

	byEntry := make(map[domain.ID][]domain.BudgetSplit, len(rows))
	for _, s := range splitRows {
		byEntry[s.BudgetEntryID] = append(byEntry[s.BudgetEntryID], toDomainBudgetSplit(s))
	}

	out := make([]*domain.BudgetEntry, 0, len(rows))
	for _, row := range rows {
		entry := toDomainBudgetEntry(row)
		entry.Splits = byEntry[entry.ID]
		out = append(out, entry)
	}
	return out, nil
}

// SoftDelete tombstones an entry.
//
// Its splits are deliberately left in place. They cascade on a hard delete and are meaningless
// without their entry, so removing them here would buy nothing and would destroy the record of
// who owed what — which is exactly the information someone disputing a deletion needs.
func (r *BudgetRepository) SoftDelete(ctx context.Context, id domain.ID, at time.Time, expectedVersion int) error {
	q := r.q(ctx)
	n, err := q.SoftDeleteBudgetEntry(ctx, sqlcgen.SoftDeleteBudgetEntryParams{
		DeletedAt: &at,
		ID:        id,
		Version:   versionArg(expectedVersion),
	})
	if err != nil {
		return mapError("budget entry", err)
	}
	if n == 0 {
		exists, existsErr := q.BudgetEntryExists(ctx, id)
		if existsErr != nil {
			return mapError("budget entry", existsErr)
		}
		return resolveWriteMiss("budget entry", exists)
	}
	return nil
}
