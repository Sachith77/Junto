package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/junto/junto/internal/domain"
)

// RECONNECT AND RESYNC (Stage 2, Slice 2, Part A).
//
// # What this file has to prove
//
// Rule 3 exists so that "everything since seq N" is a complete answer: every write, from
// either transport, goes through the service layer and lands in trip_ops. Slice 1 built that
// and then could not use it — a client with a last-known sequence number was answered
// `resync_required`, so the log's completeness was load-bearing for nothing. This is where it
// starts paying.
//
// The distinction being tested is narrow and easy to fake. A client that reconnects, throws
// its state away and re-fetches over REST also ends up correct. So correctness of the final
// state is necessary but nowhere near sufficient, and every test here additionally asserts
// HOW the client got there: the operations it missed arrived as op frames carrying the
// sequence numbers from the log, and its replica was folded forward rather than rebuilt.

// resyncFixture is a trip with four editing members: three that stay connected and write, and
// one that goes away and has to catch up.
//
// Four rather than two because the interesting case is not "did I miss an operation" but "did
// I miss a CONCURRENT INTERLEAVING that I now have to fold onto state I never saw". Three
// simultaneous writers racing at the trip's sequencer produce an interleaving that no single
// participant chose, and the returning client has to arrive at exactly it.
type resyncFixture struct {
	*convergenceFixture

	carol   *wsClient
	dave    *wsClient
	carolID domain.ID
	daveID  domain.ID

	carolHTTP  *client
	daveHTTP   *client
	carolToken string
	daveToken  string
}

// joinAsEditor puts a new member on the trip through the real invitation flow, so their
// capabilities come from a real membership row rather than a fixture that could grant more
// than production does.
func joinAsEditor(t *testing.T, f *convergenceFixture, prefix string) (*client, string, domain.ID) {
	t.Helper()

	memberHTTP := newClient(t)
	memberEmail := signupAndVerify(t, memberHTTP, prefix)

	resp := f.aliceHTTP.do(http.MethodPost, "/api/v1/trips/"+f.tripID.String()+"/invitations",
		map[string]any{"email": memberEmail, "role": "editor"}, withBearer(f.aliceToken))
	assertStatus(t, resp, http.StatusCreated)

	msg, ok := testMailer.lastTo(memberEmail)
	if !ok {
		t.Fatalf("expected an invitation email for %s", memberEmail)
	}
	inviteToken := extractToken(t, msg.TextBody, "invitations/accept")

	memberToken := login(t, memberHTTP, memberEmail)
	resp = memberHTTP.do(http.MethodPost, "/api/v1/invitations/accept",
		map[string]string{"token": inviteToken}, withBearer(memberToken))
	assertStatus(t, resp, http.StatusOK)

	return memberHTTP, memberToken, meID(t, memberHTTP, memberToken)
}

func newResyncFixture(t *testing.T, prefix string) *resyncFixture {
	t.Helper()

	base := newConvergenceFixture(t, prefix)

	carolHTTP, carolToken, carolID := joinAsEditor(t, base, prefix+"-carol")
	daveHTTP, daveToken, daveID := joinAsEditor(t, base, prefix+"-dave")

	f := &resyncFixture{
		convergenceFixture: base,
		carolID:            carolID, daveID: daveID,
		carolHTTP: carolHTTP, daveHTTP: daveHTTP,
		carolToken: carolToken, daveToken: daveToken,
	}
	f.carol = dialWS(t, "carol", carolHTTP, carolToken, carolID)
	f.dave = dialWS(t, "dave", daveHTTP, daveToken, daveID)
	f.carol.subscribe(base.tripID)
	f.dave.subscribe(base.tripID)

	// Registered on the base fixture so settle() and the convergence equality cover all four
	// clients automatically.
	base.extra = append(base.extra, f.carol, f.dave)
	return f
}

// --- the scenario the slice was specified around -------------------------------------------

