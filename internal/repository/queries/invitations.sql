-- Queries for trip_invitations.

-- name: CreateInvitation :one
INSERT INTO trip_invitations (id, trip_id, email, role, token_hash, created_by, max_uses, expires_at)
VALUES (@id, @trip_id, @email, @role, @token_hash, @created_by, @max_uses, @expires_at)
RETURNING *;

-- name: GetInvitationByHash :one
SELECT * FROM trip_invitations WHERE token_hash = @token_hash;

-- name: ListInvitationsForTrip :many
SELECT * FROM trip_invitations
WHERE trip_id = @trip_id AND revoked_at IS NULL
ORDER BY created_at DESC;

-- name: RedeemInvitation :one
-- Atomic redemption. Every condition that makes an invitation usable is checked in the SAME
-- statement that consumes a use, so a single-use link cannot be redeemed twice by two
-- concurrent requests.
--
-- The read-then-write alternative is a textbook race: both requests read use_count = 0,
-- both conclude the link is valid, both increment, and two people join on a one-use invite.
-- Returning zero rows here IS the rejection — the caller does not need a prior check, and
-- must not add one.
UPDATE trip_invitations
SET use_count = use_count + 1
WHERE id = @id
  AND revoked_at IS NULL
  AND expires_at > @now
  AND (max_uses IS NULL OR use_count < max_uses)
RETURNING *;

-- name: RevokeInvitation :execrows
UPDATE trip_invitations SET revoked_at = @revoked_at
WHERE id = @id AND revoked_at IS NULL;
