-- 000003 — trip budget (entries + splits) and attachments.
--
-- Additive over 000002. Two concerns live here because both depend on slot_options existing,
-- and both are deliberately treated DIFFERENTLY from slots by the eventual sync engine:
--
--   * Budget is an ATOMIC unit, not field-mergeable. See the note above budget_splits.
--   * Attachments are BROADCAST-only, with no conflict resolution at all. See attachments.


-- ---------------------------------------------------------------------------------------
-- Currency
--
-- One currency per trip, with no per-row currency column. Travel is genuinely
-- multi-currency, but FX is a subsystem (rate source, rate-at-time-of-entry, re-conversion
-- when rates move) and a currency column WITHOUT it produces sums that are silently
-- meaningless — the worst of both. Deferring is the honest option; adding per-row currency
-- later is an additive column plus a one-statement backfill from this value.
--
-- ISO 4217 alpha-3. char(3) rather than text because the width is genuinely fixed.
-- ---------------------------------------------------------------------------------------
ALTER TABLE trips ADD COLUMN base_currency char(3) NOT NULL DEFAULT 'USD';

ALTER TABLE trips ADD CONSTRAINT trips_base_currency
    CHECK (base_currency ~ '^[A-Z]{3}$');


-- ---------------------------------------------------------------------------------------
-- budget_entries — ledger lines for a trip.
--
-- Distinct from slot_options.estimated_cost_minor, which is a PROPOSAL'S ESTIMATE. This is
-- what the trip actually plans to spend or has spent, and it is the thing that gets split
-- between members.
-- ---------------------------------------------------------------------------------------
CREATE TABLE budget_entries (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    trip_id        uuid NOT NULL REFERENCES trips (id) ON DELETE CASCADE,

    -- Optional link to the proposal this cost belongs to. Composite FK so a budget line
    -- cannot be attached to another trip's option.
    slot_option_id uuid,

    label          text   NOT NULL,
    category       text   NOT NULL DEFAULT 'other',

    -- Minor units (paise, cents) as bigint. NEVER floating point: binary floating point
    -- cannot represent 0.10 exactly, and the error compounds across a split until the parts
    -- no longer sum to the whole — which is precisely the invariant this table exists to
    -- protect. bigint also survives currencies with large minor-unit totals.
    amount_minor   bigint NOT NULL,

    paid_by        uuid REFERENCES users (id) ON DELETE SET NULL,
    incurred_on    date,

    created_by     uuid REFERENCES users (id) ON DELETE SET NULL,
    version        integer     NOT NULL DEFAULT 1,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    deleted_at     timestamptz,

    CONSTRAINT budget_entries_option_fk FOREIGN KEY (slot_option_id, trip_id)
        REFERENCES slot_options (id, trip_id) ON DELETE SET NULL (slot_option_id),

    CONSTRAINT budget_entries_label_len CHECK (char_length(label) BETWEEN 1 AND 200),
    CONSTRAINT budget_entries_amount    CHECK (amount_minor >= 0),
    CONSTRAINT budget_entries_version   CHECK (version > 0),
    CONSTRAINT budget_entries_category  CHECK (category IN
        ('lodging', 'transport', 'food', 'activity', 'shopping', 'other'))
);

CREATE INDEX budget_entries_trip ON budget_entries (trip_id, updated_at DESC);
-- Non-partial: it backs the leading column of the slot_option composite FK, and a partial
-- index cannot be used for a foreign-key check.
CREATE INDEX budget_entries_option ON budget_entries (slot_option_id);


