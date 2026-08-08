package ws

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/time/rate"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/syncengine"
)

// sendBuffer is how many pending frames a connection may accumulate before it is considered
// too slow and dropped.
//
// The number matters less than the fact that it is BOUNDED. An unbounded queue turns a slow
// client into unbounded server memory; blocking instead of buffering turns one slow client
// into a stalled room. 256 is generous enough that a healthy client on a bad network never
// reaches it, and small enough that a thousand stuck connections cost megabytes, not gigabytes.
const sendBuffer = 256

const (
	pingInterval = 30 * time.Second
	pingTimeout  = 10 * time.Second
	writeTimeout = 10 * time.Second

	// maxConnLifetime forces periodic re-authentication.
	//
	// It WAS the entire bound on D73 — a session verified once at the handshake and never
	// re-checked, so a socket outlived its own logout for up to twelve hours. That gap is
	// closed (D91): revocations are published to every instance and matching sockets are shut,
	// so a logout, a password reset, or a reuse-detected token family now takes effect in
	// milliseconds rather than at this deadline.
	//
	// It is kept anyway, and demoted to what it should always have been: a backstop for the
	// case where the revocation never arrives — Redis unreachable at the moment of the logout,
	// or an instance that was partitioned while it happened. That is a real failure mode with
	// no other bound, so the constant stays and it still must not be raised casually.
	maxConnLifetime = 12 * time.Hour

	// revokeDrainTimeout is how long a revoked connection is given to flush its final frame
	// before the socket is torn down. Short by design: telling the client WHY it was
	// disconnected is a courtesy, and revocation must never wait on a client to accept it.
	revokeDrainTimeout = 2 * time.Second

	// readLimit caps a single incoming frame.
	readLimit = 1 << 20
)

// conn is one client socket.
//
// It implements syncengine.Sink, which is the entire surface the engine sees. Everything
// WebSocket-shaped stops here.
type conn struct {
	id     domain.ID
	userID domain.ID
	// sessionID is what makes revocation possible (D91). It comes from the handshake ticket
	// and is never re-read: the connection does not poll the session table, it waits to be told.
	sessionID domain.ID
	ws        *websocket.Conn
	engine    *syncengine.Engine
	log       *slog.Logger
	limiter   *rate.Limiter

	send chan []byte

	mu   sync.Mutex
	subs map[domain.ID]*syncengine.Subscription

	closeOnce sync.Once
	closed    chan struct{}
}

var (
	_ syncengine.Sink          = (*conn)(nil)
	_ syncengine.DroppableSink = (*conn)(nil)
)

// Deliver queues an event for this connection. It NEVER blocks.
//
// A full buffer means this client cannot keep up, and the answer is to say so and be dropped
// — not to make the writer wait. The broker closes the subscription on the error, the client
// reconnects, and the operation log has everything it missed (D70). Blocking here instead
// would let one client on a bad connection stall delivery for everyone else in the trip.
func (c *conn) Deliver(_ context.Context, ev syncengine.Event) error {
	frame, err := encodeEvent(ev)
	if err != nil {
		return err
	}
	select {
	case c.send <- frame:
		return nil
	case <-c.closed:
		return syncengine.ErrSlowConsumer
	default:
		return syncengine.ErrSlowConsumer
	}
}

func encodeEvent(ev syncengine.Event) ([]byte, error) {
	switch ev.Type {
	case syncengine.EventOp:
		op := ev.Op
		f := opFrame{
			Type: FrameOpBroadcast, TripID: op.TripID.String(), Seq: op.Seq,
			OpID: op.ID.String(), Kind: string(op.Kind), EntityID: op.EntityID.String(),
			Fields: op.Fields, Payload: op.Payload, CreatedAt: op.CreatedAt,
		}
		if op.ActorID != nil {
			f.ActorID = op.ActorID.String()
		}
		// Echoed back so the submitting client can retire the matching optimistic change.
		// This is why there is no separate ack frame: the operation IS the acknowledgement,
		// and it carries the server-assigned seq and resolved values that an ack would not.
		if op.ClientOpID != nil {
			f.ClientOpID = op.ClientOpID.String()
		}
		if op.CauseOpID != nil {
			f.CauseOpID = op.CauseOpID.String()
		}
		return json.Marshal(f)

	case syncengine.EventPresence:
		p := ev.Presence
		event := "left"
		if p.Joined {
			event = "joined"
		}
		return json.Marshal(presenceFrame{
			Type: FramePresence, TripID: p.TripID.String(), Event: event,
			UserID: p.UserID.String(), ConnID: p.ConnID.String(), At: p.At,
		})
	}
	return nil, errors.New("ws: unknown event type")
}

