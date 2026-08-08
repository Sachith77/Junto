package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/junto/junto/internal/domain"
)

// wsClient is a REAL WebSocket client for the convergence tests.
//
// It is deliberately not a mock of anything. It performs the actual ticket handshake, opens an
// actual socket against the actual router, and — critically — maintains its own replica by
// folding the operations it receives, exactly as a browser client would. That replica is one
// of the four things the convergence assertion compares, and it would be worthless if this
// client shared any code path with the server's own view of the state.
type wsClient struct {
	t      *testing.T
	name   string
	userID domain.ID
	sock   *websocket.Conn

	mu       sync.Mutex
	replica  *domain.Replica
	frames   []map[string]json.RawMessage
	applyErr error

	// arrivals records when each operation was RECEIVED, keyed by the submitting client's op
	// id. It is what the latency benchmark measures against: the submitter knows when it sent
	// and which id it used, so subtracting gives an end-to-end figure that includes the
	// sequencer, the commit, the broker and the socket — everything a member actually waits on.
	// Nil unless trackArrivals is set, so the ordinary tests pay nothing for it.
	arrivals map[domain.ID]time.Time

	closed chan struct{}
	once   sync.Once

	// finished is closed when readLoop has returned, so a caller that intends to hand this
	// client's replica to a reconnecting one can be sure nothing is still folding into it.
	// Without it the transplant races the read loop, and the race detector is right to say so.
	finished chan struct{}
}

// dialWS performs the full D10 handshake: mint a ticket with the bearer token, then connect
// with the ticket. Any shortcut here (reusing a ticket, skipping the endpoint) would mean the
// convergence test was not exercising the auth path it claims to.
func dialWS(t *testing.T, name string, c *client, accessToken string, userID domain.ID) *wsClient {
	t.Helper()
	return dialWSOn(t, name, c, testServer.URL, accessToken, userID, domain.NewReplica())
}