-- ---------------------------------------------------------------------------------------
-- budget_splits — who owes what on one entry.
--
-- CONCURRENCY POSITION (Stage 2, decided up front rather than discovered):
--
-- The invariant is SUM(splits.amount_minor) = entry.amount_minor, and it must hold at every
-- point a client renders. Budget writes are therefore ATOMIC OPERATIONS carrying the total
-- and the complete split set together — NOT field-level merges like slots and options.
--
-- Why not field-level merge plus a post-merge check: each individual merge is locally
-- plausible and jointly wrong. A sets the total to 1000; B sets their own split to 600. Both
-- operations are valid in isolation and the merged state violates the invariant. Detecting
-- that is easy; REPAIRING it is not, because repair means choosing whose edit to discard —
-- exactly the decision merging exists to avoid. Silently rewriting someone's number is worse
-- for money than showing them a conflict.
--
-- So budget deliberately has a COARSER conflict grain than the itinerary. Text has no
-- cross-field invariant; money does.
--
-- Splits carry explicit AMOUNTS, not percentages, because integer division has a remainder:
-- 1000 across three people is 333/333/334, and the extra unit must belong to a specific
-- person deterministically rather than being recomputed (and disagreed about) per client.
--
-- No version and no deleted_at here, a deliberate convention deviation: splits are never
-- edited individually, only rewritten wholesale with their entry inside one transaction.
-- They have no independent identity to version, and a soft-deleted split would still be
-- present and break the sum.
-- ---------------------------------------------------------------------------------------
CREATE TABLE budget_splits (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    budget_entry_id uuid   NOT NULL REFERENCES budget_entries (id) ON DELETE CASCADE,
    user_id         uuid   NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    amount_minor    bigint NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT budget_splits_amount CHECK (amount_minor >= 0),
    CONSTRAINT budget_splits_uq     UNIQUE (budget_entry_id, user_id)
);

CREATE INDEX budget_splits_entry ON budget_splits (budget_entry_id);
-- "What do I owe across this trip" is a per-user query.
CREATE INDEX budget_splits_user  ON budget_splits (user_id);


-- Database-level backstop for the sum invariant.
--
-- The sync engine is responsible for applying budget writes atomically, but "responsible
-- for" is not "incapable of getting wrong". This trigger makes a violating state impossible
-- to commit regardless of which writer produced it — turning a class of bug into an
-- unreachable state.
--
-- DEFERRABLE INITIALLY DEFERRED is essential: splits are rewritten by deleting and
-- reinserting inside a transaction, so the invariant is legitimately violated mid-way and
-- must only be checked at COMMIT.
--
-- Zero splits is allowed: an entry that nobody has split yet is a normal state, distinct
-- from an entry split incorrectly.
CREATE FUNCTION check_budget_split_sum() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    entry_id    uuid;
    entry_total bigint;
    split_total bigint;
    split_count bigint;
BEGIN
    entry_id := COALESCE(NEW.budget_entry_id, OLD.budget_entry_id);

    SELECT amount_minor INTO entry_total FROM budget_entries WHERE id = entry_id;
    IF NOT FOUND THEN
        -- The entry itself was deleted in this transaction; its splits cascaded away.
        RETURN NULL;
    END IF;

    SELECT COALESCE(SUM(amount_minor), 0), count(*)
      INTO split_total, split_count
      FROM budget_splits WHERE budget_entry_id = entry_id;

    IF split_count > 0 AND split_total <> entry_total THEN
        RAISE EXCEPTION
            'budget splits sum to % but the entry total is % (entry %)',
            split_total, entry_total, entry_id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER budget_splits_sum_check
    AFTER INSERT OR UPDATE OR DELETE ON budget_splits
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION check_budget_split_sum();

-- The same invariant from the other side: changing an entry's total must not orphan its
-- existing splits.
CREATE FUNCTION check_budget_entry_total() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    split_total bigint;
    split_count bigint;
BEGIN
    SELECT COALESCE(SUM(amount_minor), 0), count(*)
      INTO split_total, split_count
      FROM budget_splits WHERE budget_entry_id = NEW.id;

    IF split_count > 0 AND split_total <> NEW.amount_minor THEN
        RAISE EXCEPTION
            'entry total % does not match existing splits summing to % (entry %)',
            NEW.amount_minor, split_total, NEW.id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER budget_entry_total_check
    AFTER INSERT OR UPDATE OF amount_minor ON budget_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION check_budget_entry_total();


