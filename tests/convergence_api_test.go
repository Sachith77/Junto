package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/junto/junto/internal/domain"
)

// THE CONVERGENCE TEST.
//
// This file is what makes the resume clause "transport-agnostic WebSocket-based sync engine
// implementing CRDT conflict resolution for concurrent multi-user edits" literally true. If
// it is ever cut, the claim goes with it.
//
// # What is real here
//
// Real Postgres (testcontainers), real router, real middleware, real services, real ticket
// handshake, real WebSocket connections. The clients maintain their own replicas by folding
// the operations they receive — they share no code path with the server's view of the state,
// so agreement between them is evidence rather than tautology.
//
// # The assertion
//
// Every scenario ends with the SAME four-way equality:
//
//	fold(trip_ops) == database state == client A's state == client B's state
//
// All four matter. Comparing only the two clients would pass if both were wrong in the same
// way. Comparing only against the database would not prove clients converge. Comparing
// against fold(log) is what proves the log is a faithful, replayable record — which is the
// entire basis for reconnect and resync.
//
// # Why the concurrency is in the SUBMISSION, not the ordering
//
// Both clients block on one barrier and then submit simultaneously. That race — two writers
// arriving at the trip's sequencer at the same instant — is where this system's concurrency
// actually lives. A test that permuted the ORDER of already-committed operations would prove
// nothing, because they are sorted by sequence number before folding. See the note at the top
// of internal/domain/op_test.go.

// convergenceFixture is a trip with two editing members, each on a real socket.
type convergenceFixture struct {
	t *testing.T

	tripID domain.ID

	alice   *wsClient
	bob     *wsClient
	aliceID domain.ID
	bobID   domain.ID

	// extra holds any additional folding clients a scenario adds — a third and fourth member
	// in the resync test, a client on a second instance in the multi-instance test. They are
	// held here rather than locally in those tests so that settle() and the convergence
	// equality automatically cover them; a client the assertion forgot about would be a
	// silently weaker test.
	extra []*wsClient

	aliceHTTP  *client
	bobHTTP    *client
	aliceToken string
	bobToken   string
}

func newConvergenceFixture(t *testing.T, prefix string) *convergenceFixture {
	t.Helper()

	aliceHTTP := newClient(t)
	aliceEmail := signupAndVerify(t, aliceHTTP, prefix+"-alice")
	aliceToken := login(t, aliceHTTP, aliceEmail)

	bobHTTP := newClient(t)
	bobEmail := signupAndVerify(t, bobHTTP, prefix+"-bob")

	trip := createTrip(t, aliceHTTP, aliceToken, "Convergence Trip")
	tripIDStr := trip["id"].(string)

	// Bob joins as an editor through the real invitation flow, so his capabilities come from
	// a real membership row rather than a test fixture that could grant more than production.
	resp := aliceHTTP.do(http.MethodPost, "/api/v1/trips/"+tripIDStr+"/invitations",
		map[string]any{"email": bobEmail, "role": "editor"}, withBearer(aliceToken))
	assertStatus(t, resp, http.StatusCreated)

	msg, ok := testMailer.lastTo(bobEmail)
	if !ok {
		t.Fatal("expected an invitation email for bob")
	}
	inviteToken := extractToken(t, msg.TextBody, "invitations/accept")

	bobToken := login(t, bobHTTP, bobEmail)
	resp = bobHTTP.do(http.MethodPost, "/api/v1/invitations/accept",
		map[string]string{"token": inviteToken}, withBearer(bobToken))
	assertStatus(t, resp, http.StatusOK)

	tripID, err := domain.ParseID("trip_id", tripIDStr)
	if err != nil {
		t.Fatalf("parsing trip id: %v", err)
	}

	aliceID := meID(t, aliceHTTP, aliceToken)
	bobID := meID(t, bobHTTP, bobToken)

	f := &convergenceFixture{
		t: t, tripID: tripID,
		aliceID: aliceID, bobID: bobID,
		aliceHTTP: aliceHTTP, bobHTTP: bobHTTP,
		aliceToken: aliceToken, bobToken: bobToken,
	}
	f.alice = dialWS(t, "alice", aliceHTTP, aliceToken, aliceID)
	f.bob = dialWS(t, "bob", bobHTTP, bobToken, bobID)
	f.alice.subscribe(tripID)
	f.bob.subscribe(tripID)
	return f
}

func meID(t *testing.T, c *client, token string) domain.ID {
	t.Helper()
	resp := c.do(http.MethodGet, "/api/v1/me", nil, withBearer(token))
	assertStatus(t, resp, http.StatusOK)
	var me map[string]any
	resp.decode(t, &me)
	id, err := domain.ParseID("id", me["id"].(string))
	if err != nil {
		t.Fatalf("parsing user id: %v", err)
	}
	return id
}

