-- Adversarial schema verification.
--
-- Every assertion here ATTEMPTS a violation and expects the database to reject it. The point
-- is that the schema's guarantees are proven rather than asserted in a comment: if someone
-- later drops a constraint or widens a CHECK "temporarily", this fails.
--
-- These are database-level invariants, deliberately separate from the Go repository tests.
-- Repository tests verify that our queries behave; this verifies that the schema holds even
-- against a writer that bypasses them entirely.
--
-- Run:
--   docker exec -i junto-postgres psql -U junto -d junto -v ON_ERROR_STOP=1 < tests/schema_verify.sql
--
-- Everything runs inside a transaction that is rolled back, so it is safe against a
-- populated development database.

\set ON_ERROR_STOP on
BEGIN;

-- ---------- fixtures ----------
INSERT INTO users (id, email, password_hash, display_name) VALUES
  ('11111111-1111-7111-8111-111111111111', 'Alice@Example.com', 'x', 'Alice'),
  ('22222222-2222-7222-8222-222222222222', 'bob@example.com',   'x', 'Bob');

INSERT INTO trips (id, name, time_zone, base_currency) VALUES
  ('aaaaaaaa-0000-7000-8000-000000000001', 'Lisbon', 'Europe/Lisbon', 'EUR'),
  ('aaaaaaaa-0000-7000-8000-000000000002', 'Tokyo',  'Asia/Tokyo',    'JPY');

INSERT INTO trip_members (id, trip_id, user_id, role) VALUES
  ('bbbbbbbb-0000-7000-8000-000000000001','aaaaaaaa-0000-7000-8000-000000000001','11111111-1111-7111-8111-111111111111','owner');

INSERT INTO days (id, trip_id, date, position) VALUES
  ('cccccccc-0000-7000-8000-000000000001','aaaaaaaa-0000-7000-8000-000000000001','2026-09-01','a0'),
  ('cccccccc-0000-7000-8000-000000000002','aaaaaaaa-0000-7000-8000-000000000002','2026-09-01','a0');

INSERT INTO slots (id, trip_id, day_id, kind, title, position) VALUES
  ('dddddddd-0000-7000-8000-000000000001','aaaaaaaa-0000-7000-8000-000000000001','cccccccc-0000-7000-8000-000000000001','lodging','Where are we staying','a0'),
  ('dddddddd-0000-7000-8000-000000000002','aaaaaaaa-0000-7000-8000-000000000001','cccccccc-0000-7000-8000-000000000001','activity','Day 1 activity','a1'),
  ('dddddddd-0000-7000-8000-000000000003','aaaaaaaa-0000-7000-8000-000000000002','cccccccc-0000-7000-8000-000000000002','lodging','Tokyo lodging','a0');

INSERT INTO slot_options (id, slot_id, trip_id, title) VALUES
  ('eeeeeeee-0000-7000-8000-000000000001','dddddddd-0000-7000-8000-000000000001','aaaaaaaa-0000-7000-8000-000000000001','Taj Exotica'),
  ('eeeeeeee-0000-7000-8000-000000000002','dddddddd-0000-7000-8000-000000000001','aaaaaaaa-0000-7000-8000-000000000001','Airbnb in Anjuna'),
  ('eeeeeeee-0000-7000-8000-000000000003','dddddddd-0000-7000-8000-000000000002','aaaaaaaa-0000-7000-8000-000000000001','Beach day');

INSERT INTO comments (id, slot_id, trip_id, body, author_id) VALUES
  ('ffffffff-0000-7000-8000-000000000001','dddddddd-0000-7000-8000-000000000001','aaaaaaaa-0000-7000-8000-000000000001','Great pick!','11111111-1111-7111-8111-111111111111');

-- ---------- 1. exactly one owner per trip ----------
DO $$ BEGIN
  BEGIN
    INSERT INTO trip_members (trip_id, user_id, role)
      VALUES ('aaaaaaaa-0000-7000-8000-000000000001','22222222-2222-7222-8222-222222222222','owner');
    RAISE EXCEPTION 'FAIL 1: a second owner was allowed';
  EXCEPTION WHEN unique_violation THEN RAISE NOTICE 'PASS 1: second owner rejected';
  END;
END $$;

