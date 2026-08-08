package tests

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/junto/junto/internal/domain"
)

// NON-FUNCTIONAL TARGETS, MEASURED (Stage 2, Slice 4).
//
// CLAUDE.md has carried these as targets since Stage 1, two of them explicitly marked TBD and
// "not to be quoted anywhere until measured". This file is what turns them into numbers.
//
// # What is measured, and what the numbers are worth
//
// Everything here runs against the real stack: real Postgres in a container, real HTTP, real
// WebSockets, real sequencer, real broker. Nothing is stubbed, so the figures include the
// costs a member actually waits on.
//
// They are NOT a production capacity statement, and must not be quoted as one. They are
// single-machine figures from a developer laptop or a CI box, with the database in a container
// on the same host and no network between the tiers. What they establish is the SHAPE of the
// system's performance — specifically that per-trip write throughput, not connection count, is
// the serialized resource — and an order of magnitude for each target. That distinction is the
// whole reason the throughput benchmark below reports its trip distribution rather than a bare
// number.
//
// # Why the assertions are loose and the measurements are precise
//
// Each test logs its actual figures — that is what gets recorded in CLAUDE.md — and then
// asserts only that the target is met with a wide margin. The split is deliberate. A tight
// assertion on a shared CI box measures the box, and a flaky performance test is worse than no
// performance test: it trains everyone to re-run until green, which is the same reflex that
// makes a genuine regression invisible. So the numbers come from reading the log of a
// deliberate run, and the assertions exist only to catch an order-of-magnitude change.
//
// The assertions are also written as SHAPES rather than magnitudes wherever possible — spread
// writes must outpace single-trip writes; p99 at 100 connections must stay within an order of
// magnitude of p99 at 2 — because a shape survives being run on different hardware and a
// magnitude does not.

// --- helpers ---------------------------------------------------------------------------

// nfrFixture is a trip with an owner who can open sockets against it.
type nfrFixture struct {
	t      *testing.T
	client *client
	token  string
	userID domain.ID
	tripID domain.ID
}

// newNFRFixture builds a verified user with a trip to measure against.
//
// These are tests rather than Go benchmarks deliberately. `go test -bench` reports ns/op for a
// loop the framework sizes itself, which is the wrong instrument for every target here: three
// of the four are percentiles or ratios, and the fourth is throughput under deliberate
// contention. Measuring them as tests means the figures are produced by the same `go test`
// invocation CI already runs, and the loose assertions catch an order-of-magnitude regression
// without anyone having to remember to run a separate benchmark suite.
func newNFRFixture(t *testing.T, prefix string) *nfrFixture {
	c := newClient(t)
	email := signupAndVerify(t, c, prefix)
	token := login(t, c, email)
	userID := meID(t, c, token)
	trip := createTrip(t, c, token, "NFR Trip")
	return &nfrFixture{
		t: t, client: c, token: token, userID: userID,
		tripID: mustParseID(t, trip["id"].(string)),
	}
}

