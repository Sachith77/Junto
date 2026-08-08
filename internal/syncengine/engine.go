package syncengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/service"
)

// ErrResyncRequired tells a client its starting point is no longer serviceable and it must
// re-fetch its state through the REST surface before subscribing again.
//
// Slice 1 answered every non-zero sinceSeq with this. Slice 2 replays the log instead, and
// this is now reserved for the two cases where replay genuinely cannot help: a client that
// claims to be AHEAD of the trip's sequence (it is talking to a different or restored
// database, and nothing in the log can reconcile that), and a client so far behind that
// replaying its backlog would cost more than re-fetching the state it is trying to rebuild.
// The wire protocol did not change between the two slices — only the server's answer did.
var ErrResyncRequired = errors.New("syncengine: resync required")

// Resync bounds.
const (
	// resyncPage is one page of the replay scan. The repository caps a single ListSince at
	// 1000 rows, so this matches it: a smaller page would only add round trips.
	resyncPage = 1000

	// defaultMaxResyncOps is where "replay the log" stops being the cheaper answer.
	//
	// The question it settles is real: with no pruning, the log from seq 0 is complete, and
	// fold(log) == database state is an asserted invariant — so a client that is arbitrarily
	// far behind CAN be brought up to date by replay alone, and no snapshot endpoint is
	// needed for correctness. What replay costs is proportional to the trip's HISTORY, while
	// a re-fetch costs the trip's SIZE, and history grows without bound while size does not.
	// Ten thousand operations is roughly where a heavily-edited trip's log outgrows its own
	// itinerary; past that the honest answer is "re-fetch", which is what a client without a
	// stored seq does anyway.
	defaultMaxResyncOps = 10_000
)

// Services are the business-logic entry points the engine dispatches to.
//
// The engine calls SERVICES, never repositories. That is not stylistic: every mutation must
// pass through the layer that holds the trip's sequencer and appends to the operation log, or
// a WebSocket write would be invisible to a resyncing client while every test still passed
// (Rule 3, D1). The arch test forbids this package from importing internal/repository at all.
type Services struct {
	Trips   *service.TripService
	Days    *service.DayService
	Slots   *service.SlotService
	Options *service.SlotOptionService
	Votes   *service.VoteService
	Budget  *service.BudgetService
}

// EngineConfig is what an Engine needs.
type EngineConfig struct {
	// Broker is constructed separately and passed in, because the service layer needs it as a
	// domain.OpPublisher BEFORE the engine (which depends on those services) can exist.
	// Splitting the two objects turns what would be a construction cycle into a plain ordering.
	Broker   *Broker
	Services Services
	Ops      domain.OpLogRepository
	Trips    domain.TripRepository
	Logger   *slog.Logger

	// MaxResyncOps bounds a single replay. Zero takes defaultMaxResyncOps.
	MaxResyncOps int
}

// Engine turns client intents into committed operations and subscriptions into room
// membership. It is the whole of what a transport talks to.
type Engine struct {
	broker       *Broker
	services     Services
	ops          domain.OpLogRepository
	trips        domain.TripRepository
	log          *slog.Logger
	maxResyncOps int
}

// NewEngine builds an Engine.
func NewEngine(cfg EngineConfig) *Engine {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.MaxResyncOps <= 0 {
		cfg.MaxResyncOps = defaultMaxResyncOps
	}
	return &Engine{
		broker:       cfg.Broker,
		services:     cfg.Services,
		ops:          cfg.Ops,
		trips:        cfg.Trips,
		log:          cfg.Logger,
		maxResyncOps: cfg.MaxResyncOps,
	}
}

// SubscribeResult tells a new subscriber where it is starting from, and how to catch up.
type SubscribeResult struct {
	Subscription *Subscription

	// Seq is the sequence number the subscriber is caught up to BEFORE Replay runs. A
	// resuming client gets back the value it asked from; a fresh one gets the trip's current
	// head. Either way the contract is the same: everything after this arrives as operations.
	Seq int64

	Presence []Participant

	// Replay streams the operations the subscriber missed, then opens the live stream.
	//
	// It is a separate call rather than part of Subscribe so the transport can announce the
	// subscription FIRST. A client that learns its baseline only after the replayed
	// operations have already arrived cannot fold them — it does not yet know what it is
	// folding from. Callers must invoke it; until they do, live events accumulate in a bounded
	// buffer and the subscriber is eventually dropped for being slow.
	Replay func(context.Context) error
}

