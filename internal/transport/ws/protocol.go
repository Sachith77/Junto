package ws

import (
	"encoding/json"
	"time"
)

// The wire protocol.
//
// Every frame is a JSON object with a "type" discriminator. JSON rather than a binary codec
// because the payloads are already JSON in the operation log, and re-encoding them would mean
// two representations of the same operation that could disagree.
//
// # Where the acknowledgement went
//
// There is no separate ack frame for a successful operation. A client's own operation comes
// back through the room broadcast carrying its client_op_id, and THAT is the acknowledgement:
// it arrives with the server-assigned seq and the resolved values, which is strictly more than
// an ack would carry. One delivery path instead of two means there is no way for them to
// disagree. Errors do get their own frame, because there is nothing to echo.

// Client -> server frame types.
const (
	FrameSubscribe   = "subscribe"
	FrameUnsubscribe = "unsubscribe"
	FrameOp          = "op"
)

// Server -> client frame types.
const (
	FrameSubscribed     = "subscribed"
	FrameOpBroadcast    = "op"
	FrameError          = "error"
	FramePresence       = "presence"
	FrameResyncRequired = "resync_required"
	FrameUnsubscribed   = "unsubscribed"
)

// clientFrame is the envelope a client sends.
type clientFrame struct {
	Type   string `json:"type"`
	TripID string `json:"trip_id"`

	// SinceSeq resumes a subscription from a known point.
	//
	// A POINTER, because absent and zero are different requests. Absent means "I have no
	// prior state, start me at the current head"; 0 means "replay the entire log". Decoding
	// it into an int64 would make those the same message, and a client with nothing stored
	// would have no way to ask for a full replay — the same "explicit beats inferred"
	// reasoning as the field mask (D64), one level down.
	SinceSeq *int64 `json:"since_seq,omitempty"`

	// Operation submission.
	ClientOpID string          `json:"client_op_id,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	EntityID   string          `json:"entity_id,omitempty"`
	Fields     []string        `json:"fields,omitempty"`
	Values     json.RawMessage `json:"values,omitempty"`
}

// opFrame carries one committed operation.
//
// Fields mirrors domain.Op exactly, because a client's fold is a pure assignment of the named
// fields and anything the server reshaped here would be a chance for the two to disagree.
type opFrame struct {
	Type       string          `json:"type"`
	TripID     string          `json:"trip_id"`
	Seq        int64           `json:"seq"`
	OpID       string          `json:"op_id"`
	Kind       string          `json:"kind"`
	EntityID   string          `json:"entity_id"`
	ActorID    string          `json:"actor_id,omitempty"`
	Fields     []string        `json:"fields"`
	Payload    json.RawMessage `json:"payload"`
	ClientOpID string          `json:"client_op_id,omitempty"`
	CauseOpID  string          `json:"cause_op_id,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

type subscribedFrame struct {
	Type     string           `json:"type"`
	TripID   string           `json:"trip_id"`
	Seq      int64            `json:"seq"`
	Presence []presenceMember `json:"presence"`
}

type presenceMember struct {
	UserID   string    `json:"user_id"`
	ConnID   string    `json:"conn_id"`
	JoinedAt time.Time `json:"joined_at"`
}

type presenceFrame struct {
	Type   string    `json:"type"`
	TripID string    `json:"trip_id"`
	Event  string    `json:"event"` // "joined" | "left"
	UserID string    `json:"user_id"`
	ConnID string    `json:"conn_id"`
	At     time.Time `json:"at"`
}

// errorFrame reports a rejected frame.
//
// ClientOpID is echoed so a client can resolve exactly which pending optimistic change failed.
// Without it, a client with several operations in flight would have to discard all of them.
type errorFrame struct {
	Type       string           `json:"type"`
	ClientOpID string           `json:"client_op_id,omitempty"`
	Code       string           `json:"code"`
	Message    string           `json:"message"`
	Violations []fieldViolation `json:"violations,omitempty"`
}

type fieldViolation struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type resyncFrame struct {
	Type   string `json:"type"`
	TripID string `json:"trip_id"`
	Reason string `json:"reason"`
}

type simpleFrame struct {
	Type   string `json:"type"`
	TripID string `json:"trip_id"`
}

// Error codes. Stable, machine-readable identifiers: clients switch on these, and the human
// message is free to change.
const (
	codeBadFrame      = "bad_frame"
	codeNotFound      = "not_found"
	codeForbidden     = "forbidden"
	codeValidation    = "validation_failed"
	codeConflict      = "version_conflict"
	codeBusy          = "busy"
	codeInternal      = "internal"
	codeRateLimited   = "rate_limited"
	codeNotSubscribed = "not_subscribed"

	// codeSessionRevoked is terminal, and the only error code here that is. Every other code
	// describes a rejected frame on a connection that stays open; this one says the connection
	// itself is about to close and reconnecting will fail until the client authenticates again.
	// A client that treats it as retryable will loop against the ticket endpoint forever.
	codeSessionRevoked = "session_revoked"
)
