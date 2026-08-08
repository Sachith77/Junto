# Stage 2 — Sync engine design

**Status:** APPROVED 2026-08-08. Slice 1 and Slice 2 implemented; see §14 for outcomes and for
the corrections implementation forced on this document (two after Slice 1, two more after
Slice 2 — the Slice 2 pair are the more serious).
**Date:** 2026-08-08
**Scope of this document:** the whole of Stage 2's conflict model and protocol. The
implementation is split into two slices; this document specifies both so that Slice 1 does not
accidentally design itself into a corner Slice 2 cannot escape.

- **Slice 1** — core engine + single-instance WebSocket transport, presence, ticket auth,
  immutable op log, convergence test.
- **Slice 2** — reconnect/resync, Redis pub/sub fan-out, two-instance test.

Everything below is written to be falsifiable. Where a claim is not yet backed by a test, it
says so.

---

## 1. The decision: CRDT, not OT

**Chosen: a server-ordered, field-level LWW-register CRDT.** Each mergeable entity is a map
from field name to a last-writer-wins register. The "writer" ordering is the per-trip
operation sequence number, assigned by Postgres. Deletion is its own register. Ordering
within a list is a register holding a fractional-index key.

### Why not OT

OT's unique payoff is *intention preservation over sequences*: `insert(index 5)` and
`delete(index 2)` must be transformed against each other because an index means something
different after the other operation applies. That is the entire reason OT exists.

Junto has no index-relative operations. D2 replaced them with fractional indexing in Stage 1
— a move is an **absolute assignment** of a position key, not a relative index shift.
There is nothing left for a transformation function to transform. For scalar field
assignment, `transform(set a, set b)` degenerates to "one of them wins", which is LWW with
extra ceremony and two more functions to get subtly wrong.

Choosing OT here would mean building transformation machinery whose one advantage was
designed away in Stage 1 on purpose. It would be resume-driven engineering, and it would be
worse code.

### Why not a general-purpose CRDT library (Yjs / Automerge shape)

Those are sequence CRDTs carrying per-character identity and requiring garbage collection.
Our conflict grain is fixed at field level by D6, D39–D42 and D44. A per-field register map
is the *smallest correct* structure for this data model; anything larger is metadata with no
reader.

### What "CRDT" means here, precisely

Two honest qualifications, stated up front so a reviewer does not have to extract them:

1. **This is a sequencer-based design, not multi-master.** Convergence rests on a single
   authority (Postgres) producing a per-trip total order. It is *not* peer-to-peer, and two
   servers cannot merge histories without that sequencer. The merge rule is a CRDT; the
   system is a CRDT *under a total order*. If the sequencer were ever removed, per-field
   timestamps would become necessary. Today they are not, so they are not stored — see §4.4.

2. **Merge is field-level, never character-level.** Two members typing into the same
   `notes` field concurrently do not get their prose interleaved; the later operation wins
   the whole field. This is the single most likely place for someone to assume more than is
   delivered, so it is stated here, in the claims table, and in the README when Stage 2
   ships. Character-level `notes` editing would be a per-field sequence CRDT and is
   explicitly out of scope.

The resume clause "OT/CRDT conflict resolution for concurrent multi-user edits" is literally
true under this design, at field granularity, proven by the test in §10.

---

## 2. The load-bearing mechanism

Everything else in this document is detail. This section is the design.

```
BEGIN
  UPDATE trips SET op_seq = op_seq + 1 WHERE id = $trip RETURNING op_seq   -- (1) FIRST
  SELECT ... FROM slots WHERE id = $slot                                    -- (2)
  <apply the op's field mask to the loaded domain entity>                   -- (3)
  UPDATE slots SET <only the masked fields> ...                             -- (4)
  INSERT INTO trip_ops (trip_id, seq, ...)                                  -- (5)
COMMIT
<publish to subscribers>                                                    -- (6) after commit
```

Three properties fall out of this shape, and they are the whole conflict model:

**Field-level merge is free.** An operation names only the fields it changes (step 3/4).
Two members editing different fields of one slot both succeed because neither writes the
other's column. Two members editing the *same* field are serialized by the lock, and the
later `seq` wins. No per-field version vectors, no merge tree, no transformation functions.

**Step (1) must be first.** The `UPDATE ... RETURNING` takes a row-level exclusive lock on
the trip row for the rest of the transaction, so every writer in a trip is serialized before
it reads. If the sequence were allocated *last*, two transactions could read the same slot
state concurrently and then serialize only at the sequence step — producing a lost update at
field granularity while the log looked perfectly ordered. This is the subtlest way this
design can be broken, and it is why the allocation is an explicit first statement rather than
a convenient `RETURNING` bolted onto the entity write.

**Read-modify-write becomes safe, which means the entire Stage 1 repository layer is reused
unchanged.** Because no other trip-writer can interleave between (2) and (4), the version
that was just read is guaranteed current. The existing `expectedVersion` predicates on
`Update`/`Move`/`SetSelectedOption`/`SetStatus`/`SoftDelete` still run; for sync-originated
writes they are an assertion that can never fire, not the conflict mechanism. No new
repository methods, no field-mask SQL, no second write path.

**Lock ordering is uniform** (trip row, then entity rows, always), so this cannot deadlock
against itself.

**Cost, stated plainly:** all writers within one trip serialize. This is the intent, not a
defect — it is what produces the clean per-room total order — and CLAUDE.md's non-functional
targets already say the benchmark must state its trip distribution. The single-trip
write-throughput ceiling is **TBD, to be measured**, and is not to be quoted anywhere until it is.

---

## 3. Package layout and the transport-agnostic boundary