// Subscribe authorizes a user for a trip and joins their sink to the room.
//
// Authorization goes through TripService.Get, the same read gate every other reader uses,
// which means a non-member receives ErrNotFound rather than ErrForbidden (D53) — confirming a
// trip exists to someone with no access to it is itself a disclosure, and a WebSocket is not
// an excuse to make an exception.
//
// # sinceSeq is a pointer, and the distinction is load-bearing
//
// nil means "I have no prior state": start me at the current head and send me what happens
// next. A value means "I was at seq N": send me everything after N. Zero is therefore a
// legitimate request — replay the entire log — and not a synonym for absent. Collapsing the
// two would be D64's mistake one level down, and it would leave a client with no state and no
// snapshot endpoint unable to ask for one; instead it asks for since_seq 0 and folds the log
// from nothing, which fold(log) == database state guarantees is the same thing.
//
// # Why the ordering here is subtle
//
// The room is joined FIRST and the log is read second, with live events held behind a gate.
// The reverse order leaves a permanent hole for anything committed between the read and the
// join. See gate.go for the full argument.
func (e *Engine) Subscribe(ctx context.Context, tripID, userID, connID domain.ID, sinceSeq *int64, sink Sink) (*SubscribeResult, error) {
	if _, err := e.services.Trips.Get(ctx, tripID, userID); err != nil {
		return nil, err
	}

	head, err := e.trips.CurrentOpSeq(ctx, tripID)
	if err != nil {
		return nil, err
	}

	// Validated BEFORE the room is joined, so a refusal does not announce a presence arrival
	// followed immediately by a departure.
	from := head
	if sinceSeq != nil {
		if *sinceSeq < 0 {
			ve := &domain.ValidationError{}
			ve.Add("since_seq", "invalid", "a sequence number cannot be negative")
			return nil, ve
		}
		if *sinceSeq > head {
			// The client is ahead of the server. No replay can reconcile that — it has state
			// from a database this one is not a continuation of.
			return nil, fmt.Errorf("%w: sequence %d is ahead of the trip's current sequence %d",
				ErrResyncRequired, *sinceSeq, head)
		}
		if head-*sinceSeq > int64(e.maxResyncOps) {
			return nil, fmt.Errorf("%w: %d operations behind, which is more than the %d this server will replay",
				ErrResyncRequired, head-*sinceSeq, e.maxResyncOps)
		}
		from = *sinceSeq
	}

	// A FRESH subscriber is handled as a resume from the head read a moment ago, rather than
	// as a special case. That is not a flourish: it closes the window between reading the head
	// and joining the room, in which an operation would otherwise be broadcast to a room this
	// subscriber is not in yet and fall outside the range it will replay. One code path, and
	// the replay is normally empty.
	g := newGate(sink)
	sub := e.broker.Subscribe(ctx, tripID, userID, connID, head, g)

	return &SubscribeResult{
		Subscription: sub,
		Seq:          from,
		// Read AFTER subscribing, so the caller's own presence is included and there is no
		// window in which a joiner is invisible to itself.
		Presence: e.broker.Presence(tripID),
		Replay:   e.replayFrom(tripID, from, g, sink),
	}, nil
}

// replayFrom builds the catch-up closure: log operations straight to the sink, then open the
// gate on the live stream.
func (e *Engine) replayFrom(tripID domain.ID, from int64, g *gate, sink Sink) func(context.Context) error {
	return func(ctx context.Context) error {
		// The gate opens whatever happens. Leaving it shut on an error would leave a
		// subscriber that is in the room, counted in presence, and receiving nothing.
		defer func() {
			if err := g.release(ctx); err != nil {
				e.log.Warn("releasing a subscriber's buffered events after replay",
					"trip_id", tripID, "error", err)
			}
		}()

		cursor := from
		delivered := 0
		for {
			ops, err := e.ops.ListSince(ctx, tripID, cursor, resyncPage)
			if err != nil {
				return err
			}
			for i := range ops {
				op := ops[i]
				// Backpressure rather than drop: this is the subscriber's own goroutine and
				// nobody else is waiting on it. See gate.go.
				if err := deliverWithBackpressure(ctx, sink, Event{Type: EventOp, Op: &op}); err != nil {
					return err
				}
				cursor = op.Seq
				delivered++
				if delivered > e.maxResyncOps {
					// Only reachable if the trip is being written to faster than the replay
					// drains — the pre-check already bounded the backlog. Telling the client
					// to re-fetch is better than replaying indefinitely behind a moving head.
					return fmt.Errorf("%w: the trip is being written to faster than this replay can catch up", ErrResyncRequired)
				}
			}
			if len(ops) < resyncPage {
				return nil
			}
		}
	}
}

