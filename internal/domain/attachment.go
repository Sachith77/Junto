package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

// AttachmentKind distinguishes an uploaded file from a bare link.
type AttachmentKind string

const (
	AttachmentKindFile AttachmentKind = "file"
	AttachmentKindLink AttachmentKind = "link"
)

// Valid reports whether k is a known kind.
func (k AttachmentKind) Valid() bool {
	return k == AttachmentKindFile || k == AttachmentKindLink
}

// AttachmentStatus tracks the two-phase upload lifecycle.
type AttachmentStatus string

const (
	// AttachmentStatusPending means a presigned URL was issued but the upload has not been
	// confirmed. Rows can sit here forever if a client abandons the upload, so a sweeper
	// removes stale ones.
	AttachmentStatusPending AttachmentStatus = "pending"
	// AttachmentStatusReady means the object was verified by a server-side Stat.
	AttachmentStatusReady AttachmentStatus = "ready"
	// AttachmentStatusFailed means verification rejected it (too large, wrong type).
	AttachmentStatusFailed AttachmentStatus = "failed"
)

// Valid reports whether s is a known status.
func (s AttachmentStatus) Valid() bool {
	switch s {
	case AttachmentStatusPending, AttachmentStatusReady, AttachmentStatusFailed:
		return true
	}
	return false
}

// MaxAttachmentBytes caps a single upload.
//
// It cannot be enforced by the presigned URL itself — a presigned PUT carries no size limit —
// so it is checked server-side at confirm time against the object's actual size, and an
// oversized object is deleted. That is the whole reason the lifecycle has two phases.
const MaxAttachmentBytes int64 = 25 << 20 // 25 MiB

// Attachment is a photo, ticket screenshot, or link hanging off exactly one owner.
//
// # Ownership
//
// Exactly one of SlotOptionID, BudgetEntryID or SlotID is set. In the database this is an
// exclusive arc — three real foreign keys plus a num_nonnulls(...) = 1 check — rather than a
// polymorphic (owner_type, owner_id) pair, for the same reason trip membership is not
// polymorphic: Postgres cannot enforce a polymorphic reference, and referential integrity is
// worth more than two saved columns.
//
// # Stage 2 sync note — this is the ONE entity with no conflict resolution
//
// An upload is a one-shot event, not a mergeable field. There is no meaningful "merge" of
// two concurrent uploads: both simply exist. So attachments need only a lightweight
// broadcast in the sync vocabulary —
//
//	attachment_added(owner, attachment)
//	attachment_removed(attachment_id)
//
// — and must NOT be given operational transforms. Attachments carry no version column for
// the same reason: they are immutable once ready. You replace one, you do not edit it.
//
// # Why the API never sees the bytes
//
// The browser uploads directly to object storage via a presigned URL. Proxying files through
// the API would burn memory and bandwidth for no benefit. The consequence is that content
// cannot be validated before it lands, which is what the pending -> ready confirmation and
// the server-side Stat exist to compensate for.
type Attachment struct {
	ID     ID
	TripID ID

	// Exactly one of these is non-nil.
	SlotOptionID  *ID
	BudgetEntryID *ID
	SlotID        *ID

	Kind   AttachmentKind
	Status AttachmentStatus

	// StorageKey is the object key for files, empty for links.
	StorageKey string
	// ExternalURL is set for links, empty for files.
	ExternalURL string

	ContentType    string
	SizeBytes      *int64
	ChecksumSHA256 []byte
	OriginalName   string

	UploadedBy *ID
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  *time.Time
}

// OwnerCount returns how many owners are set. Exactly one is valid.
func (a *Attachment) OwnerCount() int {
	n := 0
	for _, id := range []*ID{a.SlotOptionID, a.BudgetEntryID, a.SlotID} {
		if id != nil {
			n++
		}
	}
	return n
}

// IsReady reports whether the attachment is usable.
func (a *Attachment) IsReady() bool { return a.Status == AttachmentStatusReady }

// IsDeleted reports whether the attachment is tombstoned.
func (a *Attachment) IsDeleted() bool { return a.DeletedAt != nil }

// Validate checks the attachment's invariants.
func (a *Attachment) Validate() error {
	ve := &ValidationError{}

	ve.AddIf(a.TripID == NilID, "trip_id", "required", "trip id is required")
	ve.AddIf(!a.Kind.Valid(), "kind", "invalid_kind", "must be one of: file, link")
	ve.AddIf(!a.Status.Valid(), "status", "invalid_status", "must be one of: pending, ready, failed")

	// Mirrors attachments_one_owner.
	if n := a.OwnerCount(); n != 1 {
		ve.Add("owner", "exactly_one_required",
			"an attachment must belong to exactly one of: slot option, budget entry, slot")
	}

	// Mirrors attachments_shape.
	switch a.Kind {
	case AttachmentKindFile:
		ve.AddIf(a.StorageKey == "", "storage_key", "required", "a file attachment needs a storage key")
		ve.AddIf(a.ExternalURL != "", "external_url", "not_allowed", "a file attachment must not carry a URL")
	case AttachmentKindLink:
		ve.AddIf(strings.TrimSpace(a.ExternalURL) == "", "external_url", "required", "a link attachment needs a URL")
		ve.AddIf(a.StorageKey != "", "storage_key", "not_allowed", "a link attachment must not carry a storage key")
		// A link has nothing to upload, so it is ready on creation.
		ve.AddIf(a.Status != AttachmentStatusReady, "status", "invalid_status",
			"a link attachment is ready immediately")
	}

	ve.AddIf(utf8.RuneCountInString(a.OriginalName) > 255, "original_name", "too_long",
		"file name must be at most 255 characters")
	ve.AddIf(a.SizeBytes != nil && *a.SizeBytes < 0, "size_bytes", "negative", "size must not be negative")
	ve.AddIf(a.SizeBytes != nil && *a.SizeBytes > MaxAttachmentBytes, "size_bytes", "too_large",
		"file exceeds the maximum upload size")
	ve.AddIf(len(a.ChecksumSHA256) != 0 && len(a.ChecksumSHA256) != 32, "checksum", "invalid_length",
		"checksum must be 32 bytes")

	return ve.OrNil()
}
