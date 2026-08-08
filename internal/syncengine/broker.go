// Package syncengine is the transport-agnostic core of the real-time sync engine.
//
// # The claim this package exists to make true
//
// Nothing here imports net/http, a WebSocket library, Redis, or any other network type. The
// engine speaks in domain types and in the Sink interface below; a WebSocket connection
// implements Sink, and so would Server-Sent Events, a long poll, or a test double. Swapping
// the transport touches internal/transport/ws and nothing else.
//
// That is enforced rather than asserted: tests/arch_test.go fails on any import from this
// package matching net/http, websocket, redis or grpc, and the rule has been verified to fail
// against planted violations rather than merely passing today. Redis fan-out arrived in Slice
// 2 without weakening that rule, because it enters through domain.OpTransport.
//
// # What lives here and what does not
//
// Conflict resolution does NOT live here — it lives in internal/domain (D68), inside the
// strictest import allowlist in the repository. This package owns rooms, presence, the
// dispatch from a client intent to the service method that applies it, and the ORDERING of
// committed operations on their way out to subscribers. It is plumbing around the merge, not
// the merge.
package syncengine

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/junto/junto/internal/domain"
)

// ErrSlowConsumer is returned by a Sink that cannot keep up.
//
// It is a normal outcome, not an exceptional one: a client on a bad connection will hit it,
// and the correct response is to close that subscription and let the client reconnect and
// resume from its last sequence number. What must NEVER happen is the broker waiting for it.
var ErrSlowConsumer = errors.New("syncengine: subscriber is too slow")

// EventType distinguishes what a Sink is being handed.
type EventType string

const (
	// EventOp is a committed operation from the log.
	EventOp EventType = "op"
	// EventPresence is a member joining or leaving a room. Presence is deliberately NOT an
	// operation: it is ephemeral state, and writing it to an immutable log would pollute a
	// permanent record with transient noise and inflate every resync with data that is stale
	// before it is read.
	EventPresence EventType = "presence"
)

// Event is one thing a subscriber needs to know about.
type Event struct {
	Type     EventType
	Op       *domain.Op
	Presence *PresenceEvent
}

// PresenceEvent reports a room membership change.
type PresenceEvent struct {
	TripID domain.ID
	UserID domain.ID
	ConnID domain.ID
	Joined bool
	At     time.Time
}

// Participant is one connected member of a room.
//
// Reported per CONNECTION rather than per user, because one member with a phone and a laptop
// is genuinely two presences and collapsing them would make "who is here" wrong the moment
// one of them disconnects. Consumers that want a per-user view deduplicate.
type Participant struct {
	UserID   domain.ID
	ConnID   domain.ID
	JoinedAt time.Time
}

// Sink receives events for a subscribed room.
//
// # The one rule
//
// Deliver MUST NOT BLOCK. The broker fans out to every subscriber in a room, and a Sink that
// waits on a slow socket stalls delivery for everyone else in that trip — the classic hub bug.
// Implementations buffer into a bounded queue and return ErrSlowConsumer when it is full; the
// broker then closes that subscription rather than waiting on it. The operation log is the
// delivery guarantee (D70), so a dropped subscriber loses nothing it cannot resume.
type Sink interface {
	Deliver(ctx context.Context, ev Event) error
}

// DroppableSink is an optional Sink capability: being told the broker gave up on it.
//
// Without this, a subscriber dropped for being slow stays in the transport's own bookkeeping
// and silently receives nothing forever — the client believes it is subscribed and has no
// event that would tell it otherwise. That was a real hole in Slice 1, and it only became
// fixable now: the honest answer is "resync from your last seq", which needed resume to exist.
type DroppableSink interface {
	Dropped(tripID domain.ID, reason error)
}

// Subscription is a subscriber's handle on a room. Close is idempotent.
type Subscription struct {
	broker *Broker
	tripID domain.ID
	userID domain.ID
	connID domain.ID
	sink   Sink

	once sync.Once
}

// Close removes the subscription and announces the departure.
func (s *Subscription) Close() error {
	s.once.Do(func() { s.broker.remove(s) })
	return nil
}