```
internal/domain/op.go          Op types, field masks, the merge functions.  stdlib + uuid only.
internal/domain/ports.go       + OpLogRepository, OpPublisher; TripRepository.NextOpSeq
internal/service/*             mutation methods gain: tx + seq + append, via one shared helper
internal/syncengine/           room registry, presence, Submit(op) -> service dispatch, ports
internal/transport/ws/         hub, upgrade, ticket auth, frame codec.  the ONLY package that
                               knows WebSockets exist
```

Dependency direction:

```
transport/ws -> syncengine -> service -> domain <- repository
```

**The conflict-resolution logic lives in `internal/domain`.** This is deliberate and it is
the strongest available statement of "domain logic separated from transport": the merge
functions sit in the package whose arch test already forbids every import outside stdlib +
`uuid` + `pkg/*`, verified to fail on a planted `net/http` import. Conflict resolution
literally cannot reach a socket.

**`internal/syncengine` is where the "transport-agnostic" claim is proven.** It owns rooms,
presence and op dispatch, and imports zero network types. The cycle that would otherwise
appear — services need to append to the log, the engine needs to call services — is broken by
making the log an **interface in `internal/domain`** (`OpLogRepository`), so services depend
on a port, never on the engine.

### The interface boundary, concretely

```go
package syncengine

// Engine is what a transport talks to. Nothing in this file, or this package, mentions a
// connection, a socket, an HTTP request or a serialization format.
type Engine interface {
    // Subscribe registers a sink for a trip's operation stream, after authorizing the
    // subscriber as a member. Returns the room's current seq so the caller knows its
    // starting point. sinceSeq is honoured in Slice 2; Slice 1 accepts and ignores it,
    // returning ErrResyncRequired for any non-zero value.
    Subscribe(ctx context.Context, tripID, userID domain.ID, sinceSeq int64, sink Sink) (Subscription, error)

    // Submit applies one client intent. It returns the committed operations — plural,
    // because one intent may resolve into more than one (see §5.3).
    Submit(ctx context.Context, in Intent) ([]domain.Op, error)

    // Presence returns who is currently in a room.
    Presence(ctx context.Context, tripID, userID domain.ID) ([]Participant, error)
}

// Sink receives operations and presence changes for a subscribed room. A WebSocket
// connection implements this; so does a test double, and so would SSE or a long-poll.
//
// Deliver MUST NOT block. The hub calls it while holding no locks, but a sink that blocks
// stalls that room's fan-out for every other member. Implementations buffer and, on
// overflow, close themselves and report ErrSlowConsumer. See §8.8.
type Sink interface {
    Deliver(ctx context.Context, ev Event) error
}

type Subscription interface{ Close() error }
```

`Intent` is the pre-commit form (what a client asked for); `domain.Op` is the post-commit
form (what actually happened, with a `Seq`). The distinction is the subject of §5.3 and it is
the least obvious part of this design.

### Arch enforcement (new, and it must be proven to fail)

Two additions to `tests/arch_test.go`:

1. A new layer rule: `internal/syncengine` may not import `internal/transport`,
   `internal/middleware`, `internal/repository`, or `cmd`. And a new forbidden entry on the
   existing `internal/service` rule: it may not import `internal/syncengine` — that is the
   import that would recreate the cycle and quietly move log-appending out of the one place
   it is allowed to live.

2. `TestSyncEngineIsTransportAgnostic`: walks `internal/syncengine` and fails on any import
   that is `net/http`, `net`, or whose path contains `websocket`, `redis`, or `grpc`.

Per the standing convention in this repo, neither is reported as done until it has been
**verified to fail against a planted violation** — a `net/http` import and a
`github.com/coder/websocket` import in a syncengine file, and a `syncengine` import in
`internal/service`. An arch rule that has never failed is decoration.

---

## 4. Operation model

### 4.1 The envelope

```go
// domain.Op is a COMMITTED operation: an immutable, resolved record of one change that
// actually happened, in a trip's total order.
type Op struct {
    ID         ID        // server-assigned, UUIDv7
    TripID     ID
    Seq        int64     // per-trip, gapless, monotonic. THE ordering authority.
    ActorID    *ID       // nil if the acting user was later hard-deleted
    Kind       OpKind    // versioned: "slot.edit.v1"
    EntityID   ID        // the slot / option / vote row this touches
    Fields     []string  // the field mask. explicit, never inferred.
    Payload    []byte    // JSON, shape determined by Kind
    ClientOpID *ID       // nil for REST-originated ops
    CauseOpID  *ID       // set on derived ops (§5.3)
    CreatedAt  time.Time // server clock. advisory: never used for ordering.
}
```

`Kind` carries a version suffix. The log is immutable and will outlive the current payload
shapes; a decoder that meets a v1 payload in three years must be able to say so rather than
mis-parse it. This is one character of insurance against a class of bug that is otherwise
unfixable after the fact.

### 4.2 Vocabulary, locked for Slice 1

| Kind | Entity | Class |
|---|---|---|
| `slot.create.v1` | slot | create |
| `slot.edit.v1` | slot | field-level merge (`kind`, `title`, `notes`, `start_time`, `end_time`) |
| `slot.move.v1` | slot | field-level merge (`day_id` + `position`, always together) |
| `slot.select_option.v1` | slot | field-level merge (`selected_option_id`) |
| `slot.set_status.v1` | slot | field-level merge (`status`, `status_changed_at`, `status_changed_by`) |
| `slot.delete.v1` | slot | tombstone register |
| `option.create.v1` | slot_option | create |
| `option.edit.v1` | slot_option | field-level merge (`title`, `notes`, `external_url`, `estimated_cost_minor`, `place_*`) |
| `option.delete.v1` | slot_option | tombstone register |
| `vote.set.v1` | option_vote | LWW register |