// run drives the connection until it closes.
func (c *conn) run(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, maxConnLifetime)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); c.writePump(ctx) }()
	go func() { defer wg.Done(); c.readPump(ctx) }()
	wg.Wait()

	c.shutdown()
}

// writePump is the ONLY goroutine that writes to the socket.
//
// coder/websocket permits one concurrent writer; funnelling every frame through this loop is
// what makes that guarantee hold without a mutex around the socket itself.
func (c *conn) writePump(ctx context.Context) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closed:
			return

		case frame := <-c.send:
			writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.ws.Write(writeCtx, websocket.MessageText, frame)
			cancel()
			if err != nil {
				c.close()
				return
			}

		case <-ticker.C:
			// A dead TCP connection is otherwise indistinguishable from an idle one, and the
			// server would hold the goroutines and room membership of a client that vanished.
			pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
			err := c.ws.Ping(pingCtx)
			cancel()
			if err != nil {
				c.close()
				return
			}
		}
	}
}

func (c *conn) readPump(ctx context.Context) {
	c.ws.SetReadLimit(readLimit)
	for {
		select {
		case <-c.closed:
			return
		default:
		}

		_, data, err := c.ws.Read(ctx)
		if err != nil {
			c.close()
			return
		}

		// Per-connection rate limit. Without it one socket can monopolise its trip's row lock
		// and starve every other member of that room — the write path is serialized per trip
		// by design, so an unthrottled client is a denial of service against its own group.
		if !c.limiter.Allow() {
			c.sendError("", codeRateLimited, "too many operations; slow down", nil)
			continue
		}

		var frame clientFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			c.sendError("", codeBadFrame, "frame is not valid JSON", nil)
			continue
		}
		c.handle(ctx, frame)
	}
}

func (c *conn) handle(ctx context.Context, frame clientFrame) {
	switch frame.Type {
	case FrameSubscribe:
		c.handleSubscribe(ctx, frame)
	case FrameUnsubscribe:
		c.handleUnsubscribe(frame)
	case FrameOp:
		c.handleOp(ctx, frame)
	default:
		c.sendError(frame.ClientOpID, codeBadFrame, "unknown frame type", nil)
	}
}

// handleSubscribe joins a room and, for a resuming client, replays what it missed.
//
// The frame order matters and is the reason the engine splits Subscribe from Replay:
// `subscribed` carries the sequence number the client is folding FROM, so it must arrive
// before the operations that fold onto it. A client that received the replayed operations
// first would have nothing to apply them to.
func (c *conn) handleSubscribe(ctx context.Context, frame clientFrame) {
	tripID, err := domain.ParseID("trip_id", frame.TripID)
	if err != nil {
		c.sendError("", codeBadFrame, "trip_id must be a UUID", nil)
		return
	}

	res, err := c.engine.Subscribe(ctx, tripID, c.userID, c.id, frame.SinceSeq, c)
	if errors.Is(err, syncengine.ErrResyncRequired) {
		c.sendResync(tripID, err)
		return
	}
	if err != nil {
		c.sendErrorFor(err, "")
		return
	}

	c.mu.Lock()
	if existing, ok := c.subs[tripID]; ok {
		// Re-subscribing replaces the old subscription rather than accumulating a second one,
		// which would double every broadcast to this connection.
		_ = existing.Close()
	}
	c.subs[tripID] = res.Subscription
	c.mu.Unlock()

	members := make([]presenceMember, 0, len(res.Presence))
	for _, p := range res.Presence {
		members = append(members, presenceMember{
			UserID: p.UserID.String(), ConnID: p.ConnID.String(), JoinedAt: p.JoinedAt,
		})
	}
	c.queue(subscribedFrame{
		Type: FrameSubscribed, TripID: tripID.String(), Seq: res.Seq, Presence: members,
	})

	// Blocks this connection's read pump for the duration of the catch-up. That is
	// intentional: an operation submitted by this client while its own replay is still
	// streaming would be applied against a replica it has not finished rebuilding.
	if err := res.Replay(ctx); err != nil {
		if !errors.Is(err, syncengine.ErrResyncRequired) {
			c.log.Error("replaying the operation log for a subscriber failed",
				"conn_id", c.id, "user_id", c.userID, "trip_id", tripID, "error", err)
		}
		c.sendResync(tripID, err)
	}
}

