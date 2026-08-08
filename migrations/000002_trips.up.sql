-- 000002_trips — the Trips domain module: trips, membership, invitations, days,
-- slots, slot options, and votes.
--
-- Everything here is domain-specific. The reusable seam is the MembershipRepository
-- *interface* in internal/domain, not this table: a polymorphic
-- memberships(resource_type, resource_id) table cannot be enforced by Postgres, and
-- trading real referential integrity for a generality with no second consumer is a bad deal.
--
-- SLOTS AND OPTIONS
--
-- A slot is a DECISION ("Where are we staying in Goa"); an option is a CANDIDATE for it
-- ("Taj Exotica", "Airbnb in Anjuna"). The simplest itinerary entry is a slot with exactly
-- one option. This is what makes "I don't like this hotel, here's an alternative" a new
-- option on the existing decision rather than a competing entry that has to be reconciled
-- by hand.
--
-- Slots are the addressable, ordered entity: fractional-index position, day assignment and
-- schedule time all live on the slot, because the itinerary renders by time and an
-- undecided slot still occupies a place in it. Place data lives on the option, because each
-- candidate has its own address and coordinates.

CREATE TABLE trips (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    -- Nullable: planning routinely starts before dates are fixed. NOT NULL here would
    -- force users to invent data.
    start_date  date,
    end_date    date,
    -- Item times are wall-clock local to the *trip*, not to the viewer. Without this,
    -- a collaborator in another zone sees a different itinerary — a correctness bug,
    -- not a display bug. IANA name; validated in the domain layer (SQL cannot check it).
    time_zone   text        NOT NULL DEFAULT 'UTC',
    version     integer     NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    deleted_at  timestamptz,

    CONSTRAINT trips_name_len         CHECK (char_length(name) BETWEEN 1 AND 200),
    CONSTRAINT trips_dates_ordered    CHECK (start_date IS NULL OR end_date IS NULL OR end_date >= start_date),
    CONSTRAINT trips_version_positive CHECK (version > 0)
);

-- Deliberately NO owner_id column (D5). It would duplicate the trip_members row with
-- role='owner' — two sources of truth for one fact, which will drift. Ownership lives in
-- exactly one place and is enforced by trip_members_owner_uq below.


CREATE TABLE trip_members (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    trip_id    uuid        NOT NULL REFERENCES trips (id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role       text        NOT NULL,
    invited_by uuid REFERENCES users (id) ON DELETE SET NULL,
    joined_at  timestamptz NOT NULL DEFAULT now(),
    version    integer     NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,

    CONSTRAINT trip_members_role CHECK (role IN ('owner', 'editor', 'viewer'))
);

CREATE UNIQUE INDEX trip_members_uq ON trip_members (trip_id, user_id) WHERE deleted_at IS NULL;

-- Exactly one owner per trip, enforced by the DATABASE. An application-level "check then
-- insert" races under concurrency; invariants expressible as constraints should be constraints.
CREATE UNIQUE INDEX trip_members_owner_uq ON trip_members (trip_id)
    WHERE role = 'owner' AND deleted_at IS NULL;

-- Drives "list my trips". Non-partial so it also backs the user_id FK — a partial index
-- cannot be used for foreign-key checks. A member belongs to few trips, so filtering
-- deleted_at after the index lookup costs nothing.
CREATE INDEX trip_members_user ON trip_members (user_id);


CREATE TABLE trip_invitations (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    trip_id    uuid        NOT NULL REFERENCES trips (id) ON DELETE CASCADE,
    -- NULL => shareable link invite. One table covers both modes because the lifecycle
    -- is the same; only the delivery channel differs.
    email      text,
    role       text        NOT NULL,
    token_hash bytea       NOT NULL,
    created_by uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    max_uses   integer,
    use_count  integer     NOT NULL DEFAULT 0,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),

    -- 'owner' is excluded at the DB level: ownership transfer is a distinct operation with
    -- distinct rules, and no invite link should be able to grant it.
    CONSTRAINT trip_invitations_role      CHECK (role IN ('editor', 'viewer')),
    CONSTRAINT trip_invitations_hash_len  CHECK (octet_length(token_hash) = 32),
    CONSTRAINT trip_invitations_uses      CHECK (max_uses IS NULL OR (max_uses > 0 AND use_count <= max_uses)),
    CONSTRAINT trip_invitations_use_count CHECK (use_count >= 0)
);

CREATE UNIQUE INDEX trip_invitations_hash_uq ON trip_invitations (token_hash);
CREATE INDEX trip_invitations_trip ON trip_invitations (trip_id);


