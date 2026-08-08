package syncengine

import (
	"context"
	"sync"
	"time"

	"github.com/junto/junto/internal/domain"
)

// room is one trip's set of subscribers plus the goroutine that owns the ORDER they are
// delivered in.
//
// # The invariant this type exists to hold
//
// Every Sink in a room sees operations with strictly increasing, contiguous Seq. That is the
// contract domain.Replica.Apply is written against: a redelivery is benign and skipped, but a
// GAP is an error, because a replica that missed an operation may be wrong about every field
// it holds afterwards. Nothing else in the system re-establishes that ordering — the
// sequencer produces it in Postgres, and between the commit and the client it can be lost
// three ways:
//
//  1. Two local transactions commit in seq order but reach Publish out of order, because
//     Publish happens after the trip row lock is released (D70).
//  2. Two instances publish through Redis, which orders nothing across publishers.
//  3. Redis drops a message entirely — pub/sub has no persistence and no acknowledgement.
//
// The dispatcher handles all three with the same mechanism, and the mechanism is the log:
// hold an early operation briefly, and if its predecessor does not turn up, read the missing
// range from trip_ops. That is what "the log is the delivery guarantee, the broadcast is only
// an accelerator" means when it is written as code rather than as a comment.
type room struct {
	broker *Broker
	tripID domain.ID

	// ctx is derived from the request that opened the room with context.WithoutCancel.
	//
	// Derived, so tracing and logging values survive; NOT cancellable by that request,
	// because the room outlives it. A room whose lifetime was tied to the HTTP request that
	// created it would tear its dispatcher down the moment the first subscriber's handshake
	// handler returned, taking every other subscriber's delivery with it.
	ctx context.Context

	mu   sync.RWMutex
	subs map[*Subscription]Participant

	// inbox carries operations to the dispatcher. Writers never block on it (see enqueue).
	inbox chan domain.Op

	done  sync.Once
	stopC chan struct{}
}

func newRoom(ctx context.Context, b *Broker, tripID domain.ID) *room {
	return &room{
		broker: b,
		tripID: tripID,
		ctx:    context.WithoutCancel(ctx),
		subs:   map[*Subscription]Participant{},
		inbox:  make(chan domain.Op, roomInbox),
		stopC:  make(chan struct{}),
	}
}

func (r *room) stop() { r.done.Do(func() { close(r.stopC) }) }

func (r *room) participants() []Participant {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Participant, 0, len(r.subs))
	for _, p := range r.subs {
		out = append(out, p)
	}
	return out
}

// broadcast delivers one event to every subscriber.
//
// The subscriber list is snapshotted under the lock and delivered outside it. Holding the
// lock across Deliver would mean one subscriber's buffer state gating every other room's
// traffic, and would deadlock outright the moment a Sink's error path called Close.
func (r *room) broadcast(ev Event) {
	r.mu.RLock()
	targets := make([]*Subscription, 0, len(r.subs))
	for sub := range r.subs {
		targets = append(targets, sub)
	}
	r.mu.RUnlock()

	for _, sub := range targets {
		if err := sub.sink.Deliver(r.ctx, ev); err != nil {
			// A subscriber that cannot keep up is dropped, never waited on. It will reconnect
			// and resume from its last sequence number; the log has everything it missed.
			r.broker.log.Warn("dropping sync subscriber",
				"trip_id", r.tripID, "user_id", sub.userID, "conn_id", sub.connID, "error", err)
			go func(sub *Subscription, err error) {
				_ = sub.Close()
				// Telling the sink is the difference between a client that reconnects and a
				// client that sits believing it is subscribed while receiving nothing.
				if d, ok := sub.sink.(DroppableSink); ok {
					d.Dropped(r.tripID, err)
				}
			}(sub, err)
		}
	}
}