// sendResync tells a client to rebuild from the REST surface and subscribe again.
//
// The reason string is deliberately non-specific about sequence numbers: a client's correct
// response is the same in every case, and the detail belongs in the server log where it can
// be correlated with the connection that produced it.
func (c *conn) sendResync(tripID domain.ID, cause error) {
	c.log.Info("telling a subscriber to resync", "conn_id", c.id, "trip_id", tripID, "reason", cause)
	c.queue(resyncFrame{
		Type: FrameResyncRequired, TripID: tripID.String(),
		Reason: "your starting point cannot be replayed; discard local state, re-fetch the trip, " +
			"then subscribe again without a since_seq",
	})
}

// Dropped is the broker telling this connection it gave up on one of its subscriptions.
//
// Answering with resync_required rather than closing the socket is the point of having the
// notification at all: the client keeps its other rooms, re-fetches this one, and resubscribes.
// Before Slice 2 there was nothing useful to say here, so a dropped subscriber simply went
// quiet and the client never found out.
func (c *conn) Dropped(tripID domain.ID, reason error) {
	c.mu.Lock()
	delete(c.subs, tripID)
	c.mu.Unlock()
	c.sendResync(tripID, reason)
}

func (c *conn) handleUnsubscribe(frame clientFrame) {
	tripID, err := domain.ParseID("trip_id", frame.TripID)
	if err != nil {
		c.sendError("", codeBadFrame, "trip_id must be a UUID", nil)
		return
	}
	c.mu.Lock()
	sub, ok := c.subs[tripID]
	delete(c.subs, tripID)
	c.mu.Unlock()

	if ok {
		_ = sub.Close()
	}
	c.queue(simpleFrame{Type: FrameUnsubscribed, TripID: tripID.String()})
}

func (c *conn) handleOp(ctx context.Context, frame clientFrame) {
	tripID, err := domain.ParseID("trip_id", frame.TripID)
	if err != nil {
		c.sendError(frame.ClientOpID, codeBadFrame, "trip_id must be a UUID", nil)
		return
	}
	clientOpID, err := domain.ParseID("client_op_id", frame.ClientOpID)
	if err != nil {
		c.sendError(frame.ClientOpID, codeBadFrame, "client_op_id must be a UUID", nil)
		return
	}
	// entity_id is required for everything except a create, where the CLIENT still supplies
	// it — D4 exists so an entity can be named before the server has seen it.
	entityID, err := domain.ParseID("entity_id", frame.EntityID)
	if err != nil {
		c.sendError(frame.ClientOpID, codeBadFrame, "entity_id must be a UUID", nil)
		return
	}

	// Subscription is required before submitting. Not for authorization — the engine checks
	// membership itself — but because an unsubscribed client would never see its own
	// operation come back, and would sit forever holding an optimistic change it thinks is
	// unacknowledged.
	c.mu.Lock()
	_, subscribed := c.subs[tripID]
	c.mu.Unlock()
	if !subscribed {
		c.sendError(frame.ClientOpID, codeNotSubscribed, "subscribe to the trip before sending operations", nil)
		return
	}

	replayed, err := c.engine.Submit(ctx, syncengine.Intent{
		TripID:     tripID,
		ActorID:    c.userID,
		ClientOpID: clientOpID,
		Kind:       domain.OpKind(frame.Kind),
		EntityID:   entityID,
		Fields:     domain.NewFieldMask(frame.Fields...),
		Values:     frame.Values,
	})
	if err != nil {
		c.sendErrorFor(err, frame.ClientOpID)
		return
	}
	// Non-nil only for an idempotent replay: the operation was already committed, so nothing
	// was broadcast and this connection alone gets the answer it lost.
	for i := range replayed {
		if payload, encErr := encodeEvent(syncengine.Event{Type: syncengine.EventOp, Op: &replayed[i]}); encErr == nil {
			c.queueRaw(payload)
		}
	}
}