`slot.move`, `slot.select_option` and `slot.set_status` are separate kinds rather than
`slot.edit` field masks, matching the existing service and repository method granularity and
their distinct capability checks (`CapReorderSlots` vs `CapEditSlots`). Collapsing them would
make a reorder indistinguishable from a content edit at the one layer that is supposed to
keep that distinction meaningful.

Deliberately absent, and why: days (`day.*`) are lower-traffic and add nothing the slot cases
do not already prove; budget and attachments are the coarse-grained and broadcast-only
classes and their services do not exist yet (Stage 1 deferred them on purpose); comments are
Stage 3. **If Slice 1 runs short on time, cut day ops and `slot.set_status` — never the
convergence test on slots + options + votes.** This restates CLAUDE.md's own instruction in
the place the cut would actually be made.

### 4.3 The field mask is explicit on the wire

An edit carries `fields: ["title","start_time"]` **and** a values object. It does not rely on
JSON key presence.

This matters more than it looks. `start_time` is nullable, so "not touched" and "explicitly
cleared" are different operations with different meanings, and a decoder that cannot tell
them apart silently coarsens conflict granularity — the exact failure D6 rejected JSONB place
data to avoid, one level up. Go-side the mask is a struct of pointers; the wire form is the
explicit list, because the wire form is also what gets written into the immutable log and
read back years later by a different decoder.

### 4.4 Why there are no per-field timestamps

An LWW-register map normally stores a timestamp per field. This design stores none, because
the sequencer already provides a total order and the server always applies in `seq` order by
construction (the seq is allocated inside the same lock as the apply — apply order *is* seq
order, always).

Per-field timestamps would be write amplification on `slots`, the hottest write path in the
system, in service of an order-insensitivity property that has no consumer. The condition
under which they would become necessary is precise and worth recording: **if the sequencer is
ever removed — true multi-master, or client-side merge of two divergent histories — per-field
timestamps become mandatory.** Redis fan-out in Slice 2 does *not* trigger this: the sequence
is still assigned in Postgres under a row lock, and Redis only moves already-ordered ops
between instances.

### 4.5 `trips.op_seq` is a column, not a Postgres SEQUENCE

A `SEQUENCE` is non-transactional: a rolled-back transaction burns its number and leaves a
gap. A counter column incremented inside the transaction rolls back with everything else, so
the log is **gapless**, and a client can treat `seq` contiguity as a completeness check
rather than merely a hint. That is worth a row lock we were taking anyway.

The `op_seq` update touches `op_seq` only — **not `trips.version`, not `trips.updated_at`**.
Otherwise every slot edit would invalidate every REST client's cached trip version and
produce spurious 409s on unrelated trip updates.

---

## 5. Conflict mechanics — the three required cases

### 5.1 Concurrent same-field / different-field edits

- **Different fields, same slot.** A sets `title`, B sets `notes`. Both are applied; both
  survive. This is the case that distinguishes this system from Stage 1's
  last-writer-*rejected* `ErrVersionConflict`, and it is the primary assertion of the
  convergence test.
- **Same field, same slot.** A sets `title="X"` (seq 41), B sets `title="Y"` (seq 42). Final
  state is `"Y"` on the server and on both clients. A's operation is **not discarded** — it is
  in the log at seq 41. "Last writer wins" describes the visible state, not the record.
- **`slot.move` masks `day_id` and `position` together** and is applied as one unit. A move
  must never be observable as a delete plus an insert; the repository's `Move` already
  guarantees this and the op preserves it.

### 5.2 Concurrent reorder — does fractional indexing carry it?

**Yes, and nothing extra is needed at the engine layer.** This was checked rather than
assumed:

- *Two members move different slots into the same gap.* Both may legitimately produce the
  same position key. Ordering is `(position, id)`, and the id tiebreak is deterministic, so
  every replica derives an identical total order. The index
  `slots_day_pos (day_id, position, id)` and the `COLLATE "C"` on the column — which pins
  Postgres byte ordering to Go's string comparison independent of database locale — are both
  already in place from Stage 1.
- *Two members move the same slot to different places.* Two `slot.move` ops; the position
  register is LWW; the later seq wins. Converges.
- *A member moves a slot relative to a neighbour that another member just deleted.* Handled
  by §5.3: the position key is resolved server-side under the trip lock, against
  post-serialization state.

One thing fractional indexing does *not* carry: key length. The 128-character CHECK is a hard
error, and a rebalance pass is **not implemented**. An op that would exceed the bound is
rejected with a distinct error code (`position_exhausted`) rather than truncating. Reaching
it requires pathological repeated insertion at one position; the failure is loud by design.

### 5.3 The log stores resolved effects, never derivations

This is the least obvious decision in the document and the one most worth reviewing.

A client sends `slot.create { after_slot_id: S }`. The server calls `NeighbourPositions` and
`fracdex.KeyBetween` to derive a position key. **That derivation must never be replayed.**
Re-running it during a resync, months later, against a list whose neighbours have all changed,
produces a *different* key — and convergence dies silently. So the log stores
`position: "a1V"`, not `after_slot_id: S`.

The same principle resolves the one cascade in the vocabulary. Deleting a slot's selected
option clears `selected_option_id` in the same transaction (Stage 1, D56). Rather than making
every client re-derive that cascade, **one intent may commit as more than one operation**:

```
seq 88  option.delete.v1     entity=O1  cause=<intent id>
seq 89  slot.select_option.v1 entity=S1  fields=[selected_option_id] values={null}  cause=<intent id>
```

