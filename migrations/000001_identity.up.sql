-- 000001_identity — core identity: users, sessions, refresh tokens, one-shot user tokens.
--
-- This is CORE (module-agnostic). A second domain module would reuse these tables unchanged.
--
-- Convention notes that apply throughout:
--   * ids are UUIDv7 supplied by the application (D4). The DB default is a safety net only.
--   * updated_at is set explicitly by each query, NOT by a trigger. In Stage 2 it sometimes
--     needs to be the operation's server timestamp, and a blanket trigger would fight that.
--   * soft delete forces every uniqueness rule to be a PARTIAL unique index, otherwise a
--     deleted row blocks re-creating the same key forever.

CREATE TABLE users (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email             text        NOT NULL,
    password_hash     text        NOT NULL,
    display_name      text        NOT NULL,
    email_verified_at timestamptz,
    version           integer     NOT NULL DEFAULT 1,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    deleted_at        timestamptz,

    CONSTRAINT users_email_len        CHECK (char_length(email) BETWEEN 3 AND 320),
    CONSTRAINT users_display_name_len CHECK (char_length(display_name) BETWEEN 1 AND 100),
    CONSTRAINT users_version_positive CHECK (version > 0)
);

-- Case-insensitive uniqueness via a functional index rather than the citext extension:
-- same semantics, one less extension, and it is explicit about where case folding happens.
-- The stored `email` keeps the casing the user typed (display); only uniqueness is folded.
CREATE UNIQUE INDEX users_email_lower_uq ON users (lower(email)) WHERE deleted_at IS NULL;


-- One row per device/login. This is the refresh-token *family*: revoking the session kills
-- every token descended from it, which is what makes reuse detection meaningful.
CREATE TABLE auth_sessions (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    user_agent     text        NOT NULL DEFAULT '',
    ip             inet,
    created_at     timestamptz NOT NULL DEFAULT now(),
    last_used_at   timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz NOT NULL,
    revoked_at     timestamptz,
    revoked_reason text
);

-- Deliberately NOT partial. A partial index does not support foreign-key checks, so
-- `WHERE revoked_at IS NULL` here would leave the user_id FK unindexed and turn a user
-- deletion into a sequential scan. It also serves the session-management UI, which lists
-- revoked sessions too.
CREATE INDEX auth_sessions_user       ON auth_sessions (user_id);
CREATE INDEX auth_sessions_expires_at ON auth_sessions (expires_at) WHERE revoked_at IS NULL;


-- Deliberate deviation from the "every table gets version + soft delete" convention:
-- credentials are not user data. They are created, consumed once, and purged. A
-- soft-deleted credential is a security liability, not a feature, and nothing ever
-- concurrently edits a token row. Deviating loudly beats applying the rule mechanically.
CREATE TABLE refresh_tokens (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id  uuid        NOT NULL REFERENCES auth_sessions (id) ON DELETE CASCADE,
    -- sha256(raw token). Refresh tokens are full-entropy random bytes, so a slow KDF
    -- would be cost with no benefit — unlike passwords, these are not guessable.
    token_hash  bytea       NOT NULL,
    issued_at   timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz,
    -- The rotation chain. Presenting a token whose used_at is set means replay:
    -- the whole family gets revoked. Without this column, rotation is theatre.
    replaced_by uuid REFERENCES refresh_tokens (id) ON DELETE SET NULL,

    CONSTRAINT refresh_tokens_hash_len CHECK (octet_length(token_hash) = 32)
);

CREATE UNIQUE INDEX refresh_tokens_hash_uq ON refresh_tokens (token_hash);
CREATE INDEX refresh_tokens_session       ON refresh_tokens (session_id);
CREATE INDEX refresh_tokens_expires_at    ON refresh_tokens (expires_at) WHERE used_at IS NULL;

-- Not optional. `replaced_by` is a self-referencing FK with ON DELETE SET NULL, so the
-- routine expiry-cleanup job (DELETE ... WHERE expires_at < now()) has to locate rows
-- pointing at each row it deletes. Without this index that is a sequential scan per
-- deleted row — quadratic behaviour on a job that runs continuously and grows with the
-- table.
--
-- Non-partial, even though only exchanged tokens have a successor and `WHERE replaced_by
-- IS NOT NULL` would index roughly half as many rows. The referential-integrity check runs
-- `WHERE replaced_by = $1`, which does imply the predicate, so the planner would probably
-- use a partial index here — but "probably", resting on planner behaviour, is not a good
-- foundation for the difference between a linear and a quadratic cleanup job. The extra
-- NULL entries cost almost nothing in a table that is purged on a schedule.
CREATE INDEX refresh_tokens_replaced_by ON refresh_tokens (replaced_by);


-- Single-use, hashed, short-TTL tokens for email verification and password reset.
-- One table with a `purpose` discriminator rather than two near-identical tables:
-- the lifecycle (issue -> email -> consume once -> expire) is genuinely identical.
CREATE TABLE user_tokens (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    purpose     text        NOT NULL,
    token_hash  bytea       NOT NULL,
    expires_at  timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT user_tokens_purpose  CHECK (purpose IN ('email_verify', 'password_reset')),
    CONSTRAINT user_tokens_hash_len CHECK (octet_length(token_hash) = 32)
);

CREATE UNIQUE INDEX user_tokens_hash_uq      ON user_tokens (token_hash);
-- Non-partial, for the same reason as auth_sessions_user: it has to back the user_id FK.
CREATE INDEX        user_tokens_user_purpose ON user_tokens (user_id, purpose);
CREATE INDEX        user_tokens_expires_at   ON user_tokens (expires_at) WHERE consumed_at IS NULL;
