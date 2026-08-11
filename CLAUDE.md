# Junto — Collaborative Planning Engine

A reusable collaborative-planning core (timelines, collaborative editing, comments, voting,
presence, permissions, notifications, synchronization) with **Trips** as the first domain module
built on top of it.

**Scope discipline:** the core is generalized only as far as the Trips module actually requires.
The core/domain seam is designed in; a second module is *not* built. If asked, the honest answer is
"the boundary was designed in, a second module wasn't built."

---

## Engineering priority order

When two goals conflict, the earlier one wins:

`correctness > data consistency > real-time sync > backend architecture > testing > security >
performance > observability > developer experience > UI polish`

UI should look intentional, not like a wireframe — but it gets the least engineering time of
anything on this list. Never polish UI while a shortcut sits in the sync engine.

---

## Stack (non-negotiable — ask before substituting)

| Layer | Choice |
|---|---|
| Frontend | Next.js (App Router), TypeScript |
| Backend | Go — REST (Chi) + WebSocket hub |
| DB | PostgreSQL **16** via pgx v5 + sqlc (no ORM) |
| Pub/Sub | Redis (Stage 2 — multi-instance WS fan-out) |
| Auth | JWT access tokens + refresh-token rotation with reuse detection |
| Deploy | Docker Compose locally; single VPS / Fly.io for the demo |

Postgres 16 is a hard floor: `ON DELETE SET NULL (column_list)` (PG15+) is what makes the
`slots → days` and `slots → slot_options` (the `selected_option_id` circular FK) composite
FKs work without violating `trip_id NOT NULL`.

---

## Architecture rules (enforced, not aspirational)

```
cmd/            entrypoints
internal/
  domain/       core types + ports (interfaces). stdlib only.
  repository/   Postgres implementations of domain ports
  service/      business logic; depends only on domain interfaces
  transport/    HTTP handlers, WebSocket handlers — call services
  middleware/   auth, logging, rate limiting
pkg/            shared, dependency-free utilities
configs/        env-based configuration
migrations/     embedded SQL migrations
tests/
```

1. **`internal/domain` imports only the standard library**, plus an explicit allowlist
   (`github.com/google/uuid`, `junto/pkg/*`). Enforced by `internal/domain/arch_test.go`, so CI
   fails on violation rather than relying on discipline.
2. **`internal/service` must never import a sqlc-generated type.** The repository layer maps
   sqlc structs ↔ domain structs. This is real, boring boilerplate; it is the price of the
   "domain logic separated from transport" claim. Pay it knowingly.
3. **Every mutation goes through a service method.** Handlers do: parse → authorize → call service
   → render. Zero business logic, zero SQL in `transport/`.
4. Context propagation everywhere. Structured logging via `log/slog`. Config from environment.
   Graceful shutdown. Dependency injection by constructor — no globals, no `init()` wiring.

### 🔒 Why rule 3 is the most important line in this file

Stage 2 claims an immutable operation log that powers reconnect/resync. If REST writes bypass
the log and only the WebSocket path appends to it, a reconnecting client asking "everything since
seq N" silently misses every REST-originated change, and resync degrades into a full re-fetch.
Routing *all* writes through services means Stage 2 adds op-log writes in exactly one place and
both transports stay consistent by construction.

---

## Claims discipline

The resume bullet this project must make **literally** true:

> "Built a real-time collaborative planning engine with a transport-agnostic WebSocket-based sync
> engine implementing [OT/CRDT] conflict resolution for concurrent multi-user edits, Redis pub/sub
> for horizontal scaling, and Clean Architecture separating domain logic from transport."

| Clause | What makes it true | Status |
|---|---|---|
| transport-agnostic sync engine | `internal/syncengine` imports no websocket/net-http/redis package; enforced by `TestSyncEngineIsTransportAgnostic` | ✅ verified to fail on planted `net/http` and `github.com/coder/websocket` imports |
| OT/CRDT conflict resolution | design doc reviewed + approved before code (`docs/stage2-sync-design.md`, D59–D72); convergence test passes | ✅ CRDT — field-level LWW registers under a server total order. **Field-level, not character-level**: concurrent edits to the same `notes` are whole-field LWW |
| concurrent multi-user edits | automated 2-client conflicting-write test asserting identical final state | ✅ `tests/convergence_api_test.go` — 11 scenarios, two real WebSocket clients, barrier-synchronised. Every one ends on the four-way equality `fold(trip_ops) == database == client A == client B`. Green under `-race` |
| Redis pub/sub for horizontal scaling | **two-instance** test: publish on instance A, subscriber on instance B receives | ✅ `tests/multi_instance_api_test.go` — two fully wired instances, two httptest servers, one Postgres, one Redis container. Verified to fail when instance B's transport is replaced with `NoopTransport`. Both instances use the Redis ticket store, so a ticket minted on A redeems on B (and is still single-use across the pair, via `GETDEL`) |
| Clean Architecture | arch tests on every layer's import graph | ✅ all 10 layers enforced against real code — `domain`, `service`, `repository`, `syncengine`, `transport`, `middleware`, `security`, `email`, `storage`, `pkg`. Verified to fail on planted `service→repository`, `transport→repository` and `pkg→internal` imports |

One instance plus a Redis client that happens to compile does **not** earn the horizontal-scaling
clause. Nothing gets ticked here until the corresponding test exists and passes.

### ✅ Final confirmation, clause by clause (Stage 2 close-out, 2026-08-08)

The bullet was re-read against the code one clause at a time, at the end of Stage 2, on the
assumption that a claim which drifted would drift quietly. Each clause below names the single
test that would fail first if it stopped being true, and the planted break that was used to
confirm that test is not vacuous.

| Clause, as written | Verdict | The test that falsifies it | Planted break it was checked against |
|---|---|---|---|
| "real-time collaborative planning engine" | ✅ true | `tests/convergence_api_test.go`, `tests/ledger_api_test.go` | — |
| "transport-agnostic … sync engine" | ✅ true | `tests/arch_test.go::TestSyncEngineIsTransportAgnostic` — and note it does NOT exclude test files, so a double needing a real socket would fail it too | `net/http` and `github.com/coder/websocket` imports planted under `internal/syncengine` |
| "WebSocket-based" | ✅ true | `tests/ws_api_test.go` + every convergence and resync test drives real sockets through the real D10 ticket handshake | — |
| "**CRDT** conflict resolution" (bracket resolved) | ✅ true, **with two stated limits** | `tests/convergence_api_test.go` — 11 scenarios ending on `fold(trip_ops) == database == client A == client B` | `RequiresTotalMask` stubbed false; op-log writes removed from budget/attachment paths |
| "for concurrent multi-user edits" | ✅ true | Two real clients released from one barrier so their writes race at the sequencer; the test also asserts neither client received an error frame, because the easiest way to pass vacuously is for both operations to have been rejected | — |
| "Redis pub/sub for horizontal scaling" | ✅ true | `tests/multi_instance_api_test.go` — two fully wired instances, one Postgres, one Redis | instance B's `OpTransport` replaced with `NoopTransport`; reconcile interval pushed to 5 minutes so the log-repair path cannot cover for it |
| "Clean Architecture separating domain logic from transport" | ✅ true | `tests/arch_test.go` across 10 layers + `internal/domain/arch_test.go` | planted `service→repository`, `transport→repository`, `pkg→internal`, and `net/http`/`pgx` in `domain` |

**The two limits on the CRDT clause, restated so they are never accidentally dropped when the
bullet is quoted:**

1. **Sequencer-based, not multi-master.** Convergence rests on a single authority (Postgres)
   producing a per-trip total order. It is not a design that survives partitioned writes.
2. **Field-level, not character-level.** Two members typing into the same `notes` do not get
   their prose interleaved; the later operation wins the whole field. Anyone hearing "CRDT" and
   picturing Google Docs is picturing something this does not do, so say it before they ask.

A third limit, added in Slice 3 and equally load-bearing: **two entity classes deliberately do
not merge at all.** The budget is atomic (whole entry plus its complete split set, or nothing)
and attachments are broadcast-only. Both are conscious choices about conflict grain rather than
gaps — money has a cross-field invariant that text does not, and an upload is an event rather
than a value — but "CRDT conflict resolution" should not be heard as "everything merges".