Consequences, all of them good:

- A log entry is uniformly *one entity, one set of field changes*.
- Replay is a **pure fold** — assign the named fields, in seq order. Clients contain zero
  domain logic and cannot drift from the server's version of a cascade rule.
- `CauseOpID` lets a client correlate a multi-op result back to the intent it submitted, and
  lets an audit view say "these two changes were one action".
- The invariant that everything rests on becomes checkable: **`fold(log) == database state`**.
  That equality is an assertion in the convergence test, not a claim in a document.

### 5.4 Concurrent delete versus edit

`deleted_at` is its own LWW register. An edit that arrives after a delete **is applied to its
own fields, and the entity stays deleted.**

The alternative — dropping edits to tombstoned entities — was rejected because it breaks
`fold(log) == database state`, which is the one invariant worth protecting above local
tidiness. Applying the edit also means a future undelete restores content rather than a stale
snapshot.

The user-visible behaviour is honest rather than silent: the editor's change is accepted, and
they also receive the `slot.delete` op, so the UI can say "this was deleted by Priya" instead
of quietly losing the edit. Slice 1 delivers the protocol for that; the UI treatment is
Stage 3.

**Carried-over gap, not silently closed:** soft-deleting a slot does not tombstone its
options (Stage 1 flagged this). Sync does not change it. An option created concurrently under
a slot that is being deleted will exist, live, under a deleted slot — invisible because
nothing lists a deleted slot's options. This is a product decision, not a technical one, and
it stays open.

### 5.5 Votes — same mechanism, and that is the point

**Votes use the general mechanism with no special-case code.** A vote is the degenerate case
of the general rule: an entity whose field mask always names exactly one field, with no
tombstone. `vote.set.v1` goes through the same seq allocation, the same transaction, the same
log.

That is precisely why it is the cleanest proof in the system — it exercises the machinery
with everything incidental stripped away. The only thing it *cannot* exhibit is the
different-fields-merge case, because it has one field.

Two details:

- No `expectedVersion`. `VoteService.Cast` already takes none, deliberately: requiring one
  would make a member's second click fail rather than simply win, which is not what changing
  your mind means.
- **Open product question, flagged rather than decided:** should a vote for a *soft-deleted*
  option still count in `Tally`? Today `Tally` excludes only retractions. Options are soft-deleted,
  so a vote for a deleted option remains representable and currently counts. Both answers are
  defensible; this needs a decision before the voting UI in Stage 3, not before Slice 1.

### 5.6 Offline edit, then reconnect

The data model must support this from Slice 1 even though the flow ships in Slice 2.

**The version vector for a room is one integer.** Because there is exactly one sequencer per
trip, a client's complete sync state is `(trip_id, last_seq)` — no per-actor vector, no
per-entity clocks. This is a direct dividend of §2 and it is what makes resume a range scan:

```sql
SELECT * FROM trip_ops WHERE trip_id = $1 AND seq > $2 ORDER BY seq
```

not a full re-fetch.

**Offline writes** are buffered client-side with client-generated UUIDv7 op ids (D4 exists
precisely so a client can name an entity before the server has seen it) and replayed on
reconnect. Replay must be idempotent: a client that sent an op, lost the connection before
the ack, and retried must not double-apply. Field-mask sets happen to be idempotent, but
`slot.create` and anything with a cascade are not. Hence:

```sql
CREATE UNIQUE INDEX trip_ops_client_op_uq ON trip_ops (trip_id, client_op_id)
    WHERE client_op_id IS NOT NULL;
```

A duplicate `client_op_id` returns the **already-committed seq** as a normal ack. The client
cannot tell, and does not need to.

**What Slice 1 delivers of this:** the schema, the client-op-id uniqueness, the seq semantics,
and `Subscribe`'s `sinceSeq` parameter — which Slice 1 accepts and answers with
`resync_required` for any non-zero value. The wire protocol does not change in Slice 2; only
the server's answer does.

---

## 6. Schema (migration `000004_sync.up.sql`)

```sql
ALTER TABLE trips ADD COLUMN op_seq bigint NOT NULL DEFAULT 0;

CREATE TABLE trip_ops (
    id           uuid PRIMARY KEY,
    trip_id      uuid   NOT NULL REFERENCES trips (id) ON DELETE CASCADE,
    seq          bigint NOT NULL,
    actor_id     uuid REFERENCES users (id) ON DELETE SET NULL,
    kind         text   NOT NULL,
    entity_id    uuid   NOT NULL,
    fields       text[] NOT NULL DEFAULT '{}',
    payload      jsonb  NOT NULL,
    client_op_id uuid,
    cause_op_id  uuid,
    created_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT trip_ops_seq_positive CHECK (seq > 0),
    CONSTRAINT trip_ops_trip_seq_uq  UNIQUE (trip_id, seq)
);

-- The resync scan, and the only query shape this table serves.
CREATE INDEX trip_ops_trip_seq ON trip_ops (trip_id, seq);

CREATE UNIQUE INDEX trip_ops_client_op_uq ON trip_ops (trip_id, client_op_id)
    WHERE client_op_id IS NOT NULL;
```

No `UPDATE` or `DELETE` statement is ever generated against this table. "Immutable" is
enforced by the absence of a mutating query plus a `schema_verify.sql` assertion, not by a
comment.

**`payload` is `jsonb`, and this does not contradict D6.** D6 rejected JSONB for *entity*
data because field-level conflict resolution needs discrete columns. The log is the exact
inverse: append-only, never merged, never queried by field, and deliberately polymorphic
across ten op kinds. The reasoning that makes columns right for `slot_options` is the
reasoning that makes JSONB right here. Called out because a reviewer would otherwise, and
correctly, flag it.