// percentile returns the p-th percentile of an unsorted duration sample.
//
// Nearest-rank on a sorted copy: with the sample sizes here (hundreds to low thousands),
// interpolation would imply a precision the measurement does not have.
func percentile(samples []time.Duration, p float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

func summarise(t *testing.T, label string, samples []time.Duration) (p50, p95, p99 time.Duration) {
	t.Helper()
	p50, p95, p99 = percentile(samples, 0.50), percentile(samples, 0.95), percentile(samples, 0.99)
	t.Logf("%s: n=%d  p50=%s  p95=%s  p99=%s  max=%s",
		label, len(samples), p50, p95, p99, percentile(samples, 1.0))
	return p50, p95, p99
}

// --- write throughput --------------------------------------------------------------------

// TestSingleTripWriteThroughputCeiling measures the number CLAUDE.md has been holding open
// since Stage 1, and demonstrates WHY it is stated per trip rather than per connection.
//
// # The claim being characterised
//
// op_seq is allocated by an UPDATE that takes the trip's row lock as the first statement of
// every write transaction (D60). Writers within one trip therefore serialize — deliberately,
// because that is what produces the clean per-trip total order the sync engine folds. Writers
// in DIFFERENT trips do not contend at all.
//
// So "100 concurrent connections" is not one number. A hundred mostly-reading connections
// spread over many trips is a different system from a hundred simultaneous writers to one trip,
// and only the second is bounded by the row lock. This test measures both and reports the
// ratio, because the ratio is the actual finding.
func TestSingleTripWriteThroughputCeiling(t *testing.T) {
	if testing.Short() {
		t.Skip("throughput measurement needs the full stack")
	}

	const (
		writers        = 8
		opsPerWriter   = 25
		degradedFactor = 1.5 // the spread-out case must beat the single-trip case by at least this
	)

	single := measureThroughput(t, "single-trip", writers, opsPerWriter, 1)
	spread := measureThroughput(t, "one-trip-per-writer", writers, opsPerWriter, writers)

	t.Logf("MEASURED single-trip write throughput: %.0f ops/sec (%d writers, %d ops each)",
		single, writers, opsPerWriter)
	t.Logf("MEASURED spread-across-%d-trips throughput: %.0f ops/sec", writers, spread)
	t.Logf("MEASURED ratio: %.2fx — this is the per-trip row lock, and it is the intended "+
		"design rather than a defect", spread/single)

	if single <= 0 || spread <= 0 {
		t.Fatal("throughput measurement produced no result")
	}
	// The assertion is the SHAPE, not the magnitude: writes spread across trips must outpace
	// writes funnelled through one trip's lock. If this ever fails, either the row lock stopped
	// serializing (which would break the total order the whole design rests on) or something
	// else became the bottleneck, and both are worth knowing.
	if spread < single*degradedFactor {
		t.Errorf("spreading writes across trips gave %.0f ops/sec against %.0f for a single "+
			"trip (%.2fx). Writers in different trips share no lock, so this should be "+
			"markedly faster; if it is not, the serialized resource is no longer the trip row",
			spread, single, spread/single)
	}
}

// measureThroughput drives `writers` concurrent WebSocket writers over `trips` trips and
// returns committed operations per second.
func measureThroughput(t *testing.T, label string, writers, opsPerWriter, trips int) float64 {
	t.Helper()

	f := newNFRFixture(t, "tput-"+label)
	tripIDs := make([]domain.ID, 0, trips)
	tripIDs = append(tripIDs, f.tripID)
	for i := 1; i < trips; i++ {
		trip := createTrip(t, f.client, f.token, fmt.Sprintf("NFR Trip %d", i))
		tripIDs = append(tripIDs, mustParseID(t, trip["id"].(string)))
	}

	socks := make([]*wsClient, writers)
	for i := 0; i < writers; i++ {
		socks[i] = dialWS(t, fmt.Sprintf("%s-w%d", label, i), f.client, f.token, f.userID)
		defer socks[i].close()
		socks[i].subscribe(tripIDs[i%len(tripIDs)])
	}

	// Released from one barrier so the writers genuinely contend rather than queueing politely.
	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
	)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sock := socks[i]
			tripID := tripIDs[i%len(tripIDs)]
			<-start
			for op := 0; op < opsPerWriter; op++ {
				sock.submit(tripID, domain.OpSlotCreate, domain.NewID(),
					[]string{"title", "kind"},
					map[string]any{"title": fmt.Sprintf("w%d-%d", i, op), "kind": "activity"})
			}
		}(i)
	}

	began := time.Now()
	close(start)
	wg.Wait()

	// Submission is asynchronous, so the clock cannot stop until the writes have actually
	// COMMITTED. Waiting on the trips' sequencers is the honest finish line: it measures work
	// the database completed, not frames the test managed to hand to a socket.
	total := int64(writers * opsPerWriter)
	waitForCommittedOps(t, tripIDs, total, 60*time.Second)
	elapsed := time.Since(began)

	return float64(total) / elapsed.Seconds()
}

