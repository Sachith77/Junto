// Package pubsub carries committed operations between server instances.
//
// It is the Redis half of the horizontal-scaling claim, and it is an ADAPTER: it implements
// domain.OpTransport and nothing above it knows Redis exists. The arch test fails on any
// import matching "redis" under internal/syncengine, so this boundary is enforced rather than
// intended — the same treatment the WebSocket library gets.
//
// # What Redis pub/sub does and does not promise
//
// It is fire-and-forget. There is no persistence, no acknowledgement, and no replay: a
// subscriber that is disconnected for a millisecond misses whatever was published in that
// millisecond, permanently. Ordering is FIFO per publishing connection and nothing at all
// across publishers, so two instances publishing seq 5 and seq 6 can be received in either
// order.
//
// Both are acceptable here, and neither is worked around at this layer. The operation log is
// the delivery guarantee (D70) and the sync engine's room dispatcher reorders by Seq and
// refills holes from the log. Adding acknowledgement or a stream with retention would buy
// nothing the log does not already provide, at the cost of a second durable record of the
// same facts that could disagree with the first.
package pubsub

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/junto/junto/internal/domain"
)

// channelPrefix namespaces this application's channels so a shared Redis is safe.
const channelPrefix = "junto:ops:"

// keepaliveChannel exists because a Redis pub/sub connection with zero subscribed channels
// has nothing to hold it open. An instance hosting no rooms would otherwise have no live
// subscriber connection to add a channel to when its first room opens.
const keepaliveChannel = channelPrefix + "keepalive"

// NewClient opens a Redis connection pool from a redis:// or rediss:// URL.
func NewClient(url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parsing REDIS_URL: %w", err)
	}
	return redis.NewClient(opts), nil
}

// OpTransport is the Redis implementation of domain.OpTransport.
type OpTransport struct {
	client *redis.Client
	log    *slog.Logger

	// instance identifies this process, so its own published operations can be recognised and
	// discarded on the way back in. Every Redis subscriber receives everything published to a
	// channel it holds, INCLUDING its own messages; the local fan-out already delivered those
	// synchronously, so echoing them would double every broadcast on the publishing instance.
	instance string

	pubsub *redis.PubSub

	mu     sync.Mutex
	joined map[domain.ID]struct{}
	closed bool
}

var _ domain.OpTransport = (*OpTransport)(nil)

// NewOpTransport builds the transport and opens its subscriber connection.
func NewOpTransport(client *redis.Client, log *slog.Logger) *OpTransport {
	if log == nil {
		log = slog.Default()
	}
	return &OpTransport{
		client:   client,
		log:      log,
		instance: domain.NewID().String(),
		pubsub:   client.Subscribe(context.Background(), keepaliveChannel),
		joined:   map[domain.ID]struct{}{},
	}
}

// InstanceID identifies this process on the wire. Exported for tests and for log correlation.
func (t *OpTransport) InstanceID() string { return t.instance }

func channelFor(tripID domain.ID) string { return channelPrefix + tripID.String() }

// Publish sends each operation to its trip's channel.
//
// Per-trip channels rather than one global channel: an instance then receives only the
// operations for trips it is actually hosting, which is what makes adding an instance reduce
// per-instance work instead of merely duplicating it. A global channel would have every
// instance decode every write in the system, and "horizontal scaling" that makes every node do
// all the work is not scaling.
func (t *OpTransport) Publish(ctx context.Context, ops []domain.Op) error {
	if len(ops) == 0 {
		return nil
	}
	pipe := t.client.Pipeline()
	for i := range ops {
		payload, err := json.Marshal(envelope{Instance: t.instance, Op: toWire(ops[i])})
		if err != nil {
			return fmt.Errorf("encoding op %s for fan-out: %w", ops[i].ID, err)
		}
		pipe.Publish(ctx, channelFor(ops[i].TripID), payload)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("publishing %d operation(s): %w", len(ops), err)
	}
	return nil
}

// Join subscribes to a trip's channel.
//
// A room that opens between an operation's commit and this call misses that operation, and
// that is fine: the room reconciles against trips.op_seq and reads whatever it missed from
// the log. Designing this call to be race-free would mean coordinating Redis subscription
// state with database commits, which is exactly the distributed problem the log exists to
// avoid having.
func (t *OpTransport) Join(ctx context.Context, tripID domain.ID) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	if _, ok := t.joined[tripID]; ok {
		t.mu.Unlock()
		return nil
	}
	t.joined[tripID] = struct{}{}
	t.mu.Unlock()

	if err := t.pubsub.Subscribe(ctx, channelFor(tripID)); err != nil {
		t.mu.Lock()
		delete(t.joined, tripID)
		t.mu.Unlock()
		return fmt.Errorf("subscribing to trip %s: %w", tripID, err)
	}
	return nil
}