// TestAReconnectingClientResyncsFromTheLogAndThenConverges is Part A's whole argument in one
// test.
//
// The shape: Dave subscribes and is up to date. Dave disappears. Alice, Bob and Carol then
// write CONCURRENTLY — released from a single barrier so their operations race at the trip's
// sequencer — and one of those writes touches the very slot Dave is going to edit when he
// gets back. Dave reconnects, resumes from the sequence number he had, folds what he missed,
// and only then makes his own edit.
//
// The assertions, in the order they matter:
//
//  1. Dave was NOT told to resync from scratch. If the server answers `resync_required`, the
//     final state can still be right and the feature is still missing.
//  2. Dave received exactly the operations he missed, by sequence number, from the log.
//  3. Dave's own edit lands ON TOP of the interleaving he was absent for — his value wins the
//     field he wrote, and the field he did not touch still holds what someone else wrote
//     while he was away. That is what proves he folded rather than clobbered.
//  4. All five views agree: fold(trip_ops), the database, and all four clients.
func TestAReconnectingClientResyncsFromTheLogAndThenConverges(t *testing.T) {
	f := newResyncFixture(t, "resync-core")

	contested := f.makeSlot("Hotel — to be argued over")
	f.settle()

	// Where Dave is when the lights go out.
	daveSeq := f.dave.snapshot().Seq
	if daveSeq == 0 {
		t.Fatal("dave folded nothing before disconnecting; the scenario would be vacuous")
	}
	f.dave.disconnect()

	// Three concurrent writers, one barrier. Alice and Bob both edit the CONTESTED slot on
	// different fields — the field-level merge case — while Carol adds an option elsewhere.
	otherSlot := f.makeSlot("Dinner")
	newOption := domain.NewID()

	f.raceSubmit(
		func() submission {
			id := f.alice.submit(f.tripID, domain.OpSlotEdit, contested,
				[]string{domain.FieldTitle}, map[string]any{"title": "Hotel Bellevue"})
			return wsSubmit(f.alice, id)
		},
		func() submission {
			id := f.bob.submit(f.tripID, domain.OpSlotEdit, contested,
				[]string{domain.FieldNotes}, map[string]any{"notes": "bob says: near the station"})
			return wsSubmit(f.bob, id)
		},
		func() submission {
			id := f.carol.submit(f.tripID, domain.OpOptionCreate, newOption,
				[]string{domain.FieldSlotID, domain.FieldTitle},
				map[string]any{"slot_id": otherSlot.String(), "title": "Trattoria"})
			return wsSubmit(f.carol, id)
		},
	)

	// The three connected clients settle; Dave is still away and must not be waited on.
	head := f.currentSeq()
	f.alice.waitForSeq(head, 15*time.Second)
	f.bob.waitForSeq(head, 15*time.Second)
	f.carol.waitForSeq(head, 15*time.Second)
	if head <= daveSeq {
		t.Fatalf("nothing happened while dave was away (seq %d -> %d); the test would prove nothing",
			daveSeq, head)
	}

	// Dave comes back on a NEW socket, carrying the replica he already had.
	reconnected := dialWSOn(t, "dave", f.daveHTTP, testServer.URL, f.daveToken, f.daveID, f.dave.snapshot())
	f.replaceDave(reconnected)
	reconnected.resume(f.tripID)
	reconnected.waitForSeq(head, 15*time.Second)

	// 1. Resume was answered by replaying, not by giving up.
	if reconnected.hasFrame("resync_required") {
		t.Fatal("dave was told to resync from scratch; resume from the operation log did not happen")
	}

	// 2. Exactly the missed range arrived, as operations, in order.
	assertReplayedRange(t, reconnected, daveSeq, head)

	// 3. Dave now edits the contested slot's notes. He is overwriting a value he only ever
	//    learned about through the resync, which is the point.
	daveEdit := reconnected.submit(f.tripID, domain.OpSlotEdit, contested,
		[]string{domain.FieldNotes}, map[string]any{"notes": "dave says: book the suite"})
	reconnected.awaitResolution(daveEdit, 15*time.Second)
	f.settle()

	final := f.dave.snapshot().Slots[contested]
	if final == nil {
		t.Fatal("dave has no fold of the contested slot at all")
	}
	if final.Notes != "dave says: book the suite" {
		t.Errorf("dave's own edit did not win the field he wrote: notes = %q", final.Notes)
	}
	if final.Title != "Hotel Bellevue" {
		t.Errorf("the title written while dave was offline was lost: %q; dave's edit clobbered "+
			"a field it did not name, which is a merge failure and not a resync failure", final.Title)
	}

	// 4. Everything agrees.
	f.assertConverged()
}

