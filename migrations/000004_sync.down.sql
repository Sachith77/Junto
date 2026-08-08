-- Reverses 000004_sync.
--
-- Dropping trip_ops destroys the operation log, and with it every client's ability to resume
-- from a sequence number. That is correct for a down-migration — reversing the sync engine
-- means reversing its history — but it is worth stating: this is not a reversible operation
-- in the sense that the data comes back. Down-migrations exist here for local development
-- and CI, not as a production rollback plan.

DROP INDEX IF EXISTS trip_ops_client_op_uq;
DROP INDEX IF EXISTS trip_ops_trip_seq;
DROP TABLE IF EXISTS trip_ops;

ALTER TABLE trips DROP CONSTRAINT IF EXISTS trips_op_seq_non_negative;
ALTER TABLE trips DROP COLUMN IF EXISTS op_seq;