// waitForCommittedOps blocks until the trips' sequence numbers account for `want` operations.
func waitForCommittedOps(t *testing.T, tripIDs []domain.ID, want int64, timeout time.Duration) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)

	baseline := int64(0)
	for {
		var total int64
		for _, id := range tripIDs {
			seq, err := testTrips.CurrentOpSeq(ctx, id)
			if err != nil {
				t.Fatalf("reading the trip sequence: %v", err)
			}
			total += seq
		}
		if total-baseline >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d operations committed within %s", total-baseline, want, timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// --- message latency ---------------------------------------------------------------------

// TestMessageLatencyAtOneHundredConnections measures the p99 CLAUDE.md left undefined, and does
// it at the connection count the target names.
//
// # What is being timed
//
// From the instant a writer hands its frame to the socket, to the instant a DIFFERENT member's
// connection receives the resulting operation. That interval contains the whole system: frame
// decode, authorization, the trip row lock, the write transaction, the op-log append, the
// commit, the broker fan-out and the write pump. It is what a collaborator waits to see someone
// else's edit appear.
//
// # Why the connections are on one trip
//
// Because that is the demanding arrangement, and the one the "no latency degradation" target is
// worth stating about. A hundred connections spread over a hundred trips share nothing; a
// hundred in one room mean every committed operation fans out a hundred times.
func TestMessageLatencyAtOneHundredConnections(t *testing.T) {
	if testing.Short() {
		t.Skip("latency measurement needs the full stack")
	}

	const (
		connections = 100
		samples     = 60
		// Loose on purpose. The measurement is the point; this only catches a change that moved
		// the figure by an order of magnitude on any machine the suite runs on.
		p99Budget = 2 * time.Second
	)

	f := newNFRFixture(t, "latency")

	// One observer is measured; the other 99 are load. They all fold, so the server is doing
	// the full fan-out work for every operation rather than serving a single lucky socket.
	observer := dialWS(t, "observer", f.client, f.token, f.userID)
	defer observer.close()
	observer.trackArrivals()
	observer.subscribe(f.tripID)

	for i := 1; i < connections-1; i++ {
		bystander := dialWS(t, fmt.Sprintf("bystander-%d", i), f.client, f.token, f.userID)
		defer bystander.close()
		bystander.subscribe(f.tripID)
	}

	writer := dialWS(t, "writer", f.client, f.token, f.userID)
	defer writer.close()
	writer.subscribe(f.tripID)

	if held := testConnections.Count(); held < connections {
		t.Fatalf("the instance is holding %d connections, want at least %d — the measurement "+
			"would not be at the connection count it claims", held, connections)
	}

	// Sequential, one operation at a time, each waited for. This measures LATENCY, not
	// throughput: firing them concurrently would queue writes behind the trip's row lock and
	// report the queueing delay as if it were delivery time.
	latencies := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		sent := time.Now()
		clientOpID := writer.submit(f.tripID, domain.OpSlotCreate, domain.NewID(),
			[]string{"title", "kind"},
			map[string]any{"title": fmt.Sprintf("latency-%d", i), "kind": "activity"})

		deadline := time.Now().Add(10 * time.Second)
		for {
			if at, ok := observer.arrivalOf(clientOpID); ok {
				latencies = append(latencies, at.Sub(sent))
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("operation %d never reached the observer", i)
			}
			time.Sleep(time.Millisecond)
		}
	}

	p50, p95, p99 := summarise(t, "broadcast latency at 100 connections", latencies)
	t.Logf("MEASURED message latency at %d connections on one trip: p50=%s p95=%s p99=%s",
		connections, p50, p95, p99)

	if p99 > p99Budget {
		t.Errorf("p99 message latency is %s, beyond the %s budget", p99, p99Budget)
	}
	observer.assertNoErrorFrames()
}

// TestOneHundredConnectionsDoNotDegradeLatency is the comparison the target actually asserts.
//
// "≥100 concurrent connections without latency degradation" is a statement about a DIFFERENCE,
// so measuring only the loaded case cannot establish it. This measures the same trip with two
// observers and then with a hundred, and compares.
func TestOneHundredConnectionsDoNotDegradeLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("latency measurement needs the full stack")
	}

	quiet := measureLatency(t, "quiet", 2, 40)
	loaded := measureLatency(t, "loaded", 100, 40)

	quietP99 := percentile(quiet, 0.99)
	loadedP99 := percentile(loaded, 0.99)
	t.Logf("MEASURED p99 with 2 connections: %s; with 100 connections: %s (%.2fx)",
		quietP99, loadedP99, float64(loadedP99)/float64(quietP99))

	// "No degradation" cannot mean "identical" — fanning out to a hundred sockets is more work
	// than fanning out to two, and pretending otherwise would be the kind of claim this project
	// exists not to make. It means the same ORDER OF MAGNITUDE, which is what a member
	// experiences as "no worse".
	if loadedP99 > quietP99*10 {
		t.Errorf("p99 latency degraded from %s to %s (%.1fx) going from 2 to 100 connections",
			quietP99, loadedP99, float64(loadedP99)/float64(quietP99))
	}
}

