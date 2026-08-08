package service

import (
	"context"

	"github.com/junto/junto/internal/domain"
)

// oplog is the ONE place a mutation becomes an entry in the operation log.
//
// It is embedded into every planning service rather than called as a package function, which
// is the same shape — and the same reasoning — as the authz helper (D52): one implementation
// of a cross-cutting concern, instead of six that drift.
//
// # Why this must be the only place (Rule 3, D1)
//
// Stage 2 claims an immutable operation log that powers reconnect and resync. If REST writes
// bypassed the log and only the WebSocket path appended to it, a reconnecting client asking
// "everything since seq N" would silently miss every REST-originated change, and resync would
// degrade into a full re-fetch while every test still passed. Routing all writes through
// services means the log is written in exactly one place and both transports stay consistent
// by construction.
//
// # The transaction shape (D60)
//
//	LockForWrite(trip)      <- FIRST. serializes writers in this trip before any read.
//	read entity
//	apply only the named fields
//	write entity
//	NextOpSeq + Append      <- once per resulting operation
//	COMMIT
//	Publish                 <- AFTER commit (D70)
//
// The lock coming first is what makes read-modify-write safe, which in turn is why the whole
// Stage 1 repository layer is reused unchanged: no field-mask SQL, no second write path, and
// the existing optimistic-concurrency predicates become assertions that cannot fire on the
// sync path.
type oplog struct {
	trips domain.TripRepository
	ops   domain.OpLogRepository
	tx    domain.TxManager
	pub   domain.OpPublisher
}

// newOplog builds the helper, defaulting the publisher so that a REST-only wiring — and most
// tests — need not supply one.
func newOplog(trips domain.TripRepository, ops domain.OpLogRepository, tx domain.TxManager, pub domain.OpPublisher) oplog {
	if pub == nil {
		pub = domain.NoopPublisher{}
	}
	return oplog{trips: trips, ops: ops, tx: tx, pub: pub}
}

// recorder accumulates the operations one intent produces.
//
// It exists because a single intent may commit as MORE THAN ONE operation (D63) — deleting a
// slot's selected option also clears the slot's selection — and because publishing must
// happen after the transaction commits, which means the operations have to outlive the
// transaction closure.
type recorder struct {
	log     oplog
	tripID  domain.ID
	actorID domain.ID
	origin  domain.OpOrigin
	ops     []domain.Op
}

// write runs a mutation with the sequencer held, records whatever operations it produces, and
// publishes them once the transaction has committed.
func (o oplog) write(ctx context.Context, tripID, actorID domain.ID, fn func(ctx context.Context, rec *recorder) error) ([]domain.Op, error) {
	rec := &recorder{log: o, tripID: tripID, actorID: actorID, origin: domain.OpOriginFrom(ctx)}

	err := o.tx.WithinTx(ctx, func(ctx context.Context) error {
		// D60. Before any entity read, without exception. Everything else in this design
		// depends on this line being first.
		if err := o.trips.LockForWrite(ctx, tripID); err != nil {
			return err
		}
		return fn(ctx, rec)
	})
	if err != nil {
		// Nothing is published: the transaction rolled back, so the sequence numbers it
		// allocated rolled back with it and the log stays gapless (D61).
		return nil, err
	}

	// D70: after commit, never inside. Publishing inside the transaction would let a
	// rolled-back write emit a phantom operation that no client could ever reconcile —
	// strictly worse than a dropped broadcast, which the log already recovers from.
	o.pub.Publish(ctx, rec.ops)
	return rec.ops, nil
}

// record allocates a sequence number and appends one operation.
//
// The payload is always built FROM the persisted entity by the callers below, never from the
// request. That is D62 made structural: there is no code path by which an unresolved value —
// an "after_slot_id" that has not yet become a position key — can reach the log.
func (r *recorder) record(ctx context.Context, kind domain.OpKind, entityID domain.ID, mask domain.FieldMask, payload []byte) error {
	if err := mask.Validate(kind); err != nil {
		return err
	}
	seq, err := r.log.trips.NextOpSeq(ctx, r.tripID)
	if err != nil {
		return err
	}
	actor := r.actorID
	op := &domain.Op{
		ID:         domain.NewID(),
		TripID:     r.tripID,
		Seq:        seq,
		ActorID:    &actor,
		Kind:       kind,
		EntityID:   entityID,
		Fields:     mask,
		Payload:    payload,
		ClientOpID: r.origin.ClientOpID,
		CauseOpID:  r.origin.CauseOpID,
	}
	// Only the FIRST operation of an intent carries the client's op id. The uniqueness index
	// on (trip_id, client_op_id) is what makes replay idempotent, and a cascade writing the
	// same id twice would violate it — the derived operations are linked by cause_op_id
	// instead, which is exactly what it is for.
	if len(r.ops) > 0 {
		op.ClientOpID = nil
	}
	if err := r.log.ops.Append(ctx, op); err != nil {
		return err
	}
	r.ops = append(r.ops, *op)
	return nil
}

