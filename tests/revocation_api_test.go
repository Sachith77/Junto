package tests

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/pubsub"
	"github.com/junto/junto/internal/transport/ws"
)

// SESSION REVOCATION CLOSES OPEN SOCKETS (Stage 2, Slice 4 — D91, closing D73).
//
// # The gap these tests close
//
// A WebSocket's session was verified once, at the handshake, and never again. Membership and
// capability were still enforced on every frame, so a member removed from a trip lost access
// immediately — but session liveness was not: a user who logged out, or whose sessions were
// killed by a password reset, kept a working socket for up to twelve hours and could read AND
// WRITE through it. CLAUDE.md carried that as the one place in the codebase where a credential
// outlived its revocation.
//
// # What "closed" has to mean to be worth claiming
//
// Not "the client stopped receiving broadcasts", which a dropped subscription also produces.
// These tests assert the socket itself was closed BY THE SERVER — observed the way a browser
// observes it, as the read loop ending — and that the instance is no longer holding the
// connection. They also assert the negative, which is the half most easily got wrong: another
// member's socket, and the same user's OTHER sessions, must survive. Closing everything on
// every revocation would satisfy a test that only checked the intended victim died.

// revocationWait bounds a local revocation. It is generous for a loaded CI box and still far
// below anything that could be confused with the 12-hour lifetime cap.
const revocationWait = 10 * time.Second

// revocationFixture is a trip whose owner holds a live, subscribed socket.
type revocationFixture struct {
	t      *testing.T
	client *client
	token  string
	userID domain.ID
	tripID domain.ID
	sock   *wsClient
}

func newRevocationFixture(t *testing.T, prefix string) *revocationFixture {
	t.Helper()
	c := newClient(t)
	email := signupAndVerify(t, c, prefix)
	token := login(t, c, email)
	userID := meID(t, c, token)

	trip := createTrip(t, c, token, "Revocation Trip")
	tripID := mustParseID(t, trip["id"].(string))

	sock := dialWS(t, prefix, c, token, userID)
	t.Cleanup(sock.close)
	sock.subscribe(tripID)

	return &revocationFixture{t: t, client: c, token: token, userID: userID, tripID: tripID, sock: sock}
}

// TestLogoutClosesTheOpenSocket is D73 closed, in its simplest form.
//
// Before this slice the assertion below was false by design: the session was revoked in the
// database, every subsequent HTTP request failed, and the WebSocket carried on regardless.
func TestLogoutClosesTheOpenSocket(t *testing.T) {
	f := newRevocationFixture(t, "revoke-logout")

	// Sanity: the socket works before the logout. Without this the test could pass against a
	// connection that was never established.
	if !f.sock.stillOpen() {
		t.Fatal("the socket was not open before the logout")
	}
	if n := testConnections.Count(); n == 0 {
		t.Fatal("the instance is not holding any connection to revoke")
	}

	resp := f.client.do(http.MethodPost, "/api/v1/auth/logout", nil)
	assertStatus(t, resp, http.StatusNoContent)

	if !f.sock.waitForServerClose(revocationWait) {
		t.Fatal("the socket survived its own logout — a revoked credential is still driving a " +
			"live connection, which is exactly the D73 gap this closes")
	}
	// Told WHY, not merely disconnected. A client that reads an ordinary close reconnects; one
	// that reads this goes to the login screen instead of looping against the ticket endpoint.
	if !f.sock.errorFrameWithCode("session_revoked") {
		t.Error("the connection closed without telling the client its session was revoked")
	}
}

// TestRevokedSocketCannotWrite states the guarantee in the terms that actually matter.
//
// "The socket closed" is the mechanism. The property is that the revoked credential can no
// longer change anything, and a write attempted through it fails — which is what a stolen
// refresh token being detected is supposed to accomplish.
func TestRevokedSocketCannotWrite(t *testing.T) {
	f := newRevocationFixture(t, "revoke-write")

	// It can write before the logout.
	slotID := domain.NewID()
	clientOpID := f.sock.submit(f.tripID, domain.OpSlotCreate, slotID,
		[]string{"title", "kind"}, map[string]any{"title": "Before logout", "kind": "lodging"})
	f.sock.awaitResolution(clientOpID, 5*time.Second)
	f.sock.assertNoErrorFrames()

	resp := f.client.do(http.MethodPost, "/api/v1/auth/logout", nil)
	assertStatus(t, resp, http.StatusNoContent)
	if !f.sock.waitForServerClose(revocationWait) {
		t.Fatal("the socket survived its own logout")
	}

	// And now it cannot. The write is attempted through the dead socket and must not reach the
	// database — checked by counting slots rather than by trusting the client's error, because
	// a client that failed to send is indistinguishable from a server that refused.
	before := countSlots(t, f.tripID)
	_ = f.sock.submit(f.tripID, domain.OpSlotCreate, domain.NewID(),
		[]string{"title", "kind"}, map[string]any{"title": "After logout", "kind": "lodging"})
	time.Sleep(500 * time.Millisecond)

	if after := countSlots(t, f.tripID); after != before {
		t.Errorf("a write through a revoked connection reached the database: %d slots became %d",
			before, after)
	}
}