// dialWSOn is dialWS with the two things a Slice 2 test needs to vary: WHICH instance to
// connect to, and an EXISTING replica to carry across a reconnect.
//
// The replica parameter is what makes the resync test model a real client rather than a fresh
// one. A browser tab that loses its connection keeps everything it had folded; it reconnects
// and asks for what happened while it was away. A test that started from an empty replica
// would be testing a first-time subscribe wearing a resume's clothes — and would fail
// immediately, because an edit to a slot created before the disconnect has nothing to apply to.
func dialWSOn(t *testing.T, name string, c *client, baseURL, accessToken string, userID domain.ID, replica *domain.Replica) *wsClient {
	t.Helper()

	res := c.do("POST", "/api/v1/ws/ticket", nil, withBearer(accessToken))
	assertStatus(t, res, 201)

	var ticket struct {
		Ticket    string    `json:"ticket"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(res.body, &ticket); err != nil {
		t.Fatalf("%s: decoding ticket: %v\nbody: %s", name, err, res.body)
	}
	if ticket.Ticket == "" {
		t.Fatalf("%s: empty ticket", name)
	}

	wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + "/api/v1/ws?ticket=" + ticket.Ticket
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	//nolint:bodyclose // the handshake response is hijacked by the upgrade; on failure Dial
	// has already drained and closed it.
	sock, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("%s: dialing websocket: %v", name, err)
	}
	sock.SetReadLimit(1 << 20)

	w := &wsClient{
		t: t, name: name, userID: userID, sock: sock,
		replica:  replica,
		closed:   make(chan struct{}),
		finished: make(chan struct{}),
	}
	go w.readLoop()
	t.Cleanup(w.close)
	return w
}

func (w *wsClient) close() {
	w.once.Do(func() {
		close(w.closed)
		_ = w.sock.Close(websocket.StatusNormalClosure, "test over")
	})
	// Wait for the fold to stop before returning, so the replica is safe to read or hand on.
	select {
	case <-w.finished:
	case <-time.After(5 * time.Second):
	}
}

// readLoop folds every operation the server sends into this client's replica.
//
// Frames are applied in arrival order, which for a single connection is send order — the
// server writes from one goroutine per connection precisely so this holds. A gap or a replay
// is recorded as applyErr rather than ignored, because a client that silently tolerated one
// would have no way left to notice it had gone stale.
func (w *wsClient) readLoop() {
	defer close(w.finished)
	for {
		select {
		case <-w.closed:
			return
		default:
		}

		_, data, err := w.sock.Read(context.Background())
		if err != nil {
			return
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			continue
		}

		received := time.Now()

		w.mu.Lock()
		w.frames = append(w.frames, raw)
		if frameType(raw) == "op" {
			if w.arrivals != nil {
				var clientOpID string
				_ = json.Unmarshal(raw["client_op_id"], &clientOpID)
				if id, idErr := domain.ParseID("client_op_id", clientOpID); idErr == nil {
					w.arrivals[id] = received
				}
			}
			if op, convErr := decodeOpFrame(raw); convErr != nil {
				if w.applyErr == nil {
					w.applyErr = convErr
				}
			} else if applyErr := w.replica.Apply(op); applyErr != nil && w.applyErr == nil {
				w.applyErr = applyErr
			}
		}
		w.mu.Unlock()
	}
}

func frameType(raw map[string]json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw["type"], &s)
	return s
}

func decodeOpFrame(raw map[string]json.RawMessage) (domain.Op, error) {
	var f struct {
		TripID     string          `json:"trip_id"`
		Seq        int64           `json:"seq"`
		OpID       string          `json:"op_id"`
		Kind       string          `json:"kind"`
		EntityID   string          `json:"entity_id"`
		ActorID    string          `json:"actor_id"`
		Fields     []string        `json:"fields"`
		Payload    json.RawMessage `json:"payload"`
		ClientOpID string          `json:"client_op_id"`
		CauseOpID  string          `json:"cause_op_id"`
		CreatedAt  time.Time       `json:"created_at"`
	}
	blob, err := json.Marshal(raw)
	if err != nil {
		return domain.Op{}, err
	}
	if err := json.Unmarshal(blob, &f); err != nil {
		return domain.Op{}, err
	}

	tripID, err := domain.ParseID("trip_id", f.TripID)
	if err != nil {
		return domain.Op{}, err
	}
	entityID, err := domain.ParseID("entity_id", f.EntityID)
	if err != nil {
		return domain.Op{}, err
	}
	op := domain.Op{
		TripID: tripID, Seq: f.Seq, Kind: domain.OpKind(f.Kind), EntityID: entityID,
		Fields: domain.FieldMask(f.Fields), Payload: f.Payload, CreatedAt: f.CreatedAt,
	}
	if f.OpID != "" {
		if id, idErr := domain.ParseID("op_id", f.OpID); idErr == nil {
			op.ID = id
		}
	}
	if f.ActorID != "" {
		if id, idErr := domain.ParseID("actor_id", f.ActorID); idErr == nil {
			op.ActorID = &id
		}
	}
	return op, nil
}

func (w *wsClient) send(frame any) {
	w.t.Helper()
	data, err := json.Marshal(frame)
	if err != nil {
		w.t.Fatalf("%s: encoding frame: %v", w.name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.sock.Write(ctx, websocket.MessageText, data); err != nil {
		w.t.Fatalf("%s: writing frame: %v", w.name, err)
	}
}

// subscribe joins a room and seeds the replica's baseline from the server's answer.
//
// The `subscribed` frame reports where the room currently is, and this client starts folding
// from THAT point rather than from zero. That models what a real client does after fetching a
// snapshot: set last_seq to the snapshot's sequence and fold forward.
//
// It also makes a genuine Slice 1 limitation visible rather than hidden. There is no snapshot
// endpoint yet, so a client joining a room that already has history holds a replica of only
// what happened after it arrived. That is exactly the hole Slice 2's resync fills, and it is
// why the convergence fixture subscribes both clients BEFORE any operation is submitted —
// their replicas are then complete by construction, and the four-way equality is meaningful.
func (w *wsClient) subscribe(tripID domain.ID) {
	w.t.Helper()
	w.send(map[string]any{"type": "subscribe", "trip_id": tripID.String()})
	frame := w.waitForFrame("subscribed", 5*time.Second)

	var seq int64
	if raw, ok := frame["seq"]; ok {
		_ = json.Unmarshal(raw, &seq)
	}
	w.mu.Lock()
	w.replica.Seq = seq
	w.mu.Unlock()
}

// resume reconnects a client that went away and asks for everything it missed.
//
// It is deliberately NOT a variant of subscribe that seeds the baseline from the server's
// answer. A resuming client already knows where it is; it TELLS the server, and the server's
// `subscribed` frame must agree. Asserting that agreement is the point — if the server
// silently reset the client to the current head, every missed operation would be skipped by
// the fold as "already applied" and a broken resume would look like a passing test.
func (w *wsClient) resume(tripID domain.ID) {
	w.t.Helper()

	w.mu.Lock()
	since := w.replica.Seq
	w.mu.Unlock()

	w.send(map[string]any{
		"type": "subscribe", "trip_id": tripID.String(), "since_seq": since,
	})
	frame := w.waitForFrame("subscribed", 10*time.Second)

	var seq int64
	if raw, ok := frame["seq"]; ok {
		_ = json.Unmarshal(raw, &seq)
	}
	if seq != since {
		w.t.Fatalf("%s: resumed from seq %d but the server answered %d; a resume must start "+
			"where the client actually is", w.name, since, seq)
	}
}

// disconnect drops the socket the way a network failure or a closed laptop does.
func (w *wsClient) disconnect() { w.close() }

// receivedSeqs reports the sequence numbers this client was sent, in arrival order.
//
// Used to prove that a resync arrived AS OPERATIONS from the log rather than as a full
// re-fetch. "The state ended up right" is not sufficient evidence for that: it would also be
// true of a client that threw everything away and reloaded.
func (w *wsClient) receivedSeqs() []int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []int64
	for _, f := range w.frames {
		var kind string
		_ = json.Unmarshal(f["type"], &kind)
		if kind != "op" {
			continue
		}
		var seq int64
		_ = json.Unmarshal(f["seq"], &seq)
		out = append(out, seq)
	}
	return out
}

// hasFrame reports whether a frame of this type has arrived, without waiting for one.
func (w *wsClient) hasFrame(frameType string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, f := range w.frames {
		var s string
		_ = json.Unmarshal(f["type"], &s)
		if s == frameType {
			return true
		}
	}
	return false
}

// submit sends one operation. It does NOT wait for the result: the convergence tests need
// two clients to submit simultaneously, and waiting here would serialize them and quietly
// remove the very race being tested.
func (w *wsClient) submit(tripID domain.ID, kind domain.OpKind, entityID domain.ID, fields []string, values map[string]any) domain.ID {
	w.t.Helper()
	clientOpID := domain.NewID()
	w.send(map[string]any{
		"type": "op", "trip_id": tripID.String(), "client_op_id": clientOpID.String(),
		"kind": string(kind), "entity_id": entityID.String(),
		"fields": fields, "values": values,
	})
	return clientOpID
}

// waitForSeq blocks until this client's replica has folded everything up to seq.
//
// This is the synchronisation point that makes the assertions meaningful: comparing replicas
// before both have caught up would test timing, not convergence. It polls rather than using a
// condition variable because the failure message needs to say how far behind the client got.
func (w *wsClient) waitForSeq(seq int64, timeout time.Duration) {
	w.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		current, err := w.replica.Seq, w.applyErr
		w.mu.Unlock()
		if err != nil {
			w.t.Fatalf("%s: folding the operation log failed: %v", w.name, err)
		}
		if current >= seq {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	w.mu.Lock()
	current := w.replica.Seq
	w.mu.Unlock()
	w.t.Fatalf("%s: timed out at seq %d, waiting for %d", w.name, current, seq)
}

// awaitResolution blocks until the server has answered a submitted operation, one way or the
// other.
//
// This is the synchronisation primitive the concurrency tests are built on, and getting it
// right matters more than it looks. Waiting on a sequence number read from the database is
// NOT sufficient: an operation still in flight has not advanced the sequence yet, so a test
// that waited on it would sail past and assert on state from before the writes. The
// authoritative signal that an operation is done is the server's answer to that specific
// client op id — the echoed operation, or an error frame naming it.
//
// An error counts as resolution. Some scenarios (delete racing an edit) have a legitimate
// loser, and treating a rejection as "still pending" would hang the test rather than let it
// assert what it is actually about.
func (w *wsClient) awaitResolution(clientOpID domain.ID, timeout time.Duration) {
	w.t.Helper()
	want := clientOpID.String()
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		w.mu.Lock()
		for _, f := range w.frames {
			var kind, got string
			_ = json.Unmarshal(f["type"], &kind)
			_ = json.Unmarshal(f["client_op_id"], &got)
			if got == want && (kind == "op" || kind == "error") {
				w.mu.Unlock()
				return
			}
		}
		w.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	w.t.Fatalf("%s: timed out waiting for the server to resolve client op %s", w.name, want)
}

func (w *wsClient) waitForFrame(frameType string, timeout time.Duration) map[string]json.RawMessage {
	w.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		for _, f := range w.frames {
			var s string
			_ = json.Unmarshal(f["type"], &s)
			if s == frameType {
				w.mu.Unlock()
				return f
			}
		}
		w.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	w.t.Fatalf("%s: timed out waiting for a %q frame", w.name, frameType)
	return nil
}

// snapshot returns a copy of the client's folded state.
func (w *wsClient) snapshot() *domain.Replica {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.applyErr != nil {
		w.t.Fatalf("%s: replica is invalid: %v", w.name, w.applyErr)
	}
	return w.replica
}

func (w *wsClient) errorFrames() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, f := range w.frames {
		var s string
		_ = json.Unmarshal(f["type"], &s)
		if s == "error" {
			out = append(out, fmt.Sprintf("%v", f))
		}
	}
	return out
}

// waitForServerClose blocks until the SERVER closed this connection, or the timeout expires.
//
// It waits on the read loop finishing, which is the only honest signal available to a client:
// a socket closed by the peer is observed as a read error, exactly as a browser would observe
// it. Returns false on timeout so the caller can fail with its own message rather than a
// generic one.
func (w *wsClient) waitForServerClose(timeout time.Duration) bool {
	select {
	case <-w.finished:
		return true
	case <-time.After(timeout):
		return false
	}
}

// trackArrivals starts recording per-operation arrival times on this client.
//
// Opt-in because the ordinary tests do not need it and a map write per frame in the fold path
// is exactly the sort of thing that quietly changes what a latency measurement is measuring.
func (w *wsClient) trackArrivals() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.arrivals == nil {
		w.arrivals = map[domain.ID]time.Time{}
	}
}

// arrivalOf returns when an operation reached this client, and whether it has yet.
func (w *wsClient) arrivalOf(clientOpID domain.ID) (time.Time, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	at, ok := w.arrivals[clientOpID]
	return at, ok
}

// stillOpen reports whether this client's connection is still live.
//
// The negative assertion in the revocation tests — "the OTHER member's socket must survive" —
// needs this, and it is the half of the specification most easily got wrong: closing every
// socket on every revocation would pass a test that only checked the intended one died.
func (w *wsClient) stillOpen() bool {
	select {
	case <-w.finished:
		return false
	default:
		return true
	}
}

// errorFrameWithCode finds a received error frame carrying a given code.
func (w *wsClient) errorFrameWithCode(code string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, f := range w.frames {
		var frameType string
		_ = json.Unmarshal(f["type"], &frameType)
		if frameType != "error" {
			continue
		}
		var got string
		_ = json.Unmarshal(f["code"], &got)
		if got == code {
			return true
		}
	}
	return false
}

// assertNoErrorFrames catches the failure mode a convergence test is most likely to hide:
// both clients agreeing perfectly because both of their operations were rejected.
func (w *wsClient) assertNoErrorFrames() {
	w.t.Helper()
	if errs := w.errorFrames(); len(errs) > 0 {
		w.t.Fatalf("%s: received %d error frame(s), so the operations under test never "+
			"applied: %v", w.name, len(errs), errs)
	}
}