-- ---------------------------------------------------------------------------------------
-- attachments — photos, ticket screenshots and links.
--
-- SYNC NOTE (Stage 2): attachments are the ONE entity that does not get conflict-resolution
-- machinery. An upload is a one-shot event, not a mergeable field, so the sync vocabulary
-- needs only a lightweight `attachment_added` / `attachment_removed` broadcast. Do not build
-- operational transforms for this table.
--
-- UPLOAD LIFECYCLE: the browser uploads DIRECTLY to object storage via a presigned URL —
-- proxying files through the API would waste memory and bandwidth for no benefit. The
-- consequence is that the API never sees the bytes and a presigned PUT cannot enforce a size
-- limit, so rows are created `pending` and only become `ready` after a server-side Stat()
-- confirms size and content type. Rows stuck in `pending` are swept by a maintenance job.
-- ---------------------------------------------------------------------------------------
CREATE TABLE attachments (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    trip_id         uuid NOT NULL REFERENCES trips (id) ON DELETE CASCADE,

    -- Exclusive arc: exactly one owner, expressed as three REAL foreign keys rather than a
    -- polymorphic (owner_type, owner_id) pair. Same reasoning as trip_members: Postgres
    -- cannot enforce a polymorphic reference, and referential integrity is worth more than
    -- two saved columns.
    slot_option_id  uuid REFERENCES slot_options (id) ON DELETE CASCADE,
    budget_entry_id uuid REFERENCES budget_entries (id) ON DELETE CASCADE,
    slot_id         uuid REFERENCES slots (id) ON DELETE CASCADE,

    kind            text NOT NULL,
    status          text NOT NULL DEFAULT 'pending',

    storage_key     text   NOT NULL DEFAULT '',   -- object key; empty for links
    external_url    text   NOT NULL DEFAULT '',   -- empty for files
    content_type    text   NOT NULL DEFAULT '',
    size_bytes      bigint,
    checksum_sha256 bytea,
    original_name   text   NOT NULL DEFAULT '',

    uploaded_by     uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    deleted_at      timestamptz,

    -- num_nonnulls is a Postgres builtin; this is the whole exclusive-arc enforcement.
    CONSTRAINT attachments_one_owner CHECK (
        num_nonnulls(slot_option_id, budget_entry_id, slot_id) = 1),

    CONSTRAINT attachments_kind   CHECK (kind IN ('file', 'link')),
    CONSTRAINT attachments_status CHECK (status IN ('pending', 'ready', 'failed')),

    -- A file has an object key and no URL; a link has a URL, no key, and is immediately
    -- ready because there is nothing to upload.
    CONSTRAINT attachments_shape CHECK (
        (kind = 'file' AND storage_key <> '' AND external_url = '') OR
        (kind = 'link' AND external_url <> '' AND storage_key = '' AND status = 'ready')),

    CONSTRAINT attachments_size     CHECK (size_bytes IS NULL OR size_bytes >= 0),
    CONSTRAINT attachments_checksum CHECK (checksum_sha256 IS NULL OR octet_length(checksum_sha256) = 32),
    CONSTRAINT attachments_name_len CHECK (char_length(original_name) <= 255)
);

-- No version column: attachments are immutable once ready. You replace one, you do not edit
-- it — which is the same fact that makes them broadcast-only above.

-- One row per object. Prevents two attachment records pointing at the same stored file,
-- which would make deletion ambiguous.
CREATE UNIQUE INDEX attachments_storage_key_uq ON attachments (storage_key) WHERE storage_key <> '';

-- Owner lookups. Non-partial so they also back the three FKs of the exclusive arc.
CREATE INDEX attachments_option ON attachments (slot_option_id);
CREATE INDEX attachments_budget ON attachments (budget_entry_id);
CREATE INDEX attachments_slot   ON attachments (slot_id);
CREATE INDEX attachments_trip   ON attachments (trip_id, created_at DESC);
-- Drives the orphan sweeper for uploads that were never confirmed.
CREATE INDEX attachments_pending ON attachments (created_at) WHERE status = 'pending';