// replaceDave swaps the reconnected client into the fixture so settle() and the convergence
// equality use the live socket rather than the dead one.
func (f *resyncFixture) replaceDave(next *wsClient) {
	for i, c := range f.extra {
		if c == f.dave {
			f.extra[i] = next
		}
	}
	f.dave = next
}

// assertReplayedRange is the "not a full re-fetch" assertion.
//
// It checks that every sequence number in (from, to] was delivered to this client as an
// operation. Duplicates are tolerated on purpose: a resuming subscriber joins its room before
// reading the log, so an operation committed during the handover legitimately arrives twice —
// once from the replay and once live. That is the at-least-once property the fold is written
// against, and forbidding it here would be asserting something the system deliberately does
// not promise.
func assertReplayedRange(t *testing.T, w *wsClient, from, to int64) {
	t.Helper()

	seen := map[int64]bool{}
	for _, seq := range w.receivedSeqs() {
		seen[seq] = true
	}
	var missing []int64
	for seq := from + 1; seq <= to; seq++ {
		if !seen[seq] {
			missing = append(missing, seq)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%s resumed from seq %d but never received operations %v (expected the whole "+
			"range up to %d from the log); it received %v",
			w.name, from, missing, to, w.receivedSeqs())
	}
}

// --- resume from nothing --------------------------------------------------------------------

// TestResumingFromZeroReplaysTheEntireLog answers the question the pointer on since_seq exists
// to make askable.
//
// Absent means "I have no state, start me at the head". Zero means "I have no state, and I
// want all of it". They are different requests and an int64 could not tell them apart. This
// matters because there is no snapshot endpoint: a brand-new client's only log-based route to
// a full picture is to fold from the beginning — which is exactly what
// fold(trip_ops) == database state guarantees is equivalent to fetching it.
func TestResumingFromZeroReplaysTheEntireLog(t *testing.T) {
	f := newConvergenceFixture(t, "resync-zero")

	slotID := f.makeSlot("Something with history")
	f.raceSubmit(func() submission {
		id := f.alice.submit(f.tripID, domain.OpSlotEdit, slotID,
			[]string{domain.FieldTitle}, map[string]any{"title": "Edited once"})
		return wsSubmit(f.alice, id)
	})
	f.settle()
	head := f.currentSeq()

	// A client that has never seen this trip, folding from nothing.
	fresh := dialWSOn(t, "newcomer", f.bobHTTP, testServer.URL, f.bobToken, f.bobID, domain.NewReplica())
	fresh.resume(f.tripID)
	fresh.waitForSeq(head, 15*time.Second)

	if fresh.hasFrame("resync_required") {
		t.Fatal("a full replay from seq 0 was refused")
	}
	assertReplayedRange(t, fresh, 0, head)

	// The whole point: folding the log from nothing reproduces the database.
	f.extra = append(f.extra, fresh)
	f.assertConverged()
}

// --- the boundaries, stated as tests ---------------------------------------------------------

// TestResumingFromAheadOfTheServerIsRefused covers a client whose sequence number cannot
// possibly be explained by this database — a restore from backup, or a client pointed at a
// different deployment. There is no range to replay, and pretending otherwise would hand it a
// fold that silently disagrees with everyone else's.
func TestResumingFromAheadOfTheServerIsRefused(t *testing.T) {
	f := newConvergenceFixture(t, "resync-ahead")
	f.makeSlot("Anything")
	f.settle()

	ahead := dialWS(t, "time-traveller", f.bobHTTP, f.bobToken, f.bobID)
	ahead.send(map[string]any{
		"type": "subscribe", "trip_id": f.tripID.String(), "since_seq": f.currentSeq() + 1000,
	})
	ahead.waitForFrame("resync_required", 10*time.Second)
}

// TestANegativeSinceSeqIsRejected keeps a client bug from becoming a full replay nobody asked
// for. A negative sequence number is malformed, not "very far behind", and silently clamping
// it to zero would replay the entire log on a typo.
func TestANegativeSinceSeqIsRejected(t *testing.T) {
	f := newConvergenceFixture(t, "resync-negative")
	f.makeSlot("Anything")
	f.settle()

	f.bob.send(map[string]any{
		"type": "subscribe", "trip_id": f.tripID.String(), "since_seq": -1,
	})
	frame := f.bob.waitForFrame("error", 10*time.Second)

	var code string
	if err := json.Unmarshal(frame["code"], &code); err != nil {
		t.Fatalf("decoding the error frame: %v", err)
	}
	if code != "validation_failed" {
		t.Fatalf("a negative since_seq produced code %q, want validation_failed", code)
	}
}

// TestResumingFromTooFarBehindIsRefused pins the documented ceiling.
//
// Replay is correct at ANY distance — the log is complete and unpruned, and
// fold(log) == database state is asserted elsewhere in this suite — so this bound is an
// economic decision, not a correctness one. Replay costs a trip's HISTORY while a re-fetch
// costs its SIZE, and only one of those grows without bound. Past the ceiling the honest
// answer is "re-fetch", which is what a client with no stored sequence number does anyway.
//
// Tested against an instance configured with a deliberately tiny ceiling rather than by
// committing ten thousand operations: the branch under test is the same one, and a test that
// spends a minute writing rows to prove a constant is comparing numbers, not behaviour.
func TestResumingFromTooFarBehindIsRefused(t *testing.T) {
	small, err := newStack(stackConfig{Pool: testPG.Pool, MaxResyncOps: 2})
	if err != nil {
		t.Fatalf("building a small-ceiling instance: %v", err)
	}
	defer small.Close()

	httpc := newClientFor(t, small.Server)
	email := signupAndVerify(t, httpc, "resync-ceiling")
	token := login(t, httpc, email)
	userID := meID(t, httpc, token)

	trip := createTrip(t, httpc, token, "Long History")
	tripID, err := domain.ParseID("trip_id", trip["id"].(string))
	if err != nil {
		t.Fatalf("parsing trip id: %v", err)
	}

	// Four operations, so a resume from zero is three past a ceiling of two.
	for i := 0; i < 4; i++ {
		makeSlotOn(t, httpc, token, tripID, fmt.Sprintf("Slot %d", i))
	}

	w := dialWSOn(t, "far-behind", httpc, small.Server.URL, token, userID, domain.NewReplica())
	w.send(map[string]any{"type": "subscribe", "trip_id": tripID.String(), "since_seq": 0})
	w.waitForFrame("resync_required", 10*time.Second)

	// And the same client, asking for a distance INSIDE the ceiling, is served normally —
	// otherwise this test would also pass if resume were broken outright.
	head, err := small.Trips.CurrentOpSeq(context.Background(), tripID)
	if err != nil {
		t.Fatalf("reading the trip sequence: %v", err)
	}
	near := dialWSOn(t, "just-behind", httpc, small.Server.URL, token, userID, domain.NewReplica())
	near.send(map[string]any{
		"type": "subscribe", "trip_id": tripID.String(), "since_seq": head - 1,
	})
	near.waitForFrame("subscribed", 10*time.Second)
	if near.hasFrame("resync_required") {
		t.Fatal("a resume well inside the ceiling was refused; the bound is not the reason " +
			"the previous subscribe failed")
	}
}

// makeSlotOn creates a slot over REST against an arbitrary instance.
func makeSlotOn(t *testing.T, c *client, token string, tripID domain.ID, title string) domain.ID {
	t.Helper()
	resp := c.do(http.MethodPost, "/api/v1/trips/"+tripID.String()+"/slots",
		map[string]any{"kind": "lodging", "title": title}, withBearer(token))
	assertStatus(t, resp, http.StatusCreated)
	var slot map[string]any
	resp.decode(t, &slot)
	id, err := domain.ParseID("id", slot["id"].(string))
	if err != nil {
		t.Fatalf("parsing slot id: %v", err)
	}
	return id
}

// TestResumeWorksAfterARESTOriginatedChange is Rule 3's guarantee, exercised from the angle it
// was written for.
//
// The failure this rules out is the one CLAUDE.md calls the most important line in the file:
// if REST writes bypassed the operation log and only WebSocket writes appended to it, a
// reconnecting client asking for everything since seq N would silently miss every
// REST-originated change — and resync would degrade into a full re-fetch while every other
// test still passed. Here the ONLY thing that happens while the client is away is a REST call.
func TestResumeWorksAfterARESTOriginatedChange(t *testing.T) {
	f := newConvergenceFixture(t, "resync-rest")
	f.makeSlot("Before the disconnect")
	f.settle()

	before := f.bob.snapshot().Seq
	f.bob.disconnect()

	// A plain HTTP request. No socket involved anywhere in this write.
	restSlot := f.makeSlot("Created over REST while bob was away")
	head := f.currentSeq()
	if head <= before {
		t.Fatal("the REST write did not advance the trip's sequence")
	}

	returning := dialWSOn(t, "bob", f.bobHTTP, testServer.URL, f.bobToken, f.bobID, f.bob.snapshot())
	f.bob = returning
	returning.resume(f.tripID)
	returning.waitForSeq(head, 15*time.Second)

	if returning.hasFrame("resync_required") {
		t.Fatal("bob was told to resync rather than being sent the REST-originated operations")
	}
	assertReplayedRange(t, returning, before, head)

	if got := returning.snapshot().Slots[restSlot]; got == nil {
		t.Fatal("the slot created over REST never reached the resuming client: a REST write is " +
			"missing from the operation log, and resync is cosmetic")
	}
	f.assertConverged()
}

// TestSubscribingWithoutASinceSeqStartsAtTheHead keeps the other half of the pointer
// distinction honest: a client that sends no since_seq must still get the live stream from
// now, exactly as it did in Slice 1, and must NOT be handed the whole log.
func TestSubscribingWithoutASinceSeqStartsAtTheHead(t *testing.T) {
	f := newConvergenceFixture(t, "resync-absent")
	f.makeSlot("History this client should not be sent")
	f.settle()
	head := f.currentSeq()

	fresh := dialWSOn(t, "fresh", f.bobHTTP, testServer.URL, f.bobToken, f.bobID, domain.NewReplica())
	fresh.subscribe(f.tripID)

	// Nothing from before should arrive. Waited on rather than checked instantly, so that a
	// replay that was merely slow would still fail this.
	time.Sleep(250 * time.Millisecond)
	for _, seq := range fresh.receivedSeqs() {
		if seq <= head {
			t.Fatalf("a client that sent no since_seq was replayed history: got seq %d, head was %d",
				seq, head)
		}
	}
	if got := fresh.snapshot().Seq; got != head {
		t.Fatalf("a fresh subscriber's baseline is %d, want the head %d", got, head)
	}
}