-- ---------- 2. cross-trip references are unrepresentable at every level ----------
DO $$ BEGIN
  -- 2a. A slot cannot point at a day in another trip.
  BEGIN
    INSERT INTO slots (trip_id, day_id, kind, title, position)
      VALUES ('aaaaaaaa-0000-7000-8000-000000000001','cccccccc-0000-7000-8000-000000000002','place','Smuggled slot','a9');
    RAISE EXCEPTION 'FAIL 2a: cross-trip day reference was allowed';
  EXCEPTION WHEN foreign_key_violation THEN RAISE NOTICE 'PASS 2a: cross-trip day reference rejected';
  END;

  -- 2b. An option cannot claim a trip its slot does not belong to.
  BEGIN
    INSERT INTO slot_options (slot_id, trip_id, title)
      VALUES ('dddddddd-0000-7000-8000-000000000001','aaaaaaaa-0000-7000-8000-000000000002','Wrong trip');
    RAISE EXCEPTION 'FAIL 2b: cross-trip option was allowed';
  EXCEPTION WHEN foreign_key_violation THEN RAISE NOTICE 'PASS 2b: cross-trip option rejected';
  END;

  -- 2c. A slot cannot select an option belonging to a DIFFERENT slot. This is the circular
  --     composite FK doing its job.
  BEGIN
    UPDATE slots SET selected_option_id = 'eeeeeeee-0000-7000-8000-000000000003'
      WHERE id = 'dddddddd-0000-7000-8000-000000000001';
    RAISE EXCEPTION 'FAIL 2c: selecting another slot''s option was allowed';
  EXCEPTION WHEN foreign_key_violation THEN RAISE NOTICE 'PASS 2c: cross-slot selection rejected';
  END;
END $$;

-- Selecting an option that DOES belong to the slot must work.
UPDATE slots SET selected_option_id = 'eeeeeeee-0000-7000-8000-000000000001'
  WHERE id = 'dddddddd-0000-7000-8000-000000000001';
DO $$ DECLARE sel uuid; BEGIN
  SELECT selected_option_id INTO sel FROM slots WHERE id='dddddddd-0000-7000-8000-000000000001';
  IF sel <> 'eeeeeeee-0000-7000-8000-000000000001' THEN
    RAISE EXCEPTION 'FAIL 2d: a valid selection did not persist';
  END IF;
  RAISE NOTICE 'PASS 2d: a valid selection persists';
END $$;

DO $$ BEGIN
  -- 2e. A comment cannot claim a trip its slot does not belong to — same composite-FK shape
  --     as 2b, one level further out (comments -> slots, not comments -> slot_options).
  BEGIN
    INSERT INTO comments (slot_id, trip_id, body)
      VALUES ('dddddddd-0000-7000-8000-000000000001','aaaaaaaa-0000-7000-8000-000000000002','Wrong trip comment');
    RAISE EXCEPTION 'FAIL 2e: cross-trip comment was allowed';
  EXCEPTION WHEN foreign_key_violation THEN RAISE NOTICE 'PASS 2e: cross-trip comment rejected';
  END;
END $$;

-- ---------- 3. case-insensitive email uniqueness ----------
DO $$ BEGIN
  BEGIN
    INSERT INTO users (email, password_hash, display_name) VALUES ('ALICE@example.com','x','Impostor');
    RAISE EXCEPTION 'FAIL 3: case-variant duplicate email was allowed';
  EXCEPTION WHEN unique_violation THEN RAISE NOTICE 'PASS 3: case-variant duplicate email rejected';
  END;
END $$;

-- ---------- 4. soft delete must not permanently burn a key ----------
UPDATE trip_members SET deleted_at = now()
  WHERE trip_id='aaaaaaaa-0000-7000-8000-000000000001' AND user_id='11111111-1111-7111-8111-111111111111';
INSERT INTO trip_members (trip_id, user_id, role)
  VALUES ('aaaaaaaa-0000-7000-8000-000000000001','11111111-1111-7111-8111-111111111111','owner');
DO $$ DECLARE n int; BEGIN
  SELECT count(*) INTO n FROM trip_members
    WHERE trip_id='aaaaaaaa-0000-7000-8000-000000000001' AND deleted_at IS NULL;
  IF n <> 1 THEN RAISE EXCEPTION 'FAIL 4: expected 1 live membership, got %', n; END IF;
  RAISE NOTICE 'PASS 4: re-add after soft delete allowed, exactly 1 live row';
END $$;

