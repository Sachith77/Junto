-- Queries for budget_entries and budget_splits — the trip ledger.
--
-- The shape of this file is dictated by D44: an entry and its COMPLETE split set are one
-- atomic unit. There is deliberately no UpdateBudgetSplit and no DeleteBudgetSplit. The only
-- way to change a split is to rewrite the whole set alongside its entry, inside one
-- transaction, which is what makes the sum invariant enforceable rather than merely checked.
--
-- The deferred constraint trigger in migration 000003 is the backstop: splits are rewritten by
-- deleting and reinserting, so the invariant is legitimately violated mid-transaction and is
-- only checked at COMMIT. That is why DeleteSplitsForEntry followed by InsertBudgetSplit is
-- safe here and would not be against an immediate constraint.

-- name: CreateBudgetEntry :one
INSERT INTO budget_entries (
  id, trip_id, slot_option_id, label, category, amount_minor, paid_by, incurred_on, created_by
) VALUES (
  @id, @trip_id, @slot_option_id, @label, @category, @amount_minor, @paid_by, @incurred_on, @created_by
)
RETURNING *;

-- name: UpdateBudgetEntry :one
-- Optimistic, and the version is NEVER optional for a budget write (D85). Where a slot merges
-- field by field and can therefore substitute the version it just read, an entry is replaced
-- whole — so a caller without a version is asking to overwrite numbers it has not seen.
UPDATE budget_entries
SET slot_option_id = @slot_option_id,
    label          = @label,
    category       = @category,
    amount_minor   = @amount_minor,
    paid_by        = @paid_by,
    incurred_on    = @incurred_on,
    version        = version + 1,
    updated_at     = @updated_at
WHERE id = @id AND version = @version AND deleted_at IS NULL
RETURNING *;

-- name: GetBudgetEntryByID :one
SELECT * FROM budget_entries WHERE id = @id AND deleted_at IS NULL;

-- name: ListBudgetEntriesForTrip :many
-- Ordered by the date the cost was incurred, then by creation, so a ledger reads
-- chronologically. NULLS LAST keeps entries with no date from leading the list. The id
-- tiebreak keeps the order total.
SELECT * FROM budget_entries
WHERE trip_id = @trip_id AND deleted_at IS NULL
ORDER BY incurred_on ASC NULLS LAST, created_at, id;

-- name: SoftDeleteBudgetEntry :execrows
UPDATE budget_entries
SET deleted_at = @deleted_at,
    version    = version + 1,
    updated_at = @deleted_at
WHERE id = @id AND version = @version AND deleted_at IS NULL;

-- name: BudgetEntryExists :one
SELECT EXISTS (SELECT 1 FROM budget_entries WHERE id = @id);

-- name: DeleteSplitsForEntry :exec
-- Half of a rewrite, never a standalone operation. Called only by BudgetRepository.Save, which
-- refuses to run outside a transaction precisely so this cannot commit on its own and leave an
-- entry with no splits.
DELETE FROM budget_splits WHERE budget_entry_id = @budget_entry_id;

-- name: InsertBudgetSplit :one
INSERT INTO budget_splits (id, budget_entry_id, user_id, amount_minor)
VALUES (@id, @budget_entry_id, @user_id, @amount_minor)
RETURNING *;

-- name: ListSplitsForEntry :many
-- Ordered by user id, matching the canonical order the operation log uses (domain.splitValues).
-- Two representations of one split set that sort differently would make every
-- fold(log) == database comparison order-sensitive.
SELECT * FROM budget_splits WHERE budget_entry_id = @budget_entry_id ORDER BY user_id;

-- name: ListSplitsForTrip :many
-- Loads every split for a trip in one query, so listing a ledger costs two round trips rather
-- than one per entry. Joined rather than passed an id array because the entry filter and the
-- soft-delete filter must agree with ListBudgetEntriesForTrip exactly.
SELECT s.* FROM budget_splits s
JOIN budget_entries e ON e.id = s.budget_entry_id
WHERE e.trip_id = @trip_id AND e.deleted_at IS NULL
ORDER BY s.budget_entry_id, s.user_id;