// Default dispatch timings. All three are about a fan-out path that is allowed to be lossy,
// and none of them affects durability — the log has already committed.
const (
	// defaultReorderWindow is how long a room holds an out-of-order operation waiting for its
	// predecessor before going to the log for it. Two instances publishing concurrently give
	// no cross-publisher ordering, so ops routinely arrive a few hundred microseconds out of
	// order; waiting briefly costs nothing and avoids a database round trip in the common case.
	defaultReorderWindow = 150 * time.Millisecond

	// defaultReconcileInterval is how often an idle room checks the trip's sequence against
	// what it has delivered.
	//
	// This exists for the one hole the reorder window cannot cover: a LOST LAST operation.
	// A gap is only detectable when a later op arrives, so if the final op of a burst is
	// dropped by Redis, nothing would ever notice. Costs one indexed row read per active room
	// per interval, which is why it is seconds rather than milliseconds.
	defaultReconcileInterval = 5 * time.Second

	// roomInbox bounds a room's dispatch queue. Overflow is survivable rather than fatal —
	// the reconcile tick refills from the log — so this is sized for a burst, not a backlog.
	roomInbox = 512

	// repairTimeout bounds a log read on the fan-out repair path. It must not be able to
	// wedge a room's dispatch goroutine.
	repairTimeout = 10 * time.Second
)

// OpReader is the slice of the operation log the broker needs: a range scan, nothing else.
//
// Narrower than domain.OpLogRepository deliberately. The broker repairs its own broadcast and
// must never be able to write to the log — the one place operations are appended is the
// service layer (Rule 3, D1), and taking the full port here would make "the broker cannot
// append" a matter of the code happening not to, rather than of it not being able to.
type OpReader interface {
	ListSince(ctx context.Context, tripID domain.ID, seq int64, limit int) ([]domain.Op, error)
}

// SeqReader answers "how far has this trip actually got", for the reconcile check.
type SeqReader interface {
	CurrentOpSeq(ctx context.Context, tripID domain.ID) (int64, error)
}

// BrokerConfig is what a Broker needs to order and repair a room's outbound stream.
type BrokerConfig struct {
	Logger *slog.Logger

	// Ops and Trips are read-only, and used only on the REPAIR path. They are what turn "the
	// log is the delivery guarantee" from a slogan into a mechanism: a hole in the broadcast
	// is filled from the log rather than left for the client to discover.
	Ops   OpReader
	Trips SeqReader

	// Transport carries operations to and from other instances. nil means single-instance.
	Transport domain.OpTransport

	ReorderWindow     time.Duration
	ReconcileInterval time.Duration
}

// Broker owns the rooms and fans committed operations out to their subscribers, IN SEQUENCE
// ORDER.
//
// It implements domain.OpPublisher, which is how the service layer reaches it without any
// service importing this package: services depend on the port, this type satisfies it, and
// cmd/api is the only place that knows they are connected. That inversion is what keeps
// "services never import the sync engine" true, and it is enforced by the arch test.
//
// # Why ordering is the broker's job and not the client's (Slice 2)
//
// domain.Replica.Apply treats a gap as an error, because a replica that has missed an
// operation may be silently wrong about every field it holds afterwards. That contract is
// only honourable if what reaches a Sink is actually in sequence order — and in Slice 1 it
// was not quite, in a way nothing had yet forced into the open:
//
//   - Locally: transaction T1 (seq 5) must commit before T2 (seq 6) can allocate, because
//     both hold the trip row lock (D60). But Publish is called AFTER commit and outside the
//     lock, so T2's goroutine can reach Publish before T1's. Narrow, but real.
//   - Across instances: Redis pub/sub gives FIFO per publisher and NOTHING across publishers.
//     Instance A publishing seq 5 and instance B publishing seq 6 can arrive in either order,
//     and a dropped message is never retried.
//
// So each room owns a dispatch goroutine holding the next sequence number it owes its
// subscribers. An early operation waits briefly for its predecessor; if the predecessor does
// not arrive, the missing range is read FROM THE LOG, which is the authority. A room that has
// gone quiet reconciles against the trip's current seq, which is the only way to notice that
// the last operation of a burst was lost. The result is that a Sink sees a gapless,
// monotonic stream regardless of how many instances are publishing into it.
type Broker struct {
	mu    sync.RWMutex
	rooms map[domain.ID]*room

	ops       OpReader
	trips     SeqReader
	transport domain.OpTransport
	log       *slog.Logger

	reorderWindow     time.Duration
	reconcileInterval time.Duration
}