-- ---------- 5. ON DELETE SET NULL (day_id) ----------
-- The PG15+ column list is the reason PG16 is pinned. Without it, a composite SET NULL would
-- also null trip_id and violate NOT NULL.
DELETE FROM days WHERE id='cccccccc-0000-7000-8000-000000000001';
DO $$ DECLARE d uuid; t uuid; BEGIN
  SELECT day_id, trip_id INTO d, t FROM slots WHERE id='dddddddd-0000-7000-8000-000000000001';
  IF d IS NOT NULL THEN RAISE EXCEPTION 'FAIL 5: day_id was not nulled'; END IF;
  IF t IS NULL     THEN RAISE EXCEPTION 'FAIL 5: trip_id was wrongly nulled'; END IF;
  RAISE NOTICE 'PASS 5: ON DELETE SET NULL (day_id) nulled day_id only, trip_id survived';
END $$;

-- ---------- 6. CHECK constraints ----------
DO $$ BEGIN
  BEGIN
    INSERT INTO slots (trip_id, kind, title, position, start_time, end_time)
      VALUES ('aaaaaaaa-0000-7000-8000-000000000001','activity','Time travel','b0','18:00','09:00');
    RAISE EXCEPTION 'FAIL 6a: end_time before start_time was allowed';
  EXCEPTION WHEN check_violation THEN RAISE NOTICE 'PASS 6a: end before start rejected';
  END;
  BEGIN
    INSERT INTO slot_options (slot_id, trip_id, title, place_lat)
      VALUES ('dddddddd-0000-7000-8000-000000000001','aaaaaaaa-0000-7000-8000-000000000001','Half a coordinate', 38.7);
    RAISE EXCEPTION 'FAIL 6b: lat without lng was allowed';
  EXCEPTION WHEN check_violation THEN RAISE NOTICE 'PASS 6b: lat without lng rejected';
  END;
  BEGIN
    INSERT INTO slot_options (slot_id, trip_id, title, place_lat, place_lng)
      VALUES ('dddddddd-0000-7000-8000-000000000001','aaaaaaaa-0000-7000-8000-000000000001','Off world', 200, 10);
    RAISE EXCEPTION 'FAIL 6c: latitude 200 was allowed';
  EXCEPTION WHEN check_violation THEN RAISE NOTICE 'PASS 6c: out-of-range latitude rejected';
  END;
  BEGIN
    INSERT INTO trip_invitations (trip_id, role, token_hash, created_by, expires_at)
      VALUES ('aaaaaaaa-0000-7000-8000-000000000001','owner', decode(repeat('ab',32),'hex'),
              '11111111-1111-7111-8111-111111111111', now() + interval '1 day');
    RAISE EXCEPTION 'FAIL 6d: an invitation granting owner was allowed';
  EXCEPTION WHEN check_violation THEN RAISE NOTICE 'PASS 6d: owner-granting invitation rejected';
  END;
  BEGIN
    INSERT INTO trip_invitations (trip_id, role, token_hash, created_by, expires_at)
      VALUES ('aaaaaaaa-0000-7000-8000-000000000001','viewer', decode('abcd','hex'),
              '11111111-1111-7111-8111-111111111111', now() + interval '1 day');
    RAISE EXCEPTION 'FAIL 6e: a 2-byte token hash was allowed';
  EXCEPTION WHEN check_violation THEN RAISE NOTICE 'PASS 6e: short token hash rejected';
  END;
  -- Slot coverage status is a closed set.
  BEGIN
    INSERT INTO slots (trip_id, kind, title, position, status)
      VALUES ('aaaaaaaa-0000-7000-8000-000000000001','place','Bad status','b1','maybe');
    RAISE EXCEPTION 'FAIL 6f: an unknown slot status was allowed';
  EXCEPTION WHEN check_violation THEN RAISE NOTICE 'PASS 6f: unknown slot status rejected';
  END;
  -- Currency is ISO 4217 alpha-3.
  BEGIN
    UPDATE trips SET base_currency = 'eu' WHERE id='aaaaaaaa-0000-7000-8000-000000000001';
    RAISE EXCEPTION 'FAIL 6g: a malformed currency code was allowed';
  EXCEPTION WHEN check_violation THEN RAISE NOTICE 'PASS 6g: malformed currency rejected';
  END;
  -- Negative money.
  BEGIN
    INSERT INTO slot_options (slot_id, trip_id, title, estimated_cost_minor)
      VALUES ('dddddddd-0000-7000-8000-000000000001','aaaaaaaa-0000-7000-8000-000000000001','Negative cost', -1);
    RAISE EXCEPTION 'FAIL 6h: a negative estimate was allowed';
  EXCEPTION WHEN check_violation THEN RAISE NOTICE 'PASS 6h: negative estimate rejected';
  END;
  -- Comments are append-only but not empty-only.
  BEGIN
    INSERT INTO comments (slot_id, trip_id, body)
      VALUES ('dddddddd-0000-7000-8000-000000000001','aaaaaaaa-0000-7000-8000-000000000001','');
    RAISE EXCEPTION 'FAIL 6i: an empty comment body was allowed';
  EXCEPTION WHEN check_violation THEN RAISE NOTICE 'PASS 6i: empty comment body rejected';
  END;