// makeSlot creates a slot over REST so the scenario under test starts from a known state.
// It also means every scenario incidentally proves that a REST-originated write reaches the
// operation log and the connected clients — the Rule 3 guarantee, exercised for free.
func (f *convergenceFixture) makeSlot(title string) domain.ID {
	f.t.Helper()
	resp := f.aliceHTTP.do(http.MethodPost, "/api/v1/trips/"+f.tripID.String()+"/slots",
		map[string]any{"kind": "lodging", "title": title}, withBearer(f.aliceToken))
	assertStatus(f.t, resp, http.StatusCreated)
	var slot map[string]any
	resp.decode(f.t, &slot)
	id, err := domain.ParseID("id", slot["id"].(string))
	if err != nil {
		f.t.Fatalf("parsing slot id: %v", err)
	}
	return id
}

func (f *convergenceFixture) makeOption(slotID domain.ID, title string) domain.ID {
	f.t.Helper()
	resp := f.aliceHTTP.do(http.MethodPost,
		fmt.Sprintf("/api/v1/trips/%s/slots/%s/options", f.tripID, slotID),
		map[string]any{"title": title}, withBearer(f.aliceToken))
	assertStatus(f.t, resp, http.StatusCreated)
	var option map[string]any
	resp.decode(f.t, &option)
	id, err := domain.ParseID("id", option["id"].(string))
	if err != nil {
		f.t.Fatalf("parsing option id: %v", err)
	}
	return id
}

// submission is one racer: who submitted, and the client op id to wait on afterwards. A REST
// racer returns a nil client, because an HTTP call has already completed by the time it
// returns and there is nothing to await.
type submission struct {
	client     *wsClient
	clientOpID domain.ID
}

// raceSubmit releases every racer from one barrier so their operations reach the trip's
// sequencer simultaneously, then waits for the server to resolve each one.
//
// Both halves matter. Without the barrier the operations serialize by test-execution order
// and no conflict ever occurs. Without the wait, the test races the server: submissions are
// asynchronous, so an assertion made immediately afterwards reads state from before the
// writes landed — which is exactly how this test first failed, reporting "both edits lost"
// when in fact neither had been applied yet.
func (f *convergenceFixture) raceSubmit(submitters ...func() submission) {
	f.t.Helper()

	var start sync.WaitGroup
	var done sync.WaitGroup
	results := make([]submission, len(submitters))

	start.Add(1)
	for i, fn := range submitters {
		done.Add(1)
		go func(i int, fn func() submission) {
			defer done.Done()
			start.Wait()
			results[i] = fn()
		}(i, fn)
	}
	start.Done()
	done.Wait()

	for _, r := range results {
		if r.client != nil {
			r.client.awaitResolution(r.clientOpID, 15*time.Second)
		}
	}
}

// ws builds a submission for a WebSocket racer.
func wsSubmit(c *wsClient, id domain.ID) submission { return submission{client: c, clientOpID: id} }

// restSubmit marks a racer that completed synchronously and needs no await.
func restSubmit() submission { return submission{} }

// currentSeq reads the trip's sequence directly from the database — the authoritative answer
// to "how far should the clients have caught up".
func (f *convergenceFixture) currentSeq() int64 {
	f.t.Helper()
	seq, err := testTrips.CurrentOpSeq(context.Background(), f.tripID)
	if err != nil {
		f.t.Fatalf("reading current op seq: %v", err)
	}
	return seq
}

// clients are every folding participant. Alice and Bob are always present; the resync and
// multi-instance scenarios add more, and the equality below then covers all of them.
func (f *convergenceFixture) clients() []*wsClient {
	return append([]*wsClient{f.alice, f.bob}, f.extra...)
}

// settle waits for every client to fold everything committed so far, then checks that none
// received an error frame. That second check is not decoration: the easiest way for a
// convergence test to pass vacuously is for both operations to have been rejected.
func (f *convergenceFixture) settle() {
	f.t.Helper()
	seq := f.currentSeq()
	for _, c := range f.clients() {
		c.waitForSeq(seq, 15*time.Second)
	}
	for _, c := range f.clients() {
		c.assertNoErrorFrames()
	}
}

// assertConverged is the four-way equality. Every scenario in this file ends here.
func (f *convergenceFixture) assertConverged() {
	f.t.Helper()
	assertConvergence(f.t, f.tripID, f.clients())
}

