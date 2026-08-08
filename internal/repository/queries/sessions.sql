-- Queries for auth_sessions and refresh_tokens.
--
-- These two tables are one aggregate. A refresh token is meaningless outside its family,
-- and reuse detection has to revoke both together in a single transaction — which is why
-- they share one repository rather than being split by table.

-- name: CreateSession :one
INSERT INTO auth_sessions (id, user_id, user_agent, ip, expires_at)
VALUES (@id, @user_id, @user_agent, @ip, @expires_at)
RETURNING *;

-- name: GetSession :one
SELECT * FROM auth_sessions WHERE id = @id;

-- name: ListActiveSessions :many
-- Ordered by most recently used: the session-management UI is answering "what is logged in
-- right now", and the answer people scan for first is their current device.
SELECT * FROM auth_sessions
WHERE user_id = @user_id AND revoked_at IS NULL AND expires_at > @now
ORDER BY last_used_at DESC;

-- name: TouchSession :execrows
UPDATE auth_sessions SET last_used_at = @last_used_at
WHERE id = @id AND revoked_at IS NULL;

-- name: RevokeSession :execrows
-- Idempotent by design: `revoked_at IS NULL` means a double revoke affects zero rows and
-- preserves the ORIGINAL reason. That matters because the first reason is the interesting
-- one — a session revoked for token reuse must not later be relabelled as a logout.
UPDATE auth_sessions
SET revoked_at = @revoked_at, revoked_reason = @revoked_reason
WHERE id = @id AND revoked_at IS NULL;

-- name: RevokeAllSessions :execrows
-- What a password reset calls. A reset that leaves an attacker's session alive is worse
-- than useless.
UPDATE auth_sessions
SET revoked_at = @revoked_at, revoked_reason = @revoked_reason
WHERE user_id = @user_id AND revoked_at IS NULL;

-- name: CreateRefreshToken :one
INSERT INTO refresh_tokens (id, session_id, token_hash, expires_at)
VALUES (@id, @session_id, @token_hash, @expires_at)
RETURNING *;

-- name: GetRefreshTokenByHash :one
-- No expiry or used_at filter here on purpose. An expired or already-used token must still
-- be FOUND so the service can tell "unknown token" from "replayed token" — the latter is a
-- security signal that revokes the entire family, and filtering it out here would silently
-- downgrade a break-in to a routine 401.
SELECT * FROM refresh_tokens WHERE token_hash = @token_hash;

-- name: MarkRefreshTokenUsed :execrows
-- The `used_at IS NULL` guard makes rotation atomic: if two requests race with the same
-- token, exactly one updates a row. The loser sees zero rows and is treated as a replay,
-- which is the correct conclusion.
UPDATE refresh_tokens
SET used_at = @used_at, replaced_by = @replaced_by
WHERE id = @id AND used_at IS NULL;

-- name: DeleteExpiredRefreshTokens :execrows
-- Relies on refresh_tokens_replaced_by. Without that index this is quadratic: the
-- self-referencing FK forces a scan for referring rows per row deleted.
DELETE FROM refresh_tokens WHERE expires_at < @before;

-- name: DeleteExpiredSessions :execrows
DELETE FROM auth_sessions WHERE expires_at < @before;
