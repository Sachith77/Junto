-- Queries for trips.

-- name: CreateTrip :one
INSERT INTO trips (id, name, description, start_date, end_date, time_zone)
VALUES (@id, @name, @description, @start_date, @end_date, @time_zone)
RETURNING *;

-- name: GetTripByID :one
SELECT * FROM trips WHERE id = @id AND deleted_at IS NULL;

-- name: UpdateTrip :one
UPDATE trips
SET name        = @name,
    description = @description,
    start_date  = @start_date,
    end_date    = @end_date,
    time_zone   = @time_zone,
    version     = version + 1,
    updated_at  = @updated_at
WHERE id = @id AND version = @version AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteTrip :execrows
UPDATE trips
SET deleted_at = @deleted_at,
    version    = version + 1,
    updated_at = @deleted_at
WHERE id = @id AND version = @version AND deleted_at IS NULL;

-- name: TripExists :one
SELECT EXISTS (SELECT 1 FROM trips WHERE id = @id);

-- name: ListTripsForUser :many
-- Keyset pagination, never OFFSET. Under concurrent inserts — the entire premise of this
-- application — OFFSET duplicates and skips rows, because an insert before the current
-- offset shifts every subsequent page by one.
--
-- The cursor is the row's own (created_at, id). Comparing the tuple rather than the two
-- columns separately is what makes the boundary exact: `(a, b) < (x, y)` is a single
-- lexicographic comparison, whereas `a < x OR (a = x AND b < y)` is the same thing written
-- in a way that is easy to get subtly wrong. id breaks ties so the order is total even when
-- two trips share a created_at.
--
-- The caller passes limit+1 and trims the probe row; that is how HasMore is determined
-- without a second COUNT, which would be both slower and wrong (the total can change
-- between the two queries).
SELECT t.* FROM trips t
JOIN trip_members m ON m.trip_id = t.id AND m.deleted_at IS NULL
WHERE m.user_id = @user_id
  AND t.deleted_at IS NULL
  AND (
    sqlc.narg('after_created_at')::timestamptz IS NULL
    OR (t.created_at, t.id) < (sqlc.narg('after_created_at')::timestamptz, sqlc.narg('after_id')::uuid)
  )
ORDER BY t.created_at DESC, t.id DESC
LIMIT @page_limit;