// measureLatency times end-to-end delivery on a trip carrying `connections` sockets.
func measureLatency(t *testing.T, label string, connections, samples int) []time.Duration {
	t.Helper()

	f := newNFRFixture(t, "lat-"+label)

	observer := dialWS(t, label+"-observer", f.client, f.token, f.userID)
	defer observer.close()
	observer.trackArrivals()
	observer.subscribe(f.tripID)

	writer := dialWS(t, label+"-writer", f.client, f.token, f.userID)
	defer writer.close()
	writer.subscribe(f.tripID)

	for i := 2; i < connections; i++ {
		bystander := dialWS(t, fmt.Sprintf("%s-bystander-%d", label, i), f.client, f.token, f.userID)
		defer bystander.close()
		bystander.subscribe(f.tripID)
	}

	out := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		sent := time.Now()
		clientOpID := writer.submit(f.tripID, domain.OpSlotCreate, domain.NewID(),
			[]string{"title", "kind"},
			map[string]any{"title": fmt.Sprintf("%s-%d", label, i), "kind": "activity"})

		deadline := time.Now().Add(10 * time.Second)
		for {
			if at, ok := observer.arrivalOf(clientOpID); ok {
				out = append(out, at.Sub(sent))
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: operation %d never arrived", label, i)
			}
			time.Sleep(time.Millisecond)
		}
	}
	summarise(t, label+" latency", out)
	return out
}

// --- reconnect and resync -----------------------------------------------------------------

// TestReconnectAndResyncWithinTwoSeconds measures the one non-functional target that has always
// had a number attached: "reconnect + resync in under 2s".
//
// The measurement runs from the moment the returning client starts its handshake to the moment
// its replica has folded everything it missed — the whole of what a user experiences as "my tab
// woke up and caught up", including the ticket round trip, the socket upgrade, the subscribe,
// and the log replay.
func TestReconnectAndResyncWithinTwoSeconds(t *testing.T) {
	if testing.Short() {
		t.Skip("reconnect measurement needs the full stack")
	}

	const (
		missedOps = 200
		budget    = 2 * time.Second
	)

	f := newNFRFixture(t, "reconnect")

	// The returning member: connects, folds a little, then disappears.
	member := dialWS(t, "member", f.client, f.token, f.userID)
	member.subscribe(f.tripID)

	writer := dialWS(t, "writer", f.client, f.token, f.userID)
	defer writer.close()
	writer.subscribe(f.tripID)

	seed := writer.submit(f.tripID, domain.OpSlotCreate, domain.NewID(),
		[]string{"title", "kind"}, map[string]any{"title": "before", "kind": "activity"})
	writer.awaitResolution(seed, 5*time.Second)
	member.waitForSeq(1, 5*time.Second)

	// Carry the replica across the disconnect, as a browser tab does. Starting from an empty
	// one would be a first-time subscribe wearing a resume's clothes.
	replica := member.snapshot()
	member.close()

	for i := 0; i < missedOps; i++ {
		writer.submit(f.tripID, domain.OpSlotCreate, domain.NewID(),
			[]string{"title", "kind"},
			map[string]any{"title": fmt.Sprintf("missed-%d", i), "kind": "activity"})
	}
	waitForCommittedOps(t, []domain.ID{f.tripID}, int64(missedOps), 60*time.Second)
	head, err := testTrips.CurrentOpSeq(context.Background(), f.tripID)
	if err != nil {
		t.Fatalf("reading the head sequence: %v", err)
	}

	// The clock starts at the handshake, because that is where the user's wait starts.
	began := time.Now()
	returning := dialWSOn(t, "member-returned", f.client, testServer.URL, f.token, f.userID, replica)
	defer returning.close()
	returning.resume(f.tripID)
	returning.waitForSeq(head, 30*time.Second)
	elapsed := time.Since(began)

	t.Logf("MEASURED reconnect + resync of %d missed operations: %s", missedOps, elapsed)

	if elapsed > budget {
		t.Errorf("reconnect and resync took %s, beyond the %s target (%d operations replayed)",
			elapsed, budget, missedOps)
	}
	// A resync that was answered with "throw everything away and re-fetch" would also be fast,
	// and would not be a resync. This is the same distinction tests/resync_api_test.go makes,
	// re-asserted here so the TIMING cannot be met by degrading the behaviour.
	if returning.hasFrame("resync_required") {
		t.Error("the returning client was told to re-fetch; this measured a re-subscribe, " +
			"not a resync")
	}
	returning.assertNoErrorFrames()
}