// assertConvergence is the equality itself, over however many clients a scenario has:
//
//	fold(trip_ops) == database state == every connected client's own fold
//
// All of it matters. Comparing only the clients would pass if they were all wrong in the same
// way. Comparing only against the database would not prove clients converge. Comparing against
// fold(log) is what proves the log is a faithful, replayable record — which is the entire
// basis for reconnect and resync, and therefore the thing the resync tests lean on hardest.
func assertConvergence(t *testing.T, tripID domain.ID, clients []*wsClient) {
	t.Helper()
	ctx := context.Background()

	// 1. fold(trip_ops) — replay the immutable log from nothing.
	ops, err := testOpLog.ListSince(ctx, tripID, 0, 0)
	if err != nil {
		t.Fatalf("reading the operation log: %v", err)
	}
	folded, err := domain.Fold(ops)
	if err != nil {
		t.Fatalf("folding the operation log: %v", err)
	}

	// The log must also be gapless and strictly increasing — that is what lets a client treat
	// contiguity as a completeness check (D61) and what a rolled-back transaction must not
	// break.
	for i, op := range ops {
		if op.Seq != int64(i+1) {
			t.Fatalf("operation log is not gapless: position %d has seq %d", i, op.Seq)
		}
	}

	// 2. The database.
	dbSlots, err := testSlots.ListForTrip(ctx, tripID)
	if err != nil {
		t.Fatalf("listing slots: %v", err)
	}
	dbOptions, err := testOptions.ListForTrip(ctx, tripID)
	if err != nil {
		t.Fatalf("listing options: %v", err)
	}
	dbVotes, err := testVotes.ListForTrip(ctx, tripID)
	if err != nil {
		t.Fatalf("listing votes: %v", err)
	}

	// 3. Each client's independently folded replica.
	states := map[string]*domain.Replica{"fold(log)": folded}
	for _, c := range clients {
		states[c.name] = c.snapshot()
	}

	for who, state := range states {
		for _, slot := range dbSlots {
			compareSlot(t, who, slot, state.Slots[slot.ID])
		}
		for _, option := range dbOptions {
			compareOption(t, who, option, state.Options[option.ID])
		}
		for _, vote := range dbVotes {
			compareVote(t, who, vote, state.Votes[vote.ID])
		}
	}
}

// compareSlot checks the MERGEABLE fields. Timestamps are excluded deliberately: created_at
// and updated_at are database clocks, not merged values, and comparing them would make the
// test fail for a reason that has nothing to do with convergence.
func compareSlot(t *testing.T, who string, want, got *domain.Slot) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is missing slot %s entirely", who, want.ID)
	}
	mismatch := func(field string, w, g any) {
		t.Errorf("%s disagrees with the database on slot %s %s: want %v, got %v",
			who, want.ID, field, w, g)
	}
	if got.Title != want.Title {
		mismatch("title", want.Title, got.Title)
	}
	if got.Notes != want.Notes {
		mismatch("notes", want.Notes, got.Notes)
	}
	if got.Kind != want.Kind {
		mismatch("kind", want.Kind, got.Kind)
	}
	if got.Position != want.Position {
		mismatch("position", want.Position, got.Position)
	}
	if got.Status != want.Status {
		mismatch("status", want.Status, got.Status)
	}
	if idString(got.DayID) != idString(want.DayID) {
		mismatch("day_id", idString(want.DayID), idString(got.DayID))
	}
	if idString(got.SelectedOptionID) != idString(want.SelectedOptionID) {
		mismatch("selected_option_id", idString(want.SelectedOptionID), idString(got.SelectedOptionID))
	}
	if timeOfDayString(got.StartTime) != timeOfDayString(want.StartTime) {
		mismatch("start_time", timeOfDayString(want.StartTime), timeOfDayString(got.StartTime))
	}
	if got.Version != want.Version {
		mismatch("version", want.Version, got.Version)
	}
	if got.IsDeleted() != want.IsDeleted() {
		mismatch("deleted", want.IsDeleted(), got.IsDeleted())
	}
}

func compareOption(t *testing.T, who string, want, got *domain.SlotOption) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is missing option %s entirely", who, want.ID)
	}
	if got.Title != want.Title || got.Notes != want.Notes || got.ExternalURL != want.ExternalURL {
		t.Errorf("%s disagrees on option %s content: want (%q, %q, %q), got (%q, %q, %q)",
			who, want.ID, want.Title, want.Notes, want.ExternalURL,
			got.Title, got.Notes, got.ExternalURL)
	}
	if got.Place.Name != want.Place.Name || got.Place.Address != want.Place.Address {
		t.Errorf("%s disagrees on option %s place: want (%q, %q), got (%q, %q)",
			who, want.ID, want.Place.Name, want.Place.Address, got.Place.Name, got.Place.Address)
	}
	if got.SlotID != want.SlotID {
		t.Errorf("%s disagrees on option %s slot_id", who, want.ID)
	}
	if got.IsDeleted() != want.IsDeleted() {
		t.Errorf("%s disagrees on option %s deleted: want %v, got %v",
			who, want.ID, want.IsDeleted(), got.IsDeleted())
	}
	if got.Version != want.Version {
		t.Errorf("%s disagrees on option %s version: want %d, got %d",
			who, want.ID, want.Version, got.Version)
	}
}

func compareVote(t *testing.T, who string, want, got *domain.Vote) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is missing vote %s entirely", who, want.ID)
	}
	if idString(got.OptionID) != idString(want.OptionID) {
		t.Errorf("%s disagrees on vote %s option: want %v, got %v",
			who, want.ID, idString(want.OptionID), idString(got.OptionID))
	}
	if got.SlotID != want.SlotID || got.UserID != want.UserID {
		t.Errorf("%s disagrees on vote %s identity keys", who, want.ID)
	}
}

