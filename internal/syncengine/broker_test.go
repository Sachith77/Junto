package syncengine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/junto/junto/internal/domain"
)

// The ordering guarantee, tested where it lives.
//
// domain.Replica.Apply treats a gap as a fatal error, and every WebSocket client in this
// system folds with it. That contract is only honourable if what reaches a Sink is genuinely
// in sequence order — and nothing in Postgres guarantees that, because the sequencer's order
// is established inside the transaction while the broadcast happens after it commits.
//
// These tests exercise the three ways that order can be lost, WITHOUT a socket or a Redis
// client: this package is forbidden from importing either (tests/arch_test.go does not exempt
// test files, deliberately), so if the ordering could only be tested through a real transport,
// the boundary would not be real.

// recordingSink is a Sink that never blocks and remembers what it was given.
type recordingSink struct {
	mu      sync.Mutex
	seqs    []int64
	dropped []domain.ID
}

func (s *recordingSink) Deliver(_ context.Context, ev Event) error {
	if ev.Type != EventOp {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seqs = append(s.seqs, ev.Op.Seq)
	return nil
}

func (s *recordingSink) Dropped(tripID domain.ID, _ error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropped = append(s.dropped, tripID)
}

func (s *recordingSink) snapshot() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.seqs...)
}

// waitForSeqs blocks until the sink has received n operations, so assertions are made on a
// settled state rather than on a race with the dispatch goroutine.
func (s *recordingSink) waitForSeqs(t *testing.T, n int, timeout time.Duration) []int64 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := s.snapshot(); len(got) >= n {
			return got
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d operations; got %v", n, s.snapshot())
	return nil
}

// fakeLog is the authoritative record the repair path reads from. It is gapless, exactly as
// trip_ops is (D61).
type fakeLog struct {
	mu   sync.Mutex
	ops  []domain.Op
	head int64
}

func (f *fakeLog) append(op domain.Op) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, op)
	if op.Seq > f.head {
		f.head = op.Seq
	}
}

func (f *fakeLog) ListSince(_ context.Context, _ domain.ID, seq int64, _ int) ([]domain.Op, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Op
	for _, op := range f.ops {
		if op.Seq > seq {
			out = append(out, op)
		}
	}
	return out, nil
}

func (f *fakeLog) CurrentOpSeq(context.Context, domain.ID) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.head, nil
}

// testBroker wires a broker with timings short enough for a test and long enough not to be
// flaky on a loaded machine.
func testBroker(t *testing.T, log *fakeLog) *Broker {
	t.Helper()
	return NewBroker(BrokerConfig{
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		Ops:               log,
		Trips:             log,
		ReorderWindow:     25 * time.Millisecond,
		ReconcileInterval: 40 * time.Millisecond,
	})
}

// op builds a committed operation carrying a valid payload, so the fold in the API-level
// tests and this one are describing the same thing.
func op(tripID domain.ID, seq int64) domain.Op {
	return domain.Op{
		ID: domain.NewID(), TripID: tripID, Seq: seq,
		Kind: domain.OpSlotEdit, EntityID: domain.NewID(),
		Fields:  domain.NewFieldMask(domain.FieldTitle),
		Payload: []byte(`{"fields":{"title":"x"},"meta":{"version":1}}`),
	}
}

// TestPublishOutOfOrderIsDeliveredInOrder is the local half of the ordering problem.
//
// Two transactions commit in sequence order because both hold the trip's row lock (D60), but
// Publish is called AFTER the commit and outside that lock — so the goroutine that committed
// seq 6 can reach the broker before the goroutine that committed seq 5. Narrow, and real, and
// it would surface as a client's fold rejecting a gap rather than as anything obviously
// ordering-shaped.
func TestPublishOutOfOrderIsDeliveredInOrder(t *testing.T) {
	t.Parallel()

	log := &fakeLog{}
	b := testBroker(t, log)
	tripID := domain.NewID()
	sink := &recordingSink{}

	sub := b.Subscribe(context.Background(), tripID, domain.NewID(), domain.NewID(), 0, sink)
	defer func() { _ = sub.Close() }()

	// Arrive 3, 1, 2. All three are in the log too, as they would be in reality.
	for _, seq := range []int64{3, 1, 2} {
		o := op(tripID, seq)
		log.append(o)
		b.Publish(context.Background(), []domain.Op{o})
	}

	got := sink.waitForSeqs(t, 3, 2*time.Second)
	want := []int64{1, 2, 3}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delivered out of order: got %v, want %v", got, want)
		}
	}
}

