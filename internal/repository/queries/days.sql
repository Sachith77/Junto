-- Queries for days.

-- name: CreateDay :one
INSERT INTO days (id, trip_id, date, label, position)
VALUES (@id, @trip_id, @date, @label, @position)
RETURNING *;

-- name: GetDayByID :one
SELECT * FROM days WHERE id = @id AND deleted_at IS NULL;

-- name: ListDaysForTrip :many
-- Ordered by (position, id). The id tiebreak is not cosmetic: two clients inserting into
-- the same gap without seeing each other legitimately produce the SAME fractional index,
-- and the id is what makes every replica derive an identical total order from that.
SELECT * FROM days
WHERE trip_id = @trip_id AND deleted_at IS NULL
ORDER BY position, id;

-- name: UpdateDay :one
UPDATE days
SET date       = @date,
    label      = @label,
    position   = @position,
    version    = version + 1,
    updated_at = @updated_at
WHERE id = @id AND version = @version AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteDay :execrows
UPDATE days
SET deleted_at = @deleted_at,
    version    = version + 1,
    updated_at = @deleted_at
WHERE id = @id AND version = @version AND deleted_at IS NULL;

-- name: DayExists :one
SELECT EXISTS (SELECT 1 FROM days WHERE id = @id);

-- name: GetDayPosition :one
SELECT position FROM days WHERE id = @id AND deleted_at IS NULL;

-- name: FirstDayPosition :one
SELECT position FROM days
WHERE trip_id = @trip_id AND deleted_at IS NULL
ORDER BY position, id
LIMIT 1;

-- name: DayPositionAfter :one
-- The position of the day immediately following the given (position, id). Comparing the
-- tuple keeps this consistent with the ORDER BY above; comparing position alone would skip
-- or repeat rows whenever two days share a position.
SELECT position FROM days
WHERE trip_id = @trip_id
  AND deleted_at IS NULL
  -- See items.sql: sqlc cannot infer parameter types inside a row comparison, so the casts
  -- are what keep @after_id typed as a UUID in the generated Go.
  AND (position, id) > (@after_position::text, @after_id::uuid)
ORDER BY position, id
LIMIT 1;
