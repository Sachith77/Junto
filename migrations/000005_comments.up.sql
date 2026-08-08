-- 000005_comments — Stage 3 Slice 2: threaded discussion, deliberately flat.
--
-- A comment attaches to exactly one slot. Flat, not threaded (no parent_comment_id): a
-- reply is just another comment in the same list, ordered by (created_at, id).
--
-- Merge model, decided before this migration was written: append-only, no merge, no edit —
-- the same treatment as attachments (D46/D84), not the field-level-LWW treatment slots and
-- options get. A comment is an event (someone said something at a point in time), not a
-- mutable shared value, so two concurrent comments never conflict; they are just two rows.
-- There is no comment.edit — post a new one, delete the old one, matching D46's "attachments
-- are immutable once ready: you replace one, you do not edit it" verbatim.
--
-- Deliberately NO version column, for the same reason attachments have none (D46): there is
-- no edit verb, so there is no concurrency story to protect. The op-log payload is built with
-- version=0 explicitly (see domain.CommentPayload), not omitted, so "nothing to version" stays
-- visible in the log rather than being an accident of a missing column.
--
-- Delete is author-only, not capability-gated like every other delete in this schema (slots,
-- options and attachments are capability-gated only, because they are shared planning
-- artifacts). A comment is a personal utterance with no precedent to copy here — the more
-- conservative reading is chosen: the author can delete their own comment, nobody else can,
-- not even the trip owner. Enforced in internal/service/comment.go, not in SQL (there is no
-- constraint that can express "the deleter must equal author_id").

CREATE TABLE comments (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slot_id    uuid NOT NULL,
    -- Denormalised from slots.trip_id, guaranteed consistent by the composite FK below — same
    -- pattern as slot_options.trip_id. Every authz check and sync-room-scoping query is by
    -- trip; this removes a join from the hottest path in the system.
    trip_id    uuid NOT NULL,

    body       text NOT NULL,

    -- Nullable, ON DELETE SET NULL: history outlives accounts (same as slot_options.proposed_by,
    -- users.email_verified_at pattern — D18). A comment from a deleted user still renders, just
    -- with no attributable author.
    author_id  uuid REFERENCES users (id) ON DELETE SET NULL,

    created_at timestamptz NOT NULL DEFAULT now(),
    -- No edit verb ever changes this after insert; it is bumped once, to the delete timestamp,
    -- when the tombstone is written. Kept (rather than omitted) because domain.buildPayload
    -- requires an UpdatedAt on every entity, and because it is what comments_trip_updated
    -- sorts by, consistent with slots_trip_updated / slot_options_trip_updated / option_votes_trip.
    updated_at timestamptz NOT NULL DEFAULT now(),
    -- Tombstone (D3): concurrent delete-versus-resync cannot converge on a hard-deleted row,
    -- and the op log cannot reference a dead FK.
    deleted_at timestamptz,

    -- Composite FK: a comment can only point at a slot belonging to its own stated trip,
    -- exactly the slot_options_slot_fk pattern. Postgres makes "comment on another trip's
    -- slot" unrepresentable rather than merely checked in Go.
    CONSTRAINT comments_slot_fk FOREIGN KEY (slot_id, trip_id) REFERENCES slots (id, trip_id)
        ON DELETE CASCADE,

    CONSTRAINT comments_body_len CHECK (char_length(body) BETWEEN 1 AND 4000)
);

-- Non-partial: also backs the leading column of the slot_id composite FK — same reasoning as
-- slot_options_slot. Flat and chronological, not fractional-indexed: comments are not a
-- user-reorderable list.
CREATE INDEX comments_slot         ON comments (slot_id, created_at, id);
-- Stage 2/3 resync: every comment in a trip since T. Not backing any FK (comments has no
-- direct trip_id -> trips FK, same as slot_options — trip_id's correctness is guaranteed
-- transitively by comments_slot_fk).
CREATE INDEX comments_trip_updated ON comments (trip_id, updated_at DESC);

-- comments.author_id is consciously left unindexed, added to the accepted list in
-- tests/schema_verify.sql alongside slot_options.proposed_by / option_votes.user_id: users are
-- soft-deleted, so a hard erasure is a rare, offline, human-initiated operation and is not
-- worth taxing the hottest write path in the system.