// Presence returns a room's current participants, for a member of that trip.
func (e *Engine) Presence(ctx context.Context, tripID, userID domain.ID) ([]Participant, error) {
	if _, err := e.services.Trips.Get(ctx, tripID, userID); err != nil {
		return nil, err
	}
	return e.broker.Presence(tripID), nil
}

// Intent is a client's request to change something: the PRE-commit form.
//
// The distinction from domain.Op is the one that carries this design. An Intent may name a
// neighbour ("put this after slot S") and may target an entity that does not exist yet. An Op
// is what actually happened, with every derivation already resolved (D62). The engine turns
// the first into the second by calling a service; nothing else in the system is allowed to.
type Intent struct {
	TripID     domain.ID
	ActorID    domain.ID
	ClientOpID domain.ID
	Kind       domain.OpKind
	// EntityID is the target. For a create it is the id the CLIENT chose — D4 exists so a
	// client can name an entity before the server has seen it, which optimistic UI and
	// offline editing both require.
	EntityID domain.ID
	Fields   domain.FieldMask
	Values   json.RawMessage
}

// Submit applies one intent.
//
// The returned operations are non-nil only on an IDEMPOTENT REPLAY: the client already
// committed this ClientOpID, nothing new was applied, and nothing is broadcast. The caller
// should deliver them to the submitting connection alone, so a client that lost its
// acknowledgement gets one rather than the whole room seeing a duplicate.
//
// On a fresh apply the return is nil and delivery happens through the broker — including back
// to the submitting client, whose own operation arrives carrying its ClientOpID. That echo IS
// the acknowledgement, which is why there is no separate ack path to keep consistent.
func (e *Engine) Submit(ctx context.Context, in Intent) ([]domain.Op, error) {
	if !in.Kind.Valid() {
		ve := &domain.ValidationError{}
		ve.Add("kind", "unknown_kind", fmt.Sprintf("unknown operation kind %q", in.Kind))
		return nil, ve
	}
	if in.ClientOpID == domain.NilID {
		ve := &domain.ValidationError{}
		ve.Add("client_op_id", "required", "an operation must carry a client op id")
		return nil, ve
	}

	// Cheap pre-check for the common replay case. It is deliberately NOT the whole story: two
	// concurrent replays can both pass it, and the unique index on (trip_id, client_op_id) is
	// what actually enforces idempotency. Correctness lives in the constraint; this is only
	// here to avoid doing the work twice in the overwhelmingly common case.
	if existing, err := e.replayed(ctx, in); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}

	ctx = domain.ContextWithOpOrigin(ctx, domain.OpOrigin{
		ClientOpID: &in.ClientOpID,
		CauseOpID:  &in.ClientOpID,
	})

	err := e.dispatch(ctx, in)
	if err == nil {
		return nil, nil
	}
	// The race the pre-check cannot close: the constraint fired, so this intent was already
	// committed by a concurrent replay. Look it up and answer with the original.
	if errors.Is(err, domain.ErrAlreadyExists) {
		if existing, lookupErr := e.replayed(ctx, in); lookupErr == nil && existing != nil {
			return existing, nil
		}
	}
	return nil, err
}

