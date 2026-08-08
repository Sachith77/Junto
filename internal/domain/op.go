package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Operations: the Stage 2 conflict-resolution core.
//
// This file is in internal/domain deliberately (D68). It is the layer whose arch test forbids
// every import outside the standard library, github.com/google/uuid and junto/pkg — verified
// to fail on a planted net/http or websocket import. Conflict resolution therefore cannot
// reach a socket, a database driver, or an HTTP request, and "domain logic separated from
// transport" is a structural fact rather than a diagram.
//
// # The model, in one paragraph
//
// Each mergeable entity is a map from field name to a last-writer-wins register (D59). The
// "writer" ordering is Op.Seq, a per-trip monotonic sequence assigned by Postgres inside the
// same transaction that applies the change. Two members editing DIFFERENT fields of one slot
// both succeed, because an operation writes only the fields it names. Two members editing the
// SAME field are serialized by the sequencer, and the later Seq wins. Deletion is its own
// register, so delete-versus-edit converges without either side being dropped. Ordering
// within a list is a register holding a fractional-index key, which is why concurrent reorder
// needs nothing at this layer beyond what Stage 1 already built.
//
// # Why CRDT and not OT (D59)
//
// OT's unique payoff is transforming index-relative sequence operations — insert(5) against
// delete(2). D2 removed every index-relative operation from this system in Stage 1 by
// choosing fractional indexing: a move is an ABSOLUTE assignment of a position key. Nothing
// remains for a transformation function to transform, and for scalar assignment
// transform(set a, set b) degenerates to "one wins", which is LWW with two more functions to
// get subtly wrong.
//
// # Two honest boundaries
//
// This is a SEQUENCER-BASED design, not multi-master: convergence rests on a single authority
// producing a per-trip total order. And merge is FIELD-LEVEL, never character-level — two
// members typing into the same Notes concurrently do not get their prose interleaved; the
// later operation wins the whole field.

// OpKind identifies what an operation does.
//
// Every kind carries a version suffix (D65). The log is immutable and will outlive the
// payload shapes written into it; a decoder that meets a v1 payload years from now must be
// able to say so rather than mis-parse it. One character of insurance against a class of bug
// that cannot be fixed after the fact.
type OpKind string

const (
	OpDayCreate OpKind = "day.create.v1"
	OpDayEdit   OpKind = "day.edit.v1"
	OpDayMove   OpKind = "day.move.v1"
	OpDayDelete OpKind = "day.delete.v1"

	OpSlotCreate OpKind = "slot.create.v1"
	OpSlotEdit   OpKind = "slot.edit.v1"
	// OpSlotMove carries day_id and position TOGETHER, always. A move must never be
	// observable as a delete followed by an insert: a client seeing only half of that would
	// render a vanished slot.
	OpSlotMove         OpKind = "slot.move.v1"
	OpSlotSelectOption OpKind = "slot.select_option.v1"
	OpSlotSetStatus    OpKind = "slot.set_status.v1"
	OpSlotDelete       OpKind = "slot.delete.v1"

	OpOptionCreate OpKind = "option.create.v1"
	OpOptionEdit   OpKind = "option.edit.v1"
	OpOptionDelete OpKind = "option.delete.v1"

	// OpVoteSet is the whole vote vocabulary: one verb, one field. A vote is the degenerate
	// case of the general rule — an entity whose mask always names exactly one mutable field,
	// with no tombstone — which is exactly why it is the cleanest convergence proof in the
	// system. It gets NO special code path.
	OpVoteSet OpKind = "vote.set.v1"

	// OpBudgetSet is the WHOLE budget-edit vocabulary: create and edit are the same verb,
	// because a budget write replaces the entry and its complete split set together (D44).
	// There is no budget.create/budget.edit split, and deliberately no per-field edit — see
	// totalMaskKinds below for why that is enforced rather than merely intended.
	OpBudgetSet    OpKind = "budget.set.v1"
	OpBudgetDelete OpKind = "budget.delete.v1"

	// OpAttachmentAdd announces an attachment that has become VISIBLE — a link at creation, a
	// file at confirmation. A `pending` row is never announced; see AttachmentService.
	OpAttachmentAdd    OpKind = "attachment.add.v1"
	OpAttachmentRemove OpKind = "attachment.remove.v1"
)

