package pubsub

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/junto/junto/internal/domain"
)

// revocationChannel is a SINGLE global channel, deliberately unlike the per-trip operation
// channels next door (D80).
//
// The reasoning that made per-trip channels right for operations is what makes one channel
// right here. An instance can subscribe only to the trips it hosts because it knows which those
// are; it cannot subscribe only to the sessions it holds, because the instance processing a
// logout has no idea which instance holds that user's socket — that is the entire problem being
// solved. So the fan-out is unavoidably global.
//
// It is affordable because revocations happen at HUMAN frequency. A busy trip generates
// operations continuously; a user logs out once. Sending every instance every revocation costs
// a decode of a small message a few times a minute, against the alternative of a session
// registry that every instance would have to keep consistent.
const revocationChannel = "junto:revocations"

// RevocationTransport is the Redis implementation of domain.RevocationTransport.
type RevocationTransport struct {
	client *redis.Client
	log    *slog.Logger

	// instance is used to discard our own publishes on the way back in. The publisher closes
	// its local sockets synchronously before publishing, so a loopback would be pure duplicate
	// work on the one instance already finished.
	instance string

	pubsub *redis.PubSub
}

var _ domain.RevocationTransport = (*RevocationTransport)(nil)

// NewRevocationTransport builds the transport and opens its subscriber connection.
//
// It takes its own *redis.PubSub rather than sharing the operation transport's. Sharing would
// couple two failure domains that have nothing to do with each other: a subscriber connection
// churning through per-trip subscribe and unsubscribe calls as rooms open and close is exactly
// the connection you do not want carrying the message that closes a compromised session.
func NewRevocationTransport(client *redis.Client, log *slog.Logger) *RevocationTransport {
	if log == nil {
		log = slog.Default()
	}
	return &RevocationTransport{
		client:   client,
		log:      log,
		instance: domain.NewID().String(),
		pubsub:   client.Subscribe(context.Background(), revocationChannel),
	}
}

// InstanceID identifies this process on the wire. Exported for tests and log correlation.
func (t *RevocationTransport) InstanceID() string { return t.instance }

// Publish broadcasts a revocation to every other instance.
func (t *RevocationTransport) Publish(ctx context.Context, ev domain.RevocationEvent) error {
	payload, err := json.Marshal(revocationEnvelope{
		Instance: t.instance,
		Event:    toWireRevocation(ev),
	})
	if err != nil {
		return fmt.Errorf("encoding revocation for fan-out: %w", err)
	}
	if err := t.client.Publish(ctx, revocationChannel, payload).Err(); err != nil {
		return fmt.Errorf("publishing revocation for user %s: %w", ev.UserID, err)
	}
	return nil
}

// Run delivers revocations published by OTHER instances until ctx is done.
func (t *RevocationTransport) Run(ctx context.Context, fn func(domain.RevocationEvent)) error {
	ch := t.pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			var env revocationEnvelope
			if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
				// Logged at ERROR rather than dropped quietly: unlike an operation, there is no
				// log to recover this from. An undecodable revocation means some session
				// somewhere keeps a live socket until its lifetime cap fires, and that is worth
				// being able to find afterwards.
				t.log.Error("discarding an undecodable revocation from a peer instance",
					"error", err)
				continue
			}
			if env.Instance == t.instance {
				continue // our own publish; local sockets were closed before it was sent
			}
			ev, err := env.Event.toDomain()
			if err != nil {
				t.log.Error("discarding a malformed revocation from a peer instance",
					"instance", env.Instance, "error", err)
				continue
			}
			fn(ev)
		}
	}
}

// Close releases the subscriber connection. The client pool belongs to the caller.
func (t *RevocationTransport) Close() error { return t.pubsub.Close() }

// --- wire types ---------------------------------------------------------------------------

type revocationEnvelope struct {
	Instance string         `json:"instance"`
	Event    wireRevocation `json:"event"`
}

// wireRevocation is declared separately from domain.RevocationEvent for the same reason wireOp
// is (D37): this format is consumed by processes that may be running a different build, so
// adding an internal field must not silently change what crosses the wire.
type wireRevocation struct {
	Scope     string    `json:"scope"`
	UserID    string    `json:"user_id"`
	SessionID string    `json:"session_id,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	At        time.Time `json:"at"`
}

func toWireRevocation(ev domain.RevocationEvent) wireRevocation {
	w := wireRevocation{
		Scope:  string(ev.Scope),
		UserID: ev.UserID.String(),
		Reason: ev.Reason,
		At:     ev.At,
	}
	if ev.SessionID != domain.NilID {
		w.SessionID = ev.SessionID.String()
	}
	return w
}

func (w wireRevocation) toDomain() (domain.RevocationEvent, error) {
	userID, err := domain.ParseID("user_id", w.UserID)
	if err != nil {
		return domain.RevocationEvent{}, err
	}

	scope := domain.RevokeScope(w.Scope)
	if scope != domain.RevokeScopeSession && scope != domain.RevokeScopeUser {
		return domain.RevocationEvent{}, fmt.Errorf("unknown revocation scope %q", w.Scope)
	}

	ev := domain.RevocationEvent{Scope: scope, UserID: userID, Reason: w.Reason, At: w.At}
	if w.SessionID != "" {
		sessionID, err := domain.ParseID("session_id", w.SessionID)
		if err != nil {
			return domain.RevocationEvent{}, err
		}
		ev.SessionID = sessionID
	}
	// A session-scoped event with no session id would match nothing and close nothing — a
	// silent no-op where the caller expected a socket to die. Rejecting it means the failure is
	// visible in a log line instead of in a credential that outlives its revocation.
	if scope == domain.RevokeScopeSession && ev.SessionID == domain.NilID {
		return domain.RevocationEvent{}, fmt.Errorf("session-scoped revocation carries no session id")
	}
	return ev, nil
}