func (e *Engine) replayed(ctx context.Context, in Intent) ([]domain.Op, error) {
	op, err := e.ops.FindByClientOpID(ctx, in.TripID, in.ClientOpID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []domain.Op{*op}, nil
}

// intentValues is the decoded value object of an intent.
//
// Every field is a pointer, but presence is decided by the FIELD MASK, not by nil-ness (D64).
// A nil DayID with "day_id" in the mask means "clear it"; a nil DayID without it means
// "untouched". Collapsing those two would silently coarsen conflict granularity, which is the
// whole reason the mask is explicit on the wire.
type intentValues struct {
	Kind               *string    `json:"kind"`
	Title              *string    `json:"title"`
	Notes              *string    `json:"notes"`
	DayID              *string    `json:"day_id"`
	StartTime          *string    `json:"start_time"`
	EndTime            *string    `json:"end_time"`
	Status             *string    `json:"status"`
	SelectedOptionID   *string    `json:"selected_option_id"`
	SlotID             *string    `json:"slot_id"`
	OptionID           *string    `json:"option_id"`
	ExternalURL        *string    `json:"external_url"`
	EstimatedCostMinor *int64     `json:"estimated_cost_minor"`
	PlaceName          *string    `json:"place_name"`
	PlaceAddress       *string    `json:"place_address"`
	PlaceLat           *float64   `json:"place_lat"`
	PlaceLng           *float64   `json:"place_lng"`
	PlaceProviderID    *string    `json:"place_provider_id"`
	Date               *time.Time `json:"date"`
	Label              *string    `json:"label"`

	// Budget. Splits is a POINTER TO A SLICE so that "no splits key" and "an empty split set"
	// stay distinguishable — the same reason every other field here is a pointer. For the
	// budget it matters more than usual: an empty set means "nobody has split this", and
	// silently reading a missing key as one would quietly unsplit an entry.
	Category     *string              `json:"category"`
	AmountMinor  *int64               `json:"amount_minor"`
	SlotOptionID *string              `json:"slot_option_id"`
	PaidBy       *string              `json:"paid_by"`
	IncurredOn   *time.Time           `json:"incurred_on"`
	Splits       *[]domain.SplitValue `json:"splits"`

	// AfterID is INTENT-ONLY and never appears in the log: the server resolves it into a
	// position key, and stores the key (D62). Replaying "after S" against a list whose
	// neighbours have changed would produce a different order.
	AfterID *string `json:"after_id"`

	// Version is the optional optimistic-concurrency precondition (D69). Omitted — the normal
	// case for a collaborative client — means merge semantics.
	Version *int `json:"version"`
}

func (e *Engine) decode(in Intent) (intentValues, error) {
	var v intentValues
	if len(in.Values) == 0 {
		return v, nil
	}
	dec := json.NewDecoder(bytes.NewReader(in.Values))
	// Unknown keys are rejected rather than ignored: a typo'd field name would otherwise
	// produce a successful response that did not do what the client asked, and the client
	// would have no way to notice.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		ve := &domain.ValidationError{}
		ve.Add("values", "malformed", "operation values could not be decoded")
		return v, ve
	}
	return v, nil
}

func (e *Engine) dispatch(ctx context.Context, in Intent) error {
	v, err := e.decode(in)
	if err != nil {
		return err
	}
	if err := in.Fields.Validate(in.Kind); err != nil {
		return err
	}

	switch in.Kind {
	case domain.OpSlotCreate:
		return e.slotCreate(ctx, in, v)
	case domain.OpSlotEdit:
		return e.slotEdit(ctx, in, v)
	case domain.OpSlotMove:
		return e.slotMove(ctx, in, v)
	case domain.OpSlotSelectOption:
		return e.slotSelectOption(ctx, in, v)
	case domain.OpSlotSetStatus:
		return e.slotSetStatus(ctx, in, v)
	case domain.OpSlotDelete:
		return e.services.Slots.Delete(ctx, in.TripID, in.ActorID, in.EntityID, v.Version)

	case domain.OpOptionCreate:
		return e.optionCreate(ctx, in, v)
	case domain.OpOptionEdit:
		return e.optionEdit(ctx, in, v)
	case domain.OpOptionDelete:
		return e.services.Options.Delete(ctx, in.TripID, in.ActorID, in.EntityID, v.Version)

	case domain.OpVoteSet:
		return e.voteSet(ctx, in, v)

	case domain.OpBudgetSet:
		return e.budgetSet(ctx, in, v)
	case domain.OpBudgetDelete:
		return e.services.Budget.Delete(ctx, in.TripID, in.ActorID, in.EntityID, v.Version)

	// Attachment operations are DELIVERED by this engine and never accepted by it (D46, D86).
	// An upload is a presign, a direct PUT to object storage and a server-side confirmation —
	// a three-party exchange with a URL in the middle, which a frame on this socket cannot
	// express. Falling through to the generic "no handler" answer below would be misleading:
	// this is a deliberate boundary, not a gap, so it says so.
	case domain.OpAttachmentAdd, domain.OpAttachmentRemove:
		ve := &domain.ValidationError{}
		ve.Add("kind", "rest_only",
			"attachments are created and removed through the REST API; this socket only "+
				"delivers their operations")
		return ve

	case domain.OpDayCreate:
		return e.dayCreate(ctx, in, v)
	case domain.OpDayEdit:
		return e.dayEdit(ctx, in, v)
	case domain.OpDayMove:
		return e.dayMove(ctx, in, v)
	case domain.OpDayDelete:
		return e.services.Days.Delete(ctx, in.TripID, in.ActorID, in.EntityID, v.Version)
	}
	ve := &domain.ValidationError{}
	ve.Add("kind", "unsupported", fmt.Sprintf("operation %q has no handler", in.Kind))
	return ve
}