// TestALostBroadcastIsFilledFromTheLog is the case that makes Redis pub/sub usable at all.
//
// Redis has no persistence and no acknowledgement: a subscriber disconnected for a moment
// misses whatever was published in that moment, permanently. Here seq 2 is committed to the
// log but its broadcast never arrives. The room holds seq 3, waits out the reorder window,
// and reads the missing operation from the authority — which is what "the log is the delivery
// guarantee and the broadcast is only an accelerator" (D70) has to mean in code.
func TestALostBroadcastIsFilledFromTheLog(t *testing.T) {
	t.Parallel()

	log := &fakeLog{}
	b := testBroker(t, log)
	tripID := domain.NewID()
	sink := &recordingSink{}

	sub := b.Subscribe(context.Background(), tripID, domain.NewID(), domain.NewID(), 0, sink)
	defer func() { _ = sub.Close() }()

	first, lost, third := op(tripID, 1), op(tripID, 2), op(tripID, 3)
	log.append(first)
	log.append(lost) // committed, and its broadcast is dropped in transit
	log.append(third)

	b.Publish(context.Background(), []domain.Op{first})
	b.Publish(context.Background(), []domain.Op{third})

	got := sink.waitForSeqs(t, 3, 2*time.Second)
	want := []int64{1, 2, 3}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("gap was not repaired: got %v, want %v", got, want)
		}
	}
}

// TestALostFinalOperationIsRecoveredByReconcile covers the hole the reorder window structurally
// cannot.
//
// A gap is only detectable when a LATER operation arrives. If the last operation of a burst is
// dropped in transit, nothing follows it, nothing waits on it, and no amount of buffering
// reveals it — the client would simply be one operation stale until it happened to reconnect.
// The periodic check against the trip's own sequence is the only thing that finds this.
func TestALostFinalOperationIsRecoveredByReconcile(t *testing.T) {
	t.Parallel()

	log := &fakeLog{}
	b := testBroker(t, log)
	tripID := domain.NewID()
	sink := &recordingSink{}

	sub := b.Subscribe(context.Background(), tripID, domain.NewID(), domain.NewID(), 0, sink)
	defer func() { _ = sub.Close() }()

	// Committed, never broadcast at all: no Publish call.
	log.append(op(tripID, 1))
	log.append(op(tripID, 2))

	got := sink.waitForSeqs(t, 2, 2*time.Second)
	if got[0] != 1 || got[1] != 2 {
		t.Fatalf("reconcile did not recover the operations: got %v", got)
	}
}

// TestRedeliveryIsNotFannedOutTwice keeps duplicate suppression at the room rather than
// relying on every client's fold to absorb it.
//
// Duplicates are routine here by design — the local fan-out and a peer's message can both
// carry the same operation, and the repair path can read one the inbox is about to deliver.
// The fold tolerates that (redelivery is a no-op), but a room that forwarded every copy would
// make a client's traffic proportional to how many paths an operation happened to take.
func TestRedeliveryIsNotFannedOutTwice(t *testing.T) {
	t.Parallel()

	log := &fakeLog{}
	b := testBroker(t, log)
	tripID := domain.NewID()
	sink := &recordingSink{}

	sub := b.Subscribe(context.Background(), tripID, domain.NewID(), domain.NewID(), 0, sink)
	defer func() { _ = sub.Close() }()

	first, second := op(tripID, 1), op(tripID, 2)
	log.append(first)
	log.append(second)

	b.Publish(context.Background(), []domain.Op{first})
	b.Publish(context.Background(), []domain.Op{first}) // the same op again, from a peer
	b.Publish(context.Background(), []domain.Op{second})

	sink.waitForSeqs(t, 2, 2*time.Second)
	// Give any duplicate a chance to show up before asserting it did not.
	time.Sleep(100 * time.Millisecond)
	got := sink.snapshot()
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("expected each operation delivered once, got %v", got)
	}
}