// Leave unsubscribes from a trip's channel.
func (t *OpTransport) Leave(ctx context.Context, tripID domain.ID) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	if _, ok := t.joined[tripID]; !ok {
		t.mu.Unlock()
		return nil
	}
	delete(t.joined, tripID)
	t.mu.Unlock()

	if err := t.pubsub.Unsubscribe(ctx, channelFor(tripID)); err != nil {
		return fmt.Errorf("unsubscribing from trip %s: %w", tripID, err)
	}
	return nil
}

// Run delivers operations published by OTHER instances until ctx is done.
func (t *OpTransport) Run(ctx context.Context, fn func(domain.Op)) error {
	ch := t.pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			var env envelope
			if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
				// A malformed message is a peer running an incompatible build, or something
				// else writing to this channel. Dropped and logged rather than fatal: the log
				// will supply whatever this message was carrying.
				t.log.Error("discarding an undecodable operation from a peer instance",
					"channel", msg.Channel, "error", err)
				continue
			}
			if env.Instance == t.instance {
				continue // our own publish, already fanned out locally
			}
			op, err := env.Op.toDomain()
			if err != nil {
				t.log.Error("discarding a malformed operation from a peer instance",
					"channel", msg.Channel, "instance", env.Instance, "error", err)
				continue
			}
			fn(op)
		}
	}
}

// Close releases the subscriber connection. The client pool belongs to the caller.
func (t *OpTransport) Close() error {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	return t.pubsub.Close()
}

// --- wire types ---------------------------------------------------------------------------

// envelope wraps an operation with the identity of the instance that published it.
type envelope struct {
	Instance string `json:"instance"`
	Op       wireOp `json:"op"`
}

// wireOp is declared separately from domain.Op for the same reason handlers declare their own
// request and response types (D37): serialising the domain struct directly would mean that
// adding an internal field silently changes what goes over the wire to every other instance,
// and this wire format is consumed by processes that may be running a different build.
type wireOp struct {
	ID         string          `json:"id"`
	TripID     string          `json:"trip_id"`
	Seq        int64           `json:"seq"`
	ActorID    *string         `json:"actor_id,omitempty"`
	Kind       string          `json:"kind"`
	EntityID   string          `json:"entity_id"`
	Fields     []string        `json:"fields"`
	Payload    json.RawMessage `json:"payload"`
	ClientOpID *string         `json:"client_op_id,omitempty"`
	CauseOpID  *string         `json:"cause_op_id,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

func idPtrString(id *domain.ID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

func toWire(op domain.Op) wireOp {
	return wireOp{
		ID:         op.ID.String(),
		TripID:     op.TripID.String(),
		Seq:        op.Seq,
		ActorID:    idPtrString(op.ActorID),
		Kind:       string(op.Kind),
		EntityID:   op.EntityID.String(),
		Fields:     op.Fields,
		Payload:    op.Payload,
		ClientOpID: idPtrString(op.ClientOpID),
		CauseOpID:  idPtrString(op.CauseOpID),
		CreatedAt:  op.CreatedAt,
	}
}

func parseOptionalID(field string, raw *string) (*domain.ID, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	id, err := domain.ParseID(field, *raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func (w wireOp) toDomain() (domain.Op, error) {
	id, err := domain.ParseID("id", w.ID)
	if err != nil {
		return domain.Op{}, err
	}
	tripID, err := domain.ParseID("trip_id", w.TripID)
	if err != nil {
		return domain.Op{}, err
	}
	entityID, err := domain.ParseID("entity_id", w.EntityID)
	if err != nil {
		return domain.Op{}, err
	}
	actorID, err := parseOptionalID("actor_id", w.ActorID)
	if err != nil {
		return domain.Op{}, err
	}
	clientOpID, err := parseOptionalID("client_op_id", w.ClientOpID)
	if err != nil {
		return domain.Op{}, err
	}
	causeOpID, err := parseOptionalID("cause_op_id", w.CauseOpID)
	if err != nil {
		return domain.Op{}, err
	}
	if w.Seq <= 0 {
		return domain.Op{}, fmt.Errorf("operation %s has a non-positive sequence number %d", w.ID, w.Seq)
	}
	return domain.Op{
		ID:         id,
		TripID:     tripID,
		Seq:        w.Seq,
		ActorID:    actorID,
		Kind:       domain.OpKind(w.Kind),
		EntityID:   entityID,
		Fields:     domain.FieldMask(w.Fields),
		Payload:    w.Payload,
		ClientOpID: clientOpID,
		CauseOpID:  causeOpID,
		CreatedAt:  w.CreatedAt,
	}, nil
}
