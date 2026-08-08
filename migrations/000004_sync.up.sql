-- 000004_sync — the Stage 2 operation log and the per-trip sequencer.
--
-- This migration is the whole conflict model. Everything the sync engine claims rests on two
-- objects: a counter column on `trips`, and an append-only table of operations ordered by it.
--
-- THE SEQUENCER (D60, D61)
--
-- Every write transaction in a trip begins with:
--
--     UPDATE trips SET op_seq = op_seq + 1 WHERE id = $1 RETURNING op_seq
--
-- That statement takes a row-level exclusive lock on the trip row for the remainder of the
-- transaction, which means every writer within one trip is serialized BEFORE it reads any
-- entity. Three things follow, and they are the entire design:
--
--   1. Field-level merge is free. An operation writes only the columns it names, so two
--      members editing different fields of one slot both succeed. Two editing the same field
--      are serialized, and the later seq wins.
--   2. Read-modify-write is safe, so the Stage 1 repository layer is reused UNCHANGED — no
--      field-mask SQL, no second write path, and the existing optimistic-concurrency
--      predicates become assertions that cannot fire on the sync path.
--   3. Lock ordering is uniform (trip row, then entity rows, always), so it cannot deadlock
--      against itself.
--
-- The allocation MUST be the first statement. If the sequence were assigned last, two
-- transactions could read the same slot state concurrently and serialize only at the sequence
-- step — a lost update at field granularity, with a log that still looked perfectly ordered.
-- That is the subtlest available way to break this design, which is why it is written here as
-- well as in the service layer.
--
-- The cost, stated rather than hidden: all writers within one trip serialize. That is the
-- intent — it is what produces the clean per-room total order the sync engine needs — and the
-- single-trip write-throughput ceiling is a number to be MEASURED, not asserted.

-- A counter column, deliberately NOT a Postgres SEQUENCE (D61).
--
-- A sequence is non-transactional: a rolled-back transaction burns its number and leaves a
-- gap. A column rolls back with everything else, so the log is GAPLESS and a client may treat
-- seq contiguity as a completeness check rather than merely a hint.
--
-- Starts at 0 so that the first allocated seq is 1 and `seq > 0` is a meaningful CHECK.
ALTER TABLE trips ADD COLUMN op_seq bigint NOT NULL DEFAULT 0;

ALTER TABLE trips ADD CONSTRAINT trips_op_seq_non_negative CHECK (op_seq >= 0);


