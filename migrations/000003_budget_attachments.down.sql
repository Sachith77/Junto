DROP TABLE IF EXISTS attachments;

-- Constraint triggers are dropped with their tables; the functions are not.
DROP TABLE IF EXISTS budget_splits;
DROP TABLE IF EXISTS budget_entries;
DROP FUNCTION IF EXISTS check_budget_split_sum();
DROP FUNCTION IF EXISTS check_budget_entry_total();

ALTER TABLE trips DROP CONSTRAINT IF EXISTS trips_base_currency;
ALTER TABLE trips DROP COLUMN IF EXISTS base_currency;
