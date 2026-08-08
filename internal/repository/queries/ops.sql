-- Queries for the operation log and the per-trip sequencer.
--
-- There is deliberately NO update and NO delete against trip_ops. The log is immutable, and
-- immutability here is enforced by the absence of the SQL that would break it (plus an
-- assertion in tests/schema_verify.sql), not by asking people not to write it.

-- name: LockTripForWrite :one
-- The FIRST statement of every write transaction in a trip (D60).
--
-- SELECT ... FOR UPDATE takes the trip's row lock, serializing every writer in that trip
-- BEFORE any of them reads an entity. That ordering is the entire conflict model: it makes
-- read-modify-write safe (so the whole Stage 1 repository layer is reused unchanged), and it
-- makes field-level merge fall out of "an operation writes only the columns it names".
--
-- Taking this lock AFTER the read instead would let two transactions read the same slot state
-- concurrently and serialize only when they allocated their sequence numbers — a lost update
-- at field granularity, with a log that still looked perfectly ordered. That is the subtlest
-- available way to break this design.
--
-- It doubles as the trip's existence-and-not-deleted check, so callers get ErrNotFound for a
-- deleted trip before doing any work.
SELECT id FROM trips WHERE id = @id AND deleted_at IS NULL FOR UPDATE;

-- name: NextOpSeq :one
-- Allocates the next sequence number.
--
-- A counter column rather than a Postgres SEQUENCE (D61): a sequence is non-transactional, so
-- a rolled-back transaction burns its number and gaps the log. This rolls back with everything
-- else, which is what lets a client treat seq contiguity as a completeness check.
--
-- Deliberately touches op_seq ONLY — not version, not updated_at. Bumping the trip's version
-- on every slot edit would invalidate every REST client's cached trip version and produce
-- spurious 409s on unrelated trip updates.
UPDATE trips SET op_seq = op_seq + 1 WHERE id = @id RETURNING op_seq;

-- name: CurrentOpSeq :one
-- Where a room currently is, for a subscriber establishing its starting point. No lock.
SELECT op_seq FROM trips WHERE id = @id AND deleted_at IS NULL;

-- name: AppendOp :one
-- Writes one committed operation. Must run in the same transaction as the mutation it
-- records — that co-location is what makes "everything since seq N" complete for REST and
-- WebSocket writes alike.
INSERT INTO trip_ops (id, trip_id, seq, actor_id, kind, entity_id, fields, payload,
                      client_op_id, cause_op_id)
VALUES (@id, @trip_id, @seq, @actor_id, @kind, @entity_id, @fields, @payload,
        @client_op_id, @cause_op_id)
RETURNING *;

-- name: ListOpsSince :many
-- The resync scan, and the only query shape this table serves.
--
-- Note what makes this a range scan rather than a re-fetch: there is exactly ONE sequencer per
-- trip, so a client's complete sync state is the single integer @after_seq. No per-actor
-- vector, no per-entity clocks — the version vector for a room is one number.
SELECT * FROM trip_ops
WHERE trip_id = @trip_id AND seq > @after_seq
ORDER BY seq
LIMIT @page_limit;

-- name: GetOpByClientOpID :one
-- Idempotent replay. A client that sent an operation, lost the connection before the
-- acknowledgement, and retried must receive the operation it already committed rather than
-- commit a second one — field-mask sets happen to be idempotent, but creates and cascades are
-- not.
SELECT * FROM trip_ops WHERE trip_id = @trip_id AND client_op_id = @client_op_id;
