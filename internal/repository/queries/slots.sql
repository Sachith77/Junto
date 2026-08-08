-- Queries for slots — the decisions in an itinerary.

-- name: CreateSlot :one
INSERT INTO slots (
  id, trip_id, day_id, kind, title, notes,
  start_time, end_time, position, status, created_by
) VALUES (
  @id, @trip_id, @day_id, @kind, @title, @notes,
  @start_time, @end_time, @position, @status, @created_by
)
RETURNING *;

-- name: GetSlotByID :one
SELECT * FROM slots WHERE id = @id AND deleted_at IS NULL;

-- name: ListSlotsForTrip :many
SELECT * FROM slots
WHERE trip_id = @trip_id AND deleted_at IS NULL
ORDER BY position, id;

-- name: ListSlotsForDay :many
SELECT * FROM slots
WHERE day_id = @day_id AND deleted_at IS NULL
ORDER BY position, id;

-- name: ListBacklogSlots :many
-- day_id IS NULL is the unscheduled backlog: decisions not yet placed on a day.
SELECT * FROM slots
WHERE trip_id = @trip_id AND day_id IS NULL AND deleted_at IS NULL
ORDER BY position, id;

-- name: UpdateSlot :one
-- Content only. Moving between days or positions goes through MoveSlot; resolving goes
-- through SetSlotSelectedOption; coverage goes through SetSlotStatus. Each is a distinct
-- operation with distinct authorization, and in Stage 2 a distinct sync operation — merging
-- them into one UPDATE would make a content edit indistinguishable from a reorder.
UPDATE slots
SET kind       = @kind,
    title      = @title,
    notes      = @notes,
    start_time = @start_time,
    end_time   = @end_time,
    version    = version + 1,
    updated_at = @updated_at
WHERE id = @id AND version = @version AND deleted_at IS NULL
RETURNING *;

-- name: MoveSlot :one
-- Day and position change together, in one statement, with one version bump.
--
-- A move must never be observable as a delete followed by an insert. A client that saw only
-- the first half would render the slot as vanished, and in Stage 2 a two-operation move
-- would give the sync engine an intermediate state that never actually existed to resolve
-- against.
--
-- trip_id is deliberately not updatable: slots do not migrate between trips, and the
-- composite FK would reject it anyway.
UPDATE slots
SET day_id     = @day_id,
    position   = @position,
    version    = version + 1,
    updated_at = @updated_at
WHERE id = @id AND version = @version AND deleted_at IS NULL
RETURNING *;

-- name: SetSlotSelectedOption :one
-- The group's resolution. The composite FK (selected_option_id, id) -> slot_options(id,
-- slot_id) means an option belonging to a different slot is rejected by the database, so
-- this query does not need to check it.
UPDATE slots
SET selected_option_id = @selected_option_id,
    version            = version + 1,
    updated_at         = @updated_at
WHERE id = @id AND version = @version AND deleted_at IS NULL
RETURNING *;

-- name: SetSlotStatus :one
-- Live-mode coverage, recording who and when. A bare enum would throw away the attribution
-- that a group arguing about "did we actually do this" wants.
UPDATE slots
SET status            = @status,
    status_changed_at = @status_changed_at,
    status_changed_by = @status_changed_by,
    version           = version + 1,
    updated_at        = @status_changed_at
WHERE id = @id AND version = @version AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteSlot :execrows
-- A tombstone, not a DELETE. Concurrent delete-versus-edit is one of the three required
-- Stage 2 conflict cases, and you cannot converge on a row that no longer exists.
UPDATE slots
SET deleted_at = @deleted_at,
    version    = version + 1,
    updated_at = @deleted_at
WHERE id = @id AND version = @version AND deleted_at IS NULL;

-- name: SlotExists :one
-- Used only to distinguish "not found" from "version conflict" after a zero-row update.
-- Deliberately ignores deleted_at: a soft-deleted row still explains why the update missed.
SELECT EXISTS (SELECT 1 FROM slots WHERE id = @id);

-- name: GetSlotPosition :one
SELECT position FROM slots WHERE id = @id AND deleted_at IS NULL;

-- name: FirstSlotPosition :one
-- `IS NOT DISTINCT FROM` so one query covers both buckets: a specific day, and the backlog
-- where day_id IS NULL (plain `=` never matches NULL). It does mean the day_id filter is
-- applied after the trip_id index lookup rather than using slots_day_pos directly — a
-- deliberate trade, because a trip holds a few hundred slots at most and the alternative is
-- two near-identical queries to maintain.
SELECT position FROM slots
WHERE trip_id = @trip_id
  AND day_id IS NOT DISTINCT FROM sqlc.narg('day_id')::uuid
  AND deleted_at IS NULL
ORDER BY position, id
LIMIT 1;

-- name: SlotPositionAfter :one
SELECT position FROM slots
WHERE trip_id = @trip_id
  AND day_id IS NOT DISTINCT FROM sqlc.narg('day_id')::uuid
  AND deleted_at IS NULL
  -- Casts are required, not decorative: sqlc cannot infer a parameter's type inside a row
  -- comparison and would otherwise type @after_id as text, producing a Go signature that
  -- takes a string where a UUID belongs.
  AND (position, id) > (@after_position::text, @after_id::uuid)
ORDER BY position, id
LIMIT 1;