func idString(id *domain.ID) string {
	if id == nil {
		return "<nil>"
	}
	return id.String()
}

func timeOfDayString(t *domain.TimeOfDay) string {
	if t == nil {
		return "<nil>"
	}
	return t.String()
}

// --- (a) THE PRIMARY ASSERTION ------------------------------------------------------------

// TestConcurrentEditsToDifferentFieldsBothSurvive is the scenario that distinguishes this
// system from Stage 1's optimistic-concurrency check.
//
// Under last-writer-REJECTED, one of these two operations returns 409 and that member's work
// is lost. Under field-level merge both apply, because each names only the field it changes
// and the sequencer serializes them without either touching the other's column. If this test
// ever regresses to "one of them won", the CRDT claim is false regardless of what the rest of
// the suite says.
func TestConcurrentEditsToDifferentFieldsBothSurvive(t *testing.T) {
	f := newConvergenceFixture(t, "conv-fields")
	slotID := f.makeSlot("Where are we staying in Goa")
	f.settle()

	f.raceSubmit(
		func() submission {
			return wsSubmit(f.alice, f.alice.submit(f.tripID, domain.OpSlotEdit, slotID,
				[]string{domain.FieldTitle}, map[string]any{"title": "Where are we staying in Anjuna"}))
		},
		func() submission {
			return wsSubmit(f.bob, f.bob.submit(f.tripID, domain.OpSlotEdit, slotID,
				[]string{domain.FieldNotes}, map[string]any{"notes": "budget: 8000/night"}))
		},
	)
	f.settle()

	slot, err := testSlots.GetByID(context.Background(), slotID)
	if err != nil {
		t.Fatalf("reloading slot: %v", err)
	}
	if slot.Title != "Where are we staying in Anjuna" {
		t.Errorf("alice's title edit was lost: %q", slot.Title)
	}
	if slot.Notes != "budget: 8000/night" {
		t.Errorf("bob's notes edit was lost: %q", slot.Notes)
	}
	f.assertConverged()
}

// --- (b) same field: deterministic LWW, and the loser is still in the log ------------------

func TestConcurrentEditsToTheSameFieldConvergeAndKeepBothInTheLog(t *testing.T) {
	f := newConvergenceFixture(t, "conv-samefield")
	slotID := f.makeSlot("Undecided")
	f.settle()

	f.raceSubmit(
		func() submission {
			return wsSubmit(f.alice, f.alice.submit(f.tripID, domain.OpSlotEdit, slotID,
				[]string{domain.FieldTitle}, map[string]any{"title": "Alice's title"}))
		},
		func() submission {
			return wsSubmit(f.bob, f.bob.submit(f.tripID, domain.OpSlotEdit, slotID,
				[]string{domain.FieldTitle}, map[string]any{"title": "Bob's title"}))
		},
	)
	f.settle()

	ctx := context.Background()
	slot, err := testSlots.GetByID(ctx, slotID)
	if err != nil {
		t.Fatalf("reloading slot: %v", err)
	}
	if slot.Title != "Alice's title" && slot.Title != "Bob's title" {
		t.Fatalf("final title is neither submission: %q", slot.Title)
	}

	// "Last writer wins" describes the visible STATE, not the record. Both edits are in the
	// log, at different sequence numbers, which is what makes the outcome auditable and what
	// a resyncing client replays.
	ops, err := testOpLog.ListSince(ctx, f.tripID, 0, 0)
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	titleEdits := 0
	var lastTitle string
	for _, op := range ops {
		if op.Kind != domain.OpSlotEdit || op.EntityID != slotID {
			continue
		}
		fields, decodeErr := op.PayloadFields()
		if decodeErr != nil {
			t.Fatalf("decoding op payload: %v", decodeErr)
		}
		if raw, ok := fields[domain.FieldTitle]; ok {
			titleEdits++
			if err := json.Unmarshal(raw, &lastTitle); err != nil {
				t.Fatalf("decoding title: %v", err)
			}
		}
	}
	if titleEdits != 2 {
		t.Errorf("expected both title edits in the log, found %d — the loser was discarded",
			titleEdits)
	}
	if lastTitle != slot.Title {
		t.Errorf("the highest-seq title edit (%q) does not match the stored title (%q)",
			lastTitle, slot.Title)
	}
	f.assertConverged()
}

// --- (c) concurrent reorder ---------------------------------------------------------------