CREATE TABLE days (
    id       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    trip_id  uuid NOT NULL REFERENCES trips (id) ON DELETE CASCADE,
    -- Nullable so a day can exist before it is scheduled.
    date     date,
    label    text NOT NULL DEFAULT '',
    -- Fractional index (D2). COLLATE "C" pins byte-wise comparison so Postgres ordering
    -- is bit-identical to Go's string comparison, independent of the database locale.
    -- Sync convergence depends on every replica agreeing on order; a locale-aware
    -- collation would quietly break that.
    position text COLLATE "C" NOT NULL,
    version  integer     NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,

    CONSTRAINT days_position_len CHECK (char_length(position) BETWEEN 1 AND 128),
    CONSTRAINT days_label_len    CHECK (char_length(label) <= 100),

    -- Redundant against the PK on its own, but required as the target of the composite
    -- FK from slots below. This is what makes cross-trip drift unrepresentable.
    CONSTRAINT days_id_trip_uq UNIQUE (id, trip_id)
);

CREATE UNIQUE INDEX days_trip_date_uq ON days (trip_id, date)
    WHERE date IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX days_trip_pos ON days (trip_id, position) WHERE deleted_at IS NULL;


-- ---------------------------------------------------------------------------------------
-- slots — a decision within a trip or day. The ordered, addressable unit of the itinerary.
-- ---------------------------------------------------------------------------------------
CREATE TABLE slots (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Denormalised from days.trip_id. Every authorization check and every sync-room scoping
    -- query is by trip; this removes a join from the hottest path in the system.
    trip_id            uuid NOT NULL,
    -- NULL => trip backlog: a decision not yet placed on a day.
    day_id             uuid,

    kind               text NOT NULL,
    title              text NOT NULL,          -- "Where are we staying in Goa"
    notes              text NOT NULL DEFAULT '',

    -- `time`, not `timestamptz` (D7): the day supplies the date and the trip supplies the
    -- zone. Time lives on the SLOT rather than the option because the itinerary renders by
    -- time — an undecided slot with three candidate hotels still occupies a schedule
    -- position, which it could not if time belonged to the chosen option.
    start_time         time,
    end_time           time,

    position           text COLLATE "C" NOT NULL,

    -- The resolution. STORED rather than derived from the vote tally, because a group
    -- routinely overrides its own vote ("we voted for A, but B had availability") and a
    -- computed winner cannot represent that. FK added after slot_options exists.
    selected_option_id uuid,

    -- Mode 2 (Live) coverage state. A field rather than a table: single-valued with no
    -- cardinality, so a table would be a join on every itinerary render and buy nothing
    -- until per-user or historical status is needed. The _at/_by columns keep the "who and
    -- when" that a bare enum throws away — same reasoning as users.email_verified_at.
    status             text NOT NULL DEFAULT 'planned',
    status_changed_at  timestamptz,
    status_changed_by  uuid REFERENCES users (id) ON DELETE SET NULL,

    created_by         uuid REFERENCES users (id) ON DELETE SET NULL,
    version            integer     NOT NULL DEFAULT 1,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    -- Tombstone, and load-bearing (D3): concurrent delete-versus-edit is one of the three
    -- required Stage 2 conflict cases, and you cannot converge on a row that is gone.
    deleted_at         timestamptz,

    CONSTRAINT slots_trip_fk FOREIGN KEY (trip_id) REFERENCES trips (id) ON DELETE CASCADE,

    -- The composite FK. Because it references days(id, trip_id), a slot can only point at a
    -- day belonging to its own trip — Postgres makes the inconsistency unrepresentable
    -- rather than trusting application code to maintain it.
    --
    -- `ON DELETE SET NULL (day_id)` needs PG15+. Without the column list, a composite
    -- SET NULL would also null trip_id and violate NOT NULL. This is why PG16 is pinned.
    CONSTRAINT slots_day_fk FOREIGN KEY (day_id, trip_id) REFERENCES days (id, trip_id)
        ON DELETE SET NULL (day_id),

    CONSTRAINT slots_kind         CHECK (kind IN ('place', 'activity', 'transport', 'lodging', 'note')),
    CONSTRAINT slots_status       CHECK (status IN ('planned', 'covered', 'skipped')),
    CONSTRAINT slots_title_len    CHECK (char_length(title) BETWEEN 1 AND 200),
    CONSTRAINT slots_notes_len    CHECK (char_length(notes) <= 10000),
    CONSTRAINT slots_position_len CHECK (char_length(position) BETWEEN 1 AND 128),
    CONSTRAINT slots_times        CHECK (end_time IS NULL OR start_time IS NULL OR end_time >= start_time),
    CONSTRAINT slots_version      CHECK (version > 0),

    -- Target for the composite FKs from slot_options and option_votes. This is the same
    -- pattern as days_id_trip_uq, applied one level deeper.
    CONSTRAINT slots_id_trip_uq UNIQUE (id, trip_id)
);