**Retention is unbounded in Stage 2.** No pruning, no compaction. A heavily-edited trip
accumulates rows forever. The protocol is designed so pruning can be added without a wire
change: `trips` would gain an `op_seq_floor`, and a `sinceSeq` below it already returns
`resync_required`. Recorded as a known limitation, not a hidden one.

New `schema_verify.sql` assertions: `(trip_id, seq)` uniqueness rejects a duplicate; the
partial client-op index rejects a replayed client op; `seq > 0` holds.

---

## 7. Transport — WebSocket (Slice 1)

### 7.1 Library

**Proposed: `github.com/coder/websocket`** (formerly `nhooyr.io/websocket`) — context-aware
read/write that respects deadlines and cancellation, and zero transitive dependencies. The
alternative, `gorilla/websocket`, is more widely deployed but its deadline-and-callback API
fights CLAUDE.md's "context propagation everywhere" rule at every call site.

**This needs your approval.** CLAUDE.md's stack table says "Go — REST (Chi) + WebSocket hub"
without naming a library, so the choice is in scope, but it is a new dependency and the rule
is ask before substituting.

### 7.2 Ticket auth (D10, implemented at last)

```
POST /api/v1/ws/ticket        Authorization: Bearer <access token>
  -> 201 { "ticket": "<43 chars base64url>", "expires_at": "..." }

GET  /api/v1/ws?ticket=<...>  (Upgrade)
```

- The ticket is **user-scoped, not trip-scoped.** One socket serves every trip a user has
  open, and membership is authorized per-room at `subscribe` time — which is where it
  belongs, since it can be revoked while a socket is open.
- 32 random bytes from `pkg/secrets`, stored as a hash, 30-second TTL, single-use, consumed
  atomically.
- A query-string credential is acceptable **here specifically** because it is single-use and
  30 seconds old; a copy in an access log is worthless. This is the D10 reasoning and it does
  not generalize to the access token, which stays header-only per D31.
- **Origin is checked on the handshake** as defence in depth. WebSocket handshakes are not
  subject to CORS, and a cookie-authenticated socket would be trivially hijackable
  cross-site (CSWSH). The ticket design already defeats that — an attacker's page cannot mint
  a ticket without the bearer token — but "already defeated by another mechanism" is not a
  reason to leave the check out.

**Ticket storage:** an in-memory `TicketStore` port for Slice 1, Redis in Slice 2. A
30-second single-use credential does not belong in Postgres — that is a write and a read per
handshake for data that is garbage in half a minute. **Stated limitation:** in-memory tickets
do not survive multi-instance deployment (minted on A, redeemed on B). Slice 1 is
single-instance by definition; the port makes the Slice 2 swap one file.

### 7.3 Connection lifecycle and known limits

- On upgrade: consume ticket → resolve user → verify the session is still active.
- **Known Slice 1 gap, flagged not hidden:** session revocation is checked at connect only. A
  socket opened before a logout survives it. Remedy, deferred to Slice 2 with Redis: publish
  session revocations and close matching sockets. Interim mitigation: a maximum connection
  lifetime (12h) forcing periodic re-authentication.
  > **CLOSED in Slice 4 (D91).** Revocations are published to every instance and matching
  > sockets are closed, so a logout or password reset now takes effect in milliseconds. The
  > 12-hour lifetime survives as a backstop for a revocation that never arrives (Redis
  > unreachable, or an instance partitioned at that moment), not as the primary bound.
- Heartbeat: protocol-level ping every 30s, close on two missed pongs.
- Graceful shutdown drains rooms and closes with a normal status code, so clients reconnect
  rather than treating it as an error.

### 7.4 Frames

```
->  subscribe        { trip_id, since_seq? }
<-  subscribed       { trip_id, seq, presence: [...] }
->  op               { client_op_id, trip_id, kind, entity_id, fields, values }
<-  ack              { client_op_id, seq }
<-  op               { seq, trip_id, kind, entity_id, actor_id, fields, values,
                       client_op_id?, cause_op_id? }
<-  error            { client_op_id?, code, message }
<-  presence         { trip_id, event: joined|left, user_id, at }
<-  resync_required  { trip_id, reason }
```

The hub's entire job is: decode a frame into a `syncengine.Intent`, call `Submit`, and encode
`Event`s from its `Sink` back into frames. **That is the literal proof of transport-agnostic**
— the engine's two entry points take and return domain types, and swapping WebSockets for SSE
would touch one package.

### 7.5 Presence

Slice 1 scope: the set of `(user_id, connection_id)` in each room, held in memory in the hub,
with join/leave broadcast to the room. Rich presence (idle / viewing / editing, cursor
position) is Stage 3.

**Presence is never written to the op log.** It is ephemeral state, not a mutation; logging it
would pollute a permanent, immutable record with transient noise and inflate every resync
with data that is stale by the time it is read.

### 7.6 Optimistic UI support (protocol only; the UI is Stage 3)

The protocol must support it now or Stage 3 requires a wire change. A client keeps its
authoritative fold plus a pending queue keyed by `client_op_id`; the local view is
`fold(authoritative) + pending overlay`. On ack or on receiving its own op back, the pending
entry is dropped. This is why `client_op_id` appears on the outbound `op` frame and not only
on the `ack`: a client's own op arriving via broadcast must be recognizable as its own.

---

## 8. Failure cases