END $$;

-- ---------- 7. votes ----------
INSERT INTO option_votes (slot_id, trip_id, user_id, option_id) VALUES
  ('dddddddd-0000-7000-8000-000000000001','aaaaaaaa-0000-7000-8000-000000000001',
   '11111111-1111-7111-8111-111111111111','eeeeeeee-0000-7000-8000-000000000001');

DO $$ BEGIN
  -- 7a. One vote per member per slot.
  BEGIN
    INSERT INTO option_votes (slot_id, trip_id, user_id, option_id) VALUES
      ('dddddddd-0000-7000-8000-000000000001','aaaaaaaa-0000-7000-8000-000000000001',
       '11111111-1111-7111-8111-111111111111','eeeeeeee-0000-7000-8000-000000000002');
    RAISE EXCEPTION 'FAIL 7a: a second vote on the same slot was allowed';
  EXCEPTION WHEN unique_violation THEN RAISE NOTICE 'PASS 7a: duplicate vote rejected';
  END;

  -- 7b. Cannot vote for an option belonging to a different slot.
  BEGIN
    INSERT INTO option_votes (slot_id, trip_id, user_id, option_id) VALUES
      ('dddddddd-0000-7000-8000-000000000002','aaaaaaaa-0000-7000-8000-000000000001',
       '11111111-1111-7111-8111-111111111111','eeeeeeee-0000-7000-8000-000000000001');
    RAISE EXCEPTION 'FAIL 7b: voting for another slot''s option was allowed';
  EXCEPTION WHEN foreign_key_violation THEN RAISE NOTICE 'PASS 7b: cross-slot vote rejected';
  END;
END $$;

-- 7c. Changing your mind is a single-row UPDATE, and retraction is a VALUE (option_id NULL),
--     not a deletion. This is the register shape Stage 2 depends on.
UPDATE option_votes SET option_id='eeeeeeee-0000-7000-8000-000000000002', version=version+1
  WHERE slot_id='dddddddd-0000-7000-8000-000000000001' AND user_id='11111111-1111-7111-8111-111111111111';
UPDATE option_votes SET option_id=NULL, version=version+1
  WHERE slot_id='dddddddd-0000-7000-8000-000000000001' AND user_id='11111111-1111-7111-8111-111111111111';
DO $$ DECLARE n int; opt uuid; BEGIN
  SELECT count(*) INTO n FROM option_votes
    WHERE slot_id='dddddddd-0000-7000-8000-000000000001' AND user_id='11111111-1111-7111-8111-111111111111';
  SELECT option_id INTO opt FROM option_votes
    WHERE slot_id='dddddddd-0000-7000-8000-000000000001' AND user_id='11111111-1111-7111-8111-111111111111';
  IF n <> 1 THEN RAISE EXCEPTION 'FAIL 7c: expected exactly 1 vote row, got %', n; END IF;
  IF opt IS NOT NULL THEN RAISE EXCEPTION 'FAIL 7c: retraction did not null the option'; END IF;
  RAISE NOTICE 'PASS 7c: vote is a single-row register; retraction nulls the value';
END $$;