// sendErrorFor maps a domain error onto the wire.
//
// The mapping mirrors the HTTP transport's exactly, including the part that matters most: a
// non-member gets not_found rather than forbidden (D53). A WebSocket is not an excuse to
// disclose that a trip exists to someone with no access to it.
func (c *conn) sendErrorFor(err error, clientOpID string) {
	if ve, ok := domain.AsValidationError(err); ok {
		violations := make([]fieldViolation, 0, len(ve.Violations))
		for _, v := range ve.Violations {
			violations = append(violations, fieldViolation{Field: v.Field, Code: v.Code, Message: v.Message})
		}
		c.sendError(clientOpID, codeValidation, "the operation was rejected", violations)
		return
	}

	switch {
	case errors.Is(err, domain.ErrNotFound):
		c.sendError(clientOpID, codeNotFound, "not found", nil)
	case errors.Is(err, domain.ErrForbidden):
		c.sendError(clientOpID, codeForbidden, "you do not have permission to do that", nil)
	case errors.Is(err, domain.ErrVersionConflict):
		// Reachable only when the client explicitly supplied a version. The sync path passes
		// none, and merges instead.
		c.sendError(clientOpID, codeConflict, "someone else wrote first; refresh and retry", nil)
	case errors.Is(err, context.DeadlineExceeded):
		// Lock contention on a hot trip. Surfaced as a retryable condition rather than an
		// unbounded wait: the per-trip write ceiling made visible instead of hidden.
		c.sendError(clientOpID, codeBusy, "the trip is busy; retry shortly", nil)
	default:
		c.log.Error("sync operation failed", "conn_id", c.id, "user_id", c.userID, "error", err)
		c.sendError(clientOpID, codeInternal, "the operation could not be applied", nil)
	}
}

func (c *conn) sendError(clientOpID, code, message string, violations []fieldViolation) {
	c.queue(errorFrame{
		Type: FrameError, ClientOpID: clientOpID, Code: code, Message: message,
		Violations: violations,
	})
}

func (c *conn) queue(frame any) {
	payload, err := json.Marshal(frame)
	if err != nil {
		c.log.Error("encoding ws frame", "conn_id", c.id, "error", err)
		return
	}
	c.queueRaw(payload)
}

func (c *conn) queueRaw(payload []byte) {
	select {
	case c.send <- payload:
	case <-c.closed:
	default:
		// Same rule as Deliver: a client that cannot keep up is closed, never waited on.
		c.close()
	}
}

// close tears the connection down: the signal channel AND the socket.
//
// # Why CloseNow belongs here and not only in shutdown
//
// readPump spends its life parked in ws.Read, which does not watch the closed channel and has
// no reason to return until a frame arrives or the socket dies. So closing the channel alone
// stops the WRITE pump and leaves the read pump blocked — and because run waits for both before
// calling shutdown, the socket is never actually closed. The connection then lingers until the
// client happens to send something or the 12-hour lifetime cap fires.
//
// That was a real bug on every server-initiated close, found by the revocation tests: a revoked
// socket sat there fully alive because nothing woke the reader. It also affected the
// slow-consumer path, where queueRaw drops a connection that cannot keep up — precisely the
// client least likely to send a frame that would unblock it.
//
// CloseNow rather than a graceful handshake: every caller has already decided this connection
// is finished, and it is safe to call concurrently with a read or a write, which a
// close-handshake write would not be while the write pump is running.
func (c *conn) close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		_ = c.ws.CloseNow()
	})
}

// revoke tells the client its credential died, then closes the socket.
//
// The frame is sent BEFORE closing, and it matters more than it looks. Without it the client
// sees an ordinary disconnect, and an ordinary disconnect means "reconnect and resume" — so it
// would fetch a ticket, be refused because the session is gone, and be left to infer from an
// authentication failure what it could have simply been told. Naming the reason turns a
// reconnect loop into a redirect to the login screen.
//
// It is best-effort by construction: queueRaw never blocks, so a client that is already gone or
// too slow to receive it is closed just the same. Delivery of this frame is a courtesy; the
// close is the guarantee.
func (c *conn) revoke(reason string) {
	if reason == "" {
		reason = "your session is no longer valid"
	}
	c.queue(errorFrame{
		Type:    FrameError,
		Code:    codeSessionRevoked,
		Message: reason,
	})

	// Give the write pump a moment to flush that frame before tearing the socket down. Bounded
	// and non-blocking: revocation must not wait on a client, so a connection that has not
	// drained by the deadline is closed with the frame undelivered — which is exactly the
	// trade-off named above.
	go func() {
		timer := time.NewTimer(revokeDrainTimeout)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-c.closed:
		}
		c.close()
	}()
}

// shutdown releases room membership and the socket. Presence departure is announced by
// closing each subscription, which is what tells the rest of the room this member left.
func (c *conn) shutdown() {
	c.close()

	c.mu.Lock()
	subs := make([]*syncengine.Subscription, 0, len(c.subs))
	for _, sub := range c.subs {
		subs = append(subs, sub)
	}
	c.subs = map[domain.ID]*syncengine.Subscription{}
	c.mu.Unlock()

	for _, sub := range subs {
		_ = sub.Close()
	}
	_ = c.ws.CloseNow()
}