// opKinds is the complete vocabulary. An unknown kind is rejected at the transport boundary
// rather than being written into an immutable log nothing can later interpret.
var opKinds = map[OpKind][]string{
	OpDayCreate: {FieldDate, FieldLabel, FieldPosition},
	OpDayEdit:   {FieldDate, FieldLabel},
	OpDayMove:   {FieldPosition},
	OpDayDelete: {FieldDeletedAt},

	OpSlotCreate:       {FieldDayID, FieldKind, FieldTitle, FieldNotes, FieldStartTime, FieldEndTime, FieldPosition, FieldStatus},
	OpSlotEdit:         {FieldKind, FieldTitle, FieldNotes, FieldStartTime, FieldEndTime},
	OpSlotMove:         {FieldDayID, FieldPosition},
	OpSlotSelectOption: {FieldSelectedOptionID},
	OpSlotSetStatus:    {FieldStatus, FieldStatusChangedAt, FieldStatusChangedBy},
	OpSlotDelete:       {FieldDeletedAt},

	OpOptionCreate: {FieldSlotID, FieldTitle, FieldNotes, FieldExternalURL, FieldEstimatedCost, FieldPlaceName, FieldPlaceAddress, FieldPlaceLat, FieldPlaceLng, FieldPlaceProviderID},
	OpOptionEdit:   {FieldTitle, FieldNotes, FieldExternalURL, FieldEstimatedCost, FieldPlaceName, FieldPlaceAddress, FieldPlaceLat, FieldPlaceLng, FieldPlaceProviderID},
	OpOptionDelete: {FieldDeletedAt},

	OpVoteSet: {FieldSlotID, FieldUserID, FieldOptionID},

	OpBudgetSet: {FieldLabel, FieldCategory, FieldAmountMinor, FieldSlotOptionID, FieldPaidBy,
		FieldIncurredOn, FieldSplits},
	OpBudgetDelete: {FieldDeletedAt},

	OpAttachmentAdd: {FieldKind, FieldStatus, FieldStorageKey, FieldExternalURL, FieldContentType,
		FieldSizeBytes, FieldOriginalName, FieldSlotOptionID, FieldBudgetEntryID, FieldSlotID,
		FieldUploadedBy},
	OpAttachmentRemove: {FieldDeletedAt},
}

// totalMaskKinds are the kinds whose mask MUST name every field the kind allows.
//
// This is how the two Stage 2 Slice 3 conflict classes are expressed in a vocabulary that was
// built for field-level merge — structurally, rather than by asking callers to be careful.
//
// # Why budget.set is total (D44, D83)
//
// The budget's invariant is that its splits sum to its total. A partial mask is precisely the
// thing that breaks it: A names {amount_minor}, B names {splits}, both are individually valid,
// the merged entry is wrong, and repairing it means choosing whose number to discard — the
// decision merging exists to avoid. Refusing a partial mask means the failure cannot be
// expressed, rather than being expressible and then detected.
//
// # Why attachment.add is total (D46, D84)
//
// Different reason, same rule. An attachment is immutable once ready; there is no such thing
// as editing one field of it. A partial mask would imply a merge grain the entity does not
// have, and the log outlives the code that wrote it — so a future reader must not find an op
// whose shape suggests attachments were once field-mergeable.
//
// Note what this does NOT do: it does not exempt these kinds from the mask. They carry a full
// explicit mask like everything else, so the log format stays uniform, the fold stays a pure
// field assignment, and the "payload keys == mask" invariant holds for every op in the system.
// The mask is required to be total; it is not permitted to be absent.
var totalMaskKinds = map[OpKind]struct{}{
	OpBudgetSet:     {},
	OpAttachmentAdd: {},
}

// RequiresTotalMask reports whether k rejects a partial field mask.
func (k OpKind) RequiresTotalMask() bool { _, ok := totalMaskKinds[k]; return ok }

// Mergeable reports whether concurrent writes to k's entity merge at field level.
//
// False for the budget (atomic, D44) and for attachments (broadcast-only, D46). Exposed
// because callers outside this package legitimately need to branch on conflict grain — the
// budget service refuses a nil expectedVersion on this basis (D85) — and deriving it from a
// kind list in the service layer would put a second copy of the conflict model outside the
// package whose arch test protects it.
func (k OpKind) Mergeable() bool { return !k.RequiresTotalMask() }

// Valid reports whether k is part of the locked vocabulary.
func (k OpKind) Valid() bool { _, ok := opKinds[k]; return ok }

// AllowedFields returns the fields k is permitted to name.
func (k OpKind) AllowedFields() []string { return opKinds[k] }

// Field names. String constants rather than reflection over struct tags: these values are
// written into an immutable log, so they are part of the persisted contract and renaming a Go
// field must not silently rewrite history.
const (
	FieldTitle            = "title"
	FieldNotes            = "notes"
	FieldKind             = "kind"
	FieldDayID            = "day_id"
	FieldSlotID           = "slot_id"
	FieldUserID           = "user_id"
	FieldOptionID         = "option_id"
	FieldStartTime        = "start_time"
	FieldEndTime          = "end_time"
	FieldPosition         = "position"
	FieldSelectedOptionID = "selected_option_id"
	FieldStatus           = "status"
	FieldStatusChangedAt  = "status_changed_at"
	FieldStatusChangedBy  = "status_changed_by"
	FieldDeletedAt        = "deleted_at"
	FieldDate             = "date"
	FieldLabel            = "label"
	FieldExternalURL      = "external_url"
	FieldEstimatedCost    = "estimated_cost_minor"
	FieldPlaceName        = "place_name"
	FieldPlaceAddress     = "place_address"
	FieldPlaceLat         = "place_lat"
	FieldPlaceLng         = "place_lng"
	FieldPlaceProviderID  = "place_provider_id"

	// Budget.
	FieldCategory     = "category"
	FieldAmountMinor  = "amount_minor"
	FieldSlotOptionID = "slot_option_id"
	FieldPaidBy       = "paid_by"
	FieldIncurredOn   = "incurred_on"
	// FieldSplits carries the COMPLETE split set as one value, which is the only shape in
	// which the sum invariant is meaningful. It is the one field in the vocabulary whose value
	// is a structure rather than a scalar, and that is a direct consequence of D44: the atomic
	// unit is the entry plus all of its splits, so the log's unit has to be too.
	FieldSplits = "splits"

	// Attachments.
	FieldStorageKey    = "storage_key"
	FieldContentType   = "content_type"
	FieldSizeBytes     = "size_bytes"
	FieldOriginalName  = "original_name"
	FieldBudgetEntryID = "budget_entry_id"
	FieldUploadedBy    = "uploaded_by"
)

