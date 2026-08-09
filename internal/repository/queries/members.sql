-- Queries for trip_members.
--
-- This is the core/domain seam. The MembershipRepository interface these back is
-- module-agnostic and a second module would satisfy it with its own table; the table itself
-- is not shared, so Postgres can still enforce referential integrity.

-- name: AddMember :one
INSERT INTO trip_members (id, trip_id, user_id, role, invited_by)
VALUES (@id, @trip_id, @user_id, @role, @invited_by)
RETURNING *;

-- name: GetMember :one
SELECT * FROM trip_members
WHERE trip_id = @trip_id AND user_id = @user_id AND deleted_at IS NULL;

-- name: ListMembers :many
-- Owner first, then by join order: the list is almost always rendered as-is, and the owner
-- is the entry people look for.
SELECT * FROM trip_members
WHERE trip_id = @trip_id AND deleted_at IS NULL
ORDER BY (role = 'owner') DESC, joined_at ASC;

-- name: UpdateMemberRole :one
UPDATE trip_members
SET role       = @role,
    version    = version + 1,
    updated_at = @updated_at
WHERE trip_id = @trip_id AND user_id = @user_id AND version = @version AND deleted_at IS NULL
RETURNING *;

-- name: RemoveMember :execrows
UPDATE trip_members
SET deleted_at = @deleted_at,
    version    = version + 1,
    updated_at = @deleted_at
WHERE trip_id = @trip_id AND user_id = @user_id AND deleted_at IS NULL;

-- name: CountMembersByRole :one
SELECT count(*) FROM trip_members
WHERE trip_id = @trip_id AND role = @role AND deleted_at IS NULL;

-- name: MemberExists :one
-- Ignores deleted_at so a zero-row update can be attributed to a version conflict rather
-- than a missing row.
SELECT EXISTS (
  SELECT 1 FROM trip_members WHERE trip_id = @trip_id AND user_id = @user_id
);

-- name: ListMemberProfiles :many
-- The member list AS RENDERED: membership plus the one user field every collaborative
-- surface needs to show a person rather than a UUID.
--
-- A read model, deliberately separate from ListMembers rather than replacing it. Membership
-- is an authorization fact and is read on the hot path by every authz check; joining users
-- there would tax that path to serve a screen. Email is NOT selected — display name is
-- enough to render an author, a voter or a split, and broadcasting everyone's address to
-- every viewer is a disclosure the UI does not need (the D53 instinct, one level down).
SELECT m.*, u.display_name
FROM trip_members m
JOIN users u ON u.id = m.user_id
WHERE m.trip_id = @trip_id AND m.deleted_at IS NULL
ORDER BY (m.role = 'owner') DESC, m.joined_at ASC;