**And "passes" is not enough on its own.** The first version of the two-instance test passed with
instance B's peer transport replaced by a no-op, because the broker's log-repair path (D75) was
quietly covering for a completely dead Redis path — the room reconciled against `trips.op_seq`
inside the test's own timeout and delivered the operation from the log. The test was measuring
robustness and reporting it as fan-out. Every test backing a claim in this table is now checked
by planting the failure it is supposed to catch, and the isolation that makes it meaningful
(a reconcile interval far beyond the test's patience) is written down in the test rather than
assumed.

### 🔬 Standing principle: a test that passes without the thing it tests

Generalised from that finding and **applied by default from Slice 3 onward**, not only to the
test that produced it:

> **Every concurrency, multi-instance, or claim-backing test must be run once with the
> behaviour it claims to verify deliberately broken, and must be seen to fail.** A test that
> still passes with the mechanism removed is measuring something else, and the fact that it is
> green is worse than having no test — it is an assertion of correctness backed by nothing.

Two rules follow, and both have already caught real problems:

1. **Name what the plant was, in the test's own doc comment.** Not in a commit message, which
   nobody reads while deciding whether to trust a test. Slice 3's claim-backing tests carry the
   sentence "verified against a planted break: …" inline; the Slice 1 and 2 tests record theirs
   in *Verifying the guarantees* below, which is where they were written before this became a
   standing rule. New tests should put it in both.
2. **When a plant does NOT break a test, that is a finding, not a relief.** It means the test's
   real coverage is narrower than its name. Either tighten it or write down what it does not
   cover.

Slice 3 found two of these, and both were the ordinary kind rather than the exotic kind:

- `TestSaveOutsideATransactionIsRefused` passed with its guard removed. It built its fixtures in
  an uncommitted test transaction, so the write it expected to be refused was in fact refused by
  a foreign-key violation from a connection that could not see the trip. `err != nil` was
  satisfied by the wrong error entirely. Fixed by committing the fixtures and asserting on the
  specific sentinel.
- `TestConcurrentBudgetEditsLeaveOneWinnerAndOneConflict` stayed green when merge semantics were
  planted in the budget service, because both racers in it supply a version. That half of D85 is
  covered by a different test, and the boundary between the two is now written into both.

The cheapest version of this discipline is the one to keep: plant the break, watch it fail,
restore, and write down which break it was.

**Stage 2 scope, locked in advance.** The domain has four conflict classes, and they are
*not* equally important to the claim:

| Entity | Class | Priority | Status |
|---|---|---|---|
| Slots, slot options | field-level merge | **The convergence test lives here. Never cut.** | ✅ Slice 1 |
| Votes | LWW register, one row per (slot, user) | Trivial, and the cleanest proof in the system | ✅ Slice 1 |
| Budget entry + splits | atomic operation, not field-mergeable | Coarse by design; easy | ✅ Slice 3 |
| Attachments | broadcast only, no merge | Trivial | ✅ Slice 3 |

If Stage 2 runs short on time, **cut op-vocabulary breadth, never the convergence test on
slots + options + votes.** That test is what earns the resume bullet; everything else is
surface area. This is restated in the Stage 2 design doc, not only here.

**"Coarse by design" and "trivial" turned out to be accurate about the RISK, not about the
work.** Both landed in Slice 3, and the thing worth carrying forward is that in each case the
danger was the same: a conflict grain that is correct in the design and unenforced in the code.
Budget is atomic only if a partial write cannot be *expressed* — so `budget.set.v1` requires a
total field mask (D83), an edit requires an explicit version (D85), and a deferred trigger
refuses an unbalanced ledger from any writer at all. Attachments merge nothing, which made it
tempting to leave them out of the operation log entirely; that would have left an offline member
permanently unaware of every photo added while they were away, with every existing test still
green (D84).

---

## Non-functional targets — MEASURED (Stage 2 Slice 4)

Every number below comes from `tests/nfr_test.go`, running the real stack: real Postgres in a
container, real HTTP, real WebSockets, real sequencer and broker. Nothing is stubbed.

**Read the caveat before quoting any of them.** These are single-machine figures — application,
database and clients on one host, no network between the tiers. What they establish is the
*shape* of the system's performance and an order of magnitude per target, not a production
capacity statement. Quoting "600 ops/sec" as a capacity claim would be exactly the kind of
unearned number this file exists to prevent.

| Target | Measured | How |
|---|---|---|
| **Single-trip write-throughput ceiling** | **≈155 ops/sec** | 8 concurrent WebSocket writers, all on ONE trip, measured to commit |
| Same writers spread one-per-trip | **≈600 ops/sec** (**≈3.9×**) | identical load, 8 trips — the row lock is the only difference |
| **Message latency, 100 connections on one trip** | **p50 13ms · p95 19–32ms · p99 23–49ms** | writer → different member's socket, end to end |
| Latency degradation, 2 → 100 connections | **p99 12ms → 30ms (≈2.5×)** | same trip, same writer, only the audience changes |
| **Reconnect + resync** (target: under 2s) | **≈40ms** for 200 missed operations | handshake to fully folded, replay not re-fetch |
| Zero data corruption under concurrent load | non-negotiable, holds | `tests/convergence_api_test.go`, green under `-race` |

**The ratio is the finding, not the throughput.** ≈155 ops/sec on one trip against ≈600 for the
same writers spread across eight is the per-trip row lock doing precisely what D60 designed it
to do: `op_seq` is allocated by an `UPDATE … RETURNING` that takes the trip row before any
entity read, so writers *within* a trip serialize and writers in *different* trips do not
contend at all. That is what produces the clean per-room total order the sync engine folds.

So "≥100 concurrent connections" is not one number, and the benchmark states its trip
distribution for that reason. A hundred mostly-reading connections on one trip cost ≈2.5× the
p99 of two — more work, same order of magnitude, which is what "without degradation" can
honestly mean. A hundred *simultaneous writers to one trip* would queue behind the lock, and
that is a ceiling rather than a defect.

Serializing writers within a single trip is the *intent*. It is only a problem if it goes
unstated — which is why it now has a number attached instead of a TBD.

`TestSingleTripWriteThroughputCeiling` asserts the *shape* rather than the magnitude: writes
spread across trips must outpace writes funnelled through one. A tight assertion on absolute
throughput would measure whichever machine CI happened to schedule; this fails only if the row
lock stops serializing, which would break the total order everything else rests on.

---

## Decisions log

Stage 1 design, approved 2026-08-07:

| # | Decision | Rationale |
|---|---|---|
| D1 | All writes through services; op log added in one place in Stage 2 | Otherwise resync is cosmetic (see rule 3) |
| D2 | Fractional indexing (`position text`) for ordering, ordered by `(position, id)` | A move is 1 row / 1 op instead of O(N). Neutral between OT and CRDT — does not pre-decide Stage 2 |
| D3 | Soft delete (`deleted_at`) on `days`, `slots` and `slot_options` is load-bearing, not stylistic | Concurrent delete-vs-edit cannot converge on a hard-deleted row; the op log cannot reference a dead FK |
| D4 | Application-generated **UUIDv7** IDs, never `DEFAULT gen_random_uuid()` | Optimistic UI and offline edits require naming an entity before the server sees it. v7 also gives B-tree insert locality |
| D5 | No `trips.owner_id`; owner is the `trip_members` row with `role='owner'` | Two sources of truth for one fact will drift. Enforced by a partial unique index, not app code |
| D6 | Place data as discrete columns, not JSONB | Stage 2 does field-level conflict resolution; an opaque blob silently coarsens conflict granularity |
| D7 | `start_time`/`end_time` are `time`, not `timestamptz` | Day supplies the date, trip supplies the zone. Absolute instants break on trip-date shifts and DST |
| D8 | Capability-shaped checks (`actor.Can(CapEditSlots)`) backed by roles today | Zero-cost forward compatibility: the depth-add becomes one function + one column, not a handler audit |
| D9 | Refresh tokens are opaque random bytes, not JWTs | Revocation needs a DB lookup anyway; a JWT adds forgery surface and buys nothing |
| D10 | WS auth via single-use 30s ticket endpoint | Browsers cannot set headers on a WS handshake; cookies drag in CSRF, query-string JWTs leak into logs. Designed Stage 1, implemented Stage 2 |
| D11 | `comments`, `trips.op_seq` deferred to their stages | Schema with no code behind it reads as more complete than it is. Migrations are additive. (`item_votes`, also deferred here originally, was superseded by `option_votes` — see D42) |
| D12 | Keyset (cursor) pagination, never `OFFSET` | Under concurrent inserts — the entire premise of this app — offset pagination duplicates and skips rows |
| D13 | `github.com/google/uuid` allowlisted in `domain` | A leaf value-type library with no I/O and no transitive deps — same category as stdlib. Explicitly allowlisted so it is intentional, not accidental drift |
| D14 | Transactions via `domain.TxManager` carrying the tx on `context` | Keeps `pgx` out of service signatures. Accepted cost: the tx is an implicit context value |
| D15 | Index FK referencing columns only where the parent is hard-deleted routinely, or the column is queried. A **partial index cannot back an FK check** | "Index every FK" is the usual advice but it is write amplification on `slots`, the hottest write path. Exceptions are listed and justified in `migrations/000002`/`000003`, and `tests/schema_verify.sql` fails on any *undocumented* one |
| D16 | `TimeOfDay` is its own type, not `time.Time` | A slot's time is "09:30 on this day in the trip's zone", not an instant. A `time.Time` invites `.UTC()` and cross-day comparison, both silently wrong |
| D17 | `time/tzdata` embedded in the binary | Otherwise timezone validation depends on host tz files, absent from scratch containers and stock Windows — it would pass locally and fail in production |
| D18 | Nullable only where NULL differs from the zero value | `place_name` is `NOT NULL DEFAULT ''` (absent == empty); `place_lat` is nullable ((0,0) is a real location). Three-valued logic with no third meaning is pure bug surface |
| D19 | Malformed env values abort startup; they never fall back to the default | `ACCESS_TOKEN_TTL=15` (no unit) would otherwise silently run at the 15-minute default and the operator could not tell their change had no effect. `Validate()` cannot catch it — the default is a legal value |

Repository slice, approved 2026-08-07:

| # | Decision | Rationale |
|---|---|---|
| D20 | `TxManager` nests via **SAVEPOINT**, not a second transaction | Postgres aborts a whole transaction on any failed statement (SQLSTATE 25P02). A service that catches a duplicate-key error and takes another path is holding a *dead* transaction unless the risky statement ran in a savepoint. Pinned by `TestConstraintViolationInNestedTxIsRecoverable` |
| D21 | Invitation redemption is **one UPDATE** carrying every validity condition | Read-then-write is a textbook race: two requests both read `use_count = 0`, both succeed, two people join on a one-use link. Callers must NOT pre-check. Proven by 12 racing goroutines in `TestConcurrentInvitationRedemptionIsAtomic` |
| D22 | A zero-row write triggers a follow-up existence query (`resolveWriteMiss`) | "Row absent" and "row at a different version" both produce zero rows. Collapsing them returns 404 for a concurrent edit, and a client that retries a 404 abandons data that is still there. The extra query runs only on the failure path |
| D23 | Postgres errors map to domain errors by **constraint name**, never by message text | Message text is a version- and locale-dependent implementation detail. Constraint names are ours. An unmapped constraint degrades to a generic conflict rather than leaking database internals into an API response |
| D24 | Generated sqlc code is committed; CI runs `sqlc generate` and fails on a diff | The build needs no code generator, but only works if generation actually happens when SQL changes |
| D25 | `sqlc.yaml` globs `migrations/*.up.sql` | Listing files explicitly means a new migration can be silently omitted, generating against a stale schema. Down-migrations must be excluded or sqlc sees the drops |
| D26 | Repository tests use testcontainers + a per-test transaction rolled back at cleanup | Mocking the DB would test nothing: the interesting behaviour *is* the constraints, partial indexes, composite FK and row-level concurrency. Rollback isolation avoids truncation between tests and ordering dependencies |
| D27 | Argon2id hashes are PHC strings carrying their own parameters | Lets cost be raised later without invalidating existing hashes; `Verify` reports `needsRehash` so login transparently upgrades — the only moment the plaintext is available |
| D28 | `internal/security` is its own adapter package, not part of `repository` | "How a password is hashed" and "how a row is stored" change for different reasons |

Auth + transport slice, approved 2026-08-07:

| # | Decision | Rationale |
|---|---|---|
| D29 | Signup does **not** auto-login; login requires a verified email | An address that was never proven cannot be recovered by password reset, and an unrecoverable account is worse than an unusable one. The stricter of two defensible options |
| D30 | Access token in the response body (client holds it in memory); refresh token in an `HttpOnly; SameSite=Lax` cookie path-scoped to `/api/v1/auth` | HttpOnly means an XSS bug cannot exfiltrate the long-lived credential. SameSite=Lax is the CSRF defence for refresh. Path scoping keeps it off every other call |
| D31 | The access token is read from the `Authorization` header ONLY — never a cookie or query param | Browsers attach cookies to cross-site requests automatically but never an Authorization header, so a state-changing request cannot be forged. Also keeps credentials out of access logs |
| D32 | Every credential failure collapses to one identical 401 | Distinguishing unknown-account / wrong-password / expired / revoked / forged turns each endpoint into an oracle. Logged in full internally, opaque externally |
| D33 | Login runs a dummy Argon2id verify when the account does not exist | Otherwise the not-found path skips ~55ms of work and registered addresses are distinguishable by latency alone |
| D34 | `ErrEmailNotVerified` is the ONE credential failure that is distinguished (403) | The client must be able to offer "resend link" and cannot guess when. Accepted knowingly: it confirms an address is registered, which is why the rate limiter matters |
| D35 | Rate limiting on auth endpoints is a requirement, not a depth-add | The password policy is length-only by design; the documented compensating controls are Argon2id **and** rate limiting. Shipping without it would make that stated reasoning false |
| D36 | Rate limits are configurable, not constants | Ops must tune without recompiling — and a production-strict limiter throttles the test suite itself, since every test request shares one source IP |
| D37 | Wire types are declared separately from domain types in handlers | Serialising domain structs directly means adding an internal field silently publishes it. This is how password hashes reach API responses |
| D38 | Coverage measured with `-coverpkg` | Without it, code exercised by a test in another package counts as zero — `middleware` and `transport/http` read 0% while their integration tests passed |

Slots/options/votes reshape, approved 2026-08-07 (replaces the flat item model):

| # | Decision | Rationale |
|---|---|---|
| D39 | **Slot** (a decision) + **slot_options** (candidates) replaces the flat `items` table | "I don't like this hotel, here's an alternative" becomes another candidate under the same decision, not a competing entry someone reconciles by hand. The simplest itinerary entry is a slot with one option |
| D40 | Time and position live on the **slot**; place data lives on the **option** | The itinerary renders by time, so an undecided slot with three candidate hotels still needs a schedule position. Each candidate has its own address |
| D41 | `slots.selected_option_id` is **stored**, not derived from the vote tally | Groups routinely override their own vote ("we voted A, but B had availability"). A computed winner cannot represent that |
| D42 | One vote per (slot, user); retraction sets `option_id = NULL` rather than deleting | The one deliberate break from the tombstone convention. It makes a vote a last-writer-wins **register** — no tombstone reconciliation, no delete-vs-edit case, one verb (`set_vote`). The simplest convergent entity in the system |
| D43 | Money is `bigint` **minor units**; one `trips.base_currency`, FX deferred | Binary floating point cannot represent 0.10, and the error compounds until splits stop summing to the total. A per-row currency without FX produces silently meaningless sums |
| D44 | Budget is an **atomic unit**, not field-mergeable — total + complete split set, applied whole | Each field merge is locally plausible and jointly wrong (A sets total 1000, B sets their split 600). Detection is easy; repair means choosing whose edit to discard, which is what merging exists to avoid. Coarser grain than the itinerary because money has a cross-field invariant that text does not |
| D45 | Budget splits carry explicit **amounts**, not percentages; a deferred constraint trigger enforces the sum | 1000 across three is 333/333/334 and the extra unit must belong to someone deterministically. The trigger makes a violating state impossible to commit regardless of what the sync engine does |
| D46 | Attachments are **broadcast-only** — no conflict resolution, no version column | An upload is a one-shot event, not a mergeable field. Two concurrent uploads simply both exist. Attachments are immutable once ready: you replace one, you do not edit it |
| D47 | Attachment ownership is an **exclusive arc** (three real FKs + `num_nonnulls(...) = 1`), not polymorphic | Same reasoning as `trip_members`: Postgres cannot enforce a polymorphic reference, and referential integrity is worth more than two saved columns |
| D48 | Two-phase upload: presigned PUT direct to storage, then server-side `Stat` to confirm | Proxying files through the API wastes memory and bandwidth. The cost is that a presigned PUT cannot enforce a size limit, so `pending → ready` plus a sweeper for abandoned uploads |
| D49 | Slot coverage (`planned`/`covered`/`skipped`) is a **field** with `_at`/`_by`, not a table | Single-valued with no cardinality; a table would be a join on every render. The `_at`/`_by` keep the attribution a bare enum throws away — same shape as `users.email_verified_at` |
| D50 | `minio-go/v7` over `aws-sdk-go-v2` | Presigning behaves identically against MinIO, S3 and R2, and it is a far smaller dependency. Invisible above `internal/storage` by construction — swapping SDKs touches one file |
| D51 | `sqlc generate` deletes its output directory first | sqlc does not remove generated files whose query file has gone. A stale committed `.sql.go` would survive a plain `git diff` check in CI |

Planning CRUD slice, approved 2026-08-07:

| # | Decision | Rationale |
|---|---|---|
| D52 | A shared `authz` helper (`internal/service/authz.go`) resolves `Actor` and checks one capability per call, embedded into every planning service | One implementation of "how does a caller's membership become an authorization decision" rather than six copies drifting independently |
| D53 | Non-member access to a trip or its nested resources returns `ErrNotFound`, never `ErrForbidden` | Confirming a trip exists to someone with no access to it is itself a disclosure — the same reasoning already applied to session revocation (D-series, auth slice) |
| D54 | Every nested-resource method re-checks that the loaded resource's `TripID` matches the URL's trip id (`checkTrip`) | The capability check authorizes against the URL's trip; a slot/day/option *id* in the URL can still belong to a different trip. Without this a member of trip A who can guess a trip-B resource id acts on it authorized against the wrong trip |
| D55 | Ownership cannot be granted or revoked through `UpdateRole`/`RemoveMember`; both reject the owner as a target and reject `role: owner` as a value | There is no ownership-transfer flow yet (`CapTransferOwnership` is declared, unimplemented). Leaving the generic paths able to touch ownership would make that boundary fiction rather than fact |
| D56 | Deleting a slot's selected option clears `selected_option_id` back to `nil` in the same transaction as the delete, and does **not** auto-promote another candidate | Un-resolving is the only answer that does not substitute the service's judgment for the group's. Fixes a gap flagged and deliberately left open in the previous slice |
| D57 | `CapInviteMembers` is granted to editors, but `CapManageMembers` (role changes, removal) and `CapDeleteTrip` stay owner-only | Inviting a collaborator is itinerary-planning work; changing who has what power over the trip, or destroying it, is not |
| D58 | Invitation redemption re-checks the invitee's email against a targeted invite's address, inside the same flow that calls the atomic `IncrementUseCount` | Without it, a forwarded or leaked email-invite link lets anyone with the raw token join under the addressed role — "targeted" would mean nothing |

Stage 2 sync design, approved 2026-08-08. Full reasoning in
[`docs/stage2-sync-design.md`](docs/stage2-sync-design.md); this table is the summary that
must survive a context reset:

| # | Decision | Rationale |
|---|---|---|
| D59 | **CRDT** — a field-level LWW-register map under a server-assigned per-trip total order. Not OT | OT's unique payoff is transforming *index-relative* sequence operations, which D2's fractional indexing designed away in Stage 1. For scalar field assignment `transform(set a, set b)` degenerates to LWW with two more functions to get subtly wrong. Sequencer-based, not multi-master; merge is field-level, never character-level |
| D60 | `op_seq` is allocated as the **first statement** of every write transaction | The `UPDATE trips SET op_seq = op_seq+1 RETURNING op_seq` takes the trip row lock *before* any entity read, which is what makes read-modify-write safe and field-level merge free — and is why the whole Stage 1 repository layer is reused unchanged. Allocating it last would permit lost updates at field granularity while the log still looked perfectly ordered. This is the subtlest way the design can be broken |
| D61 | `trips.op_seq` is a counter column, never a Postgres `SEQUENCE` | A sequence is non-transactional: a rolled-back transaction burns its number and gaps the log. A column rolls back with everything else, so the log is **gapless** and `seq` contiguity is a usable completeness check rather than a hint |
| D62 | The op log stores **resolved effects, never derivations** | `slot.create {after_slot_id: S}` replayed months later, against a list whose neighbours have all changed, derives a *different* position key — and convergence dies silently. The log stores `position: "a1V"`, not the anchor. Same rule for every server-side derivation |
| D63 | One client intent may commit as **multiple ops**, linked by `cause_op_id` | Makes a log entry uniformly *one entity, one set of field changes*, so replay is a pure fold and clients hold zero cascade logic — they cannot drift from the server's version of a rule like D56. Also makes `fold(log) == database state` a testable assertion rather than a hope |
| D64 | The field mask is **explicit on the wire** (`fields: [...]`), never inferred from JSON key presence | "Untouched" and "explicitly cleared" are different operations with different meanings on every nullable column. Inferring them is the D6 failure mode one level up — and the log outlives the decoder that wrote it |
| D65 | Op `Kind` carries a version suffix (`slot.edit.v1`) | The log is immutable and will outlive its payload shapes. One character of insurance against a class of bug that is unfixable after the fact |
| D66 | **No per-field timestamps** | The sequencer already gives a total order and the server always applies in seq order by construction, so they would be write amplification on `slots` — the hottest write path — with no reader. Trigger for revisiting, stated precisely: removal of the single sequencer (true multi-master). Redis fan-out does **not** trigger it; the seq is still assigned in Postgres |
| D67 | `trip_ops.payload` is `jsonb`, deliberately inverting D6 | D6's reasoning applies to *merged entity* data, where field-level granularity is the point. The log is append-only, never merged, never queried by field, and polymorphic across op kinds. The reasoning that makes columns right for `slot_options` makes JSONB right here |
| D68 | Conflict-resolution logic lives in `internal/domain`; rooms/dispatch in `internal/syncengine`; sockets only in `internal/transport/ws` | Puts the merge functions inside the strictest arch test in the repo — they literally cannot reach a socket. The service→domain-port direction for the op log breaks the cycle that would otherwise force the engine into the service layer |
| D69 | `expectedVersion` becomes `*int`: **conflict semantics are a property of the request, not the transport** | nil = merge semantics (sync), a value = 409 on mismatch (REST). Forking into sync/REST service methods would duplicate business logic and recreate exactly the two-write-path drift Rule 3 and D1 exist to prevent |
| D70 | The broadcast happens **after** commit; the log is the delivery guarantee, the broadcast only an accelerator | Publishing inside the transaction lets a rolled-back write emit a phantom op no client can ever reconcile — strictly worse than a dropped broadcast, which the log already recovers from on reconnect |
| D71 | Votes for **soft-deleted options do not count** in the tally, filtered at the query level | Consistent with every other soft-delete visibility rule in the codebase. Filtering in SQL rather than in Go keeps one definition of "visible" instead of two that drift. The vote *rows* are still retained, so undeleting an option restores the group's expressed preferences |
| D72 | The op log covers the **itinerary** (days, slots, options, votes). Trip metadata and membership are **not** logged in Slice 1 | Deliberate and bounded, not an oversight: a resyncing client re-fetches those two. Days ARE logged despite being a proposed cut candidate — omitting them would leave a REST-originated day change invisible to resync, which is the precise hole Rule 3 exists to close |
| D73 | A WebSocket's **session** is verified at connect only. Revoking a session (logout, password reset, admin revoke) does **not** close sockets already open. Bounded by a 12-hour maximum connection lifetime; closing it properly is Slice 2 work | Spelled out below rather than compressed, because the difference between what IS and IS NOT enforced is the whole point |

**D73, stated precisely.** The ticket proves a live session at the handshake, and nothing
re-checks it afterwards. What that does and does not mean:

- **Still enforced, on every frame:** membership and capability. `Subscribe` goes through
  `TripService.Get` and every mutation through the service layer's `authz` helper, so removing
  a member or demoting them to viewer takes effect immediately on an already-open socket.
- **Not enforced:** session liveness. A user who logs out — or whose session is revoked by a
  password reset — keeps a working socket for trips they are still a member of, and can read
  **and write** through it, until it closes or the 12-hour cap fires.

Why it was left open rather than fixed in Slice 1: the correct fix is to publish revocations
so every instance closes matching sockets, and that channel **is** Redis, which arrives in
Slice 2. The only alternative available now is polling the session table per connection — a
query per socket per interval, on the hottest resource in the system — to shrink a window the
lifetime cap already bounds. That is a bad trade to make when the Redis path replaces it in
the very next slice.

This is the one place in the codebase where a credential outlives its revocation, so it is
recorded here rather than only in a code comment. `maxConnLifetime` in
`internal/transport/ws/conn.go` is the compensating control and must not be raised without
first closing this gap.

> ### ✅ D73 is CLOSED, by D91 in Slice 4
>
> Everything above describes the gap as it stood through Slices 1–3, and is kept because the
> reasoning for leaving it open — and the discipline of writing down a known hole rather than
> hoping nobody looked — is the part worth preserving. **The behaviour it describes is no
> longer current.**
>
> Revocations are now published to every instance and matching sockets are closed on all of
> them: a logout, a password reset, a user revoking a device, and refresh-token reuse detection
> all take effect in **milliseconds** rather than at the 12-hour deadline. There is no longer
> any place in this codebase where a credential outlives its revocation.
>
> `maxConnLifetime` stays, demoted to what it should always have been: a backstop for the case
> where the revocation itself never arrives — Redis unreachable at the moment of the logout, or
> an instance partitioned while it happened. That is a real failure mode with no other bound,
> so the constant remains and still should not be raised casually.

Stage 2 Slice 2 (reconnect/resync + Redis fan-out), approved 2026-08-08:

| # | Decision | Rationale |
|---|---|---|
| D74 | `since_seq` is **nullable on the wire** (`*int64`). Absent = "no prior state, start me at the head"; `0` = "replay the whole log" | Two different requests that an `int64` cannot tell apart — D64's reasoning one level down. It matters because there is no snapshot endpoint: a client with nothing stored can only bootstrap from the log, and `fold(log) == database state` is exactly the guarantee that makes doing so equivalent to fetching it |
| D75 | The broker **re-establishes sequence order** per room, and fills any hole **from the log** | `Replica.Apply` treats a gap as fatal, and that contract was not actually being honoured. `Publish` runs *after* commit and outside the trip row lock, so even locally two transactions can reach the broker out of order; across instances Redis pub/sub orders nothing between publishers and drops messages silently. The room holds an early op for a reorder window, then reads the missing range from `trip_ops` — which is what "the log is the delivery guarantee, the broadcast is only an accelerator" (D70) has to mean once there is more than one publisher |
| D76 | Each room additionally **reconciles against `trips.op_seq`** on a timer | The reorder window structurally cannot see a **lost LAST operation**: a gap is only detectable when a later op arrives. This is the "periodic seq heartbeat" the design doc's failure table reserved for Slice 2. Costs one indexed row read per active room per interval, and it is the reason a dropped Redis message is a latency blip rather than a silently stale client |
| D77 | A resuming subscriber **joins its room first**, then replays the log, with live events held behind a bounded gate | Both other orderings are broken. Read-then-join loses anything committed in between, permanently. Join-then-stream delivers seq 60 while the replay is at seq 12, and the client's fold rejects it. Joining first and buffering makes overlap *expected* rather than merely tolerated — which is fine, because redelivery is a no-op in the fold |
| D78 | A **fresh** subscribe is implemented as a resume from the head read a moment earlier — one code path, not a special case | It closes the window between reading the head and joining the room, where an operation would be broadcast to a room the subscriber is not in yet AND fall outside the range it replays. The replay is normally empty; the uniformity is the point |
| D79 | Replay is **capped** (`MaxResyncOps`, default 10 000) and a client past the cap gets `resync_required` | Not a correctness limit — replay is correct at any distance, since the log is complete and unpruned. It is economics: replay costs a trip's *history*, a re-fetch costs its *size*, and only one of those grows without bound. Stated as a test against a tiny configured bound so that changing it is deliberate |
| D80 | Peer fan-out uses **per-trip Redis channels**, and an instance discards its own publishes by instance id | A single global channel would make every instance decode every write in the system — scaling that makes every node do all the work is not scaling. Local delivery stays synchronous and independent of Redis, so a Redis outage degrades cross-instance delivery without touching the local room, and single-instance behaviour is byte-for-byte Slice 1's |
| D81 | A subscriber the broker **drops** is told so (`DroppableSink` → `resync_required` for that trip) | Slice 1 dropped slow subscribers and said nothing, so the client kept believing it was subscribed and received nothing forever. There was no useful thing to say until resume existed; now there is |
| D82 | Redis handshake tickets are redeemed with **`GETDEL`** | Read-and-delete in one atomic server-side operation. `GET` then `DEL` is the textbook check-then-act race, and it would let a replayed ticket through under precisely the concurrency that makes multi-instance worth having — the same reasoning as D21's single-statement invitation redemption |

Stage 2 Slice 3 (budget + attachment op vocabulary, and their deferred service/HTTP layers),
approved 2026-08-08:

| # | Decision | Rationale |
|---|---|---|
| D83 | `budget.set.v1` **must name every field it is allowed to name** — a partial mask is refused by `FieldMask.Validate`, not merely discouraged | This is what makes D44's "atomic, not field-mergeable" a property of the system rather than of its callers. The failure D44 describes — A sets the total, B sets their split, both valid alone, the pair wrong — is *exactly* a pair of partial masks. Refusing to encode one means the bad state cannot be requested, which is strictly better than detecting it after the fact and then having to choose whose number to discard. Create and edit are ONE verb for the same reason: a write that must carry everything has nothing left for a separate create to add |
| D84 | Attachments **are written to the operation log**, despite having nothing to merge | The tempting conclusion is that an entity with no conflict resolution needs no log entry, since replay exists to reconstruct merges. It is wrong, and wrong in the exact shape Rule 3 exists to catch: resync reads the log and nothing else, so an unlogged attachment is invisible to every member who was offline when it was added — permanently, with every other test still green. "Broadcast-only" describes the merge grain, not the delivery guarantee |
| D85 | A budget write **requires an explicit `expectedVersion`**; nil is refused. The one documented exception to D69 | D69 makes conflict semantics a property of the request: nil means "merge me". That is a coherent instruction only where a merge exists. For an entry replaced whole it does not, and both readings of nil are bad — substituting the version just read makes it a silent wholesale overwrite (the "someone's number vanished" outcome the coarse grain was chosen to prevent), and refusing all concurrent writes makes the ledger unusable. So the caller states what it believes it is editing and gets a 409 if it is wrong. Scoped to kinds where `OpKind.Mergeable()` is false, read from one place so the rule and the total-mask rule cannot drift apart |
| D86 | Attachment operations are **delivered by the sync engine and never accepted by it**; uploads are REST-only | An upload is a presign, a direct browser PUT to object storage, and a server-side confirmation — a three-party exchange with a URL in the middle, which a WebSocket frame cannot express. The engine rejects `attachment.*` intents with a message saying so rather than the generic "unknown operation", because a client needs to tell a deliberate boundary from a gap. Costs nothing in completeness: a REST-originated attachment reaches an absent member through the same replay as any other REST write, which is the whole point of Rule 3 |
| D87 | An attachment is logged when it becomes **visible** — a link at creation, a file at confirmation — never at presign | A pending row may never become anything; abandoned uploads are the normal failure mode of a presigned design. Announcing at presign would advertise a photo that may not arrive and then require retracting it with a removal for an entity no client ever saw — which a fold correctly rejects as an operation on an unknown entity. It also keeps presigning, the high-frequency half of an upload, off the trip's write lock entirely, so photo uploads do not contend with itinerary edits |
| D88 | `BudgetRepository.Save` **refuses to run outside a transaction** | It writes the entry, deletes the whole split set and reinserts it. Outside a transaction each statement commits alone and the deferred sum trigger passes every time — the intermediate state (an entry with zero splits) is legal — so a crash mid-way leaves a permanently wrong ledger that no constraint can catch, because every state it passed through was valid. There is nothing to detect after the fact, so the only defence is refusing to start |
| D89 | A split may only name a **current trip member**, checked in the service | `budget_splits.user_id` references `users`, not `trip_members`, so the database will record that a stranger owes money on a trip they have nothing to do with. A composite FK to `trip_members` would fix it and break worse: membership is soft-deleted, so a member who leaves would either invalidate every historical split naming them or the FK would point at tombstones and enforce nothing. Checking at write time while letting existing splits stand is the honest form of that trade |
| D90 | The presigned **download URL never enters the operation log**; the storage key does | D62 says store resolved effects rather than derivations, and a signed URL is neither — it is a value that is FALSE by the time anything replays it. The log is immutable and outlives every TTL in the system. Clients receive the key and exchange it for a fresh URL per request |

Stage 2 Slice 4 (sync close-out: session revocation + measured NFRs), approved 2026-08-08:

| # | Decision | Rationale |
|---|---|---|
| D91 | Session revocation **closes matching open sockets**, on every instance. Closes D73 | The gap was never that the fix was hard; it was that the only fix available before Redis was polling the session table once per connection per interval — a query per socket on the hottest resource in the system, to shrink a window `maxConnLifetime` already bounded. With the peer channel in place the correct fix is small: the auth service publishes, every instance closes what matches. `maxConnLifetime` survives as a backstop for a revocation that never arrives, not as the primary bound |
| D92 | Revocations travel on their **own port and their own Redis channel**, not through `OpTransport` | It would have been possible to push them through the operation channel as a pseudo-op. That would be wrong in a way worth naming: the op log is a per-trip, gapless, replayable record of ITINERARY facts, and a revocation is none of those — not trip-scoped, never replayed to a resyncing client, and folding it would mean `Replica` had to know what a session is. Two kinds of fact, two channels. It also gets its own Redis subscriber connection: the one churning through per-trip subscribe/unsubscribe as rooms open and close is not the one that should carry a message closing a compromised session |
| D93 | The revocation channel is **global**, deliberately unlike D80's per-trip op channels | D80's reasoning is what forces the opposite answer here. An instance can subscribe only to trips it hosts because it knows which those are; it cannot subscribe only to sessions it holds, because the instance processing a logout has no idea which instance holds that user's socket — that is the entire problem. Affordable precisely because revocations happen at HUMAN frequency: a busy trip emits operations continuously, a user logs out once |
| D94 | `Registry.Publish` closes local sockets **synchronously, before** telling peers | Same shape as the broker, same reason. The instance that handled the logout has honoured it before anything touches the network, so the guarantee on that instance does not depend on Redis being reachable, and a publish failure is a degraded fan-out rather than a failed revocation. It is also why `RevocationPublisher` returns no error: refusing to log a user out because their socket could not be closed leaves them strictly worse off, and the database write that stops the next request has already happened |
| D95 | Revocation is scoped: `session` kills one, `user` kills all. The match rule lives in `internal/domain` | Signing out on a phone must not sign you out on a laptop, and a password reset must kill everything. One line decides whether a logged-out credential keeps working, so it lives in the layer with the strictest import allowlist and is called by the transport rather than reimplemented there — a second copy is how the two would eventually disagree |
| D96 | A revoked connection is **told why** (`session_revoked`) before the socket closes | Without it the client sees an ordinary disconnect, and an ordinary disconnect means "reconnect and resume" — so it would fetch a ticket, be refused, and have to infer from an auth failure what it could simply have been told. It is the only terminal error code in the protocol, and delivery is best-effort on a short drain: telling the client is a courtesy, the close is the guarantee |
| D97 | `conn.close()` closes the **socket**, not just the signal channel | Found by the revocation tests and worth recording because it was a latent bug on every server-initiated close, not only this one. `readPump` parks in `ws.Read`, which does not watch the closed channel; closing the channel alone stopped the write pump and left the reader blocked, and since `run` waits for both before shutting down, the socket was never actually closed. A revoked connection sat fully alive. The same hole affected the slow-consumer drop — precisely the client least likely to send the frame that would have unblocked it |

Stage 3 Slice 2 (comments — flat, per-slot discussion), approved 2026-08-08:

| # | Decision | Rationale |
|---|---|---|
| D98 | Comments are **append-only, no merge, no edit** — the same treatment as attachments (D46/D84), not the field-level-LWW treatment slots and options get | A comment is an event (someone said something at a point in time), not a mutable shared value. Two concurrent comments never conflict — they are just two rows. The vocabulary is `comment.create.v1` / `comment.delete.v1` only; there is no `comment.edit.v1`, matching D46's "you replace one, you do not edit it" verbatim |
| D99 | Comments carry **no version column**, for the same reason attachments have none | There is no edit verb, so there is no concurrency story to protect. `CommentPayload` passes `version=0` explicitly rather than omitting it, keeping "nothing to version" visible in the log the way `AttachmentPayload` does |
| D100 | Comment delete is **author-only**, not capability-gated | The one deliberate departure from every other delete path in this codebase — slots, options and attachments are capability-gated only, because they are shared planning artifacts an editor may prune. A comment is a personal utterance with no precedent to copy, so the more conservative reading was chosen: the author can delete their own comment, nobody else can, not even the trip owner. Enforced in `CommentService.Delete`, not in SQL — no constraint can express "the deleter must equal author_id" |
| D101 | Comments attach to exactly **one slot**, flat (no `parent_comment_id`), ordered by `(created_at, id)` | Matches where collaborative decision-making actually concentrates — the voting UI's shape — rather than a trip-wide thread or a nested-reply structure neither the product nor this slice asked for. Composite FK `(slot_id, trip_id) -> slots(id, trip_id)`, the same pattern as `slot_options_slot_fk`, makes "comment on another trip's slot" unrepresentable |
| D102 | `author_id` is **derived from `op.ActorID` at write and fold time, never carried as a wire field** | The server ignores any author a client might supply anyway — `CommentService.Create` always uses the authenticated actor's own id — so putting it in the field mask would require a client to send a value with no effect and the server to decode and discard it. Matches how `applySlot`/`applyOption` derive `created_by`/`proposed_by` from `op.ActorID` rather than the payload — attachments' `uploaded_by` is the one precedent that DOES carry it as a payload field, but attachments never go through WS intent decoding at all (D86), so the question of trusting a client-supplied value never arose for them. **Found the hard way**: the first version of this slice put `author_id` in the total mask without adding a matching field to `intentValues`, and the engine's `DisallowUnknownFields()` decoder (deliberately strict — a typo'd field must not silently no-op) rejected every WS-submitted comment with `validation_failed`. Adding the field to `intentValues` would have fixed the symptom while leaving the actual problem: a client-supplied value the server was always going to discard. Removing `author_id` from the vocabulary entirely was the correct fix |
| D103 | Comments are **WS-native**, unlike attachments (D86) | An upload needs a presign exchange a WebSocket frame cannot express; a comment is just text. `comment.create.v1`/`comment.delete.v1` get the ordinary create/delete dispatch in `internal/syncengine`, the same as votes and slots, so a comment posted by one client reaches every other subscribed client live rather than only through a REST-triggered broadcast |