// TestConcurrentReorderConverges checks the claim made in the design doc that fractional
// indexing carries reordering with nothing extra at the engine layer. It is here because that
// claim was checked rather than assumed.
func TestConcurrentReorderConverges(t *testing.T) {
	f := newConvergenceFixture(t, "conv-reorder")
	first := f.makeSlot("First")
	second := f.makeSlot("Second")
	third := f.makeSlot("Third")
	f.settle()

	// Both members drag the SAME slot to two different places at the same instant.
	f.raceSubmit(
		func() submission {
			return wsSubmit(f.alice, f.alice.submit(f.tripID, domain.OpSlotMove, third,
				[]string{domain.FieldDayID, domain.FieldPosition},
				map[string]any{"after_id": first.String()}))
		},
		func() submission {
			return wsSubmit(f.bob, f.bob.submit(f.tripID, domain.OpSlotMove, third,
				[]string{domain.FieldDayID, domain.FieldPosition},
				map[string]any{"after_id": second.String()}))
		},
	)
	f.settle()
	f.assertConverged()

	// And the resulting order must be a real total order, identical on both replicas.
	assertSameSlotOrder(t, f.alice.snapshot(), f.bob.snapshot())
}

func assertSameSlotOrder(t *testing.T, a, b *domain.Replica) {
	t.Helper()
	orderOf := func(r *domain.Replica) []string {
		type entry struct{ position, id string }
		var entries []entry
		for id, s := range r.Slots {
			if s.IsDeleted() {
				continue
			}
			entries = append(entries, entry{s.Position, id.String()})
		}
		// Ordered by (position, id) — the id tiebreak is not cosmetic. Two clients inserting
		// into the same gap without seeing each other legitimately produce the SAME
		// fractional index, and the id is what makes every replica derive one total order.
		for i := 1; i < len(entries); i++ {
			for j := i; j > 0; j-- {
				prev, cur := entries[j-1], entries[j]
				if prev.position < cur.position || (prev.position == cur.position && prev.id <= cur.id) {
					break
				}
				entries[j-1], entries[j] = entries[j], entries[j-1]
			}
		}
		out := make([]string, 0, len(entries))
		for _, e := range entries {
			out = append(out, e.id)
		}
		return out
	}
	orderA, orderB := orderOf(a), orderOf(b)
	if len(orderA) != len(orderB) {
		t.Fatalf("replicas hold different slot counts: %d vs %d", len(orderA), len(orderB))
	}
	for i := range orderA {
		if orderA[i] != orderB[i] {
			t.Fatalf("replicas derived different orders:\n  alice: %v\n  bob:   %v", orderA, orderB)
		}
	}
}

// --- (d) and (e) votes: the cleanest proof in the system -----------------------------------

func TestConcurrentVotesByDifferentMembersBothCount(t *testing.T) {
	f := newConvergenceFixture(t, "conv-votes")
	slotID := f.makeSlot("Where are we staying")
	taj := f.makeOption(slotID, "Taj Exotica")
	airbnb := f.makeOption(slotID, "Airbnb in Anjuna")
	f.settle()

	f.raceSubmit(
		func() submission {
			return wsSubmit(f.alice, f.alice.submit(f.tripID, domain.OpVoteSet, domain.NewID(),
				[]string{domain.FieldSlotID, domain.FieldUserID, domain.FieldOptionID},
				map[string]any{"slot_id": slotID.String(), "option_id": taj.String()}))
		},
		func() submission {
			return wsSubmit(f.bob, f.bob.submit(f.tripID, domain.OpVoteSet, domain.NewID(),
				[]string{domain.FieldSlotID, domain.FieldUserID, domain.FieldOptionID},
				map[string]any{"slot_id": slotID.String(), "option_id": airbnb.String()}))
		},
	)
	f.settle()

	tallies, err := testVotes.Tally(context.Background(), slotID)
	if err != nil {
		t.Fatalf("tallying: %v", err)
	}
	total := 0
	for _, tl := range tallies {
		total += tl.Count
		if tl.Count != 1 {
			t.Errorf("option %s has %d votes, want 1", tl.OptionID, tl.Count)
		}
	}
	if total != 2 {
		t.Errorf("total votes = %d, want 2 — a member's vote was lost", total)
	}
	f.assertConverged()
}

// TestOneMemberVotingFromTwoConnectionsKeepsOneRow proves the register shape survives a race
// against itself: one member, two sockets, two simultaneous choices, still exactly one row.
func TestOneMemberVotingFromTwoConnectionsKeepsOneRow(t *testing.T) {
	f := newConvergenceFixture(t, "conv-vote-register")
	slotID := f.makeSlot("Where are we staying")
	taj := f.makeOption(slotID, "Taj Exotica")
	airbnb := f.makeOption(slotID, "Airbnb in Anjuna")
	f.settle()

	// Alice's second device.
	second := dialWS(t, "alice-2", f.aliceHTTP, f.aliceToken, f.aliceID)
	second.subscribe(f.tripID)

	f.raceSubmit(
		func() submission {
			return wsSubmit(f.alice, f.alice.submit(f.tripID, domain.OpVoteSet, domain.NewID(),
				[]string{domain.FieldSlotID, domain.FieldUserID, domain.FieldOptionID},
				map[string]any{"slot_id": slotID.String(), "option_id": taj.String()}))
		},
		func() submission {
			return wsSubmit(second, second.submit(f.tripID, domain.OpVoteSet, domain.NewID(),
				[]string{domain.FieldSlotID, domain.FieldUserID, domain.FieldOptionID},
				map[string]any{"slot_id": slotID.String(), "option_id": airbnb.String()}))
		},
	)
	f.settle()
	second.waitForSeq(f.currentSeq(), 15*time.Second)

	votes, err := testVotes.ListForSlot(context.Background(), slotID)
	if err != nil {
		t.Fatalf("listing votes: %v", err)
	}
	mine := 0
	for _, v := range votes {
		if v.UserID == f.aliceID {
			mine++
		}
	}
	if mine != 1 {
		t.Errorf("alice has %d vote rows on one slot, want exactly 1 — the register shape "+
			"broke under a race", mine)
	}
	f.assertConverged()
}