-- ---------- 8. position ordering is byte-wise ----------
-- Sync convergence requires every replica to agree on order. COLLATE "C" makes Postgres
-- ordering bit-identical to Go's string comparison regardless of the database locale.
DO $$ DECLARE got text; BEGIN
  INSERT INTO slots (trip_id, kind, title, position) VALUES
    ('aaaaaaaa-0000-7000-8000-000000000002','place','p1','a1'),
    ('aaaaaaaa-0000-7000-8000-000000000002','place','p2','A0'),
    ('aaaaaaaa-0000-7000-8000-000000000002','place','p3','a0V');
  SELECT string_agg(position, ',' ORDER BY position) INTO got
    FROM slots WHERE trip_id='aaaaaaaa-0000-7000-8000-000000000002' AND position IN ('a1','A0','a0V','a0');
  IF got <> 'A0,a0,a0V,a1' THEN
    RAISE EXCEPTION 'FAIL 8: slots.position order is %, expected A0,a0,a0V,a1', got;
  END IF;
  RAISE NOTICE 'PASS 8: slots.position collation is byte-wise (%)', got;
END $$;

-- ---------- 9. optimistic concurrency ----------
DO $$ DECLARE n int; BEGIN
  UPDATE slots SET title='winner', version=version+1, updated_at=now()
    WHERE id='dddddddd-0000-7000-8000-000000000002' AND version=1;
  GET DIAGNOSTICS n = ROW_COUNT;
  IF n <> 1 THEN RAISE EXCEPTION 'FAIL 9: fresh-version update affected % rows', n; END IF;

  UPDATE slots SET title='loser', version=version+1, updated_at=now()
    WHERE id='dddddddd-0000-7000-8000-000000000002' AND version=1;  -- stale
  GET DIAGNOSTICS n = ROW_COUNT;
  IF n <> 0 THEN RAISE EXCEPTION 'FAIL 9: stale-version update affected % rows', n; END IF;
  RAISE NOTICE 'PASS 9: stale-version update affected 0 rows (maps to HTTP 409)';
END $$;

-- ---------- 10. budget split sum invariant ----------
-- The database-level backstop for the atomic-write position recorded in 000003. The
-- constraint triggers are DEFERRABLE INITIALLY DEFERRED, so they normally fire at COMMIT;
-- SET CONSTRAINTS ALL IMMEDIATE forces them to fire now so this rolled-back script can
-- observe them at all.
INSERT INTO budget_entries (id, trip_id, label, amount_minor, category) VALUES
  ('ffffffff-0000-7000-8000-000000000001','aaaaaaaa-0000-7000-8000-000000000001','Hotel', 1000, 'lodging');

DO $$ BEGIN
  -- 10a. Splits that do not sum to the total are rejected.
  BEGIN
    INSERT INTO budget_splits (budget_entry_id, user_id, amount_minor) VALUES
      ('ffffffff-0000-7000-8000-000000000001','11111111-1111-7111-8111-111111111111', 400),
      ('ffffffff-0000-7000-8000-000000000001','22222222-2222-7222-8222-222222222222', 400);
    SET CONSTRAINTS ALL IMMEDIATE;
    RAISE EXCEPTION 'FAIL 10a: splits summing to 800 against a 1000 total were allowed';
  EXCEPTION WHEN check_violation THEN RAISE NOTICE 'PASS 10a: mismatched split sum rejected';
  END;
  SET CONSTRAINTS ALL DEFERRED;
END $$;

-- 10b. Splits that DO sum to the total are accepted, including the odd remainder unit.
INSERT INTO budget_splits (budget_entry_id, user_id, amount_minor) VALUES
  ('ffffffff-0000-7000-8000-000000000001','11111111-1111-7111-8111-111111111111', 500),
  ('ffffffff-0000-7000-8000-000000000001','22222222-2222-7222-8222-222222222222', 500);
SET CONSTRAINTS ALL IMMEDIATE;
SET CONSTRAINTS ALL DEFERRED;
DO $$ BEGIN RAISE NOTICE 'PASS 10b: splits summing to the total are accepted'; END $$;

DO $$ BEGIN
  -- 10c. Changing the entry total must not orphan existing splits.
  BEGIN
    UPDATE budget_entries SET amount_minor = 2000, version=version+1
      WHERE id='ffffffff-0000-7000-8000-000000000001';
    SET CONSTRAINTS ALL IMMEDIATE;
    RAISE EXCEPTION 'FAIL 10c: retotalling an entry away from its splits was allowed';
  EXCEPTION WHEN check_violation THEN RAISE NOTICE 'PASS 10c: retotal that breaks the sum rejected';
  END;
  SET CONSTRAINTS ALL DEFERRED;

  -- 10d. An entry with NO splits is legitimate — "not split yet" differs from "split wrong".
  BEGIN
    INSERT INTO budget_entries (id, trip_id, label, amount_minor) VALUES
      ('ffffffff-0000-7000-8000-000000000002','aaaaaaaa-0000-7000-8000-000000000001','Unsplit', 700);
    SET CONSTRAINTS ALL IMMEDIATE;
    RAISE NOTICE 'PASS 10d: an unsplit entry is allowed';
  EXCEPTION WHEN check_violation THEN RAISE EXCEPTION 'FAIL 10d: an unsplit entry was rejected';
  END;
  SET CONSTRAINTS ALL DEFERRED;
