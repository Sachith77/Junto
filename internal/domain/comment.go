package domain

import (
	"time"
	"unicode/utf8"
)

// MaxCommentBodyLength mirrors the comments_body_len CHECK constraint (migration 000005).
const MaxCommentBodyLength = 4000

// Comment is a flat, non-threaded message attached to exactly one slot.
//
// # Stage 3 sync note — the same treatment as Attachment, not SlotOption
//
// A comment is an event (someone said something at a point in time), not a mutable shared
// value like a slot's notes field. Two concurrent comments never conflict — they are just
// two rows — so there is no register, no version, nothing to merge. Comments carry no
// version column for the same reason Attachment does not (D46): there is no edit verb, so
// there is no concurrency story to protect. Post a new one, delete the old one.
type Comment struct {
	ID     ID
	SlotID ID
	TripID ID

	Body string

	// AuthorID is nil once the author's account is gone (ON DELETE SET NULL) — history
	// outlives accounts, same as SlotOption.ProposedBy.
	AuthorID *ID

	CreatedAt time.Time
	// UpdatedAt never changes after insert except once, to the delete timestamp, when the
	// tombstone is written. Kept because buildPayload requires one on every entity and
	// comments_trip_updated sorts by it, matching every other resync-support index.
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// IsDeleted reports whether the comment is tombstoned.
func (c *Comment) IsDeleted() bool { return c.DeletedAt != nil }

// Validate checks the comment's invariants.
func (c *Comment) Validate() error {
	ve := &ValidationError{}

	ve.AddIf(c.SlotID == NilID, "slot_id", "required", "slot id is required")
	ve.AddIf(c.TripID == NilID, "trip_id", "required", "trip id is required")

	length := utf8.RuneCountInString(c.Body)
	ve.AddIf(length == 0, "body", "required", "comment body is required")
	ve.AddIf(length > MaxCommentBodyLength, "body", "too_long", "comment exceeds the maximum length")

	return ve.OrNil()
}