Also found while wiring this slice, unrelated to comments themselves but caught in the same code path: `cmd/api/main.go` declared `syncengine.Services.Budget` but never set it in the `Services{}` literal — a real client submitting `budget.set.v1`/`budget.delete.v1` over the socket in the deployed binary would nil-panic. Fixed alongside adding `Comments` to the same literal, and closed properly as D104 below.

| D104 | `cmd/api`'s `syncengine.Services` construction is pulled into a named function (`newSyncEngineServices`) with a **reflection-based test asserting every returned field is non-nil**, called with distinct sentinel values | This is a different failure shape from every entry in the "standing principle" section above, and worth distinguishing rather than filing alongside them: those are tests whose ASSERTION was wrong (measuring something narrower than its name claimed). This was a code path with **no test on it at all** — `tests/stack_test.go` builds its own hand-written copy of the same `syncengine.Services{}` literal for the full-stack suite, and the two had already drifted (stack_test.go had `Budget` correctly wired; `main.go` did not) before anyone noticed. Extracting the literal into a function does not, by itself, stop a second hand-written copy from drifting the same way — the test is what closes that, because it calls the EXACT function `run()` calls rather than a re-implementation, and fails if anything it returns is nil. Verified against the actual historical bug: reverting `newSyncEngineServices`'s `Budget` parameter to a literal `nil` fails the test with the same nil-panic-in-production message the real bug would have produced |
| D105 | `AUTH_AUTO_VERIFY_EMAIL` marks new accounts verified at signup, **refused outright in production** by config validation | A deliberate, bounded exception to D29 rather than a softening of it. D29's reasoning — an address that was never proven cannot be recovered by password reset, so an unrecoverable account is worse than an unusable one — is about REAL users with REAL addresses; locally there is no address to prove and the sign-up/open-Mailpit/find-the-link loop is friction with no reviewing value. The failure mode this avoids is the realistic one: when a demo loop is annoying enough, the thing people actually reach for is weakening the real policy for everyone. Two properties keep it honest. It is **refused, not ignored**, in production (D19's reasoning: an operator who set it believes signups are auto-verified, so booting with it silently off is the worse failure). And it issues **no token and sends no mail** — a version that emailed a link AND verified the account would leave a live unspent credential in an inbox. The frontend branches on `email_verified_at` in the signup response rather than on a client-side copy of the server's configuration, so the two cannot disagree |
| D106 | The demo seed is **deliberately unable to run against production**, and says so rather than acquiring a way | It needs a verified account to log in (D29), which only auto-verify (refused in production, D105) or a readable mailbox can provide. Both of the obvious ways to "fix" that are worse than the limitation: a seeding endpoint that mints verified accounts is an authentication bypass with a friendly name, and a service account with a fixed password is a credential in the repository. So the limit stands, the script fails with an actionable message naming `JUNTO_MAILBOX_URL` and the auto-verify flag, and a deployed demo runs against staging. Recorded here because the failure it prevents is the one that shows up at deploy time, in front of an audience, rather than in a test |
| D107 | `/auth/refresh` gets its **own, permissive limiter** (`middleware.RefreshRateLimit`, 30 burst then 1/sec) instead of sharing the strict credential bucket. D35 is unchanged for signup/login/verify/reset | The two endpoints defend against different attacks, and putting them in one bucket applied a password-guessing posture to something that accepts no password. Login takes a HUMAN-CHOSEN secret from a length-only policy, so an attacker's best move is many attempts and throttling is the compensating control that makes D35's reasoning true. Refresh takes an opaque 256-bit random token (D9) in an HttpOnly cookie scoped to `/api/v1/auth` (D30); it cannot be guessed at any rate worth defending against, and a *replayed* one is met by reuse detection revoking the entire token family — strictly stronger than a 429. So the strict limit bought nothing and cost real usability: the access token is memory-only, every hard navigation costs one refresh, and at burst 5 refilling one per ten seconds a few quick reloads exhausted the bucket and the app rendered as signed-out. Found by direct testing, not in a test — which is why `TestRefreshIsNotThrottledLikeLogin` now pins BOTH directions, since a version asserting only that refresh survives would stay green if the strict limiter were deleted outright |
| D108 | A **link** invite returns its redemption URL on the create response (`accept_url`); a **targeted** invite deliberately does not | Not a loosening of the "only a hash is stored" rule — the discovery of an invite mode that had no delivery channel at all. `CreateInvitation` generated a token, hashed it, stored the hash and dropped the raw value on the floor; for an email invite the raw value still reached the invitee's inbox, but for a link invite (`email: null`) there was no second copy, so the row was created, listed as pending, showed an expiry, and **could never be redeemed by anyone**. The handler's own doc comment had described the fix as already implemented since Stage 1 Slice 4, which is why nothing looked wrong. The asymmetry is the substance of the decision: a targeted invite's token going to the named address *and nowhere else* is half of what makes D58's email check mean anything, and an inviter who wants a pasteable link can simply ask for a link invite. Pinned from both sides — `TestLinkInviteReturnsARedeemableAcceptURL` redeems the returned URL end to end as a second user (a URL that is present but malformed fails identically to an absent one from the invitee's side, so asserting non-empty would not have been enough), and `TestTargetedInviteWithholdsItsAcceptURL` catches the tempting widening to "always return it", which every other invitation test would have stayed green through because they all recover the token from the mail. The list projection stays token-free, and is now a separate wire type from the create projection rather than an `omitempty` on a shared one, so a future field cannot be added to the wrong struct and publish live links to everyone who can list them |
| D109 | The frontend's missing create paths are ordinary CRUD, but their absence is recorded because of **how** they went missing: every one had a working, tested backend endpoint behind it | Days, slots and options could all be created through the API, were all covered by service and full-stack tests, and were all reachable by the seed script — which is exactly why the gap survived Stage 3. The e2e suite drives *seeded* trips, so it exercises voting, comments and presence on data that already exists; nothing in it ever creates an itinerary through the interface, and a trip created by hand through the UI was therefore a permanently empty screen with no control on it. The generalisable form is the one already stated for `syncengine.Services.Budget` (D104) and the phantom component tests: the failure was not in any layer, it was in the seam nothing executed. A backend endpoint with no caller and a UI with no way to reach it both pass every test in the repository |
| D110 | **Liveness checks no dependency, ever.** `/livez` consults nothing external and stays 200 even while draining | The instinct is that a liveness probe should be thorough, and acting on it is the most common way a health check makes an outage worse. A `/livez` that pings Postgres means every instance fails liveness at the same instant when the database blips, and the orchestrator restarts all of them — but restarting the API cannot fix the database. It discards the warm pool and every in-flight request, so recovery is strictly slower than having done nothing, and the restarts themselves look like a cascading application failure to whoever is paging. Liveness answers exactly one question: is this process wedged. It also stays 200 during the drain, because a draining process is not a wedged one and a 503 there invites a kill mid-handover — reintroducing precisely the dropped connections the drain exists to prevent. `TestLivenessIgnoresDependencies` and `TestDrainingFailsReadinessButNotLiveness` both fail against the planted "make liveness accurate" change |
| D111 | Readiness fails on **Postgres only**. Redis is probed, reported, and deliberately **not** critical; object storage is not probed at all | Two independent reasons, and the second is the one that would be missed. First, a readiness check that fails on Redis pulls *every* instance out of rotation simultaneously, converting a degradation into a total outage — readiness is a routing decision, and there is nowhere better to route to. Second, and more specific to this system: Redis being down is a mode it is *designed* for. No `REDIS_URL` at all is a supported single-instance topology, and when it is configured and drops, D75's log-backed gap fill and D76's reconcile tick repair cross-instance delivery out of the operation log — the cost is latency, not lost writes. Postgres has no equivalent: every read, every write and every `op_seq` allocation (D60) goes through it, so an instance that cannot reach it has nothing to serve. Storage is unprobed because attachment failure affects one feature rather than the API, and probing a third party makes its uptime silently become ours. Pinned by `TestReadinessFailsOnCriticalButNotOnDegraded`, verified against dropping the `c.Critical &&` guard |
| D112 | Shutdown **fails readiness first, then keeps serving** for `HTTP_DRAIN_DELAY`, and only then calls `server.Shutdown` | Without the delay this is the graceful shutdown that still 502s, and it is graceful in name only. `Shutdown` stops accepting new connections the moment it is called, but the load balancer does not learn that until its next probe fails — and every request routed into that window fails for no reason except the deploy. Going unready *while still serving* inverts it: the instance leaves the pool with zero errors, and the listener closes only after nothing is being sent to it. The cost is two numeric constraints that are easy to break by editing one of them (`drain > probe interval × failures`, and the platform's `kill_timeout > drain + shutdown`), so both are written in `fly.toml` next to the values and again in `docs/deploy.md`. Fly's default `kill_timeout` is 5s, which would SIGKILL partway through an 8s drain — a drain that gets killed halfway is worse than no drain, because connections are cut at an arbitrary moment rather than a chosen one. The delay defaults to 0 in development and 8s in production, keyed on whether the variable was **set** rather than on its value, so an operator who deliberately writes `HTTP_DRAIN_DELAY=0` in production keeps their decision (D19's distinction between absent and stated) |
| D113 | Probe responses carry **component status and nothing else** — no error strings — and the probe paths are exempt from both the rate limiter and the request log | The endpoints are unauthenticated by necessity, since an orchestrator cannot hold a credential, so everything in the body is public. A driver error naming a host and port is exactly the internal topology that must not be published, and an `error` field is the most natural thing in the world for a well-meaning contributor to add — so `TestProbeResponsesLeakNoInternals` plants it. The detail is not lost: `HealthHandler` logs it itself, ERROR for critical and WARN for degraded, where it reaches whoever can read logs and nobody else. Same shape as D32's identical-401 and D23's constraint-name mapping. The exemptions are for two different failures: mounted under `/api/v1` the probes would share the general per-IP bucket and eventually 429 — a rate limiter capable of taking a *healthy* deployment down, since the platform reads 429 as unhealthy — and at INFO per hit a 2-second poll makes successful probes the overwhelming majority of the log, burying whatever someone opened it to find |

---

## Build order

- **Stage 1 — Foundation** ✅ complete.
  - ✅ Slice 1: Compose, embedded migrations, domain types + ports, fractional indexing,
    config, arch test, schema verification, CI.
  - ✅ Slice 2: repository layer (sqlc + pgx), `TxManager` with savepoint nesting, Argon2id
    hasher, repository tests against real Postgres via testcontainers, cross-layer arch test.
  - ✅ Slice 3: auth service (signup/login/refresh/verify/reset/sessions), JWT issuer, SMTP
    adapter, HTTP transport with RFC 7807 problems and the response envelope, middleware
    (request id, logging, recovery, security headers, CORS, rate limiting, bearer auth),
    full-stack API tests.
  - ✅ Slice 4: domain reshaped to slots + options + votes; budget, attachments and
    `FileStorage` schema and ports landed; MinIO in Compose. Services + HTTP transport for
    trip/day/slot/option/vote CRUD, members + invitations (create/list/revoke/redeem),
    keyset-paginated trip listing. `Actor.Can()` enforced in every write path via a shared
    `authz` helper (membership + capability check, plus a trip-scoping guard against
    IDOR-by-nested-id). Fix: deleting a slot's selected option clears the slot's selection
    in the same transaction, proven at both the service (fake-backed) and full-stack
    (real Postgres + real HTTP) level.
  - ✅ Budget and attachment services + HTTP, deliberately deferred until after Stage 2
    convergence was proven, and delivered in Stage 2 Slice 3 alongside their op vocabulary.
    Deferring was the right call and for the reason given: building them first would have
    grown the surface without de-risking anything, and both turned out to need conflict-grain
    decisions (D83–D85) that could only be made once the sync engine existed. Note the
    correction to the earlier claim that "the repository layer exists" — only the SCHEMA and
    the domain ports did; the repository implementations landed in Slice 3.
- **Stage 2 — Sync engine** ✅ complete. Design doc approved 2026-08-08
  (`docs/stage2-sync-design.md`, decisions D59–D72); extended by D74–D82 (Slice 2),
  D83–D90 (Slice 3) and D91–D97 (Slice 4).
  - ✅ Slice 1: `trips.op_seq` sequencer + immutable `trip_ops` log (migration 000004);
    `internal/domain/op.go` (op vocabulary, explicit field masks, `Replica` fold) inside the
    strictest arch allowlist; op-log writing in ONE place in the service layer, so REST and
    WebSocket writes are both in the log by construction; `expectedVersion *int` (D69);
    `internal/syncengine` (rooms, presence, intent dispatch) importing zero network types;
    `internal/transport/ws` (hub, D10 ticket auth, bounded per-connection buffers,
    per-connection rate limit). 11-scenario convergence test on two real sockets, plus 9
    WebSocket transport tests and 9 service-level op-log-shape tests.
  - ✅ Slice 2, complete once its two carried-forward gaps closed in Slice 4. Decisions D74–D82.
    - ✅ **Reconnect/resync.** `since_seq` is now a nullable `*int64` on the wire (D74) and
      answered by replaying `trip_ops`: absent starts at the head, a value replays everything
      after it, `0` replays the whole log. Bounded by `MaxResyncOps` (D79); a client that is
      ahead of the server, or further behind than the cap, still gets `resync_required`.
      Ordering is re-established per room with log-backed gap filling (D75) and a reconcile
      tick against `trips.op_seq` (D76). `tests/resync_api_test.go` — 6 scenarios including the
      four-member one (three concurrent writers while a fourth is offline) and a REST-only
      change during the absence, which is Rule 3's guarantee tested from the angle it was
      written for. Verified to fail when the Slice 1 refusal is planted back in.
    - ✅ **Redis pub/sub fan-out + the two-instance test.** `internal/pubsub` implements
      `domain.OpTransport` over per-trip channels (D80); the sync engine still imports nothing
      matching `redis`. `tests/multi_instance_api_test.go` — 3 scenarios. See the claims table.
    - ✅ **Ticket store moved to Redis** (`ws.RedisTicketStore`, `GETDEL` for single-use
      redemption, D82). This was a prerequisite rather than a cleanup: with per-process
      tickets, a two-instance test is measuring which server the client happened to land on.
      The swap is one constructor call in `cmd/api`, as the `TicketStore` port promised.
    - ➡️ **Session revocation must close open sockets** (D73) — deferred to Slice 4, and
      **done there** (D91).
    - ➡️ **Single-trip write-throughput benchmark** — deferred to Slice 4, and **measured
      there**: ≈155 ops/sec single-trip, ≈600 spread across trips.
  - ✅ Slice 3: budget + attachment op vocabulary and their service/HTTP layers. Decisions
    D83–D90.
    - ✅ **Two new conflict classes, both non-mergeable, both enforced structurally.**
      `budget.set.v1` / `budget.delete.v1` and `attachment.add.v1` / `attachment.remove.v1`
      join the vocabulary inside the strictest arch allowlist. The masks for `budget.set` and
      `attachment.add` are required to be TOTAL (D83/D84), so a partial write to either cannot
      be encoded — which is how "atomic" and "broadcast-only" stop being adjectives. The
      `Replica` fold gained `Budgets` and `Attachments` and no special cases: the coarseness
      was already enforced at write time, so the fold stays a pure field assignment.
    - ✅ **Repository layer** (`internal/repository/budget.go`, `attachment.go`, two query
      files, sqlc regenerated). `Save` writes an entry and its complete split set atomically
      and refuses to run outside a transaction (D88). 20 repository tests against real
      Postgres, covering the deferred sum trigger from both sides, the split rewrite that
      shrinks a member set (the case that makes the DEFERRAL necessary rather than stylistic),
      the exclusive arc, and the two-phase upload.
    - ✅ **Services**: `BudgetService` (atomic, version required — D85, plus the trip-member
      check the schema cannot express, D89) and `AttachmentService` (presign → direct PUT →
      server-side `Stat` → confirm, with an oversized object marked failed and deleted).
      Attachments are logged when they become VISIBLE, never at presign (D87).
    - ✅ **HTTP**: `PUT /trips/{id}/budget/{entryID}` — PUT rather than PATCH, because the
      entry is replaced whole and the verb should say so — plus the attachment upload,
      confirm, link, signed-download and delete endpoints. Attachment routes mount only when
      object storage is configured; `STORAGE_ENDPOINT` is optional exactly like `REDIS_URL`.
    - ✅ **Sync**: budget intents are accepted over the socket; attachment intents are refused
      with an explicit "REST-only" error (D86). `tests/ledger_api_test.go` — 9 scenarios,
      including the racing budget writes that must produce exactly one winner and one 409, two
      concurrent uploads that must both survive, and `fold(trip_ops) == database` extended to
      both new entities. Every claim-backing test verified against a planted break.
  - ✅ Slice 4 — **sync close-out**. Decisions D91–D97. Both gaps carried out of Slice 2 are
    closed, and every non-functional target now has a measured number.
    - ✅ **Session revocation closes open sockets** (D91, closing D73). `internal/domain/
      revocation.go` declares the port and the match rule; `internal/pubsub/revocation.go`
      carries events between instances on their own global channel and their own subscriber
      connection; `ws.Registry` holds this instance's sockets and shuts the matching ones. The
      auth service publishes at every revocation point — logout, password reset, a user
      revoking a device, and both refresh-token-reuse paths. `tests/revocation_api_test.go` —
      5 scenarios including the two-instance case (logout on A closes a socket on B) and the
      two negatives that make it precise: another member's socket survives a password reset,
      and revoking one session leaves the same user's other sessions alone. Verified to fail
      with instance B's revocation transport replaced by a no-op.
    - ✅ **Found and fixed while doing it (D97):** `conn.close()` closed the signal channel but
      not the socket, so `readPump` — parked in `ws.Read`, which does not watch that channel —
      never returned, and `run` waits for both pumps before shutting the socket down. A revoked
      connection sat fully alive. The same latent bug affected the slow-consumer drop path.
    - ✅ **Non-functional targets measured**, replacing two TBDs and one "currently undefined"
      with numbers: single-trip write throughput ≈155 ops/sec against ≈600 spread across
      trips (≈3.9×, which is the row lock and is the finding), p99 message latency 23–49ms at
      100 connections on one trip, ≈2.5× p99 degradation from 2 → 100 connections, and
      reconnect + resync of 200 missed operations in ≈40ms against a 2s target. See
      *Non-functional targets* above, including the caveat about what single-machine figures
      are and are not worth.
- **Stage 3 — Collaboration UX** 🚧 in progress. No frontend existed before this stage — it is
  built directly against the already-complete Stage 1/2 backend.
  - ✅ Slice 1: presence + voting UI. `web/` scaffolded (Next.js App Router, TypeScript). Trip-
    level presence (confirmed against the hub's actual granularity — no per-slot tracking
    exists, so none was built) via a framework-agnostic WS client (`web/lib/socket.ts`).
    Voting UI casts/retracts over `vote.set.v1` per spec, not REST, and shows the resolved
    `selected_option_id` distinctly from the raw tally (D41). Found and fixed two real bugs
    while wiring: a missing `PUT` in the backend's CORS allowed-methods list, and a duplicate
    WS subscribe frame in the client that was corrupting presence server-side. Tested by 1 real
    two-browser Playwright e2e against the full real stack (Postgres, Redis, Mailpit,
    `go run ./cmd/api`, `next dev`) — no mocks. **Correction (2026-08-09):** this entry
    previously claimed "5 component tests" as well. No such tests were ever written — see
    *Frontend test coverage, corrected* below.
  - ✅ Slice 2: comments (flat, per-slot, append-only — D98–D103) + optimistic reconciliation.
    New backend surface built for it: migration 000005, `internal/domain/comment.go` +
    op vocabulary (`comment.create.v1`/`comment.delete.v1`, WS-native unlike attachments),
    repository, `CommentService` (the one delete path in the service layer that is
    author-gated rather than capability-gated), HTTP routes, sync-engine dispatch. Comments
    UI posts/deletes optimistically — the row renders under its final id immediately (D4) and
    reconciles when the op broadcast for that id arrives, rolling back on rejection. Found and
    fixed two more real bugs: `author_id` was briefly a wire field the server would decode and
    then ignore, rejected by the engine's strict decoder until removed from the vocabulary
    (D102); and `cmd/api/main.go` had never actually wired `Budget` into `syncengine.Services`
    despite declaring the field, a latent nil-panic on any real `budget.set.v1` WS op that the
    test suite's own wiring never exercised — closed properly as D104, with a test on the
    actual construction path rather than a narrative note. Backend: repository/service/domain
    tests plus two
    full-stack tests (`tests/comments_api_test.go`) covering REST authz and WS live delivery +
    fold consistency. Frontend: the same e2e page extended with a Part C
    proving a comment posted by one client appears live on a second client's screen, and that
    the author-only delete control does not even render for a non-author. **Correction
    (2026-08-09):** this entry previously claimed "8 component tests" as well. No such tests
    were ever written — see *Frontend test coverage, corrected* below.
  - ✅ Slice 3 — the visual pass, in four reviewed groups. **Part 1** produced `web/app/
    tokens.css` before any screen was touched: two visual languages (cinematic outer shell,
    dense inner app) threaded by one serif, one accent hue and one `--radius-card`. **Group A**
    the outer shell (landing, trips list, create trip, three-mode picker). **Group B** Plan
    mode — itinerary, slot detail, comment thread, members, budget — where the D41 hierarchy
    finally lands: a filled accent badge for the group's decision against a neutral counter for
    the raw tally, never colour alone. **Group C** Memories, derived from resolved slots
    because no Memories backend exists. **Group D** the auth screens.
    - A token-usage self-check was run at each group boundary rather than at the end, because
      the failure mode is a token file that exists and is unused. It caught real drift each
      time: `PresenceBar` still on raw Tailwind after Group B, and three diverged copies of the
      avatar palette. The whole app is now free of raw Tailwind colours and text sizes,
      verified by grep, and `/design` remains as a living specimen.
    - Password reset had **no UI at all** — the backend has emailed `/reset-password?token=…`
      links since Stage 1 Slice 3 and that route 404'd. Built in Group D.
  - ✅ Slice 4 — **the create paths**, found by using the app rather than by a failing test.
    Decisions D108–D109. Three surfaces the backend had supported since Stage 1 Slice 4 were
    unreachable from the interface, and one invite mode had never worked at all:
    - ✅ **Shareable invite links** (D108). `MembershipService.CreateInvitation` now returns a
      `CreatedInvitation` carrying `AcceptURL` for link invites; the Members panel offers
      "create a shareable link" beside the email form and shows the URL once, with a copy
      control that falls back to selecting the text when `navigator.clipboard` is absent —
      which is every demo served over plain http on a LAN address, not a hypothetical.
      Verified end to end against the real stack: link created, pasted, redeemed by a second
      account, joiner on the roster as editor.
    - ✅ **Adding to an itinerary.** `web/lib/api/{slots,options}.ts` gained `createSlot` and
      `createOption` (`createDay` already existed and had **no caller**, which is its own
      small indictment). The itinerary now has an add-a-day control, a per-day add-a-slot
      disclosure, and an unscheduled-backlog path; the slot detail can propose an option, so
      the empty state that said "someone needs to suggest a candidate" now lets them. All
      gated on `owner`/`editor`, which hides the controls without pretending to enforce
      anything — the service still refuses a viewer's write.
    - ✅ **Now covered**, by `web/e2e/create-paths.spec.ts` — see Stage 4 Slice 4 below. The
      gap was real and is closed; the entry above is left as written because the reason it
      existed (the suite only ever drove *seeded* trips) is the part worth remembering.
- **Stage 4 — Demo polish** 🚧: intentional UI ✅ (Stage 3 Slice 3), seed script ✅ (Slice 2,
  below); health/ready/live endpoints and deploy ✅ (Slice 3, below).
  - ✅ Slice 2 — `web/scripts/seed-demo.ts` (`npm run seed`). Builds a demo-ready trip through
    the PUBLIC API only — three real members via the real invitation flow, two days, five
    slots, competing options, split votes, comments and a four-entry budget — and prints
    credentials plus a step-by-step two-window collaboration script. Credentials are stable by
    default so a demo can be rehearsed and re-run (`SEED_UNIQUE=1` for throwaway accounts).
    API, web and mailbox URLs are all environment-overridable. Verified against BOTH
    verification modes: with `AUTH_AUTO_VERIFY_EMAIL` off it reads the real verification links
    out of the mailbox, which is the path a non-development environment would take.
  - ⚠️ **Known limit, flagged before deployment rather than at deploy time (D106).** The seed
    cannot run against a *production* deployment, and this is deliberate rather than a gap to
    close. Logging in requires a verified email (D29), which can only be satisfied by
    auto-verify (refused in production by config validation, D105) or by reading the mailbox
    (production SMTP goes to real inboxes). A tool that could mint verified accounts against
    production would be a hole, not a feature. For a deployed demo, point it at a staging stack
    with a readable mailbox via `JUNTO_MAILBOX_URL`. The script fails with that instruction
    rather than a confusing error.
  - ✅ Slice 3 — **operational readiness and deploy.** Decisions D110–D113. Target is Fly.io;
    the frontend is deliberately out of scope.
    - ✅ **Three probe endpoints, and the differences between them are the design.** `/livez`
      consults nothing (D110), `/readyz` fails on Postgres and while draining but not on Redis
      (D111), `/healthz` is the human view with per-component status. All mounted at the root
      rather than under `/api/v1`, which is not cosmetic — the rate limiters live inside that
      route, and a throttled probe reads to the platform as an unhealthy instance (D113).
    - ✅ **Shutdown drains before it stops accepting** (D112). Readiness goes false, the
      process keeps serving for `HTTP_DRAIN_DELAY`, and only then does `server.Shutdown` run.
      Verified against a real container: readiness flipped to 503 at t+2s, liveness held at
      200 throughout, ordinary API requests were served for the whole drain window, and the
      listener closed after it.
    - ✅ **Dockerfile** — multi-stage, static, `scratch` plus CA certificates. Two Stage-1
      decisions pay off here rather than needing workarounds: D17's embedded `time/tzdata` is
      exactly what makes a scratch image viable, and the embedded migrations mean `migrate`
      cannot drift from the binary beside it. `/healthz` reports the git revision from Go's
      own VCS stamp, so it needs no ldflags and cannot drift from a hand-kept constant —
      confirmed reporting `a64349a8bf60-dirty` from a real image build.
    - ✅ **`fly.toml` + `docs/deploy.md`** — probe contract, the two timeout constraints that
      must agree, secrets checklist, runbook, and an explicit list of what the deployment does
      *not* do (no seed against production per D106, no frontend, no metrics, single region
      because Postgres is the sequencer for every write).
    - ✅ **6 tests** (`tests/health_api_test.go`), each verified against the specific planted
      break it names. One of those plants produced a genuine finding rather than a
      confirmation — see below.
    - ⚠️ **Not deployed.** Every artifact is built and exercised locally against real
      containers, but `fly deploy` needs an account and has not been run. The claim this slice
      earns is "deployable and verified locally", not "deployed".

  - ✅ Slice 4 — **frontend e2e for the create paths**, closing the gap Stage 3 Slice 4 left
    open. Stage 4's test coverage is complete; deployment is the only thing left.
    - ✅ `web/e2e/create-paths.spec.ts` — 3 tests, and the rule that makes them worth having is
      stated at the top of the file: **it may not use `createVotingFixture` and may not POST
      itinerary content.** Every other spec starts from a seeded trip, which is exactly why the
      gap survived (D109); a fixture here would rebuild the blind spot. Only account setup goes
      through the API. Everything else is typed and clicked.
      1. A brand-new trip offers a control to start planning it — the single assertion that
         would have caught the whole class on its own, since the empty state used to *instruct*
         without rendering anything that complied.
      2. An itinerary built from nothing: day → slot (with a start time, because `"19:30"` from
         an `<input type="time">` is the one conversion this path has, and a slot arriving with
         a null time still renders a row) → unscheduled slot, asserted to land in the backlog
         and *not* on the day → option → a second option → a vote on an option this test
         created, then resolving it, then confirming the itinerary row shows the decision. That
         last stretch is the join to `voting.spec.ts`: everything before it could pass while
         producing rows the rest of the app cannot use.
      3. A shareable link created in the UI, redeemed by a second account, roster confirmed —
         and then the second browser (needed anyway) reused to prove a slot created through one
         client's interface arrives on the other's screen live, with no reload.
    - ✅ **Four planted breaks, each seen to fail**: the empty state's `AddDay` removed;
      `createSlot` stubbed to a no-op; `AddOption` removed from `SlotDetail`; and
      `CreatedInvitation.AcceptURL` left unset — the exact state the code shipped in, which
      fails at the copy-panel assertion.
    - ✅ **Found while verifying, and fixed**: `access-and-revocation.spec.ts` had two tests
      that could not pass. Neither sets `test.setTimeout`, so both ran at the config's 30s
      default — and the second contains an unconditional `page.waitForTimeout(45_000)`, so it
      was not a slow-CI flake but arithmetic. The other exceeded 30s as soon as the auth
      limiter's backoff was in play, which is any run following another spec. Both failed with
      a bare timeout and no assertion, which reads as the feature being broken rather than the
      budget being too small. **Generalisable, and now in `web/README.md`: in a suite whose
      fixtures deliberately back off against a real rate limiter, a per-spec timeout is not
      optional.**

#### The plant that found something (Slice 3)

Continuing the standing principle above, and worth recording because it is the second kind of
outcome — the kind that is a finding rather than a relief.

`TestProbesAreNotRateLimited` **passed** against its own planted break. The plant moves the
three routes inside the `/api/v1` group, which also relocates them to `/api/v1/livez` — so
every request 404'd, nothing was throttled, and a test whose entire purpose is to detect that
move sailed straight through it. The assertion was `!= 429`, and "not 429" is satisfied
perfectly well by an endpoint that does not exist.

The fix was to assert the status the probe is actually supposed to return (`== 200`), which is
the only assertion that a missing route cannot satisfy. Re-planted afterwards, it fails. The
generalisable form: **a negative assertion about a failure mode is usually satisfiable by a
second, different failure** — prefer asserting the success you expect over the failure you
fear.

### Product modes (context; do not build ahead)

Plan (pre-trip collaboration) is what Stages 1–3 serve. Live (during-trip coverage) is served
by `slots.status` and needs only UI. **Memories** (post-trip gallery) is deferred to Stage 3/4
and has no schema impact.

**Deferred, explicitly not built:** AI-generated plan suggestions. When built — after Stage 2
ships — it populates slots and slot_options via the Anthropic API with tool-calling grounded
in real search results. It is a *writer* against the existing model, not a schema change.

Do not skip ahead. After each stage: summarize what was built, what is tested, and the trade-offs.

## Testing bar (applies across all stages)

Unit + repository + HTTP + WebSocket tests. Stage 2 adds concurrency tests under `go test -race`.
CI fails on lint failure, test failure, or race-detector failure. ~80% coverage on `domain/` and
`service/` as a guideline — not a number to game with trivial tests.

Repository tests run against real Postgres via `testcontainers-go`. Mocking the DB would test
nothing here: the interesting behaviour *is* the constraints and partial indexes.

### ⚠️ Frontend test coverage, corrected (2026-08-09)

Stage 3 Slices 1 and 2 claimed "5 component tests" and "8 component tests" respectively.
**Neither set exists, and neither ever did.** `git ls-files web` returns no `*.test.tsx` or
`*.test.ts` file; `vitest.config.ts` and `vitest.setup.ts` are present and configured, but
`npm test` exits 1 with "No test files found". The claim was never true.

It survived because nothing checked it: **`web/` is not in CI at all.** The workflow and the
`make check` target run Go only, so a `npm test` that has never passed also never failed, and
`npx tsc --noEmit` / `npx eslint` are run by hand or not at all.

What the frontend coverage actually is, stated so the next reader does not have to re-derive it:

| Claimed | Actual |
|---|---|
| 13 component tests (5 + 8) | **none** |
| Playwright e2e against the real stack | **real** — `web/e2e/`, no mocks, real Postgres/Redis/Mailpit/Go API |
| Frontend in CI | **no** — Go only |

**Update (2026-08-11).** The e2e half has grown and is now the whole of the frontend's coverage,
deliberately: 6 correctness tests across `voting.spec.ts`, `access-and-revocation.spec.ts` and
`create-paths.spec.ts`, all green together against the real stack. Still **no component tests
and still not in CI** — an e2e run needs Postgres, Redis, Mailpit, the Go API and `next dev`, and
takes ~4 minutes. That remains a real gap in *speed of feedback* rather than in correctness, and
it is stated here rather than closed by writing tests whose only purpose is to make a sentence
true.

**Why this was corrected rather than backfilled.** Writing thirteen component tests to make a
sentence true is precisely the coverage-gaming this file warns against two paragraphs up, and
the e2e tests are the stronger article for this app: what breaks here is the seam between the
API's response and what the UI does with it, which is what both bugs found on 2026-08-09 were,
and which a test with a mocked fetch cannot see. The honest position is that frontend behaviour
is covered end-to-end and not at the unit level, and that this is a real gap in *speed of
feedback* — an e2e run costs minutes and needs the whole stack up — rather than in correctness.

**The generalisable finding is not about the frontend.** Every claim in this file that has held
up did so because a test would fail if it stopped being true, and every claim that drifted —
this one, and `syncengine.Services.Budget` before it (D104) — did so in a place nothing
executed. A number in prose is not a claim, it is a memory of an intention. Prefer naming the
file that proves the statement, the way the claims table above does.

---

## Commands

```bash
docker compose up -d          # postgres:16 (5433) + redis:7 (6380) + mailpit + minio
go run ./cmd/migrate up       # apply embedded migrations
go run ./cmd/migrate version  # current schema version
go run ./cmd/api              # API (wiring smoke test until transport lands)
go test ./... -race           # full suite (repository tests start their own Postgres)
go test ./... -short          # skip anything needing Docker
sqlc generate                 # after ANY change to migrations/ or queries/
make verify-schema            # adversarial schema invariant checks
make check                    # everything CI enforces, locally

# Probes (Stage 4 Slice 3). /healthz is the one to read: it names each dependency and
# reports the running build's git revision, which is how you confirm a deploy landed.
curl -s localhost:8080/healthz | jq   # detail: per-component status + version
curl -s localhost:8080/readyz         # routing decision — 503 on Postgres down, or draining
curl -s localhost:8080/livez          # process only; never consults a dependency (D110)

docker build -t junto-api .           # static scratch image; see Dockerfile and docs/deploy.md

# Re-measure the non-functional targets and print the figures recorded above.
# They run as part of the normal suite too; -v is what surfaces the numbers.
go test ./tests/ -run 'TestSingleTripWriteThroughputCeiling|TestMessageLatencyAtOneHundredConnections|TestOneHundredConnectionsDoNotDegradeLatency|TestReconnectAndResyncWithinTwoSeconds' -v
```

Postgres is on **host port 5433**, not 5432: the default is commonly taken by another local
Postgres, and colliding with an unrelated project's database is a bad first impression.

Mailpit UI: <http://localhost:8025>.

Redis is on **host port 6380**, for the same reason Postgres is on 5433. It is **optional**:
with no `REDIS_URL` the process runs single-instance with in-memory fan-out and in-memory
handshake tickets, and startup says so at WARN level. Running more than one instance without
it is not a supported topology — tickets minted on one would be unredeemable on the other, and
neither instance's subscribers would see the other's writes. The repository and multi-instance
tests start their own containers, so `go test ./...` needs Docker but not Compose.

MinIO is on **host port 9000**, and `STORAGE_ENDPOINT` is **optional in exactly the same way**:
with no endpoint the attachment routes are simply not mounted and the rest of the API is
unaffected, which startup reports at WARN level. Set it and the credentials together — a
half-configured bucket aborts startup rather than failing at the first upload. The API tests do
not need it: they wire `storage.MemoryStorage`, because a real MinIO in those tests would be
verifying MinIO rather than our bookkeeping. Note the consequence, which is unavoidable rather
than accidental: since the browser PUTs directly to storage, a test reaches the confirm path by
writing to that in-memory store itself — the API never sees the bytes, by design.

**Windows note:** PowerShell mangles unquoted `-coverprofile=coverage.out`; quote the whole
argument (`go test ./... "-coverprofile=cover.out"`). Unaffected in CI, which runs bash.

## Verifying the guarantees

These checks exist because the claims they back are otherwise unfalsifiable:

- `internal/domain/arch_test.go` — parses every domain source file and fails on any import
  outside the allowlist. Verified to fail on a planted `net/http` / `pgx` import, so it is not
  decorative.
- `tests/arch_test.go` — enforces the dependency direction across all 8 layers (`domain`,
  `service`, `repository`, `transport`, `middleware`, `security`, `email`, `storage`, `pkg`)
  against real code — no layer is staged-but-empty anymore. Also fails if any package outside
  `internal/repository` imports the generated sqlc package — the specific way the "domain
  independent of the database" claim would quietly stop being true while everything still
  compiled. Verified to fail on planted `service→repository`, `transport→repository` and
  `pkg→internal` imports.
- `tests/arch_test.go::TestSyncEngineIsTransportAgnostic` — fails on any import under
  `internal/syncengine` matching `websocket`, `net/http`, `redis`, `grpc` or `encoding/gob`.
  Unlike the layering rules it does NOT exclude test files: a test double that needs a real
  socket would mean the boundary is not usable without one, which is the thing being claimed.
  Verified to fail on planted `net/http` and `github.com/coder/websocket` imports. (The
  `service → syncengine` edge is additionally impossible: the Go compiler rejects it as an
  import cycle, which is stronger than the arch rule and was confirmed by planting it.)
- `tests/multi_instance_api_test.go` — the test the horizontal-scaling clause rests on. Two
  fully wired instances against one Postgres and one Redis container, each with its own broker,
  engine, rooms and presence. Its reconcile interval is deliberately pushed to five minutes:
  the broker can ALSO repair a room from the operation log, and with the two paths overlapping
  the test passed while instance B's peer transport was a no-op. Verified to fail on that
  planted break now that they are separated.
- `tests/resync_api_test.go` — reconnect and resume. Asserts not just that a returning client
  ends up correct (a client that threw its state away and re-fetched would too) but HOW: the
  operations it missed arrived as op frames carrying the log's sequence numbers, no
  `resync_required` was sent, and its own subsequent edit merged onto an interleaving it was
  absent for. Verified to fail with the Slice 1 refusal planted back in.
- `internal/syncengine/broker_test.go` — the ordering guarantee, tested without a socket or a
  Redis client, because this package may not import either even in test files. Covers
  out-of-order publish, a lost broadcast repaired from the log, and a lost LAST operation
  recovered by the reconcile tick — the three ways sequence order can be lost between the
  sequencer in Postgres and a client's fold.
- `tests/convergence_api_test.go` — the test the resume bullet rests on. Two real WebSocket
  clients folding their own replicas, released from one barrier so their writes race at the
  trip's sequencer. Asserts `fold(trip_ops) == database == client A == client B` in every
  scenario, plus that the log is gapless and that neither client received an error frame —
  the last one because the easiest way for a convergence test to pass vacuously is for both
  operations to have been rejected.
- `tests/ledger_api_test.go` — the two non-mergeable conflict classes, end to end. Its two
  load-bearing assertions are deliberately the *opposite* of the convergence test's: racing
  budget writes must produce exactly ONE success and ONE 409 (a merge here would be a ledger
  neither writer wrote), and two racing uploads must BOTH survive (there is nothing to merge).
  It also extends `fold(trip_ops) == database` to budget entries — totals, versions and split
  sets — and to attachments. Verified to fail with the `rec.budget` / `rec.attachment` calls
  removed, which is the exact shortcut that would have made attachments invisible to resync.
- `tests/revocation_api_test.go` — the D73 close-out (D91). Asserts the socket was closed BY THE
  SERVER, observed the way a browser observes it, and that the instance is no longer holding the
  connection — not merely that broadcasts stopped, which a dropped subscription also produces.
  The two negatives are what make it precise: another member's socket survives a password reset,
  and revoking one session leaves the same user's other sessions alone. Verified to fail with
  instance B's revocation transport replaced by `NoopRevocationTransport` — the plant matters
  because instance A closes its own sockets synchronously, so a test watching only A would pass
  with the peer path completely dead.
- `tests/health_api_test.go` — the probe contract (D110–D113). Its assertions are deliberately
  about the DIFFERENCES between the three endpoints, because every one of them is a decision
  that a plausible simplification would quietly undo: liveness must stay 200 with a dead
  critical dependency AND while draining, readiness must fail on Postgres but NOT on Redis,
  and no probe body may contain a host, a port or a driver message. Verified against four
  planted breaks — liveness consulting dependencies, readiness dropping the `Critical` guard,
  an `error` field added to the component status, and the routes moved behind the rate
  limiter. The last of those is the one that found a weak assertion rather than confirming a
  strong one; see *The plant that found something* above.
- `tests/nfr_test.go` — the non-functional targets, measured rather than asserted. Reports
  single-trip write throughput against the same writers spread across trips (the ratio is the
  finding), end-to-end p99 message latency at 100 connections on one trip, the p99 difference
  between 2 and 100 connections, and reconnect + resync time. Its assertions are deliberately
  loose — a tight bound on a shared CI box measures the box — and the reconnect test additionally
  refuses a `resync_required`, so the timing target cannot be met by degrading the behaviour into
  a re-fetch.
- `internal/domain/op_coarse_test.go` — the total-mask rule and the wholesale split-set
  replacement, tested without a database. Verified to fail with `RequiresTotalMask` stubbed to
  return false.
- `internal/repository/budget_test.go` — the deferred sum trigger from both directions, and the
  split rewrite that shrinks a member set, which is the case an IMMEDIATE constraint would make
  impossible. These commit for real, because a deferred trigger has nothing to fire at inside a
  transaction that is always rolled back — a distinction the file states, since it decides what
  each test in it can prove.
- `tests/schema_verify.sql` — 32 assertions that each *attempt* a violation and expect the
  database to reject it: second owner, cross-trip/cross-slot/cross-option references at every
  composite-FK level, case-variant duplicate email, re-add after soft delete,
  `ON DELETE SET NULL` on both `day_id` and the circular `selected_option_id`, the vote
  register's single-row-per-member shape, CHECK constraints, byte-wise position collation,
  stale-version updates, the deferred budget-split-sum trigger, the attachment exclusive arc,
  and the FK/index audit. Stage 2 added five more: duplicate `(trip_id, seq)` rejected, a
  replayed `client_op_id` rejected while multiple NULL ones coexist (proving the index is
  still partial), `op_seq` rolling back with its transaction so the log stays gapless, a
  non-positive `seq` rejected, and no trigger or rule able to rewrite `trip_ops`. Runs in a
  rolled-back transaction, so it is safe against a populated database.
