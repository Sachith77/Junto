package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/junto/junto/internal/domain"
)

// Transport-level tests for the WebSocket surface: the D10 ticket handshake, room
// authorization, and presence. The conflict-resolution behaviour is in
// convergence_api_test.go; this file is about the socket in front of it.

func mintTicket(t *testing.T, c *client, token string) string {
	t.Helper()
	resp := c.do(http.MethodPost, "/api/v1/ws/ticket", nil, withBearer(token))
	assertStatus(t, resp, http.StatusCreated)

	var out struct {
		Ticket    string    `json:"ticket"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(resp.body, &out); err != nil {
		t.Fatalf("decoding ticket: %v\nbody: %s", err, resp.body)
	}
	return out.Ticket
}

func dialRaw(t *testing.T, ticket string) (*websocket.Conn, error) {
	t.Helper()
	return dialRawOn(t, testServer.URL, ticket)
}

// dialRawOn is a bare handshake against a named instance, with no subscribe — for asserting
// whether a ticket is accepted at all. Taking the base URL is what lets the multi-instance
// tests mint on one server and redeem on another.
func dialRawOn(t *testing.T, baseURL, ticket string) (*websocket.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	//nolint:bodyclose // the handshake response is hijacked by the upgrade; on failure Dial
	// has already drained and closed it, which is why every dial site in this file ignores it.
	sock, _, err := websocket.Dial(ctx,
		"ws"+strings.TrimPrefix(baseURL, "http")+"/api/v1/ws?ticket="+ticket, nil)
	return sock, err
}

// TestWSTicketIsSingleUse is the property the whole D10 argument rests on.
//
// A query-string credential is only acceptable because it is worthless the instant it is
// redeemed. If a ticket could be replayed, a copy in an access log, a proxy log or browser
// history would be a working credential — which is precisely the objection that ruled out
// putting the access token there (D31).
func TestWSTicketIsSingleUse(t *testing.T) {
	c := newClient(t)
	email := signupAndVerify(t, c, "ws-ticket")
	token := login(t, c, email)

	ticket := mintTicket(t, c, token)

	sock, err := dialRaw(t, ticket)
	if err != nil {
		t.Fatalf("first use of a fresh ticket failed: %v", err)
	}
	_ = sock.Close(websocket.StatusNormalClosure, "done")

	if second, err := dialRaw(t, ticket); err == nil {
		_ = second.Close(websocket.StatusNormalClosure, "")
		t.Error("the same ticket was accepted twice; a leaked ticket would be a live credential")
	}
}

// TestWSTicketRequiresAuthentication keeps the chain intact: the ticket derives its authority
// from a bearer token a cross-site attacker cannot produce. If this endpoint were reachable
// without one, the handshake credential could be minted by anybody and the CSWSH protection
// would be gone.
func TestWSTicketRequiresAuthentication(t *testing.T) {
	c := newClient(t)
	resp := c.do(http.MethodPost, "/api/v1/ws/ticket", nil)
	if resp.status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for an unauthenticated ticket request", resp.status)
	}
}

func TestWSRejectsAMissingOrForgedTicket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	base := "ws" + strings.TrimPrefix(testServer.URL, "http") + "/api/v1/ws"
	for _, url := range []string{base, base + "?ticket=not-a-real-ticket"} {
		if sock, _, err := websocket.Dial(ctx, url, nil); err == nil {
			_ = sock.Close(websocket.StatusNormalClosure, "")
			t.Errorf("the handshake succeeded without a valid ticket: %s", url)
		}
	}
}

// TestWSSubscribeRefusesANonMember checks that a socket is not a way around the authorization
// the HTTP surface enforces — and that the refusal is not_found rather than forbidden (D53),
// because confirming a trip exists to someone with no access is itself a disclosure.
func TestWSSubscribeRefusesANonMember(t *testing.T) {
	owner := newClient(t)
	ownerEmail := signupAndVerify(t, owner, "ws-owner")
	ownerToken := login(t, owner, ownerEmail)
	trip := createTrip(t, owner, ownerToken, "Private Trip")

	outsider := newClient(t)
	outsiderEmail := signupAndVerify(t, outsider, "ws-outsider")
	outsiderToken := login(t, outsider, outsiderEmail)
	outsiderID := meID(t, outsider, outsiderToken)

	w := dialWS(t, "outsider", outsider, outsiderToken, outsiderID)
	w.send(map[string]any{"type": "subscribe", "trip_id": trip["id"].(string)})

	frame := w.waitForFrame("error", 5*time.Second)
	var code string
	_ = json.Unmarshal(frame["code"], &code)
	if code != "not_found" {
		t.Errorf("error code = %q, want %q — a socket must not disclose that a trip exists",
			code, "not_found")
	}
}

// TestWSRejectsAnOperationBeforeSubscribing catches a client that would otherwise sit forever
// holding an optimistic change it believes is unacknowledged: without a subscription it would
// never see its own operation come back.
func TestWSRejectsAnOperationBeforeSubscribing(t *testing.T) {
	f := newConvergenceFixture(t, "ws-nosub")
	slotID := f.makeSlot("A slot")
	f.settle()

	lone := dialWS(t, "lone", f.aliceHTTP, f.aliceToken, f.aliceID)
	lone.submit(f.tripID, domain.OpSlotEdit, slotID,
		[]string{domain.FieldTitle}, map[string]any{"title": "should not apply"})

	frame := lone.waitForFrame("error", 5*time.Second)
	var code string
	_ = json.Unmarshal(frame["code"], &code)
	if code != "not_subscribed" {
		t.Errorf("error code = %q, want %q", code, "not_subscribed")
	}

	slot, err := testSlots.GetByID(context.Background(), slotID)
	if err != nil {
		t.Fatalf("reloading slot: %v", err)
	}
	if slot.Title == "should not apply" {
		t.Error("an operation from an unsubscribed connection was applied")
	}
}

// TestWSRejectsAnUnknownOperationKind guards the immutable log: an unrecognised kind must
// never be written, because an append-only table offers no later opportunity to fix it.
func TestWSRejectsAnUnknownOperationKind(t *testing.T) {
	f := newConvergenceFixture(t, "ws-badkind")
	slotID := f.makeSlot("A slot")
	f.settle()
	seqBefore := f.currentSeq()

	f.alice.send(map[string]any{
		"type": "op", "trip_id": f.tripID.String(),
		"client_op_id": domain.NewID().String(),
		"kind":         "slot.explode.v1", "entity_id": slotID.String(),
		"fields": []string{"title"}, "values": map[string]any{"title": "x"},
	})
	f.alice.waitForFrame("error", 5*time.Second)

	if got := f.currentSeq(); got != seqBefore {
		t.Errorf("an unknown operation kind consumed a sequence number (%d -> %d)",
			seqBefore, got)
	}
}

// TestWSRejectsAFieldTheKindMayNotChange stops a client widening an operation's blast radius
// past what its kind is allowed to touch — a reorder quietly rewriting a title, say.
func TestWSRejectsAFieldTheKindMayNotChange(t *testing.T) {
	f := newConvergenceFixture(t, "ws-badfield")
	slotID := f.makeSlot("Original title")
	f.settle()

	clientOpID := f.alice.submit(f.tripID, domain.OpSlotMove, slotID,
		[]string{domain.FieldTitle}, map[string]any{"title": "sneaky rename"})
	f.alice.awaitResolution(clientOpID, 5*time.Second)

	slot, err := testSlots.GetByID(context.Background(), slotID)
	if err != nil {
		t.Fatalf("reloading slot: %v", err)
	}
	if slot.Title != "Original title" {
		t.Errorf("a move operation changed the title to %q", slot.Title)
	}
}

// TestWSPresenceAnnouncesJoinAndLeave covers basic presence: who is in which room. Rich
// presence — idle, viewing, editing — is Stage 3 and deliberately absent here.
func TestWSPresenceAnnouncesJoinAndLeave(t *testing.T) {
	f := newConvergenceFixture(t, "ws-presence")

	// Alice and Bob are already subscribed by the fixture, so each should have seen the
	// other arrive.
	joined := f.alice.waitForFrame("presence", 5*time.Second)
	var event string
	_ = json.Unmarshal(joined["event"], &event)
	if event != "joined" {
		t.Errorf("first presence event = %q, want %q", event, "joined")
	}

	// A third connection joins, then leaves; the leave must reach the room too, or a member
	// who closed their laptop would appear online forever.
	third := dialWS(t, "third", f.bobHTTP, f.bobToken, f.bobID)
	third.subscribe(f.tripID)
	third.close()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if presenceEventCount(f.alice, "left") > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("no presence 'left' event reached the room after a connection closed")
}

func presenceEventCount(w *wsClient, event string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, f := range w.frames {
		var kind, ev string
		_ = json.Unmarshal(f["type"], &kind)
		_ = json.Unmarshal(f["event"], &ev)
		if kind == "presence" && ev == event {
			n++
		}
	}
	return n
}

// Resume used to be pinned here as a Slice 1 LIMITATION: since_seq was carried on the wire and
// answered `resync_required`, and the test existed so that the day resume started working, it
// would fail and have to be updated deliberately. That day is Slice 2, and this is the
// deliberate update — the resume behaviour, its boundaries and its failure modes now live in
// resync_api_test.go, which asserts what the server sends rather than that it refuses.