// TestPasswordResetClosesEverySocketForTheUser is the user-scoped case, and the one where
// getting the fan-out wrong matters most.
//
// A reset that leaves an attacker's socket alive tells the victim they have fixed a problem
// they have not. So it is not enough for the session that requested the reset to die — every
// session the user holds must, including ones opened from other devices.
func TestPasswordResetClosesEverySocketForTheUser(t *testing.T) {
	c := newClient(t)
	email := signupAndVerify(t, c, "revoke-reset")

	// Two independent logins for ONE user: two sessions, two sockets, as two browsers would be.
	firstToken := login(t, c, email)
	userID := meID(t, c, firstToken)
	trip := createTrip(t, c, firstToken, "Reset Trip")
	tripID := mustParseID(t, trip["id"].(string))

	second := newClient(t)
	secondToken := login(t, second, email)

	sockA := dialWS(t, "device-a", c, firstToken, userID)
	defer sockA.close()
	sockA.subscribe(tripID)
	sockB := dialWS(t, "device-b", second, secondToken, userID)
	defer sockB.close()
	sockB.subscribe(tripID)

	// A bystander on the same trip, to prove the blast radius is the USER and not the room.
	bystander := newClient(t)
	bystanderEmail := signupAndVerify(t, bystander, "revoke-bystander")
	bystanderToken := login(t, bystander, bystanderEmail)
	inviteToTrip(t, c, firstToken, tripID, bystander, bystanderToken, bystanderEmail)
	bystanderID := meID(t, bystander, bystanderToken)
	sockC := dialWS(t, "bystander", bystander, bystanderToken, bystanderID)
	defer sockC.close()
	sockC.subscribe(tripID)

	resp := c.do(http.MethodPost, "/api/v1/auth/request-password-reset",
		map[string]string{"email": email})
	assertStatus(t, resp, http.StatusAccepted)
	msg, ok := testMailer.lastTo(email)
	if !ok {
		t.Fatal("expected a password reset email")
	}
	resetToken := extractToken(t, msg.TextBody, "reset-password")

	resp = c.do(http.MethodPost, "/api/v1/auth/reset-password", map[string]string{
		"token": resetToken, "password": "a-brand-new-password-1234",
	})
	assertStatus(t, resp, http.StatusNoContent)

	if !sockA.waitForServerClose(revocationWait) {
		t.Error("the socket that requested the reset survived it")
	}
	if !sockB.waitForServerClose(revocationWait) {
		t.Error("a second device's socket survived the password reset — a reset that leaves " +
			"an attacker's connection alive is worse than useless")
	}
	if !sockC.stillOpen() {
		t.Error("another member's socket was closed by someone else's password reset; the " +
			"blast radius of a user-scoped revocation must be that user")
	}
}

// TestRevokingOneSessionSparesTheOthers is the precision half of the specification.
//
// Session scope must mean session scope. An implementation that closed every socket belonging
// to the user would pass every test above and would log people out of their laptop when they
// signed out on their phone.
func TestRevokingOneSessionSparesTheOthers(t *testing.T) {
	c := newClient(t)
	email := signupAndVerify(t, c, "revoke-one-session")

	keptToken := login(t, c, email)
	userID := meID(t, c, keptToken)
	trip := createTrip(t, c, keptToken, "Session Scope Trip")
	tripID := mustParseID(t, trip["id"].(string))

	doomed := newClient(t)
	doomedToken := login(t, doomed, email)

	keptSock := dialWS(t, "kept", c, keptToken, userID)
	defer keptSock.close()
	keptSock.subscribe(tripID)
	doomedSock := dialWS(t, "doomed", doomed, doomedToken, userID)
	defer doomedSock.close()
	doomedSock.subscribe(tripID)

	// Find the session id belonging to the second login and revoke exactly that one.
	resp := doomed.do(http.MethodGet, "/api/v1/auth/sessions", nil, withBearer(doomedToken))
	assertStatus(t, resp, http.StatusOK)
	var sessions []map[string]any
	resp.decode(t, &sessions)
	if len(sessions) < 2 {
		t.Fatalf("expected at least 2 active sessions, got %d", len(sessions))
	}

	// The caller's own session is identified by which one this token can revoke without
	// affecting the other; the API exposes `current` for exactly this.
	var doomedSessionID string
	for _, s := range sessions {
		if current, ok := s["current"].(bool); ok && current {
			doomedSessionID = s["id"].(string)
			break
		}
	}
	if doomedSessionID == "" {
		t.Fatal("could not identify the calling session; the sessions listing exposes no `current` flag")
	}

	resp = doomed.do(http.MethodDelete, "/api/v1/auth/sessions/"+doomedSessionID, nil,
		withBearer(doomedToken))
	assertStatus(t, resp, http.StatusNoContent)

	if !doomedSock.waitForServerClose(revocationWait) {
		t.Error("the revoked session's socket survived")
	}
	// The load-bearing negative.
	if !keptSock.stillOpen() {
		t.Error("revoking one session closed another session's socket for the same user; " +
			"signing out on one device must not sign you out everywhere")
	}
}