func (r *recorder) slot(ctx context.Context, kind domain.OpKind, s *domain.Slot, fields ...string) error {
	mask := maskFor(kind, fields)
	payload, err := domain.SlotPayload(s, mask)
	if err != nil {
		return err
	}
	return r.record(ctx, kind, s.ID, mask, payload)
}

func (r *recorder) option(ctx context.Context, kind domain.OpKind, o *domain.SlotOption, fields ...string) error {
	mask := maskFor(kind, fields)
	payload, err := domain.OptionPayload(o, mask)
	if err != nil {
		return err
	}
	return r.record(ctx, kind, o.ID, mask, payload)
}

func (r *recorder) day(ctx context.Context, kind domain.OpKind, d *domain.Day, fields ...string) error {
	mask := maskFor(kind, fields)
	payload, err := domain.DayPayload(d, mask)
	if err != nil {
		return err
	}
	return r.record(ctx, kind, d.ID, mask, payload)
}

// budget records a ledger operation.
//
// Callers do not pass a field list for OpBudgetSet, and could not usefully: the mask is total
// by rule (D83), so maskFor's "no fields named means everything the kind allows" fallback is
// not a convenience here but the only legal answer. domain.FieldMask.Validate refuses anything
// narrower, so a future caller that tries to name a subset is rejected before the log is
// touched rather than after the ledger is wrong.
func (r *recorder) budget(ctx context.Context, kind domain.OpKind, e *domain.BudgetEntry, fields ...string) error {
	mask := maskFor(kind, fields)
	payload, err := domain.BudgetPayload(e, mask)
	if err != nil {
		return err
	}
	return r.record(ctx, kind, e.ID, mask, payload)
}

// attachment records an attachment broadcast.
//
// There is no merge and no version here — the operation exists purely so that a client which
// was offline learns about the upload through the same replay path as everything else. Without
// it, attachments would be the one entity a resyncing client silently missed, which is the hole
// Rule 3 exists to close.
func (r *recorder) attachment(ctx context.Context, kind domain.OpKind, a *domain.Attachment, fields ...string) error {
	mask := maskFor(kind, fields)
	payload, err := domain.AttachmentPayload(a, mask)
	if err != nil {
		return err
	}
	return r.record(ctx, kind, a.ID, mask, payload)
}

func (r *recorder) vote(ctx context.Context, v *domain.Vote) error {
	mask := maskFor(domain.OpVoteSet, nil)
	payload, err := domain.VotePayload(v, mask)
	if err != nil {
		return err
	}
	return r.record(ctx, domain.OpVoteSet, v.ID, mask, payload)
}

// maskFor resolves an explicit field list, falling back to everything the kind may change.
//
// The fallback is what a whole-object REST write means: PATCH /slots/{id} without a field
// list genuinely does replace all five editable fields, and saying so in the log is accurate
// rather than lazy. A masked write names fewer, and that is the difference between a
// collaborative edit and an overwrite.
func maskFor(kind domain.OpKind, fields []string) domain.FieldMask {
	if len(fields) == 0 {
		return domain.NewFieldMask(kind.AllowedFields()...)
	}
	return domain.NewFieldMask(fields...)
}

// versionOrCurrent resolves the nilable expected version (D69).
//
// nil means "no optimistic-concurrency precondition" — the sync path, where conflicts are
// resolved by merging rather than by rejecting. Substituting the version just read is safe
// precisely because LockForWrite is held: no other writer in this trip can interleave between
// the read and the write, so the predicate cannot fail.
//
// A value means the caller asked for a precondition and gets ErrVersionConflict on mismatch,
// which is the REST behaviour, unchanged.
//
// Note this makes conflict semantics a property of the REQUEST, not of the transport: a REST
// client may omit the version and get merge semantics, and a sync client may supply one.
func versionOrCurrent(expected *int, current int) int {
	if expected == nil {
		return current
	}
	return *expected
}

// requireVersion is versionOrCurrent's counterpart for an entity that CANNOT merge (D85).
//
// D69 makes conflict semantics a property of the request: nil means "merge me". That is a
// coherent instruction only where a merge exists. For a budget entry it does not — the entry
// and its complete split set are replaced whole (D44) — so nil would have to mean something
// else, and the only candidates are both bad. Silently substituting the version just read makes
// the write a wholesale last-writer-wins overwrite, which is precisely the "someone's number
// vanished" outcome the coarse grain was chosen to prevent; refusing every concurrent write
// outright would make the ledger unusable.
//
// So a budget write must state the version it believes it is editing, and gets
// ErrVersionConflict if it is wrong. That is a real divergence from D69 rather than an
// oversight, and it is confined to entities whose kind reports Mergeable() == false — the same
// property that drives the total-mask rule, read from one place so the two cannot drift.
func requireVersion(expected *int, field string) (int, error) {
	if expected == nil {
		ve := &domain.ValidationError{}
		ve.Add(field, "version_required",
			"this entity is replaced as a whole rather than merged, so an edit must state the "+
				"version it is based on")
		return 0, ve
	}
	return *expected, nil
}