// NewBroker builds a Broker.
func NewBroker(cfg BrokerConfig) *Broker {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Transport == nil {
		cfg.Transport = domain.NoopTransport{}
	}
	if cfg.ReorderWindow <= 0 {
		cfg.ReorderWindow = defaultReorderWindow
	}
	if cfg.ReconcileInterval <= 0 {
		cfg.ReconcileInterval = defaultReconcileInterval
	}
	return &Broker{
		rooms:             map[domain.ID]*room{},
		ops:               cfg.Ops,
		trips:             cfg.Trips,
		transport:         cfg.Transport,
		log:               cfg.Logger,
		reorderWindow:     cfg.ReorderWindow,
		reconcileInterval: cfg.ReconcileInterval,
	}
}

var _ domain.OpPublisher = (*Broker)(nil)

// Run pumps operations from other instances into the local rooms until ctx is done.
//
// Single-instance wiring passes domain.NoopTransport, whose Run simply blocks — so cmd/api
// starts this unconditionally rather than branching on whether Redis is configured.
func (b *Broker) Run(ctx context.Context) error {
	return b.transport.Run(ctx, func(op domain.Op) { b.enqueue(op) })
}

// Publish fans committed operations out to a trip's subscribers and to the other instances.
//
// Called by the service layer AFTER its transaction commits (D70). It returns nothing and
// cannot fail from the caller's point of view: the operations are already durable in the log,
// so a subscriber that misses this broadcast recovers by resuming from its last sequence
// number. The broadcast is an accelerator, not the delivery guarantee — which is exactly why
// this is allowed to drop a slow subscriber, or a failed cross-instance publish, instead of
// slowing the writer down.
func (b *Broker) Publish(ctx context.Context, ops []domain.Op) {
	if len(ops) == 0 {
		return
	}
	// Local delivery first, and independent of the transport. A single-instance deployment
	// therefore behaves exactly as it did in Slice 1, and a Redis outage degrades cross-
	// instance delivery without touching the local room.
	for i := range ops {
		b.enqueue(ops[i])
	}

	// WithoutCancel because the caller's context belongs to a request that has already
	// committed. Cancelling the publish because the writer disconnected would drop a
	// broadcast every other member of the trip is waiting for.
	pubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), repairTimeout)
	defer cancel()
	if err := b.transport.Publish(pubCtx, ops); err != nil {
		// Degraded, not lost. Peers recover the operations from the log via their rooms'
		// reorder and reconcile paths.
		b.log.Warn("publishing operations to peer instances failed; peers will recover from the log",
			"trip_id", ops[0].TripID, "count", len(ops), "error", err)
	}
}

// enqueue hands one operation to its room's ordering goroutine. It never blocks.
func (b *Broker) enqueue(op domain.Op) {
	b.mu.RLock()
	r, ok := b.rooms[op.TripID]
	b.mu.RUnlock()
	if !ok {
		return // nobody is here; there is nothing to accelerate
	}

	select {
	case r.inbox <- op:
	default:
		// The room is behind. Dropping is safe precisely because the reconcile tick will
		// notice the trip's seq has moved past what was delivered and refill from the log.
		b.log.Warn("room dispatch inbox is full; the reconcile tick will refill from the log",
			"trip_id", op.TripID, "seq", op.Seq)
	}
}