END $$;

-- ---------- 11. attachments exclusive arc ----------
DO $$ BEGIN
  -- 11a. No owner.
  BEGIN
    INSERT INTO attachments (trip_id, kind, storage_key, status)
      VALUES ('aaaaaaaa-0000-7000-8000-000000000001','file','k/orphan','pending');
    RAISE EXCEPTION 'FAIL 11a: an ownerless attachment was allowed';
  EXCEPTION WHEN check_violation THEN RAISE NOTICE 'PASS 11a: ownerless attachment rejected';
  END;

  -- 11b. Two owners.
  BEGIN
    INSERT INTO attachments (trip_id, slot_id, slot_option_id, kind, storage_key, status)
      VALUES ('aaaaaaaa-0000-7000-8000-000000000001','dddddddd-0000-7000-8000-000000000001',
              'eeeeeeee-0000-7000-8000-000000000001','file','k/two','pending');
    RAISE EXCEPTION 'FAIL 11b: an attachment with two owners was allowed';
  EXCEPTION WHEN check_violation THEN RAISE NOTICE 'PASS 11b: two-owner attachment rejected';
  END;

  -- 11c. A file must carry a storage key and no URL.
  BEGIN
    INSERT INTO attachments (trip_id, slot_option_id, kind, external_url, status)
      VALUES ('aaaaaaaa-0000-7000-8000-000000000001','eeeeeeee-0000-7000-8000-000000000001',
              'file','https://example.test/x','pending');
    RAISE EXCEPTION 'FAIL 11c: a file attachment with a URL and no key was allowed';
  EXCEPTION WHEN check_violation THEN RAISE NOTICE 'PASS 11c: malformed file attachment rejected';
  END;

  -- 11d. A link is immediately ready; there is nothing to upload.
  BEGIN
    INSERT INTO attachments (trip_id, slot_option_id, kind, external_url, status)
      VALUES ('aaaaaaaa-0000-7000-8000-000000000001','eeeeeeee-0000-7000-8000-000000000001',
              'link','https://example.test/booking','pending');
    RAISE EXCEPTION 'FAIL 11d: a pending link attachment was allowed';
  EXCEPTION WHEN check_violation THEN RAISE NOTICE 'PASS 11d: pending link attachment rejected';
  END;
END $$;

-- 11e. Exactly one owner, well formed, is accepted.
INSERT INTO attachments (id, trip_id, slot_option_id, kind, storage_key, content_type, status) VALUES
  ('99999999-0000-7000-8000-000000000001','aaaaaaaa-0000-7000-8000-000000000001',
   'eeeeeeee-0000-7000-8000-000000000001','file','trips/a/opt/e/ticket.png','image/png','pending');
DO $$ BEGIN
  -- 11f. Two rows must not claim the same stored object; deletion would be ambiguous.
  BEGIN
    INSERT INTO attachments (trip_id, slot_id, kind, storage_key, status)
      VALUES ('aaaaaaaa-0000-7000-8000-000000000001','dddddddd-0000-7000-8000-000000000001',
              'file','trips/a/opt/e/ticket.png','pending');
    RAISE EXCEPTION 'FAIL 11f: a duplicate storage key was allowed';
  EXCEPTION WHEN unique_violation THEN RAISE NOTICE 'PASS 11e/f: valid attachment accepted, duplicate object key rejected';
  END;
END $$;