// dispatch owns the room's outbound sequence position for the life of the room.
//
// next is the sequence number the room owes its subscribers. It starts at the trip's current
// seq plus one, supplied by the caller that created the room, rather than being adopted from
// the first operation to arrive — see Broker.Subscribe for why that difference matters.
func (r *room) dispatch(next int64) {
	pending := map[int64]domain.Op{}

	reconcile := time.NewTicker(r.broker.reconcileInterval)
	defer reconcile.Stop()

	// The reorder timer is armed only while something is waiting on a missing predecessor, so
	// a healthy room runs no timer at all.
	var gap *time.Timer
	var gapC <-chan time.Time
	arm := func() {
		if len(pending) == 0 || gapC != nil {
			return
		}
		gap = time.NewTimer(r.broker.reorderWindow)
		gapC = gap.C
	}
	disarm := func() {
		if gap != nil {
			gap.Stop()
		}
		gap, gapC = nil, nil
	}
	defer disarm()

	for {
		select {
		case <-r.stopC:
			return

		case op := <-r.inbox:
			if op.Seq >= next {
				pending[op.Seq] = op
				next = r.drain(next, pending)
			}
			// Anything below next was already delivered: a duplicate from the local fan-out
			// and a peer, or an op the repair path had already read from the log.
			if len(pending) == 0 {
				disarm()
			} else {
				arm()
			}

		case <-gapC:
			gap, gapC = nil, nil
			next = r.fill(next, pending, false)
			arm()

		case <-reconcile.C:
			next = r.reconcile(next, pending)
			arm()
		}
	}
}

// drain emits every pending operation that is now contiguous.
func (r *room) drain(next int64, pending map[int64]domain.Op) int64 {
	for {
		op, ok := pending[next]
		if !ok {
			return next
		}
		delete(pending, next)
		r.broadcast(Event{Type: EventOp, Op: &op})
		next++
	}
}

// fill reads the missing range from the operation log.
//
// Reached when an operation has been held for the reorder window and its predecessor never
// arrived — which means the broadcast for that predecessor was lost, not merely late. The log
// is gapless by construction (D61: op_seq is a column that rolls back with its transaction,
// not a sequence that burns numbers), so if a later operation exists then every operation
// before it does too, and this read cannot come back empty for a real hole.
//
// force makes it read even with nothing pending, which is what reconcile needs: a lost LAST
// operation leaves the room behind while holding nothing at all.
func (r *room) fill(next int64, pending map[int64]domain.Op, force bool) int64 {
	if len(pending) == 0 && !force {
		return next
	}
	ctx, cancel := context.WithTimeout(r.ctx, repairTimeout)
	defer cancel()

	ops, err := r.broker.ops.ListSince(ctx, r.tripID, next-1, 0)
	if err != nil {
		r.broker.log.Error("repairing a gap in a room's outbound stream failed",
			"trip_id", r.tripID, "next_seq", next, "error", err)
		return next
	}
	r.broker.log.Warn("filling a gap in a room's outbound stream from the operation log",
		"trip_id", r.tripID, "next_seq", next, "found", len(ops))

	for i := range ops {
		if ops[i].Seq >= next {
			pending[ops[i].Seq] = ops[i]
		}
	}
	return r.drain(next, pending)
}

// reconcile checks the room against the trip's authoritative sequence.
//
// This covers the case the reorder window structurally cannot: a LOST LAST operation. A gap
// is only visible when a later operation arrives, so if the final op of a burst is dropped in
// transit, no amount of waiting reveals it. Comparing against trips.op_seq does, and it is the
// same "periodic seq heartbeat" the design doc's failure table (row 10) reserved for Slice 2.
func (r *room) reconcile(next int64, pending map[int64]domain.Op) int64 {
	ctx, cancel := context.WithTimeout(r.ctx, repairTimeout)
	defer cancel()

	head, err := r.broker.trips.CurrentOpSeq(ctx, r.tripID)
	if err != nil {
		r.broker.log.Warn("reconciling a room against the trip sequence failed",
			"trip_id", r.tripID, "error", err)
		return next
	}
	if head < next && len(pending) == 0 {
		return next // caught up: the common case, and it costs one indexed row read
	}
	return r.fill(next, pending, true)
}