| # | Case | Behaviour |
|---|---|---|
| 1 | Op for a trip the actor is not a member of | `error{code: not_found}`. No log entry. Never `forbidden` — D53: confirming a trip exists to a non-member is itself a disclosure |
| 2 | Op fails domain validation | `error` with the field violations and the `client_op_id`. Transaction rolls back, **so no seq is consumed and the log stays gapless** |
| 3 | Op lacks the capability | `error{code: forbidden}`, no log entry. Capability checks are per-op, in the service, exactly as in REST |
| 4 | Op targets a deleted entity | Applied; entity stays deleted (§5.4) |
| 5 | Op targets an entity in another trip | `not_found` via the existing `checkTrip` guard |
| 6 | Duplicate `client_op_id` | Returns the original seq as a normal ack. No second apply |
| 7 | Lock contention on a hot trip | Statement timeout → `error{code: busy}`, client retries with backoff. This is the per-trip write ceiling made visible rather than hidden behind an unbounded wait |
| 8 | **Slow consumer** | Bounded per-connection send buffer. On overflow the connection is closed with `resync_required`, **never blocking the room's fan-out.** One slow client must not stall a room — this is the classic hub bug and it is designed against, not discovered later |
| 9 | Crash between commit and publish (§2 step 6) | Subscribers miss the broadcast. Recovered on reconnect from the log. **The broadcast is a best-effort accelerator; the log is the delivery guarantee.** Slice 1 alone therefore guarantees no data loss *in the database*, not no data loss *at a client* — that is what Slice 2's resume buys, and the distinction is not blurred |
| 10 | Client's `last_seq` lags silently | Server sends a periodic seq heartbeat; a lagging client requests resume. Slice 2 |
| 11 | Position key exceeds 128 chars | `error{code: position_exhausted}`. No truncation, no silent corruption |
| 12 | Client clock is wrong | Cannot affect anything. Client timestamps are advisory and are never used for ordering — ordering is exclusively server-assigned `seq` |
| 13 | A client floods ops | Per-connection token bucket. Without it one socket monopolizes the trip lock and starves the room |
| 14 | REST write and WS write race | Both take the trip lock; whichever commits first gets the lower seq. No anomaly, by construction |

---

## 9. REST versus WebSocket log consistency — the question, answered

**There is exactly one write path, and it is the service layer.** Both transports call the
same service methods. The seq allocation and log append happen *inside* those methods, in the
same transaction as the mutation, via one shared helper embedded in each planning service —
the same pattern D52 already established for `authz`, for the same reason: one implementation
of a cross-cutting concern rather than six that drift.

Therefore:

- A REST `PATCH /slots/{id}` appends to the log **and** is broadcast to WS subscribers.
- A WS `slot.edit` takes the identical path.
- "Everything since seq N" is complete by construction, for both origins.
- The only way to break it is to add a write that does not go through a service — which is
  the specific thing `tests/arch_test.go` already fails on (`transport -> repository`), and
  the reason CLAUDE.md calls rule 3 the most important line in the file.

**The publish happens after commit, not inside the transaction.** Publishing inside would let
a rolled-back transaction emit a phantom operation that no client could ever reconcile —
strictly worse than the dropped-broadcast failure (§8, case 9), which the log already
recovers from.

### Two conflict semantics, one method

REST clients today send `expectedVersion` and expect last-writer-*rejected*. Sync clients
must not. Rather than forking the service methods, `expectedVersion` becomes `*int`:

