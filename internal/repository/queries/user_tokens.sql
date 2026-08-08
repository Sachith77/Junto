-- Queries for user_tokens: single-use email verification and password reset tokens.

-- name: CreateUserToken :one
INSERT INTO user_tokens (id, user_id, purpose, token_hash, expires_at)
VALUES (@id, @user_id, @purpose, @token_hash, @expires_at)
RETURNING *;

-- name: GetUserTokenByHash :one
-- Unfiltered, like GetRefreshTokenByHash: the service needs to distinguish "unknown",
-- "expired" and "already consumed" to return the right message and the right log line.
SELECT * FROM user_tokens WHERE token_hash = @token_hash;

-- name: ConsumeUserToken :execrows
-- `consumed_at IS NULL` makes single use atomic. Two requests racing on the same reset link
-- produce exactly one winner; the loser gets zero rows and is rejected.
UPDATE user_tokens SET consumed_at = @consumed_at
WHERE id = @id AND consumed_at IS NULL;

-- name: ConsumeAllUserTokensForPurpose :execrows
-- Issuing a new reset link must retire the previous one, or every link ever mailed stays
-- live until it expires — turning an old inbox into a standing account takeover.
UPDATE user_tokens SET consumed_at = @consumed_at
WHERE user_id = @user_id AND purpose = @purpose AND consumed_at IS NULL;

-- name: DeleteExpiredUserTokens :execrows
DELETE FROM user_tokens WHERE expires_at < @before;