-- Rendering a day: ordered by (position, id). The id tiebreak is not cosmetic — two clients
-- inserting into the same gap without seeing each other legitimately produce the SAME
-- fractional index, and the id is what makes every replica derive an identical total order.
CREATE INDEX slots_day_pos      ON slots (day_id, position, id) WHERE deleted_at IS NULL;
CREATE INDEX slots_trip_backlog ON slots (trip_id, position, id) WHERE day_id IS NULL AND deleted_at IS NULL;
-- Supports the Stage 2 "everything in this trip since T" resync path, and doubles as the
-- non-partial index backing the trip_id FK.
CREATE INDEX slots_trip_updated ON slots (trip_id, updated_at DESC);


-- ---------------------------------------------------------------------------------------
-- slot_options — candidate proposals under a slot.
-- ---------------------------------------------------------------------------------------
CREATE TABLE slot_options (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slot_id              uuid NOT NULL,
    -- Denormalised two levels for authz and sync-room scoping, guaranteed consistent by the
    -- composite FK below.
    trip_id              uuid NOT NULL,

    title                text NOT NULL,          -- "Taj Exotica"
    notes                text NOT NULL DEFAULT '',
    external_url         text NOT NULL DEFAULT '',

    -- The proposer's ESTIMATE. Deliberately distinct from budget_entries, which is the
    -- ledger: this answers "roughly what would this option cost", not "what did we spend".
    -- Minor units (paise/cents) as bigint — never floating point, which cannot represent
    -- 0.10 exactly and accumulates error across a split.
    estimated_cost_minor bigint,

    -- Discrete columns rather than a JSONB blob (D6): Stage 2 resolves conflicts at field
    -- level, so two users editing `title` and `place_address` concurrently should both
    -- succeed. An opaque blob would coarsen that back to whole-entity conflicts.
    --
    -- Nullability rule: a column is nullable only where NULL is semantically distinct from
    -- the zero value. An absent place name and an empty one are the same thing, so those are
    -- NOT NULL DEFAULT ''. Coordinates ARE nullable, because (0, 0) is a real location in
    -- the Gulf of Guinea and "unknown" must not collide with it.
    place_name           text NOT NULL DEFAULT '',
    place_address        text NOT NULL DEFAULT '',
    place_lat            double precision,
    place_lng            double precision,
    place_provider_id    text NOT NULL DEFAULT '',

    proposed_by          uuid REFERENCES users (id) ON DELETE SET NULL,
    version              integer     NOT NULL DEFAULT 1,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    deleted_at           timestamptz,

    CONSTRAINT slot_options_slot_fk FOREIGN KEY (slot_id, trip_id) REFERENCES slots (id, trip_id)
        ON DELETE CASCADE,

    CONSTRAINT slot_options_title_len CHECK (char_length(title) BETWEEN 1 AND 200),
    CONSTRAINT slot_options_notes_len CHECK (char_length(notes) <= 10000),
    CONSTRAINT slot_options_cost      CHECK (estimated_cost_minor IS NULL OR estimated_cost_minor >= 0),
    CONSTRAINT slot_options_lat       CHECK (place_lat IS NULL OR place_lat BETWEEN -90 AND 90),
    CONSTRAINT slot_options_lng       CHECK (place_lng IS NULL OR place_lng BETWEEN -180 AND 180),
    -- Coordinates are meaningless individually.
    CONSTRAINT slot_options_latlng    CHECK ((place_lat IS NULL) = (place_lng IS NULL)),
    CONSTRAINT slot_options_version   CHECK (version > 0),

    -- Two composite-FK targets, deliberately. (id, slot_id) lets a vote prove its option
    -- belongs to the slot being voted on, and lets slots.selected_option_id prove the same.
    -- (id, trip_id) lets budget entries and attachments prove trip membership without a join.
    -- The cost is two extra unique indexes on a frequently-written table; the benefit is
    -- that "vote for another slot's option" and "bill another trip's option" are
    -- unrepresentable rather than merely unlikely.
    CONSTRAINT slot_options_id_slot_uq UNIQUE (id, slot_id),
    CONSTRAINT slot_options_id_trip_uq UNIQUE (id, trip_id)
);

-- Non-partial: also backs the leading column of the slot_id composite FK. Options are
-- ordered by creation, not by a fractional index — reordering candidates is not a gesture
-- the product needs, and if it becomes one, fracdex applies identically as an added column.
CREATE INDEX slot_options_slot ON slot_options (slot_id, created_at, id);
CREATE INDEX slot_options_trip_updated ON slot_options (trip_id, updated_at DESC);


-- The circular reference, added after slot_options exists.
--
-- slots -> slot_options -> slots is a genuine cycle. It is safe because selected_option_id
-- is nullable: a slot is created with no selection, options are added, then the selection is
-- set. `ON DELETE SET NULL (selected_option_id)` (PG15+ column list again) is what keeps a
-- hard-deleted option from taking its slot with it.
ALTER TABLE slots ADD CONSTRAINT slots_selected_option_fk
    FOREIGN KEY (selected_option_id, id) REFERENCES slot_options (id, slot_id)
    ON DELETE SET NULL (selected_option_id);

