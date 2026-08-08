package tests

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/pubsub"
	"github.com/junto/junto/internal/testsupport"
	"github.com/junto/junto/internal/transport/ws"
)

// HORIZONTAL SCALING (Stage 2, Slice 2, Part B).
//
// # The clause this file exists to earn
//
// CLAUDE.md's claims table has been carrying this line since Stage 2 started:
//
//	Redis pub/sub for horizontal scaling — one instance plus a Redis client that
//	happens to compile earns nothing.
//
// So the test is written to the literal specification it was given: publish an operation via a
// client connected to instance A, and assert that a client connected to instance B receives
// it. Everything here is real — two fully wired API instances, two httptest servers, real
// Redis in a container, real WebSocket clients doing the real ticket handshake. The only thing
// the two instances share is what two instances behind a load balancer would share: one
// Postgres and one Redis.
//
// # Why the ticket store had to move first
//
// Before this slice, tickets lived in each process's memory. A client that minted a ticket on
// instance A and was routed to instance B for the WebSocket upgrade would be rejected — and
// with a load balancer, which instance you get is arbitrary. A two-instance test built on the
// in-memory store would therefore have been measuring whether the clients happened to land on
// the right servers, which is why moving it was a prerequisite and not a cleanup.

var (
	testRedis     *testsupport.Redis
	testRedisOnce sync.Once
	testRedisErr  error
)

// sharedRedis starts one Redis container for the whole package, on first use.
//
// Lazy rather than started in TestMain because only these tests need it, and paying a
// container start for every run of the suite would be a tax on tests that have nothing to do
// with multi-instance behaviour.
func sharedRedis(t *testing.T) *testsupport.Redis {
	t.Helper()
	testRedisOnce.Do(func() {
		testRedis, testRedisErr = testsupport.StartRedis(context.Background())
		if testRedisErr == nil {
			// Torn down when the package's tests finish. There is no TestMain hook left to
			// hang this on, so it rides on the process exiting.
			log.Printf("test redis started at %s", testRedis.URL)
		}
	})
	if testRedisErr != nil {
		t.Fatalf("starting redis: %v", testRedisErr)
	}
	return testRedis
}

// crossInstanceWait bounds every wait that a cross-instance delivery must satisfy.
//
// Generous enough for a container under load, and far below the reconcile interval that would
// otherwise let the log-repair path answer instead. Both halves of that sentence matter.
const crossInstanceWait = 20 * time.Second

// cluster is two independent API instances sharing one Postgres and one Redis.
type cluster struct {
	A, B *stack
}