-- ---------- 12. foreign-key index audit ----------
-- Postgres indexes the REFERENCED side of a foreign key automatically and the REFERENCING
-- side never, so an unindexed referencing column makes every parent delete a sequential scan.
-- Crucially, a PARTIAL index cannot back an FK check at all — this audit therefore ignores
-- indexes with a WHERE clause, which is what caught refresh_tokens.replaced_by (a self-FK
-- whose routine cleanup job was quadratic without an index).
--
-- The allowlist is the set of consciously accepted exceptions, justified in the migrations.
-- Anything NOT on the list is a failure, so this stays a real regression guard.
DO $$
DECLARE
  accepted text[] := ARRAY[
    'days.trip_id',                  -- trips are soft-deleted; hard delete is admin/test only
    'trip_members.trip_id',          -- same
    'slots.day_id',                  -- days are soft-deleted (tombstones needed for Stage 2)
    'slots.created_by',              -- users are soft-deleted; erasure is rare and offline
    'slots.status_changed_by',       -- same
    'slot_options.proposed_by',      -- same
    'trip_members.invited_by',       -- same
    'trip_invitations.created_by',   -- same
    'option_votes.user_id',          -- same
    'budget_entries.paid_by',        -- same
    'budget_entries.created_by',     -- same
    'attachments.uploaded_by',       -- same
    'comments.author_id',            -- same
    -- trip_ops is the hottest INSERT path in the system: one row per mutation, forever.
    -- Indexing actor_id would tax every single write to speed up a hard user erasure, which
    -- is a rare, offline, human-initiated operation that may scan. Note trip_ops.trip_id IS
    -- indexed (trip_ops_trip_seq) because trips are hard-deleted on the admin path and this
    -- is the largest child table in the schema.
    'trip_ops.actor_id'
  ];
  unexpected text;
BEGIN
  SELECT string_agg(sig, ', ' ORDER BY sig) INTO unexpected
  FROM (
    SELECT c.conrelid::regclass::text || '.' ||
           (SELECT a.attname FROM pg_attribute a
             WHERE a.attrelid = c.conrelid AND a.attnum = c.conkey[1]) AS sig
    FROM pg_constraint c
    WHERE c.contype = 'f'
      AND c.connamespace = 'public'::regnamespace
      AND NOT EXISTS (
        SELECT 1 FROM pg_index i
        WHERE i.indrelid = c.conrelid
          AND i.indpred IS NULL          -- partial indexes cannot support an FK check
          AND i.indkey[0] = c.conkey[1]  -- indkey is 0-based, conkey is 1-based
      )
  ) q
  WHERE sig <> ALL (accepted);

  IF unexpected IS NOT NULL THEN
    RAISE EXCEPTION 'FAIL 12: foreign keys with no usable index on the referencing column: %. '
      'Either add a non-partial index, or add it to the accepted list with a justification.', unexpected;
  END IF;
  RAISE NOTICE 'PASS 12: every FK is indexed or on the documented exception list';
END $$;

-- ---------- 13. version columns start at 1 and are constrained ----------
DO $$ BEGIN
  BEGIN
    INSERT INTO trips (name, time_zone, version) VALUES ('Bad version', 'UTC', 0);
    RAISE EXCEPTION 'FAIL 13: version 0 was allowed';
  EXCEPTION WHEN check_violation THEN RAISE NOTICE 'PASS 13: non-positive version rejected';
  END;
END $$;