-- Non-partial. `WHERE selected_option_id IS NOT NULL` would index half as many rows and
-- still serve every query — but a partial index cannot back a foreign-key check, so the FK
-- would be unindexed and the audit in tests/schema_verify.sql rejects it. Same call as
-- refresh_tokens.replaced_by: the planner would probably use the partial index here, and
-- "probably, resting on planner behaviour" is not a foundation for delete performance.
CREATE INDEX slots_selected_option ON slots (selected_option_id);


-- ---------------------------------------------------------------------------------------
-- option_votes — one member's choice within one slot.
-- ---------------------------------------------------------------------------------------
--
-- Shape note for Stage 2: this table is exactly one row per (slot, user) with one mutable
-- field. That makes it a last-writer-wins REGISTER — the simplest convergent structure
-- there is, with no tombstone reconciliation and no delete-versus-edit case. It should be
-- the easiest entity in the system to prove convergent, and the vote operation in the sync
-- vocabulary is correspondingly trivial: set_vote(slot_id, option_id | null).
--
-- Single choice per slot, not approval voting: "a slot resolves to a selected option"
-- implies picking one, the tally is then unambiguous, and changing your mind is a
-- single-row UPDATE rather than a delete plus an insert.
CREATE TABLE option_votes (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slot_id    uuid NOT NULL,
    trip_id    uuid NOT NULL,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,

    -- NULL means explicitly retracted. Retraction is a VALUE, not a deletion.
    --
    -- This is the one deliberate break from the tombstone convention, and it is for a
    -- convergence reason: keeping the row and nulling the field preserves the register
    -- shape above. A soft-deleted vote row would reintroduce exactly the delete-versus-edit
    -- conflict the shape exists to avoid.
    option_id  uuid,

    version    integer     NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    -- Proves the vote's slot is in the stated trip.
    CONSTRAINT option_votes_slot_fk FOREIGN KEY (slot_id, trip_id) REFERENCES slots (id, trip_id)
        ON DELETE CASCADE,
    -- Proves the voted option belongs to that slot. Transitively, voting for another trip's
    -- option is unrepresentable.
    CONSTRAINT option_votes_option_fk FOREIGN KEY (option_id, slot_id)
        REFERENCES slot_options (id, slot_id) ON DELETE SET NULL (option_id),

    CONSTRAINT option_votes_version CHECK (version > 0)
);

-- One vote per member per slot. Non-partial (there is no soft delete here), so it also
-- backs the leading column of the slot composite FK.
CREATE UNIQUE INDEX option_votes_uq ON option_votes (slot_id, user_id);
-- Tallying votes for an option, and backing the option composite FK. Non-partial for the
-- FK reason above; retracted votes (option_id NULL) are a small fraction of the table.
CREATE INDEX option_votes_option ON option_votes (option_id);
-- Stage 2 resync: every vote in a trip.
CREATE INDEX option_votes_trip ON option_votes (trip_id, updated_at DESC);


-- ---------------------------------------------------------------------------------------
-- Index policy, restated because the convention has real exceptions.
--
-- The rule is NOT "index every FK column". Postgres indexes the referenced side of a
-- foreign key automatically and the referencing side never, so an unindexed referencing
-- column turns a parent delete into a sequential scan of the child table. But an index is
-- also write amplification, and slots/slot_options/option_votes are the hottest write paths
-- in a real-time collaborative app.
--
-- The refined rule: index the referencing column when the parent is hard-deleted on a
-- ROUTINE path, or when the column is queried directly. Note that a PARTIAL index cannot
-- support a foreign-key check at all, so where the FK matters the index must be
-- unconditional — which is why several indexes above are non-partial despite every query
-- filtering on deleted_at.
--
-- Consciously left unindexed, with the justification:
--   days.trip_id, trip_members.trip_id, slots.day_id
--       Parents here are SOFT-deleted by design (tombstones are required for Stage 2
--       conflict resolution), so a hard delete is an administrative or test-teardown
--       operation, not a routine one.
--   slots.created_by, slots.status_changed_by, slot_options.proposed_by,
--   trip_members.invited_by, trip_invitations.created_by, option_votes.user_id
--       Users are soft-deleted too. A hard erasure (e.g. a GDPR request) may scan; it is a
--       rare, offline, human-initiated operation and is not worth taxing every write.
--
-- If any of these becomes routine, add the index then — and re-run the FK/index audit in
-- tests/schema_verify.sql, which is what surfaced this list in the first place.
-- ---------------------------------------------------------------------------------------
