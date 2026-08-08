package syncengine

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/junto/junto/internal/domain"
)

// gateBuffer bounds how many live events a catching-up subscriber may accumulate.
//
// Generous, because it only has to cover the duration of one replay, and a subscriber that
// exceeds it is dropped and told to resync — the same rule as every other buffer here.
const gateBuffer = 1024

// gate holds live events back until a subscriber has finished replaying the log.
//
// # Why a subscriber cannot simply join the room and start folding
//
// A resuming client needs everything since its last sequence number, and it needs it BEFORE
// the live stream, because domain.Replica.Apply rejects a gap. The obvious two orderings both
// fail:
//
//   - Read the log, then join the room: an operation committed in between is broadcast to a
//     room this subscriber is not yet in, and is not in the log range it already read. The
//     client has a permanent hole.
//   - Join the room, then read the log: correct for completeness, but the live operation for
//     seq 60 reaches the sink while the replay is still at seq 12, and the client's fold
//     rejects it as a gap.
//
// So: join first (nothing can be missed), buffer what arrives, replay the log directly to the
// sink, then release the buffer. Overlap between the two is not merely tolerated but expected,
// and it is harmless because redelivery is a no-op in the fold — which is exactly the
// at-least-once property Slice 1 established and this design now depends on.
type gate struct {
	mu   sync.Mutex
	sink Sink
	open bool
	buf  []Event
}

func newGate(sink Sink) *gate { return &gate{sink: sink} }

var _ Sink = (*gate)(nil)

// Deliver buffers while closed and passes through once open. It never blocks, in either state.
func (g *gate) Deliver(ctx context.Context, ev Event) error {
	g.mu.Lock()
	if g.open {
		sink := g.sink
		g.mu.Unlock()
		return sink.Deliver(ctx, ev)
	}
	if len(g.buf) >= gateBuffer {
		g.mu.Unlock()
		return ErrSlowConsumer
	}
	g.buf = append(g.buf, ev)
	g.mu.Unlock()
	return nil
}

// Dropped forwards the broker's give-up notice to the real sink.
func (g *gate) Dropped(tripID domain.ID, reason error) {
	if d, ok := g.sink.(DroppableSink); ok {
		d.Dropped(tripID, reason)
	}
}

// release flushes what was buffered and switches to pass-through.
//
// The lock is held across the flush deliberately. Sink.Deliver is required not to block, so
// the cost is bounded, and it is what guarantees no live event overtakes the buffer it was
// supposed to queue behind.
func (g *gate) release(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.open {
		return nil
	}
	var firstErr error
	for _, ev := range g.buf {
		if err := g.sink.Deliver(ctx, ev); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	g.buf = nil
	g.open = true
	return firstErr
}

// Backpressure on the replay path.
//
// The Sink contract forbids BLOCKING during fan-out, because one slow client must never stall
// a room. A replay is the opposite situation: it runs on the subscriber's own goroutine, in
// response to that subscriber's own request, and nobody else is waiting on it. Dropping a
// replayed operation because a 256-frame socket buffer filled would make resync fail for
// exactly the clients that need it most — the ones that have been away long enough to have a
// lot to catch up on. So here, and only here, a full buffer is waited on rather than fatal.
const (
	replayRetryInterval = 2 * time.Millisecond
	replayMaxWait       = 15 * time.Second
)

func deliverWithBackpressure(ctx context.Context, sink Sink, ev Event) error {
	deadline := time.Now().Add(replayMaxWait)
	for {
		err := sink.Deliver(ctx, ev)
		if !errors.Is(err, ErrSlowConsumer) {
			return err
		}
		if time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(replayRetryInterval):
		}
	}
}
