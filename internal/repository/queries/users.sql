-- Queries for the users table.
--
-- Conventions used throughout this directory:
--   * IDs are always supplied by the application (UUIDv7), never defaulted by the database.
--   * updated_at is set explicitly, never by a trigger, so Stage 2 can substitute an
--     operation's server timestamp.
--   * Every mutating query that participates in optimistic concurrency ends with
--     `AND version = @version` and RETURNS the row, so a zero-row result is detectable.
--   * Soft-deleted rows are excluded by every read unless the name says otherwise.

-- name: CreateUser :one
INSERT INTO users (id, email, password_hash, display_name, email_verified_at)
VALUES (@id, @email, @password_hash, @display_name, @email_verified_at)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users
WHERE id = @id AND deleted_at IS NULL;

-- name: GetUserByEmail :one
-- lower(email) matches the users_email_lower_uq functional index exactly. The caller passes
-- an already-normalised address; normalising on both sides would still be correct but would
-- hide which side owns the rule.
SELECT * FROM users
WHERE lower(email) = lower(@email) AND deleted_at IS NULL;

-- name: UpdateUser :one
UPDATE users
SET email             = @email,
    display_name      = @display_name,
    password_hash     = @password_hash,
    email_verified_at = @email_verified_at,
    version           = version + 1,
    updated_at        = @updated_at
WHERE id = @id AND version = @version AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteUser :execrows
UPDATE users
SET deleted_at = @deleted_at,
    version    = version + 1,
    updated_at = @deleted_at
WHERE id = @id AND deleted_at IS NULL;

-- name: UserExists :one
-- Used only to distinguish "not found" from "version conflict" after a zero-row update.
-- Deliberately ignores deleted_at: a soft-deleted row still explains why the update missed.
SELECT EXISTS (SELECT 1 FROM users WHERE id = @id);