-- ---------- 14. the operation log's total order is enforced by the DATABASE ----------
--
-- Two operations sharing a sequence number within a trip would make "everything since N"
-- ambiguous and break the ordering the entire conflict model rests on. The application takes
-- a row lock to prevent it (D60); this asserts the database would reject it anyway, so a bug
-- in the locking becomes a loud failure rather than silent divergence.
DO $$
DECLARE t uuid; u uuid; e uuid;
BEGIN
  INSERT INTO trips (name, time_zone) VALUES ('Seq trip', 'UTC') RETURNING id INTO t;
  INSERT INTO users (email, password_hash, display_name)
    VALUES ('seq@example.com', 'x', 'Seq') RETURNING id INTO u;
  e := gen_random_uuid();

  INSERT INTO trip_ops (id, trip_id, seq, actor_id, kind, entity_id, payload)
    VALUES (gen_random_uuid(), t, 1, u, 'slot.edit.v1', e, '{}'::jsonb);

  BEGIN
    INSERT INTO trip_ops (id, trip_id, seq, actor_id, kind, entity_id, payload)
      VALUES (gen_random_uuid(), t, 1, u, 'slot.edit.v1', e, '{}'::jsonb);
    RAISE EXCEPTION 'FAIL 14: two operations shared a sequence number in one trip';
  EXCEPTION WHEN unique_violation THEN
    RAISE NOTICE 'PASS 14: duplicate (trip_id, seq) rejected';
  END;

  -- ---------- 15. idempotent replay is enforced by the constraint, not by app code ----------
  --
  -- The engine pre-checks for a known client_op_id, but two concurrent replays can both pass
  -- that check. This index is what actually makes replay-after-a-lost-ack safe.
  BEGIN
    INSERT INTO trip_ops (id, trip_id, seq, actor_id, kind, entity_id, payload, client_op_id)
      VALUES (gen_random_uuid(), t, 2, u, 'slot.edit.v1', e, '{}'::jsonb,
              '11111111-1111-1111-1111-111111111111');
    INSERT INTO trip_ops (id, trip_id, seq, actor_id, kind, entity_id, payload, client_op_id)
      VALUES (gen_random_uuid(), t, 3, u, 'slot.edit.v1', e, '{}'::jsonb,
              '11111111-1111-1111-1111-111111111111');
    RAISE EXCEPTION 'FAIL 15: the same client op id was committed twice';
  EXCEPTION WHEN unique_violation THEN
    RAISE NOTICE 'PASS 15: duplicate client_op_id rejected';
  END;

  -- The index is PARTIAL, so REST-originated rows (no client op id) must not collide with
  -- each other. If this ever starts failing, the index stopped being partial and every REST
  -- write after the first would be rejected.
  INSERT INTO trip_ops (id, trip_id, seq, actor_id, kind, entity_id, payload)
    VALUES (gen_random_uuid(), t, 4, u, 'slot.edit.v1', e, '{}'::jsonb);
  INSERT INTO trip_ops (id, trip_id, seq, actor_id, kind, entity_id, payload)
    VALUES (gen_random_uuid(), t, 5, u, 'slot.edit.v1', e, '{}'::jsonb);
  RAISE NOTICE 'PASS 15b: multiple operations with a NULL client_op_id coexist';

  -- ---------- 16. the sequence is gapless because it is a column, not a SEQUENCE ----------
  --
  -- A Postgres SEQUENCE is non-transactional: a rolled-back transaction burns its number.
  -- This asserts the counter rolls back with everything else, which is what lets a client
  -- treat seq contiguity as a completeness check rather than merely a hint (D61).
  DECLARE before_seq bigint; after_seq bigint;
  BEGIN
    SELECT op_seq INTO before_seq FROM trips WHERE id = t;
    BEGIN
      UPDATE trips SET op_seq = op_seq + 1 WHERE id = t;
      RAISE EXCEPTION 'deliberate rollback';
    EXCEPTION WHEN raise_exception THEN NULL;
    END;
    SELECT op_seq INTO after_seq FROM trips WHERE id = t;
    IF after_seq <> before_seq THEN
      RAISE EXCEPTION 'FAIL 16: op_seq advanced across a rolled-back savepoint (% -> %)',
        before_seq, after_seq;
    END IF;
    RAISE NOTICE 'PASS 16: op_seq rolls back, so the log stays gapless';
  END;

  -- ---------- 17. seq is positive ----------
  BEGIN
    INSERT INTO trip_ops (id, trip_id, seq, actor_id, kind, entity_id, payload)
      VALUES (gen_random_uuid(), t, 0, u, 'slot.edit.v1', e, '{}'::jsonb);
    RAISE EXCEPTION 'FAIL 17: seq 0 was allowed';
  EXCEPTION WHEN check_violation THEN
    RAISE NOTICE 'PASS 17: non-positive seq rejected';
  END;
END $$;

-- ---------- 18. the operation log is append-only in practice ----------
--
-- "Immutable" is enforced by no UPDATE or DELETE query existing against trip_ops, which is a
-- property of the Go code rather than the schema. What the database CAN assert is that
-- nothing has quietly added a trigger or rule that rewrites history behind the application's
-- back — the way an audit table's integrity is usually lost.
DO $$
DECLARE n int;
BEGIN
  SELECT count(*) INTO n
  FROM pg_trigger
  WHERE tgrelid = 'trip_ops'::regclass AND NOT tgisinternal;
  IF n > 0 THEN
    RAISE EXCEPTION 'FAIL 18: trip_ops has % user trigger(s); the log must not be rewritten', n;
  END IF;

  SELECT count(*) INTO n FROM pg_rules WHERE tablename = 'trip_ops';
  IF n > 0 THEN
    RAISE EXCEPTION 'FAIL 18: trip_ops has % rewrite rule(s)', n;
  END IF;
  RAISE NOTICE 'PASS 18: no triggers or rules rewrite the operation log';
END $$;

ROLLBACK;