// TestRevocationCrossesInstances is the multi-instance half, and the reason this could not be
// built before Slice 2.
//
// The socket to close may be on any instance, and the one processing the logout has no way to
// know which. That is the whole problem: without a peer channel the only available fix was
// polling the session table once per connection per interval, which is a query per socket on
// the hottest resource in the system to shrink a window the lifetime cap already bounded.
//
// Verified against a planted break: replacing instance B's revocation transport with
// domain.NoopRevocationTransport leaves B's socket open and fails this test. That plant is the
// whole point — instance A closes its own sockets synchronously, so a test that only watched a
// socket on A would pass with the peer path completely dead.
func TestRevocationCrossesInstances(t *testing.T) {
	rds := sharedRedis(t)

	client, err := pubsub.NewClient(rds.URL)
	if err != nil {
		t.Fatalf("building the redis client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	revA := pubsub.NewRevocationTransport(client, discardLogger())
	revB := pubsub.NewRevocationTransport(client, discardLogger())
	t.Cleanup(func() { _ = revA.Close() })
	t.Cleanup(func() { _ = revB.Close() })

	if revA.InstanceID() == revB.InstanceID() {
		t.Fatal("both instances claim the same identity; each would discard the other's " +
			"revocations as its own echo and this test would prove the opposite of what it says")
	}

	a, err := newStack(stackConfig{
		Pool:                testPG.Pool,
		Tickets:             ws.NewRedisTicketStore(client, domain.SystemClock{}),
		Transport:           pubsub.NewOpTransport(client, discardLogger()),
		RevocationTransport: revA,
	})
	if err != nil {
		t.Fatalf("building instance A: %v", err)
	}
	t.Cleanup(a.Close)

	b, err := newStack(stackConfig{
		Pool:                testPG.Pool,
		Tickets:             ws.NewRedisTicketStore(client, domain.SystemClock{}),
		Transport:           pubsub.NewOpTransport(client, discardLogger()),
		RevocationTransport: revB,
	})
	if err != nil {
		t.Fatalf("building instance B: %v", err)
	}
	t.Cleanup(b.Close)

	// One user. The REST calls go to instance A; the socket lives on instance B.
	onA := newClientFor(t, a.Server)
	email := signupAndVerify(t, onA, "revoke-cross")
	token := login(t, onA, email)
	userID := meID(t, onA, token)
	trip := createTrip(t, onA, token, "Cross Instance Revocation")
	tripID := mustParseID(t, trip["id"].(string))

	onB := newClientFor(t, b.Server)
	sock := dialWSOn(t, "socket-on-B", onB, b.Server.URL, token, userID, domain.NewReplica())
	defer sock.close()
	sock.subscribe(tripID)

	if b.Connections.Count() == 0 {
		t.Fatal("instance B is not holding the connection this test is about")
	}
	if a.Connections.Count() != 0 {
		t.Fatal("instance A is holding a connection; this test would then pass through the " +
			"local path and prove nothing about the peer channel")
	}

	// Logout on instance A. Nothing in this process closes B's socket except the Redis message.
	resp := onA.do(http.MethodPost, "/api/v1/auth/logout", nil)
	assertStatus(t, resp, http.StatusNoContent)

	if !sock.waitForServerClose(crossInstanceWait) {
		t.Fatal("a socket on instance B survived a logout processed on instance A; " +
			"cross-instance revocation is not working, and a logged-out session keeps a live " +
			"connection for up to the 12-hour lifetime cap")
	}
	if !sock.errorFrameWithCode("session_revoked") {
		t.Error("instance B closed the connection without telling the client why")
	}
}

// --- helpers ---

// countSlots reads how many live slots a trip has, straight from the database.
func countSlots(t *testing.T, tripID domain.ID) int {
	t.Helper()
	slots, err := testSlots.ListForTrip(t.Context(), tripID)
	if err != nil {
		t.Fatalf("listing slots: %v", err)
	}
	return len(slots)
}

// inviteToTrip adds a member through the real invitation flow.
func inviteToTrip(t *testing.T, host *client, hostToken string, tripID domain.ID,
	guest *client, guestToken, guestEmail string) {
	t.Helper()
	resp := host.do(http.MethodPost, fmt.Sprintf("/api/v1/trips/%s/invitations", tripID),
		map[string]any{"email": guestEmail, "role": "editor"}, withBearer(hostToken))
	assertStatus(t, resp, http.StatusCreated)

	msg, ok := testMailer.lastTo(guestEmail)
	if !ok {
		t.Fatal("expected an invitation email")
	}
	acceptToken := extractToken(t, msg.TextBody, "invitations/accept")
	resp = guest.do(http.MethodPost, "/api/v1/invitations/accept",
		map[string]string{"token": acceptToken}, withBearer(guestToken))
	assertStatus(t, resp, http.StatusOK)
}