// newCluster builds the pair.
//
// Both instances get a Redis-backed ticket store and a Redis operation transport, and NOTHING
// else in common beyond the database — separate brokers, separate engines, separate rooms,
// separate presence. If an operation crosses between them, it crossed through Redis.
func newCluster(t *testing.T) *cluster {
	t.Helper()
	rds := sharedRedis(t)

	client, err := pubsub.NewClient(rds.URL)
	if err != nil {
		t.Fatalf("building the redis client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	transportA := pubsub.NewOpTransport(client, discardLogger())
	transportB := pubsub.NewOpTransport(client, discardLogger())
	t.Cleanup(func() { _ = transportA.Close() })
	t.Cleanup(func() { _ = transportB.Close() })

	if transportA.InstanceID() == transportB.InstanceID() {
		t.Fatal("both instances claim the same identity; each would discard the other's " +
			"operations as its own echo and this test would prove the opposite of what it says")
	}

	// The reconcile interval is pushed FAR out of reach, and this is not a detail.
	//
	// The broker deliberately has a second way to learn about an operation: if the broadcast
	// never arrives, a room reconciles against trips.op_seq and reads the missing range from
	// the log. That makes the system robust and it makes this test worthless if the two paths
	// are allowed to overlap — the first version of this file used a ten-second interval
	// against a fifteen-second wait, and PASSED with instance B's peer transport replaced by a
	// no-op. The Redis path was not being tested at all; the log-repair path was quietly
	// covering for it.
	//
	// Five minutes is longer than this suite could ever run, so within the waits below there
	// is exactly one way for an operation to cross between instances. Re-verified by planting
	// domain.NoopTransport on instance B, which now fails as it always should have.
	const noReconcile = 5 * time.Minute

	a, err := newStack(stackConfig{
		Pool:              testPG.Pool,
		Tickets:           ws.NewRedisTicketStore(client, domain.SystemClock{}),
		Transport:         transportA,
		ReconcileInterval: noReconcile,
	})
	if err != nil {
		t.Fatalf("building instance A: %v", err)
	}
	t.Cleanup(a.Close)

	b, err := newStack(stackConfig{
		Pool:              testPG.Pool,
		Tickets:           ws.NewRedisTicketStore(client, domain.SystemClock{}),
		Transport:         transportB,
		ReconcileInterval: noReconcile,
	})
	if err != nil {
		t.Fatalf("building instance B: %v", err)
	}
	t.Cleanup(b.Close)

	return &cluster{A: a, B: b}
}

// clusterTrip is a trip with two members, each connected to a DIFFERENT instance.
type clusterTrip struct {
	tripID domain.ID

	onA *wsClient
	onB *wsClient

	httpA, httpB   *client
	tokenA, tokenB string
}

func (c *cluster) newTrip(t *testing.T, prefix string) *clusterTrip {
	t.Helper()

	// Signup and login go through instance A; the second member logs in on instance B. Both
	// work because sessions live in the shared database — which is worth exercising, since a
	// deployment where you must return to the instance that logged you in is not horizontally
	// scaled either.
	httpA := newClientFor(t, c.A.Server)
	emailA := signupAndVerify(t, httpA, prefix+"-a")
	tokenA := login(t, httpA, emailA)

	httpB := newClientFor(t, c.B.Server)
	emailB := signupAndVerify(t, httpB, prefix+"-b")

	trip := createTrip(t, httpA, tokenA, "Cross-instance Trip")
	tripIDStr := trip["id"].(string)

	resp := httpA.do(http.MethodPost, "/api/v1/trips/"+tripIDStr+"/invitations",
		map[string]any{"email": emailB, "role": "editor"}, withBearer(tokenA))
	assertStatus(t, resp, http.StatusCreated)

	msg, ok := testMailer.lastTo(emailB)
	if !ok {
		t.Fatalf("expected an invitation email for %s", emailB)
	}
	inviteToken := extractToken(t, msg.TextBody, "invitations/accept")

	tokenB := login(t, httpB, emailB)
	resp = httpB.do(http.MethodPost, "/api/v1/invitations/accept",
		map[string]string{"token": inviteToken}, withBearer(tokenB))
	assertStatus(t, resp, http.StatusOK)

	tripID, err := domain.ParseID("trip_id", tripIDStr)
	if err != nil {
		t.Fatalf("parsing trip id: %v", err)
	}

	ct := &clusterTrip{
		tripID: tripID,
		httpA:  httpA, httpB: httpB, tokenA: tokenA, tokenB: tokenB,
	}
	ct.onA = dialWSOn(t, "client-on-A", httpA, c.A.Server.URL, tokenA, meID(t, httpA, tokenA), domain.NewReplica())
	ct.onB = dialWSOn(t, "client-on-B", httpB, c.B.Server.URL, tokenB, meID(t, httpB, tokenB), domain.NewReplica())
	ct.onA.subscribe(tripID)
	ct.onB.subscribe(tripID)
	return ct
}

// --- THE TEST NAMED IN CLAUDE.MD -------------------------------------------------------------

// TestAnOperationPublishedOnInstanceAReachesAClientOnInstanceB is the literal specification:
// publish an operation via a client connected to instance A, assert a client connected to
// instance B receives it.
//
// It is written narrowly on purpose. The convergence machinery could have been pointed at the
// pair and would have produced a more impressive-looking test, but the clause being earned is
// this one sentence, and a test that asserts exactly it cannot pass for an adjacent reason.
func TestAnOperationPublishedOnInstanceAReachesAClientOnInstanceB(t *testing.T) {
	c := newCluster(t)
	trip := c.newTrip(t, "cross-instance")

	slotID := domain.NewID()
	clientOpID := trip.onA.submit(trip.tripID, domain.OpSlotCreate, slotID,
		[]string{domain.FieldKind, domain.FieldTitle},
		map[string]any{"kind": "lodging", "title": "Published on A"})
	trip.onA.awaitResolution(clientOpID, 15*time.Second)

	head, err := c.A.Trips.CurrentOpSeq(context.Background(), trip.tripID)
	if err != nil {
		t.Fatalf("reading the trip sequence: %v", err)
	}

	// The assertion. The client on B never spoke to instance A, and instance B's broker only
	// learns about this operation over Redis — nothing else can deliver it inside this window.
	trip.onB.waitForSeq(head, crossInstanceWait)
	trip.onB.assertNoErrorFrames()

	got := trip.onB.snapshot().Slots[slotID]
	if got == nil {
		t.Fatal("the client on instance B never received the operation published on instance A: " +
			"cross-instance fan-out is not working, whatever compiles")
	}
	if got.Title != "Published on A" {
		t.Fatalf("the client on B received the operation but folded it wrong: title = %q", got.Title)
	}
}

// TestAHandshakeTicketMintedOnOneInstanceIsRedeemableOnAnother is the prerequisite, tested
// rather than assumed.
//
// This is the failure that would make a two-instance deployment look like flaky networking:
// with per-process ticket storage, a client that minted on A and upgraded on B is rejected,
// and which instance it gets is up to the load balancer. Single-use must still hold across
// the pair, which is what GETDEL buys — so the second half of this test is as important as
// the first.
func TestAHandshakeTicketMintedOnOneInstanceIsRedeemableOnAnother(t *testing.T) {
	c := newCluster(t)

	httpA := newClientFor(t, c.A.Server)
	email := signupAndVerify(t, httpA, "cross-ticket")
	token := login(t, httpA, email)

	ticket := mintTicketOn(t, httpA, token)

	sock, err := dialRawOn(t, c.B.Server.URL, ticket)
	if err != nil {
		t.Fatalf("a ticket minted on instance A was rejected by instance B: %v", err)
	}
	_ = sock.CloseNow()

	// And it is still single-use across the pair: the same ticket must not work anywhere.
	if second, err := dialRawOn(t, c.A.Server.URL, ticket); err == nil {
		_ = second.CloseNow()
		t.Fatal("a redeemed ticket was accepted a second time on the other instance; " +
			"single-use is not enforced across instances")
	}
}

// TestConcurrentWritesOnBothInstancesConverge is the convergence proof extended across the
// instance boundary.
//
// The narrow test above proves delivery. This one proves that delivery does not damage the
// property the whole design rests on: two members editing different fields of one slot from
// DIFFERENT PROCESSES still both survive, and every view agrees afterwards.
//
// It is also where the ordering question gets its answer. Redis pub/sub gives FIFO per
// publishing connection and nothing across publishers, so instance A's seq 5 and instance B's
// seq 6 can arrive at a third party in either order — and a client's fold rejects a gap. If
// the room dispatcher's reordering were removed, this test is what would start failing.
func TestConcurrentWritesOnBothInstancesConverge(t *testing.T) {
	c := newCluster(t)
	trip := c.newTrip(t, "cross-converge")

	// A slot both instances will edit, created over REST on instance A.
	slotID := makeSlotOn(t, trip.httpA, trip.tokenA, trip.tripID, "Contested across instances")

	head, err := c.A.Trips.CurrentOpSeq(context.Background(), trip.tripID)
	if err != nil {
		t.Fatalf("reading the trip sequence: %v", err)
	}
	trip.onA.waitForSeq(head, crossInstanceWait)
	trip.onB.waitForSeq(head, crossInstanceWait)

	// Both clients submit at once, on different instances, to different fields.
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(2)

	var opA, opB domain.ID
	go func() {
		defer done.Done()
		start.Wait()
		opA = trip.onA.submit(trip.tripID, domain.OpSlotEdit, slotID,
			[]string{domain.FieldTitle}, map[string]any{"title": "titled from A"})
	}()
	go func() {
		defer done.Done()
		start.Wait()
		opB = trip.onB.submit(trip.tripID, domain.OpSlotEdit, slotID,
			[]string{domain.FieldNotes}, map[string]any{"notes": "noted from B"})
	}()
	start.Done()
	done.Wait()

	trip.onA.awaitResolution(opA, 15*time.Second)
	trip.onB.awaitResolution(opB, 15*time.Second)

	head, err = c.A.Trips.CurrentOpSeq(context.Background(), trip.tripID)
	if err != nil {
		t.Fatalf("reading the trip sequence: %v", err)
	}
	trip.onA.waitForSeq(head, crossInstanceWait)
	trip.onB.waitForSeq(head, crossInstanceWait)
	trip.onA.assertNoErrorFrames()
	trip.onB.assertNoErrorFrames()

	// Field-level merge survived the instance boundary: neither write was lost.
	for _, w := range []*wsClient{trip.onA, trip.onB} {
		slot := w.snapshot().Slots[slotID]
		if slot == nil {
			t.Fatalf("%s is missing the slot entirely", w.name)
		}
		if slot.Title != "titled from A" {
			t.Errorf("%s: title = %q, want the value written on instance A", w.name, slot.Title)
		}
		if slot.Notes != "noted from B" {
			t.Errorf("%s: notes = %q, want the value written on instance B", w.name, slot.Notes)
		}
	}

	assertConvergence(t, trip.tripID, []*wsClient{trip.onA, trip.onB})
}

// mintTicketOn is mintTicket against an arbitrary instance.
//
// The ticket endpoint answers with a bare document rather than the success envelope the rest
// of the API uses, so this decodes the body directly.
func mintTicketOn(t *testing.T, c *client, token string) string {
	t.Helper()
	resp := c.do(http.MethodPost, "/api/v1/ws/ticket", nil, withBearer(token))
	assertStatus(t, resp, http.StatusCreated)

	var out struct {
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(resp.body, &out); err != nil {
		t.Fatalf("decoding ticket: %v\nbody: %s", err, resp.body)
	}
	if out.Ticket == "" {
		t.Fatal("the ticket endpoint returned an empty ticket")
	}
	return out.Ticket
}