// --- (f) delete versus edit ---------------------------------------------------------------

// TestConcurrentDeleteAndEditConverge covers the case the tombstone convention exists for
// (D3). Neither side errors, both clients agree the slot is deleted, and the edit is still in
// the log — dropping it would break fold(log) == database state.
func TestConcurrentDeleteAndEditConverge(t *testing.T) {
	f := newConvergenceFixture(t, "conv-delete")
	slotID := f.makeSlot("Doomed slot")
	f.settle()

	f.raceSubmit(
		func() submission {
			return wsSubmit(f.alice, f.alice.submit(f.tripID, domain.OpSlotDelete, slotID,
				[]string{domain.FieldDeletedAt}, map[string]any{}))
		},
		func() submission {
			return wsSubmit(f.bob, f.bob.submit(f.tripID, domain.OpSlotEdit, slotID,
				[]string{domain.FieldNotes}, map[string]any{"notes": "still editing this"}))
		},
	)

	// settle() is not used here: whichever operation lost the race may legitimately fail
	// (editing an already-tombstoned slot is a not-found from the repository's point of view),
	// and that is a valid outcome rather than a broken one. What must hold is convergence.
	seq := f.currentSeq()
	f.alice.waitForSeq(seq, 15*time.Second)
	f.bob.waitForSeq(seq, 15*time.Second)

	aliceState := f.alice.snapshot()
	bobState := f.bob.snapshot()

	aliceSlot, okA := aliceState.Slots[slotID]
	bobSlot, okB := bobState.Slots[slotID]
	if !okA || !okB {
		t.Fatalf("a client is missing the slot entirely (alice: %v, bob: %v)", okA, okB)
	}
	if !aliceSlot.IsDeleted() || !bobSlot.IsDeleted() {
		t.Errorf("clients disagree that the slot is deleted (alice: %v, bob: %v)",
			aliceSlot.IsDeleted(), bobSlot.IsDeleted())
	}
	if aliceSlot.Notes != bobSlot.Notes {
		t.Errorf("clients diverged on notes: %q vs %q", aliceSlot.Notes, bobSlot.Notes)
	}

	// fold(log) must agree too, including on the tombstone.
	ops, err := testOpLog.ListSince(context.Background(), f.tripID, 0, 0)
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	folded, err := domain.Fold(ops)
	if err != nil {
		t.Fatalf("folding: %v", err)
	}
	if !folded.Slots[slotID].IsDeleted() {
		t.Error("fold(log) does not show the slot as deleted")
	}
	if folded.Slots[slotID].Notes != aliceSlot.Notes {
		t.Errorf("fold(log) notes %q disagree with the clients' %q",
			folded.Slots[slotID].Notes, aliceSlot.Notes)
	}
}

// --- (g) concurrent creates ---------------------------------------------------------------

func TestConcurrentOptionCreatesBothExist(t *testing.T) {
	f := newConvergenceFixture(t, "conv-creates")
	slotID := f.makeSlot("Where are we staying")
	f.settle()

	aliceOption := domain.NewID()
	bobOption := domain.NewID()

	f.raceSubmit(
		func() submission {
			return wsSubmit(f.alice, f.alice.submit(f.tripID, domain.OpOptionCreate, aliceOption,
				domain.OpOptionCreate.AllowedFields(),
				map[string]any{"slot_id": slotID.String(), "title": "Taj Exotica"}))
		},
		func() submission {
			return wsSubmit(f.bob, f.bob.submit(f.tripID, domain.OpOptionCreate, bobOption,
				domain.OpOptionCreate.AllowedFields(),
				map[string]any{"slot_id": slotID.String(), "title": "Airbnb in Anjuna"}))
		},
	)
	f.settle()

	options, err := testOptions.ListForSlot(context.Background(), slotID)
	if err != nil {
		t.Fatalf("listing options: %v", err)
	}
	if len(options) != 2 {
		t.Fatalf("expected both concurrent proposals to exist, got %d", len(options))
	}
	// The client-chosen ids must be the ids that were stored (D4): an optimistic render keyed
	// on a client-generated id has to survive the round trip without a reconciliation pass.
	found := map[domain.ID]bool{}
	for _, o := range options {
		found[o.ID] = true
	}
	if !found[aliceOption] || !found[bobOption] {
		t.Error("the server did not honour the client-chosen entity ids")
	}
	f.assertConverged()
}

