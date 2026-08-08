-- Queries for comments — flat, per-slot discussion.
--
-- Note what is absent: there is no UpdateComment. Comments are append-only (D46-style, decided
-- before migration 000005 was written) — post a new one, delete the old one. The only two
-- statements a comment's life ever needs are the insert and the tombstone below.

-- name: CreateComment :one
INSERT INTO comments (id, slot_id, trip_id, body, author_id)
VALUES (@id, @slot_id, @trip_id, @body, @author_id)
RETURNING *;

-- name: GetCommentByID :one
SELECT * FROM comments WHERE id = @id AND deleted_at IS NULL;

-- name: ListCommentsForSlot :many
SELECT * FROM comments
WHERE slot_id = @slot_id AND deleted_at IS NULL
ORDER BY created_at, id;

-- name: SoftDeleteComment :execrows
-- No version predicate, because comments have no version column (D46-style). Deleting an
-- already-deleted comment affects zero rows, read as "already gone" — there is no
-- concurrent-edit case to distinguish it from, since there is no edit at all.
UPDATE comments
SET deleted_at = @deleted_at,
    updated_at = @deleted_at
WHERE id = @id AND deleted_at IS NULL;