// TestAnEmptyRoomIsTornDown guards against a server that has been up for a month holding a
// map entry, a goroutine and a ticker for every trip anyone ever opened.
func TestAnEmptyRoomIsTornDown(t *testing.T) {
	t.Parallel()

	b := testBroker(t, &fakeLog{})
	tripID := domain.NewID()

	sub := b.Subscribe(context.Background(), tripID, domain.NewID(), domain.NewID(), 0, &recordingSink{})
	if b.RoomCount() != 1 {
		t.Fatalf("room count = %d, want 1", b.RoomCount())
	}
	_ = sub.Close()
	if b.RoomCount() != 0 {
		t.Fatalf("room count after the last subscriber left = %d, want 0", b.RoomCount())
	}
}

// TestAGatedSinkHoldsLiveEventsUntilReleased pins the ordering rule that makes resume work.
//
// A resuming subscriber joins its room BEFORE reading the log, so that nothing committed in
// between can be missed. The cost of that ordering is that live operations start arriving
// while the replay is still streaming much older ones — and a client folding seq 60 before
// seq 12 would reject it as a gap. The gate is what makes joining early safe.
func TestAGatedSinkHoldsLiveEventsUntilReleased(t *testing.T) {
	t.Parallel()

	tripID := domain.NewID()
	sink := &recordingSink{}
	g := newGate(sink)

	live := op(tripID, 60)
	if err := g.Deliver(context.Background(), Event{Type: EventOp, Op: &live}); err != nil {
		t.Fatalf("delivering into a closed gate: %v", err)
	}
	if got := sink.snapshot(); len(got) != 0 {
		t.Fatalf("a closed gate let an event through: %v", got)
	}

	// The replay writes straight to the sink, bypassing the gate — that is what puts it first.
	replayed := op(tripID, 12)
	if err := deliverWithBackpressure(context.Background(), sink, Event{Type: EventOp, Op: &replayed}); err != nil {
		t.Fatalf("replaying: %v", err)
	}
	if err := g.release(context.Background()); err != nil {
		t.Fatalf("releasing the gate: %v", err)
	}

	got := sink.snapshot()
	if len(got) != 2 || got[0] != 12 || got[1] != 60 {
		t.Fatalf("expected the replayed operation before the live one, got %v", got)
	}
}

// TestAFullGateReportsASlowConsumer keeps the catch-up buffer bounded.
//
// A subscriber whose replay never completes must not grow server memory without limit. It is
// dropped and told to resync, which is the same answer every other buffer in this system
// gives — and the reason DroppableSink exists is so the client actually finds out.
func TestAFullGateReportsASlowConsumer(t *testing.T) {
	t.Parallel()

	tripID := domain.NewID()
	g := newGate(&recordingSink{})
	for i := 0; i < gateBuffer; i++ {
		o := op(tripID, int64(i+1))
		if err := g.Deliver(context.Background(), Event{Type: EventOp, Op: &o}); err != nil {
			t.Fatalf("event %d was rejected before the buffer was full: %v", i, err)
		}
	}
	overflow := op(tripID, gateBuffer+1)
	err := g.Deliver(context.Background(), Event{Type: EventOp, Op: &overflow})
	if !errors.Is(err, ErrSlowConsumer) {
		t.Fatalf("a full gate returned %v, want ErrSlowConsumer", err)
	}
}