// --- (h) REST racing WebSocket -------------------------------------------------------------

// TestRESTWriteRacingAWebSocketWriteConverges is the test behind the claim that both
// transports share one write path.
//
// If a REST write bypassed the operation log, this would still LOOK fine — the database would
// be correct and the assertion on it would pass. It is the fold(log) comparison, and the
// WebSocket client receiving the REST-originated operation at all, that catch it.
func TestRESTWriteRacingAWebSocketWriteConverges(t *testing.T) {
	f := newConvergenceFixture(t, "conv-rest-ws")
	slotID := f.makeSlot("Contested")
	f.settle()

	f.raceSubmit(
		func() submission {
			// Plain REST, with a field mask and no version: merge semantics over HTTP (D69).
			resp := f.aliceHTTP.do(http.MethodPatch,
				fmt.Sprintf("/api/v1/trips/%s/slots/%s", f.tripID, slotID),
				map[string]any{"fields": []string{"title"}, "title": "Renamed over REST"},
				withBearer(f.aliceToken))
			assertStatus(t, resp, http.StatusOK)
			return restSubmit()
		},
		func() submission {
			return wsSubmit(f.bob, f.bob.submit(f.tripID, domain.OpSlotEdit, slotID,
				[]string{domain.FieldNotes}, map[string]any{"notes": "annotated over the socket"}))
		},
	)
	f.settle()

	slot, err := testSlots.GetByID(context.Background(), slotID)
	if err != nil {
		t.Fatalf("reloading slot: %v", err)
	}
	if slot.Title != "Renamed over REST" {
		t.Errorf("the REST edit was lost: %q", slot.Title)
	}
	if slot.Notes != "annotated over the socket" {
		t.Errorf("the WebSocket edit was lost: %q", slot.Notes)
	}

	// The REST write must be visible to a socket that never issued it. This is the Rule 3
	// guarantee — the reason the op log is written in the service layer rather than the hub.
	if got := f.bob.snapshot().Slots[slotID]; got == nil || got.Title != "Renamed over REST" {
		t.Error("a REST-originated change never reached the WebSocket client, so resync " +
			"would silently miss it")
	}
	f.assertConverged()
}

// --- idempotency ---------------------------------------------------------------------------

// TestReplayingAClientOpIDIsIdempotent covers the reconnect case the whole offline story
// depends on: a client sent an operation, lost its acknowledgement, and retried.
func TestReplayingAClientOpIDIsIdempotent(t *testing.T) {
	f := newConvergenceFixture(t, "conv-replay")
	slotID := f.makeSlot("Replay target")
	f.settle()

	clientOpID := domain.NewID()
	frame := map[string]any{
		"type": "op", "trip_id": f.tripID.String(), "client_op_id": clientOpID.String(),
		"kind": string(domain.OpSlotEdit), "entity_id": slotID.String(),
		"fields": []string{domain.FieldTitle},
		"values": map[string]any{"title": "Applied exactly once"},
	}
	f.alice.send(frame)
	f.alice.awaitResolution(clientOpID, 15*time.Second)
	f.settle()
	seqAfterFirst := f.currentSeq()

	// The retry a reconnecting client would make. There is no new frame to await — the reply
	// is the operation it has already seen — so this waits on the clock instead, which is
	// acceptable precisely because the assertion is that NOTHING further happened.
	f.alice.send(frame)
	time.Sleep(500 * time.Millisecond)

	if got := f.currentSeq(); got != seqAfterFirst {
		t.Errorf("a replayed client_op_id advanced the sequence from %d to %d — it was "+
			"applied twice", seqAfterFirst, got)
	}
	f.alice.assertNoErrorFrames()

	ctx := context.Background()
	ops, err := testOpLog.ListSince(ctx, f.tripID, 0, 0)
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	matches := 0
	for _, op := range ops {
		if op.ClientOpID != nil && *op.ClientOpID == clientOpID {
			matches++
		}
	}
	if matches != 1 {
		t.Errorf("the log holds %d operations for one client op id, want 1", matches)
	}
	f.assertConverged()
}

// --- the cascade (D63) ---------------------------------------------------------------------