// FieldMask names exactly the fields an operation changes.
//
// It is EXPLICIT (D64) — never inferred from which JSON keys happen to be present. On every
// nullable column, "untouched" and "explicitly cleared" are different operations with
// different meanings, and a decoder that cannot tell them apart silently coarsens conflict
// granularity. That is precisely the failure D6 rejected JSONB place data to avoid, one level
// up; and unlike a request body, the log outlives the decoder that wrote it.
//
// Stored sorted and deduplicated so that two logically identical operations serialise
// identically — which is what lets a test compare log entries by value.
type FieldMask []string

// NewFieldMask builds a canonical mask.
func NewFieldMask(fields ...string) FieldMask {
	out := slices.Clone(fields)
	slices.Sort(out)
	return slices.Compact(out)
}

// Has reports whether the mask names f.
func (m FieldMask) Has(f string) bool { return slices.Contains(m, f) }

// Validate rejects a mask that is empty or names a field the kind does not allow.
//
// This runs before anything is written, because an operation naming an unknown field would be
// permanently unreadable: the log is append-only, so there is no later opportunity to fix it.
func (m FieldMask) Validate(kind OpKind) error {
	if !kind.Valid() {
		return fmt.Errorf("%w: unknown operation kind %q", ErrValidation, kind)
	}
	if len(m) == 0 {
		return fmt.Errorf("%w: operation %q names no fields", ErrValidation, kind)
	}
	allowed := kind.AllowedFields()
	for _, f := range m {
		if !slices.Contains(allowed, f) {
			return fmt.Errorf("%w: operation %q may not change field %q", ErrValidation, kind, f)
		}
	}
	// A non-mergeable kind is applied WHOLE or not at all, so a mask naming a subset is not a
	// smaller edit — it is a request for a merge grain the entity does not have. Rejecting it
	// here means the budget's sum invariant cannot be broken by a partial write anywhere in the
	// system, including by a future op kind added carelessly.
	if kind.RequiresTotalMask() {
		// Checked by containment rather than by length: a FieldMask can be built directly as a
		// slice literal, so a duplicated entry could otherwise make a partial mask pass a
		// length comparison.
		for _, f := range allowed {
			if !slices.Contains(m, f) {
				return fmt.Errorf("%w: operation %q is applied whole and must name every one of its fields; %q is missing",
					ErrValidation, kind, f)
			}
		}
	}
	return nil
}

// Op is a COMMITTED operation: an immutable, fully resolved record of one change that
// actually happened, at one position in one trip's total order.
//
// # Resolved effects, never derivations (D62)
//
// A client asks to create a slot "after S". The server derives a fractional-index position
// from S's neighbours and stores THE POSITION. Replaying the derivation during a resync —
// against a list whose neighbours have all changed — would produce a different key, and
// convergence would die silently with nothing to detect it. The same rule holds for every
// server-side derivation, and it is made structural by BuildPayload taking the PERSISTED
// entity as its input: there is no code path by which an unresolved request value can reach
// the log.
//
// # One intent, possibly several ops (D63)
//
// Deleting a slot's selected option also clears that slot's selection (D56). Rather than
// making every client re-derive the cascade, the server commits both effects as separate ops
// sharing a CauseOpID. A log entry is therefore uniformly "one entity, one set of field
// changes", replay is a pure fold, and clients hold zero domain logic.
type Op struct {
	ID     ID
	TripID ID

	// Seq is the ordering authority: per-trip, gapless, monotonic, assigned by Postgres.
	// Nothing else in this struct orders anything.
	Seq int64

	// ActorID is nil when the acting user was later hard-deleted. History outlives accounts.
	ActorID *ID

	Kind     OpKind
	EntityID ID
	Fields   FieldMask

	// Payload's "fields" object has EXACTLY the keys named in Fields. Checked by
	// TestPayloadKeysMatchTheFieldMask, because a drift between the two would make the mask
	// decorative and the fold ambiguous.
	Payload []byte

	// ClientOpID is the client's own identifier for the intent, and the idempotency key for
	// replay after a lost acknowledgement. Nil for REST-originated operations.
	ClientOpID *ID

	// CauseOpID links the operations derived from one intent.
	CauseOpID *ID

	// CreatedAt is the server clock, and is ADVISORY ONLY. It never orders anything — a
	// client with a wrong clock, or a server with clock skew, cannot affect convergence.
	CreatedAt time.Time
}

// opPayload is the wire and storage shape of an operation's data.
//
// Fields and Meta are separated so the invariant "payload field keys == Op.Fields" stays
// exact. Version and UpdatedAt are metadata about the resulting row, not merged values, and
// folding them in with the masked fields would blur a distinction the whole design rests on.
type opPayload struct {
	Fields map[string]json.RawMessage `json:"fields"`
	Meta   opMeta                     `json:"meta"`
}