-- ---------------------------------------------------------------------------------------
-- trip_ops — the immutable operation log. One row per resolved change, in trip order.
-- ---------------------------------------------------------------------------------------
--
-- WHAT A ROW IS (D62, D63)
--
-- A row is a RESOLVED EFFECT, never an intent and never a derivation.
--
-- A client sends `slot.create {after_slot_id: S}`. The server derives a fractional-index
-- position from S's neighbours and stores THE POSITION, not the anchor. Replaying the
-- derivation during a resync — months later, against a list whose neighbours have all changed
-- — would produce a different key, and convergence would die silently. The same rule applies
-- to every server-side derivation without exception.
--
-- For the same reason, one client intent may commit as MORE THAN ONE row, linked by
-- cause_op_id. Deleting a slot's selected option clears the slot's selection (D56); rather
-- than making every client re-derive that cascade, the server writes both effects:
--
--     seq 88  option.delete.v1      entity=O1  cause=<intent>
--     seq 89  slot.select_option.v1 entity=S1  cause=<intent>  fields={selected_option_id}
--
-- So a row is uniformly "one entity, one set of field changes", replay is a pure fold, and
-- clients contain zero domain logic. It also makes the invariant checkable rather than merely
-- asserted:  fold(trip_ops) == database state.
--
-- IMMUTABILITY is enforced by the absence of any UPDATE or DELETE query against this table
-- (verified in tests/schema_verify.sql), not by a comment. ON DELETE CASCADE from trips is
-- the one exception, and it is a hard trip deletion — an administrative operation.
CREATE TABLE trip_ops (
    -- Server-assigned UUIDv7. Not client-assigned: the client's identifier is client_op_id,
    -- and conflating them would let a client choose a primary key in a shared table.
    id           uuid PRIMARY KEY,

    trip_id      uuid   NOT NULL REFERENCES trips (id) ON DELETE CASCADE,

    -- The ordering authority. Per-trip, gapless, monotonic.
    seq          bigint NOT NULL,

    -- Nullable to match the ON DELETE SET NULL used for every other user reference in this
    -- schema: a hard user erasure must not destroy the itinerary's history.
    actor_id     uuid REFERENCES users (id) ON DELETE SET NULL,

    -- Versioned kind (D65), e.g. 'slot.edit.v1'. The log is immutable and will outlive its
    -- payload shapes; a decoder meeting a v1 payload in three years must be able to say so
    -- rather than mis-parse it. One character of insurance against a bug class that is
    -- unfixable after the fact.
    kind         text   NOT NULL,

    -- The slot / option / vote / day row this operation touches.
    entity_id    uuid   NOT NULL,

    -- The field mask, EXPLICIT (D64) rather than inferred from which JSON keys are present.
    -- On every nullable column "untouched" and "explicitly cleared" are different operations
    -- with different meanings; inferring them is the D6 failure mode one level up.
    fields       text[] NOT NULL DEFAULT '{}',

    -- jsonb here deliberately INVERTS D6 (D67). D6 rejected JSONB for entity data because
    -- field-level conflict resolution needs discrete columns. This table is the exact
    -- inverse: append-only, never merged, never queried by field, and polymorphic across a
    -- dozen op kinds. The reasoning that makes columns right for slot_options is the
    -- reasoning that makes JSONB right here.
    payload      jsonb  NOT NULL,

    -- The client's own identifier for the intent. NULL for REST-originated operations, which
    -- have no client op id — and that nullability is exactly why the uniqueness index below
    -- is partial.
    client_op_id uuid,

    -- Links the derived operations of one intent (D63). Deliberately NOT a foreign key to
    -- trip_ops.id: the cause is the client's INTENT, which for a multi-op commit is not
    -- itself any single row in this table.
    cause_op_id  uuid,

    created_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT trip_ops_seq_positive CHECK (seq > 0),
    CONSTRAINT trip_ops_kind_len     CHECK (char_length(kind) BETWEEN 1 AND 64),

    -- The total order, enforced by the database rather than trusted from the application.
    -- Two operations sharing a seq within a trip would make "everything since N" ambiguous.
    CONSTRAINT trip_ops_trip_seq_uq  UNIQUE (trip_id, seq)
);

-- The resync scan, and the only query shape this table serves:
--     SELECT * FROM trip_ops WHERE trip_id = $1 AND seq > $2 ORDER BY seq
--
-- Note this is what makes the "version vector" for a room a SINGLE INTEGER: there is exactly
-- one sequencer per trip, so a client's complete sync state is (trip_id, last_seq) — no
-- per-actor vector, no per-entity clocks. Resume is a range scan, not a re-fetch.
--
-- Also serves as the non-partial index backing the trip_id foreign key, which matters here
-- because trips ARE hard-deleted on the administrative path and this is the largest child
-- table in the schema.
CREATE INDEX trip_ops_trip_seq ON trip_ops (trip_id, seq);

-- Idempotent replay of client operations.
--
-- A client that sent an operation, lost the connection before the acknowledgement, and
-- retried must not double-apply it. Field-mask sets happen to be idempotent; `slot.create`
-- and anything with a cascade are not. A duplicate client_op_id returns the ALREADY-COMMITTED
-- seq as a normal ack, and the client cannot tell the difference.
--
-- Partial because client_op_id is NULL for every REST-originated row, and NULLs are not
-- distinct in a plain unique index only from PG15's NULLS NOT DISTINCT — relying on that
-- default here would be relying on a subtlety instead of stating the intent.
CREATE UNIQUE INDEX trip_ops_client_op_uq ON trip_ops (trip_id, client_op_id)
    WHERE client_op_id IS NOT NULL;

-- RETENTION is unbounded in Stage 2. No pruning, no compaction: a heavily-edited trip
-- accumulates rows forever. This is a stated limitation, not a hidden one, and the protocol
-- is designed so pruning can be added WITHOUT a wire change — trips would gain an
-- `op_seq_floor`, and a resume request below it already answers `resync_required`.
--
-- Consciously left unindexed, in the same style as the audit in 000002:
--   trip_ops.actor_id   Users are soft-deleted; a hard erasure may scan. Indexing it would
--                       tax the hottest INSERT path in the system for a rare, offline,
--                       human-initiated operation.
--   trip_ops.entity_id  No query filters by it. "Every operation touching slot X" is a
--                       plausible future audit view, and the index belongs to that feature
--                       rather than to speculation.