// Subscribe joins a sink to a room and announces the arrival.
//
// Authorization is NOT performed here — the Engine does it before calling this, using the
// same service read gate every other reader goes through. Keeping the membership check out of
// the broker means there is still exactly one implementation of "may this user see this trip".
//
// headSeq is the trip's current sequence number, used ONLY when this call creates the room:
// it establishes where the room's outbound stream starts. Adopting the sequence of whatever
// operation happens to arrive first would be wrong — if the first two ops raced and the later
// one arrived first, the room would declare it the baseline and silently discard its
// predecessor, handing every subscriber a gap.
func (b *Broker) Subscribe(ctx context.Context, tripID, userID, connID domain.ID, headSeq int64, sink Sink) *Subscription {
	sub := &Subscription{broker: b, tripID: tripID, userID: userID, connID: connID, sink: sink}
	participant := Participant{UserID: userID, ConnID: connID, JoinedAt: time.Now().UTC()}

	b.mu.Lock()
	r, existed := b.rooms[tripID]
	if !existed {
		r = newRoom(ctx, b, tripID)
		b.rooms[tripID] = r
	}
	r.mu.Lock()
	r.subs[sub] = participant
	r.mu.Unlock()
	if !existed {
		// Started under the broker lock so that no Publish can find the room in the map
		// before its dispatcher is running. The inbox is buffered, so an operation that
		// arrives in the meantime waits rather than being lost.
		//
		//nolint:contextcheck // The dispatcher deliberately does not inherit this request's
		// cancellation. It serves every subscriber in the room and outlives the handshake
		// that happened to open it; r.ctx carries the request's values via WithoutCancel,
		// which is the whole reason the room holds a context of its own.
		go r.dispatch(headSeq + 1)
	}
	b.mu.Unlock()

	if !existed {
		//nolint:contextcheck // Derived from the room's context, not from nothing: the peer
		// subscription belongs to the room's lifetime, not to this caller's request.
		joinCtx, cancel := context.WithTimeout(r.ctx, repairTimeout)
		defer cancel()
		//nolint:contextcheck // joinCtx derives from the ROOM's context, not from this
		// caller's request. The peer subscription belongs to the room's lifetime: tying it to
		// the handshake that happened to open the room would unsubscribe every other member's
		// fan-out the moment that one request returned.
		if err := b.transport.Join(joinCtx, tripID); err != nil {
			// Recoverable: until the subscription lands, this instance simply misses peers'
			// operations, and the room's reconcile tick reads them from the log instead.
			b.log.Warn("joining the peer channel for a trip failed; falling back to the log",
				"trip_id", tripID, "error", err)
		}
	}

	b.fanout(tripID, Event{Type: EventPresence, Presence: &PresenceEvent{
		TripID: tripID, UserID: userID, ConnID: connID, Joined: true, At: participant.JoinedAt,
	}})
	return sub
}

// Presence returns who is currently in a room.
func (b *Broker) Presence(tripID domain.ID) []Participant {
	b.mu.RLock()
	r, ok := b.rooms[tripID]
	b.mu.RUnlock()
	if !ok {
		return nil
	}
	return r.participants()
}

// RoomCount reports how many rooms have at least one subscriber. Used by tests and, later, by
// metrics — an empty room must not linger, or a long-running server leaks a map entry per
// trip ever opened.
func (b *Broker) RoomCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.rooms)
}

func (b *Broker) remove(sub *Subscription) {
	b.mu.Lock()
	r, ok := b.rooms[sub.tripID]
	if !ok {
		b.mu.Unlock()
		return
	}
	r.mu.Lock()
	_, present := r.subs[sub]
	delete(r.subs, sub)
	empty := len(r.subs) == 0
	r.mu.Unlock()
	// An empty room is deleted rather than kept: otherwise a server that has been up for a
	// month holds a map entry, a goroutine and a ticker for every trip anyone ever opened.
	if empty {
		delete(b.rooms, sub.tripID)
	}
	b.mu.Unlock()

	if empty {
		//nolint:contextcheck // Same reasoning as Join: this is the room's own lifetime.
		// Subscription.Close is io.Closer-shaped and has no request context to inherit.
		leaveCtx, cancel := context.WithTimeout(r.ctx, repairTimeout)
		defer cancel()
		r.stop()
		//nolint:contextcheck // Same reasoning as Join, and Subscription.Close is
		// io.Closer-shaped: there is no request context here to inherit.
		if err := b.transport.Leave(leaveCtx, sub.tripID); err != nil {
			b.log.Warn("leaving the peer channel for a trip failed",
				"trip_id", sub.tripID, "error", err)
		}
	}

	if !present {
		return
	}
	b.fanout(sub.tripID, Event{Type: EventPresence, Presence: &PresenceEvent{
		TripID: sub.tripID, UserID: sub.userID, ConnID: sub.connID, Joined: false,
		At: time.Now().UTC(),
	}})
}

// fanout delivers a NON-OPERATION event to a room immediately.
//
// Presence deliberately skips the ordering goroutine. It carries no sequence number and no
// ordering requirement, and routing it through the dispatcher would let a presence
// announcement sit behind an operation that is waiting for a missing predecessor — a
// several-hundred-millisecond delay on the one kind of event whose entire value is being
// immediate.
func (b *Broker) fanout(tripID domain.ID, ev Event) {
	b.mu.RLock()
	r, ok := b.rooms[tripID]
	b.mu.RUnlock()
	if !ok {
		return
	}
	r.broadcast(ev)
}