type opMeta struct {
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PayloadFields decodes an operation's field values.
func (o Op) PayloadFields() (map[string]json.RawMessage, error) {
	var p opPayload
	if err := json.Unmarshal(o.Payload, &p); err != nil {
		return nil, fmt.Errorf("decoding op %s payload: %w", o.ID, err)
	}
	return p.Fields, nil
}

// PayloadVersion returns the entity version the operation produced.
func (o Op) PayloadVersion() (int, error) {
	var p opPayload
	if err := json.Unmarshal(o.Payload, &p); err != nil {
		return 0, fmt.Errorf("decoding op %s payload: %w", o.ID, err)
	}
	return p.Meta.Version, nil
}

// Entity returns the entity class an operation acts on, which is what a fold switches on.
func (k OpKind) Entity() string {
	switch k {
	case OpDayCreate, OpDayEdit, OpDayMove, OpDayDelete:
		return "day"
	case OpSlotCreate, OpSlotEdit, OpSlotMove, OpSlotSelectOption, OpSlotSetStatus, OpSlotDelete:
		return "slot"
	case OpOptionCreate, OpOptionEdit, OpOptionDelete:
		return "option"
	case OpVoteSet:
		return "vote"
	case OpBudgetSet, OpBudgetDelete:
		return "budget"
	case OpAttachmentAdd, OpAttachmentRemove:
		return "attachment"
	}
	return ""
}

// ---------------------------------------------------------------------------------------
// Payload construction — always FROM the committed entity (D62).
// ---------------------------------------------------------------------------------------

func buildPayload(values map[string]any, mask FieldMask, version int, updatedAt time.Time) ([]byte, error) {
	fields := make(map[string]json.RawMessage, len(mask))
	for _, f := range mask {
		v, ok := values[f]
		if !ok {
			// Not a user-input error: it means the mask and the encoder disagree about what a
			// field name means, which is a programming error that must not reach the log.
			return nil, fmt.Errorf("op payload: no encoder for field %q", f)
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("op payload: encoding field %q: %w", f, err)
		}
		fields[f] = raw
	}
	return json.Marshal(opPayload{Fields: fields, Meta: opMeta{Version: version, UpdatedAt: updatedAt}})
}

func idPtrString(id *ID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

func timeOfDayString(t *TimeOfDay) *string {
	if t == nil {
		return nil
	}
	s := t.String()
	return &s
}

// SlotValues projects a slot into the wire representation of each of its fields.
func SlotValues(s *Slot) map[string]any {
	return map[string]any{
		FieldDayID:            idPtrString(s.DayID),
		FieldKind:             string(s.Kind),
		FieldTitle:            s.Title,
		FieldNotes:            s.Notes,
		FieldStartTime:        timeOfDayString(s.StartTime),
		FieldEndTime:          timeOfDayString(s.EndTime),
		FieldPosition:         s.Position,
		FieldSelectedOptionID: idPtrString(s.SelectedOptionID),
		FieldStatus:           string(s.Status),
		FieldStatusChangedAt:  s.StatusChangedAt,
		FieldStatusChangedBy:  idPtrString(s.StatusChangedBy),
		FieldDeletedAt:        s.DeletedAt,
	}
}

// SlotPayload encodes the masked fields of a slot AS PERSISTED.
func SlotPayload(s *Slot, mask FieldMask) ([]byte, error) {
	return buildPayload(SlotValues(s), mask, s.Version, s.UpdatedAt)
}

// OptionValues projects a slot option into wire field values.
func OptionValues(o *SlotOption) map[string]any {
	return map[string]any{
		FieldSlotID:          o.SlotID.String(),
		FieldTitle:           o.Title,
		FieldNotes:           o.Notes,
		FieldExternalURL:     o.ExternalURL,
		FieldEstimatedCost:   o.EstimatedCostMinor,
		FieldPlaceName:       o.Place.Name,
		FieldPlaceAddress:    o.Place.Address,
		FieldPlaceLat:        o.Place.Lat,
		FieldPlaceLng:        o.Place.Lng,
		FieldPlaceProviderID: o.Place.ProviderID,
		FieldDeletedAt:       o.DeletedAt,
	}
}

// OptionPayload encodes the masked fields of an option as persisted.
func OptionPayload(o *SlotOption, mask FieldMask) ([]byte, error) {
	return buildPayload(OptionValues(o), mask, o.Version, o.UpdatedAt)
}

// VoteValues projects a vote into wire field values.
//
// The mask always names slot_id, user_id and option_id together. Identity keys cost nothing
// here and let a fold construct the row without a second lookup — the register has no
// creation operation to carry them separately.
func VoteValues(v *Vote) map[string]any {
	return map[string]any{
		FieldSlotID:   v.SlotID.String(),
		FieldUserID:   v.UserID.String(),
		FieldOptionID: idPtrString(v.OptionID),
	}
}

// VotePayload encodes a vote as persisted.
func VotePayload(v *Vote, mask FieldMask) ([]byte, error) {
	return buildPayload(VoteValues(v), mask, v.Version, v.UpdatedAt)
}

// SplitValue is the wire shape of one budget split.
//
// A named type rather than an anonymous map because it is written into the immutable log: the
// keys are a persisted contract, and a struct with tags makes that explicit and greppable
// where a map literal would leave the shape implicit at the call site.
type SplitValue struct {
	UserID      string `json:"user_id"`
	AmountMinor int64  `json:"amount_minor"`
}

// splitValues projects a split set, ordered deterministically.
//
// The order is by user id, not by insertion. Two servers folding the same log must produce
// byte-identical entries for a value comparison to mean anything, and insertion order is not
// something the database preserves — the same reasoning that makes SplitEvenly demand a stable
// member order in the first place.
func splitValues(splits []BudgetSplit) []SplitValue {
	out := make([]SplitValue, 0, len(splits))
	for _, s := range splits {
		out = append(out, SplitValue{UserID: s.UserID.String(), AmountMinor: s.AmountMinor})
	}
	slices.SortFunc(out, func(a, b SplitValue) int { return strings.Compare(a.UserID, b.UserID) })
	return out
}

// BudgetValues projects a ledger entry into wire field values.
//
// Splits are one field holding the complete set (D44): the atomic unit is the entry together
// with every split, so it is also the log's unit. There is deliberately no way to express
// "just my share changed".
func BudgetValues(e *BudgetEntry) map[string]any {
	return map[string]any{
		FieldLabel:        e.Label,
		FieldCategory:     string(e.Category),
		FieldAmountMinor:  e.AmountMinor,
		FieldSlotOptionID: idPtrString(e.SlotOptionID),
		FieldPaidBy:       idPtrString(e.PaidBy),
		FieldIncurredOn:   e.IncurredOn,
		FieldSplits:       splitValues(e.Splits),
		FieldDeletedAt:    e.DeletedAt,
	}
}

// BudgetPayload encodes the masked fields of an entry AS PERSISTED.
func BudgetPayload(e *BudgetEntry, mask FieldMask) ([]byte, error) {
	return buildPayload(BudgetValues(e), mask, e.Version, e.UpdatedAt)
}

// AttachmentValues projects an attachment into wire field values.
//
// # What is deliberately NOT here
//
// A download URL. Attachments are private, so every read is a presigned URL with a TTL — and a
// time-limited value written into an immutable log is false by the time anything replays it.
// That is D62 ("resolved effects, never derivations") read carefully: the rule is to store the
// resolved value rather than the recipe, but a value that EXPIRES is neither. Clients receive
// the storage key here and exchange it for a fresh URL through the REST surface.
func AttachmentValues(a *Attachment) map[string]any {
	return map[string]any{
		FieldKind:          string(a.Kind),
		FieldStatus:        string(a.Status),
		FieldStorageKey:    a.StorageKey,
		FieldExternalURL:   a.ExternalURL,
		FieldContentType:   a.ContentType,
		FieldSizeBytes:     a.SizeBytes,
		FieldOriginalName:  a.OriginalName,
		FieldSlotOptionID:  idPtrString(a.SlotOptionID),
		FieldBudgetEntryID: idPtrString(a.BudgetEntryID),
		FieldSlotID:        idPtrString(a.SlotID),
		FieldUploadedBy:    idPtrString(a.UploadedBy),
		FieldDeletedAt:     a.DeletedAt,
	}
}

// AttachmentPayload encodes the masked fields of an attachment as persisted.
//
// # Why the version argument is zero
//
// Attachments have no version column, because they are immutable once ready and there is
// nothing to conflict over (D46). Passing 0 rather than inventing a counter keeps that fact
// visible in the log: an attachment op carries meta.version 0 forever, and a reader that finds
// one knows it is looking at an entity with no optimistic-concurrency story rather than at a
// row that has been edited zero times.
func AttachmentPayload(a *Attachment, mask FieldMask) ([]byte, error) {
	return buildPayload(AttachmentValues(a), mask, 0, a.UpdatedAt)
}

// DayValues projects a day into wire field values.
func DayValues(d *Day) map[string]any {
	return map[string]any{
		FieldDate:      d.Date,
		FieldLabel:     d.Label,
		FieldPosition:  d.Position,
		FieldDeletedAt: d.DeletedAt,
	}
}

// DayPayload encodes the masked fields of a day as persisted.
func DayPayload(d *Day, mask FieldMask) ([]byte, error) {
	return buildPayload(DayValues(d), mask, d.Version, d.UpdatedAt)
}

// ---------------------------------------------------------------------------------------
// OpOrigin — the ambient provenance of a write.
// ---------------------------------------------------------------------------------------

// OpOrigin carries where a mutation came from, so the log can record it.
//
// It travels on the context rather than through every service signature, for the same reason
// and at the same accepted cost as TxManager's transaction (D14): threading two nullable
// identifiers through fifteen method signatures would put request provenance into business
// APIs that have no use for it. A REST write simply sets no origin, and gets a nil
// ClientOpID — which is exactly what the partial unique index on (trip_id, client_op_id)
// expects.
type OpOrigin struct {
	// ClientOpID is the client's identifier for the intent. It is the idempotency key that
	// makes replay after a lost acknowledgement safe.
	ClientOpID *ID
	// CauseOpID groups the operations one intent produced.
	CauseOpID *ID
}

// opOriginKey is an unexported struct type, so no other package can collide with it or
// forge an origin by writing a string key.
type opOriginKey struct{}

// ContextWithOpOrigin attaches operation provenance to a context.
func ContextWithOpOrigin(ctx context.Context, o OpOrigin) context.Context {
	return context.WithValue(ctx, opOriginKey{}, o)
}

// OpOriginFrom reads provenance from a context. The zero value — no client op id, no cause —
// is the correct answer for a REST write, so this deliberately has no "ok" return: an absent
// origin is not an error condition.
func OpOriginFrom(ctx context.Context) OpOrigin {
	o, _ := ctx.Value(opOriginKey{}).(OpOrigin)
	return o
}

// ---------------------------------------------------------------------------------------
// Replica — the reference fold of an operation log.
// ---------------------------------------------------------------------------------------

// Replica is an in-memory projection of a trip's itinerary built PURELY by folding operations.
//
// It is the reference implementation of a client, and it exists so that the central invariant
// is a testable assertion rather than a claim in a document:
//
//	fold(trip_ops) == database state == client A's state == client B's state
//
// Because ops are resolved effects (D62) and cascades are expanded into separate ops (D63),
// this fold contains no domain rules at all: it looks up an entity and assigns the named
// fields. Anything more would be a place for a client to drift from the server.
type Replica struct {
	Seq     int64
	Days    map[ID]*Day
	Slots   map[ID]*Slot
	Options map[ID]*SlotOption
	Votes   map[ID]*Vote
	// Budgets and Attachments fold identically to everything above, which is the point: the
	// entities with a COARSER conflict grain get no special machinery in the replica, because
	// the coarseness was already enforced at write time by the total-mask rule. A fold that had
	// to know "the budget is atomic" would be a place for a client to disagree with the server.
	Budgets     map[ID]*BudgetEntry
	Attachments map[ID]*Attachment
}

// NewReplica returns an empty projection.
func NewReplica() *Replica {
	return &Replica{
		Days:        map[ID]*Day{},
		Slots:       map[ID]*Slot{},
		Options:     map[ID]*SlotOption{},
		Votes:       map[ID]*Vote{},
		Budgets:     map[ID]*BudgetEntry{},
		Attachments: map[ID]*Attachment{},
	}
}

// Fold applies a sequence of operations in order, returning the resulting projection.
func Fold(ops []Op) (*Replica, error) {
	r := NewReplica()
	for _, op := range ops {
		if err := r.Apply(op); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Apply folds one operation into the projection.
//
// # Redelivery is benign; a GAP is not
//
// These two look alike and must not be treated alike. Delivery is at-least-once by
// construction — the broadcast is only an accelerator and the log is the guarantee (D70), so
// a resume, a retried operation, or a reconnect can all legitimately re-deliver something
// already folded. Re-applying it is a no-op, because the entity is already at that state.
//
// A gap is the dangerous case: it means this replica has missed something and every
// subsequent field value it holds may be wrong. That is precisely what the gapless sequence
// (D61) exists to make detectable, so it is an error, and a client seeing it should resync
// rather than carry on.
//
// Conflating the two was a real bug, found by TestReplayingAClientOpIDIsIdempotent: a client
// that retried after a lost acknowledgement received the already-committed operation back and
// its fold rejected it as out of order. Rejecting redelivery would make correct at-least-once
// delivery unusable.
func (r *Replica) Apply(op Op) error {
	if op.Seq <= r.Seq {
		return nil // already folded
	}
	if op.Seq > r.Seq+1 {
		return fmt.Errorf("replica: gap in the operation log — got seq %d, expected %d; "+
			"this replica is stale and must resync", op.Seq, r.Seq+1)
	}
	fields, err := op.PayloadFields()
	if err != nil {
		return err
	}
	version, err := op.PayloadVersion()
	if err != nil {
		return err
	}

	switch op.Kind.Entity() {
	case "day":
		err = r.applyDay(op, fields, version)
	case "slot":
		err = r.applySlot(op, fields, version)
	case "option":
		err = r.applyOption(op, fields, version)
	case "vote":
		err = r.applyVote(op, fields, version)
	case "budget":
		err = r.applyBudget(op, fields, version)
	case "attachment":
		err = r.applyAttachment(op, fields)
	default:
		err = fmt.Errorf("replica: unknown op kind %q", op.Kind)
	}
	if err != nil {
		return err
	}
	r.Seq = op.Seq
	return nil
}

func decodeInto[T any](fields map[string]json.RawMessage, name string, dst *T) (bool, error) {
	raw, ok := fields[name]
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return false, fmt.Errorf("replica: decoding field %q: %w", name, err)
	}
	return true, nil
}

func decodeIDPtr(fields map[string]json.RawMessage, name string) (*ID, bool, error) {
	var s *string
	present, err := decodeInto(fields, name, &s)
	if err != nil || !present {
		return nil, present, err
	}
	if s == nil {
		return nil, true, nil
	}
	id, err := ParseID(name, *s)
	if err != nil {
		return nil, true, err
	}
	return &id, true, nil
}

func decodeTimeOfDay(fields map[string]json.RawMessage, name string) (*TimeOfDay, bool, error) {
	var s *string
	present, err := decodeInto(fields, name, &s)
	if err != nil || !present {
		return nil, present, err
	}
	if s == nil {
		return nil, true, nil
	}
	t, err := ParseTimeOfDay(*s)
	if err != nil {
		return nil, true, err
	}
	return &t, true, nil
}

func (r *Replica) applySlot(op Op, fields map[string]json.RawMessage, version int) error {
	s, ok := r.Slots[op.EntityID]
	if !ok {
		if op.Kind != OpSlotCreate {
			return fmt.Errorf("replica: op %s targets unknown slot %s", op.Kind, op.EntityID)
		}
		s = &Slot{ID: op.EntityID, TripID: op.TripID, CreatedBy: op.ActorID}
		r.Slots[op.EntityID] = s
	}

	if v, present, err := decodeIDPtr(fields, FieldDayID); err != nil {
		return err
	} else if present {
		s.DayID = v
	}
	var str string
	if present, err := decodeInto(fields, FieldKind, &str); err != nil {
		return err
	} else if present {
		s.Kind = SlotKind(str)
	}
	if present, err := decodeInto(fields, FieldTitle, &str); err != nil {
		return err
	} else if present {
		s.Title = str
	}
	if present, err := decodeInto(fields, FieldNotes, &str); err != nil {
		return err
	} else if present {
		s.Notes = str
	}
	if present, err := decodeInto(fields, FieldPosition, &str); err != nil {
		return err
	} else if present {
		s.Position = str
	}
	if present, err := decodeInto(fields, FieldStatus, &str); err != nil {
		return err
	} else if present {
		s.Status = SlotStatus(str)
	}
	if v, present, err := decodeTimeOfDay(fields, FieldStartTime); err != nil {
		return err
	} else if present {
		s.StartTime = v
	}
	if v, present, err := decodeTimeOfDay(fields, FieldEndTime); err != nil {
		return err
	} else if present {
		s.EndTime = v
	}
	if v, present, err := decodeIDPtr(fields, FieldSelectedOptionID); err != nil {
		return err
	} else if present {
		s.SelectedOptionID = v
	}
	if v, present, err := decodeIDPtr(fields, FieldStatusChangedBy); err != nil {
		return err
	} else if present {
		s.StatusChangedBy = v
	}
	var ts *time.Time
	if present, err := decodeInto(fields, FieldStatusChangedAt, &ts); err != nil {
		return err
	} else if present {
		s.StatusChangedAt = ts
	}
	// The tombstone is its own register (D-series, §5.4 of the design doc). An edit arriving
	// after a delete applies to its own fields and leaves this alone, which is what keeps
	// fold(log) == database state true in the delete-versus-edit case.
	if present, err := decodeInto(fields, FieldDeletedAt, &ts); err != nil {
		return err
	} else if present {
		s.DeletedAt = ts
	}
	s.Version = version
	return nil
}

func (r *Replica) applyOption(op Op, fields map[string]json.RawMessage, version int) error {
	o, ok := r.Options[op.EntityID]
	if !ok {
		if op.Kind != OpOptionCreate {
			return fmt.Errorf("replica: op %s targets unknown option %s", op.Kind, op.EntityID)
		}
		o = &SlotOption{ID: op.EntityID, TripID: op.TripID, ProposedBy: op.ActorID}
		r.Options[op.EntityID] = o
	}

	if v, present, err := decodeIDPtr(fields, FieldSlotID); err != nil {
		return err
	} else if present && v != nil {
		o.SlotID = *v
	}
	var str string
	for _, f := range []struct {
		name string
		dst  *string
	}{
		{FieldTitle, &o.Title},
		{FieldNotes, &o.Notes},
		{FieldExternalURL, &o.ExternalURL},
		{FieldPlaceName, &o.Place.Name},
		{FieldPlaceAddress, &o.Place.Address},
		{FieldPlaceProviderID, &o.Place.ProviderID},
	} {
		if present, err := decodeInto(fields, f.name, &str); err != nil {
			return err
		} else if present {
			*f.dst = str
		}
	}
	var cost *int64
	if present, err := decodeInto(fields, FieldEstimatedCost, &cost); err != nil {
		return err
	} else if present {
		o.EstimatedCostMinor = cost
	}
	var coord *float64
	if present, err := decodeInto(fields, FieldPlaceLat, &coord); err != nil {
		return err
	} else if present {
		o.Place.Lat = coord
	}
	if present, err := decodeInto(fields, FieldPlaceLng, &coord); err != nil {
		return err
	} else if present {
		o.Place.Lng = coord
	}
	var ts *time.Time
	if present, err := decodeInto(fields, FieldDeletedAt, &ts); err != nil {
		return err
	} else if present {
		o.DeletedAt = ts
	}
	o.Version = version
	return nil
}

func (r *Replica) applyVote(op Op, fields map[string]json.RawMessage, version int) error {
	v, ok := r.Votes[op.EntityID]
	if !ok {
		v = &Vote{ID: op.EntityID, TripID: op.TripID}
		r.Votes[op.EntityID] = v
	}
	if id, present, err := decodeIDPtr(fields, FieldSlotID); err != nil {
		return err
	} else if present && id != nil {
		v.SlotID = *id
	}
	if id, present, err := decodeIDPtr(fields, FieldUserID); err != nil {
		return err
	} else if present && id != nil {
		v.UserID = *id
	}
	if id, present, err := decodeIDPtr(fields, FieldOptionID); err != nil {
		return err
	} else if present {
		v.OptionID = id
	}
	v.Version = version
	return nil
}

// applyBudget folds a ledger entry.
//
// budget.set.v1 is create-and-edit in one verb, so there is no "unknown entity" check of the
// kind applySlot makes: an entry the replica has not seen is simply one whose first operation
// this is. The total-mask rule guarantees the op carries every field, so the resulting entry is
// complete however it was reached — which is precisely why create and edit need not be
// distinguished here.
func (r *Replica) applyBudget(op Op, fields map[string]json.RawMessage, version int) error {
	e, ok := r.Budgets[op.EntityID]
	if !ok {
		if op.Kind != OpBudgetSet {
			return fmt.Errorf("replica: op %s targets unknown budget entry %s", op.Kind, op.EntityID)
		}
		e = &BudgetEntry{ID: op.EntityID, TripID: op.TripID, CreatedBy: op.ActorID}
		r.Budgets[op.EntityID] = e
	}

	var str string
	if present, err := decodeInto(fields, FieldLabel, &str); err != nil {
		return err
	} else if present {
		e.Label = str
	}
	if present, err := decodeInto(fields, FieldCategory, &str); err != nil {
		return err
	} else if present {
		e.Category = BudgetCategory(str)
	}
	var amount int64
	if present, err := decodeInto(fields, FieldAmountMinor, &amount); err != nil {
		return err
	} else if present {
		e.AmountMinor = amount
	}
	if v, present, err := decodeIDPtr(fields, FieldSlotOptionID); err != nil {
		return err
	} else if present {
		e.SlotOptionID = v
	}
	if v, present, err := decodeIDPtr(fields, FieldPaidBy); err != nil {
		return err
	} else if present {
		e.PaidBy = v
	}
	var ts *time.Time
	if present, err := decodeInto(fields, FieldIncurredOn, &ts); err != nil {
		return err
	} else if present {
		e.IncurredOn = ts
	}

	// The split set is replaced wholesale, never merged element-wise. Any other treatment here
	// would reintroduce at fold time exactly the partial update the total-mask rule refuses at
	// write time, and the replica would drift from the database while both looked healthy.
	var splits []SplitValue
	if present, err := decodeInto(fields, FieldSplits, &splits); err != nil {
		return err
	} else if present {
		out := make([]BudgetSplit, 0, len(splits))
		for _, s := range splits {
			userID, err := ParseID(FieldSplits, s.UserID)
			if err != nil {
				return err
			}
			out = append(out, BudgetSplit{UserID: userID, AmountMinor: s.AmountMinor})
		}
		e.Splits = out
	}

	if present, err := decodeInto(fields, FieldDeletedAt, &ts); err != nil {
		return err
	} else if present {
		e.DeletedAt = ts
	}
	e.Version = version
	return nil
}

// applyAttachment folds an attachment.
//
// No version parameter: attachments have none (D46). The absence is the honest encoding of
// "this entity has no concurrent-edit story", and taking a version here just to discard it
// would suggest otherwise.
func (r *Replica) applyAttachment(op Op, fields map[string]json.RawMessage) error {
	a, ok := r.Attachments[op.EntityID]
	if !ok {
		if op.Kind != OpAttachmentAdd {
			return fmt.Errorf("replica: op %s targets unknown attachment %s", op.Kind, op.EntityID)
		}
		a = &Attachment{ID: op.EntityID, TripID: op.TripID}
		r.Attachments[op.EntityID] = a
	}

	var str string
	for _, f := range []struct {
		name string
		dst  *string
	}{
		{FieldStorageKey, &a.StorageKey},
		{FieldExternalURL, &a.ExternalURL},
		{FieldContentType, &a.ContentType},
		{FieldOriginalName, &a.OriginalName},
	} {
		if present, err := decodeInto(fields, f.name, &str); err != nil {
			return err
		} else if present {
			*f.dst = str
		}
	}
	if present, err := decodeInto(fields, FieldKind, &str); err != nil {
		return err
	} else if present {
		a.Kind = AttachmentKind(str)
	}
	if present, err := decodeInto(fields, FieldStatus, &str); err != nil {
		return err
	} else if present {
		a.Status = AttachmentStatus(str)
	}
	var size *int64
	if present, err := decodeInto(fields, FieldSizeBytes, &size); err != nil {
		return err
	} else if present {
		a.SizeBytes = size
	}
	for _, f := range []struct {
		name string
		dst  **ID
	}{
		{FieldSlotOptionID, &a.SlotOptionID},
		{FieldBudgetEntryID, &a.BudgetEntryID},
		{FieldSlotID, &a.SlotID},
		{FieldUploadedBy, &a.UploadedBy},
	} {
		if v, present, err := decodeIDPtr(fields, f.name); err != nil {
			return err
		} else if present {
			*f.dst = v
		}
	}
	var ts *time.Time
	if present, err := decodeInto(fields, FieldDeletedAt, &ts); err != nil {
		return err
	} else if present {
		a.DeletedAt = ts
	}
	return nil
}

func (r *Replica) applyDay(op Op, fields map[string]json.RawMessage, version int) error {
	d, ok := r.Days[op.EntityID]
	if !ok {
		if op.Kind != OpDayCreate {
			return fmt.Errorf("replica: op %s targets unknown day %s", op.Kind, op.EntityID)
		}
		d = &Day{ID: op.EntityID, TripID: op.TripID}
		r.Days[op.EntityID] = d
	}
	var str string
	if present, err := decodeInto(fields, FieldLabel, &str); err != nil {
		return err
	} else if present {
		d.Label = str
	}
	if present, err := decodeInto(fields, FieldPosition, &str); err != nil {
		return err
	} else if present {
		d.Position = str
	}
	var ts *time.Time
	if present, err := decodeInto(fields, FieldDate, &ts); err != nil {
		return err
	} else if present {
		d.Date = ts
	}
	if present, err := decodeInto(fields, FieldDeletedAt, &ts); err != nil {
		return err
	} else if present {
		d.DeletedAt = ts
	}
	d.Version = version
	return nil
}