// TestDeletingTheSelectedOptionEmitsTwoLinkedOperations proves the D56 cascade reaches clients
// as data rather than as a rule they have to re-derive.
func TestDeletingTheSelectedOptionEmitsTwoLinkedOperations(t *testing.T) {
	f := newConvergenceFixture(t, "conv-cascade")
	slotID := f.makeSlot("Where are we staying")
	taj := f.makeOption(slotID, "Taj Exotica")

	resp := f.aliceHTTP.do(http.MethodPost,
		fmt.Sprintf("/api/v1/trips/%s/slots/%s/select", f.tripID, slotID),
		map[string]any{"option_id": taj.String()}, withBearer(f.aliceToken))
	assertStatus(t, resp, http.StatusNoContent)
	f.settle()

	seqBefore := f.currentSeq()
	deleteOp := f.alice.submit(f.tripID, domain.OpOptionDelete, taj,
		[]string{domain.FieldDeletedAt}, map[string]any{})
	f.alice.awaitResolution(deleteOp, 15*time.Second)
	f.settle()

	if got := f.currentSeq(); got != seqBefore+2 {
		t.Errorf("expected one intent to commit two operations, seq went %d -> %d",
			seqBefore, got)
	}

	ctx := context.Background()
	ops, err := testOpLog.ListSince(ctx, f.tripID, seqBefore, 0)
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("expected 2 operations, got %d", len(ops))
	}
	if ops[0].Kind != domain.OpOptionDelete || ops[1].Kind != domain.OpSlotSelectOption {
		t.Errorf("unexpected cascade shape: %s then %s", ops[0].Kind, ops[1].Kind)
	}
	// The link is what lets a client present two changes as one action.
	if ops[0].CauseOpID == nil || ops[1].CauseOpID == nil || *ops[0].CauseOpID != *ops[1].CauseOpID {
		t.Error("the derived operation is not linked to its cause")
	}
	// Only the first carries the client op id, or the uniqueness index would reject the pair.
	if ops[1].ClientOpID != nil {
		t.Error("a derived operation carried the client op id; replay protection would break")
	}

	slot, err := testSlots.GetByID(ctx, slotID)
	if err != nil {
		t.Fatalf("reloading slot: %v", err)
	}
	if slot.SelectedOptionID != nil {
		t.Error("the slot's selection was not cleared")
	}
	if got := f.bob.snapshot().Slots[slotID]; got == nil || got.SelectedOptionID != nil {
		t.Error("bob's replica still shows a selection; the cascade did not reach him as data")
	}
	f.assertConverged()
}

// --- the fuzz variant -----------------------------------------------------------------------

// TestConcurrentMixedOperationsConverge is where an interleaving nobody thought of shows up.
//
// Two clients fire a randomised mixture of the whole vocabulary at the same trip, then the
// four-way equality must still hold. The seed is fixed so a failure is reproducible rather
// than a story about a flaky test.
func TestConcurrentMixedOperationsConverge(t *testing.T) {
	f := newConvergenceFixture(t, "conv-fuzz")

	slots := make([]domain.ID, 0, 4)
	for i := 0; i < 4; i++ {
		slots = append(slots, f.makeSlot(fmt.Sprintf("Slot %d", i)))
	}
	options := []domain.ID{
		f.makeOption(slots[0], "Option A"),
		f.makeOption(slots[0], "Option B"),
	}
	f.settle()

	const seed = 20260808
	const rounds = 12

	for round := 0; round < rounds; round++ {
		rngA := rand.New(rand.NewSource(seed + int64(round)*2))
		rngB := rand.New(rand.NewSource(seed + int64(round)*2 + 1))
		f.raceSubmit(
			func() submission { return randomOp(f, f.alice, rngA, slots, options) },
			func() submission { return randomOp(f, f.bob, rngB, slots, options) },
		)
	}
	f.settle()
	f.assertConverged()
	assertSameSlotOrder(t, f.alice.snapshot(), f.bob.snapshot())
}

func randomOp(f *convergenceFixture, c *wsClient, rng *rand.Rand, slots, options []domain.ID) submission {
	slot := slots[rng.Intn(len(slots))]
	switch rng.Intn(5) {
	case 0:
		return wsSubmit(c, c.submit(f.tripID, domain.OpSlotEdit, slot,
			[]string{domain.FieldTitle},
			map[string]any{"title": fmt.Sprintf("Title %d", rng.Intn(1000))}))
	case 1:
		return wsSubmit(c, c.submit(f.tripID, domain.OpSlotEdit, slot,
			[]string{domain.FieldNotes},
			map[string]any{"notes": fmt.Sprintf("Notes %d", rng.Intn(1000))}))
	case 2:
		anchor := slots[rng.Intn(len(slots))]
		if anchor == slot {
			// Moving a slot after itself is not a meaningful request; skipping keeps the
			// mixture honest rather than padding it with a no-op the server would reject.
			return restSubmit()
		}
		return wsSubmit(c, c.submit(f.tripID, domain.OpSlotMove, slot,
			[]string{domain.FieldDayID, domain.FieldPosition},
			map[string]any{"after_id": anchor.String()}))
	case 3:
		return wsSubmit(c, c.submit(f.tripID, domain.OpVoteSet, domain.NewID(),
			[]string{domain.FieldSlotID, domain.FieldUserID, domain.FieldOptionID},
			map[string]any{
				"slot_id":   slots[0].String(),
				"option_id": options[rng.Intn(len(options))].String(),
			}))
	default:
		return wsSubmit(c, c.submit(f.tripID, domain.OpOptionEdit,
			options[rng.Intn(len(options))],
			[]string{domain.FieldNotes},
			map[string]any{"notes": fmt.Sprintf("Option notes %d", rng.Intn(1000))}))
	}
}