- **non-nil** — precondition enforced, `ErrVersionConflict` on mismatch (today's REST behaviour, unchanged)
- **nil** — merge semantics, no precondition

Stated as a principle: **conflict semantics are a property of the request, not of the
transport.** A REST client that wants merge semantics may omit the version; a WS client that
wants a precondition may supply one. One method, both behaviours, explicit at the call site.

Note this is the one Stage 1 API change: services gain a `TxManager` and the planning
services gain the log helper.

---

## 10. The convergence test — the thing that earns the claim

Full rigour, treated with the same weight as the Stage 1 concurrency tests, regardless of the
leaner bar elsewhere.

### 10.1 The assertion

Every scenario ends with the same four-way equality:

```
fold(trip_ops) == database state == client A's state == client B's state
```

One helper performs all four comparisons. That equality *is* the claim; everything else in
the test is setup.

### 10.2 Shape

Real Postgres via testcontainers, real HTTP server, real WebSocket connections, two
authenticated members of one trip. Both clients block on a barrier, then submit conflicting
ops simultaneously. Under `-race`.

| # | Scenario | What it proves |
|---|---|---|
| a | Same slot, **different fields** (A: title, B: notes) | Field-level merge. **The primary assertion** — this is what separates the system from `ErrVersionConflict` |
| b | Same slot, **same field** | Deterministic LWW; loser's op still present in the log at the lower seq |
| c | **Concurrent reorder** — A moves S after S1, B moves S after S2 | Identical final position everywhere; §5.2 confirmed rather than assumed |
| d | **Votes** — two members vote for different options | Both survive, tally 1/1 |
| e | **Votes** — one member from two connections | Single row, deterministic winner, register shape holds |
| f | **Delete versus edit** | Both clients agree it is deleted; the edit is in the log; no error, no divergence |
| g | **Concurrent option create** under one slot | Both exist, deterministic order |
| h | **REST write racing a WS write** | The §9 claim, tested rather than asserted |

### 10.3 Fuzz variant

*k* clients × *m* random ops from the vocabulary against one trip, seeded RNG so a failure
reproduces exactly, then the four-way equality. This is where an unconsidered interleaving
shows up.

### 10.4 What the pure unit tests do — and honestly do not — prove

A permutation test ("apply these ops in every order, get the same state") would be
**trivially true** under this design, because ops are sorted by seq before folding. Presenting
it as a convergence proof would be dishonest. So the domain-level unit tests claim something
narrower and real:

- the merge function writes **only** masked fields and never clobbers an unmasked one
- `fold(log)` is deterministic and total
- tombstone-register semantics (§5.4) hold

The **convergence claim rests on the racing WebSocket test in §10.2**, because the concurrency
in this system lives in the *submission race*, not in application order. Said plainly here so
that nobody later mistakes a fast green unit test for the proof.

---

## 11. Trade-offs, collected

1. **Sequencer-based, not multi-master.** Convergence depends on Postgres. Not offline-first
   between peers. Correct for a product where the server is always the authority.
2. **Per-trip write serialization.** The design's cost and its central benchmark question.
   Ceiling TBD — to be measured, then recorded in CLAUDE.md as a number.
3. **Field-level, not character-level merge.** Concurrent `notes` editing is last-writer-wins
   for the whole field.
4. **Unbounded log growth.** No pruning in Stage 2; the protocol is designed to accept it later.
5. **Broadcast is best-effort.** Slice 1 guarantees durability, not client delivery; Slice 2's
   resume closes that.
6. **In-memory ticket store** does not survive multi-instance; Slice 2 moves it to Redis.
7. **Session revocation does not close open sockets** in Slice 1. **Closed in Slice 4 (D91)** —
   see §7.3.
8. **No per-field timestamps** — a deliberate omission with a precisely stated trigger for
   revisiting it (§4.4).

---

## 12. Open questions — resolved 2026-08-08

1. **WebSocket library** — `github.com/coder/websocket`. **Approved.**
2. **Package name `internal/syncengine`** — **Approved.**
3. **Votes for soft-deleted options** — **Do not count.** Filtered at the query level, in
   `TallyVotesForSlot`, consistent with every other soft-delete visibility rule in the
   codebase. Vote *rows* are still retained (an option undelete restores the group's
   expressed preferences). Recorded as D71.
4. **`expectedVersion *int`** — **Approved, single method.** nil = merge semantics, a value =
   409 on mismatch. Explicitly not forked into sync/REST variants: that would duplicate
   business logic and recreate the two-write-path drift Rule 3 and D1 exist to prevent.
   Recorded as D69. This is the one real Stage 1 API change; call sites are enumerated in the
   slice report.
5. **Op vocabulary cut order** — **Confirmed unchanged:** slots/options/votes convergence
   proven first; budget/attachments only after.

   **One deviation from §4.2, made deliberately:** day ops (`day.create`/`edit`/`move`/`delete`)
   are **included** in Slice 1 rather than held as the first cut. Omitting them would leave a
   REST-originated day change absent from the log, so a client asking "everything since seq N"
   would silently miss it — the precise failure Rule 3 exists to close, and not a trade worth
   making to save four op kinds. Trip metadata and membership remain unlogged and are
   re-fetched on resync; that boundary is stated in D72 rather than left implicit.

---

## 13. Proposed decisions log entries (D59–D70)

For CLAUDE.md, on approval.

| # | Decision | Rationale |
|---|---|---|
| D59 | CRDT (field-level LWW-register map under a server total order), not OT | OT's payoff is index-relative sequence transformation, which D2's fractional indexing designed away in Stage 1. For scalar assignment, OT degenerates to LWW with two more functions to get wrong |
| D60 | `op_seq` is allocated as the **first** statement of the write transaction | It takes the trip row lock before any entity read, which is what makes read-modify-write safe and field-level merge free. Allocating it last would permit lost updates while the log looked correctly ordered |
| D61 | `trips.op_seq` is a counter column, never a Postgres `SEQUENCE` | A sequence is non-transactional: a rollback gaps it. A column rolls back, so the log is gapless and `seq` contiguity is a usable completeness check |
| D62 | The op log stores **resolved effects**, never derivations | A replayed derivation (`after_slot_id` → position key) evaluated against a changed base state produces a different result, and convergence dies silently |
| D63 | One client intent may commit as **multiple ops**, linked by `cause_op_id` | Makes a log entry uniformly one-entity-one-change, so replay is a pure fold and clients hold zero cascade logic |
| D64 | The field mask is explicit on the wire, not inferred from JSON key presence | "Untouched" and "explicitly cleared" are different operations. Inferring them is the D6 failure mode one level up, and the log outlives the decoder that wrote it |
| D65 | Op `Kind` carries a version suffix (`slot.edit.v1`) | The log is immutable and will outlive its payload shapes. One character of insurance against a class of bug that is unfixable after the fact |
| D66 | No per-field timestamps | The sequencer already gives a total order; they would be write amplification on the hottest path with no reader. Trigger for revisiting is stated precisely: removal of the single sequencer |
| D67 | `trip_ops.payload` is `jsonb`, deliberately inverting D6 | D6's reasoning applies to merged entity data. The log is append-only, never merged, never queried by field, and polymorphic across op kinds |
| D68 | Conflict-resolution logic lives in `internal/domain`; rooms and dispatch in `internal/syncengine`; sockets only in `internal/transport/ws` | Puts the merge functions inside the strictest existing arch test. The service→domain-port direction for the log breaks the cycle that would otherwise force the engine into the service layer |
| D69 | `expectedVersion` becomes `*int`: conflict semantics are a property of the request, not the transport | Avoids forking every planning service method into sync and REST variants that would drift |
| D70 | The publish happens **after** commit; the log is the delivery guarantee, the broadcast an accelerator | Publishing inside the transaction lets a rollback emit a phantom op no client can reconcile — strictly worse than a dropped broadcast, which the log already recovers from |
| D71 | Votes for soft-deleted options do not count in the tally, filtered at the query level | Consistent with every other soft-delete visibility rule. Filtering in SQL keeps one definition of "visible" rather than two that drift. Vote rows are retained so an undelete restores preferences |
| D72 | The op log covers the itinerary (days, slots, options, votes); trip metadata and membership are not logged in Slice 1 | Bounded and stated rather than accidental. Days are logged despite being a proposed cut candidate, because omitting them would hide a REST-originated change from resync |

---

## 14. Claims-discipline status after Slice 1

Nothing is ticked in CLAUDE.md until the corresponding test exists and passes.

| Clause | Slice 1 outcome |
|---|---|
| transport-agnostic sync engine | ✅ `TestSyncEngineIsTransportAgnostic` passes and was verified to fail on planted `net/http` and `github.com/coder/websocket` imports |
| OT/CRDT conflict resolution | ✅ approved as D59–D72; the merge functions live in `internal/domain` |
| concurrent multi-user edits | ✅ 11 scenarios in `tests/convergence_api_test.go`, all green under `-race` |
| Redis pub/sub for horizontal scaling | ⬜ Slice 2. One instance plus a Redis client that compiles earns nothing |
| Clean Architecture | ✅ extended to a 10th layer (`internal/syncengine`) |

### Corrections to this document, found during implementation

Two things this design got wrong, discovered by tests and fixed in code:

1. **§5.6 / D61 implied a strictly monotonic client fold.** It is wrong to reject an operation
   whose sequence number the replica has already applied. Delivery is at-least-once by
   construction (the broadcast is an accelerator, the log is the guarantee), so redelivery
   after a retry, a resume or a reconnect is NORMAL. `Replica.Apply` now skips an
   already-folded sequence number and errors only on a GAP — which is the case that actually
   signals staleness. Found by `TestReplayingAClientOpIDIsIdempotent`.

2. **Nothing in §7.4 said what a client joining a room WITH history should do.** There is no
   snapshot endpoint in Slice 1, so such a client can only fold what happened after it
   arrived. The `subscribed` frame's `seq` is the baseline it must adopt. This was a real
   Slice 1 limitation, pinned by a test rather than left implicit; Slice 2 closed it.

---

## 15. Claims-discipline status after Slice 2

| Clause | Slice 2 outcome |
|---|---|
| transport-agnostic sync engine | ✅ still true with Redis in the system. Fan-out enters through `domain.OpTransport`; `internal/syncengine` imports nothing matching `redis`, and the arch test does not exempt its test files |
| Redis pub/sub for horizontal scaling | ✅ `tests/multi_instance_api_test.go`. Two fully wired instances, one Postgres, one Redis container, verified to fail on a planted `NoopTransport` for instance B |
| concurrent multi-user edits | ✅ extended across the instance boundary: two members on DIFFERENT processes editing different fields of one slot both survive, and every view still agrees |
| reconnect/resync | ✅ `tests/resync_api_test.go`, 6 scenarios. Verified to fail with the Slice 1 refusal planted back in |

### Corrections to this document, found during Slice 2

Two more, and both are more serious than the Slice 1 pair, because in each case the document
asserted something that was not true of the code it had already shipped.

3. **§2 and D70 together imply that ops reach subscribers in sequence order. They did not.**
   The sequencer establishes the order INSIDE the transaction, but `Publish` runs after the
   commit and outside the trip row lock, so two local transactions that committed in order
   could reach the broker out of order. Redis makes it far worse: pub/sub gives FIFO per
   publishing connection and nothing at all across publishers, and drops messages with no
   acknowledgement. Meanwhile `Replica.Apply` treats a gap as fatal — so the client contract
   was strictly stronger than the delivery it was given.

   This was never observed as a failure, which is the uncomfortable part: the window is
   microseconds on one instance, and a test that hit it would have looked like flakiness.
   Fixed by D75/D76 — each room owns its outbound sequence position, holds an early operation
   for a reorder window, fills a real hole FROM THE LOG, and reconciles against `trips.op_seq`
   on a timer to catch a lost last operation. That is the first place "the log is the delivery
   guarantee, the broadcast is only an accelerator" is implemented rather than merely asserted.

4. **§5.6 said resume is "a range scan, not a full re-fetch" without saying how a subscriber
   joins the room without either missing or misordering operations.** Both obvious orderings
   are broken: read-then-join loses whatever commits in between, and join-then-stream delivers
   live seq 60 while the replay is still at seq 12, which the client's fold rejects. D77
   settles it — join first, buffer live events behind a gate, replay to the sink directly,
   then release. Overlap between the replay and the buffer is expected rather than merely
   tolerated, which is only safe because of correction 1 above.

### Deliberately still open after Slice 2

> **Status note, added at the Stage 2 close-out.** The first item below was closed in Slice 4
> (D91) and is struck through rather than deleted, because the reasoning for having left it open
> — and the discipline of writing a known hole down instead of hoping nobody looked — is the
> part worth keeping. The rest of this list is still accurate.

- ~~**Session revocation does not close open sockets (D73).**~~ **Closed in Slice 4 (D91):**
  revocations are published on their own Redis channel and every instance shuts the sockets that
  match, verified by a two-instance test. `maxConnLifetime` (12h) is now a backstop for an
  undelivered revocation rather than the only bound.
- **No snapshot endpoint.** Not needed for correctness — `fold(log) == database state` means a
  client can bootstrap from `since_seq: 0` — but it is why the replay cap (D79) exists at all,
  and why a very stale client is told to re-fetch over REST instead.
- **Retention is still unbounded.** No pruning, no compaction. When it arrives, `trips` gains
  an `op_seq_floor` and a `since_seq` below it already answers `resync_required`; the wire
  protocol does not change.
- **No per-trip write-throughput number.** Still unmeasured, still not to be quoted.
