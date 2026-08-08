-- Queries for option_votes.
--
-- One row per (slot_id, user_id) with one mutable field: a last-writer-wins register. There
-- is no delete — retraction sets option_id to NULL — which is what keeps the register shape
-- and, in Stage 2, makes a vote the simplest convergent entity in the system.

-- name: CastVote :one
-- Upsert on (slot_id, user_id). This is why "change my vote" is a single round trip and a
-- single row: without ON CONFLICT the caller would have to read first and then decide
-- between INSERT and UPDATE, which is a race between two of the member's own devices.
--
-- The version bump on conflict is what an optimistic reader observes; the vote itself is
-- last-writer-wins by design, so this query deliberately does NOT take an expected version.
-- Requiring one would make a member's second click fail rather than simply win, which is not
-- what anyone means by changing their mind.
INSERT INTO option_votes (id, slot_id, trip_id, user_id, option_id)
VALUES (@id, @slot_id, @trip_id, @user_id, @option_id)
ON CONFLICT (slot_id, user_id) DO UPDATE
SET option_id  = EXCLUDED.option_id,
    version    = option_votes.version + 1,
    updated_at = now()
RETURNING *;

-- name: GetVote :one
SELECT * FROM option_votes WHERE slot_id = @slot_id AND user_id = @user_id;

-- name: ListVotesForSlot :many
SELECT * FROM option_votes WHERE slot_id = @slot_id ORDER BY created_at, id;

-- name: ListVotesForTrip :many
-- Stage 2 resync: every vote in a sync room.
SELECT * FROM option_votes WHERE trip_id = @trip_id ORDER BY slot_id, created_at, id;

-- name: TallyVotesForSlot :many
-- Retractions (option_id IS NULL) are excluded: a withdrawn vote counts for nothing, but the
-- row stays so the register keeps exactly one entry per member.
--
-- Votes for SOFT-DELETED options are also excluded (D71), which is why this joins rather than
-- grouping the vote table alone. Two things are true at once and both are wanted: a withdrawn
-- candidate must not appear in a live tally, and the vote ROWS are retained so that undeleting
-- the option restores the group's expressed preferences rather than silently discarding them.
--
-- Filtered here in SQL rather than in Go so there is ONE definition of "a visible vote".
-- The same rule applied in two places is the same rule until someone changes one of them.
SELECT v.option_id, count(*) AS vote_count
FROM option_votes v
JOIN slot_options o ON o.id = v.option_id AND o.deleted_at IS NULL
WHERE v.slot_id = @slot_id AND v.option_id IS NOT NULL
GROUP BY v.option_id
ORDER BY vote_count DESC, v.option_id;

-- name: DeleteVotesForOption :execrows
-- Used when an option is hard-deleted. Soft deletion does NOT call this: votes for a
-- tombstoned option are retained so that undeleting restores the group's expressed
-- preferences rather than silently discarding them.
DELETE FROM option_votes WHERE option_id = @option_id;
