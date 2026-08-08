package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/junto/junto/internal/domain"
)

// Attachment lifecycle timings.
const (
	// uploadURLTTL bounds a presigned PUT. Long enough for a large photo on a bad connection,
	// short enough that a URL leaking from a browser history or a proxy log is not a standing
	// write grant against the bucket.
	uploadURLTTL = 15 * time.Minute

	// downloadURLTTL bounds a presigned GET. Shorter, because a read URL is what actually gets
	// pasted into a chat window by mistake.
	downloadURLTTL = 5 * time.Minute

	// abandonedUploadAge is how long a pending row waits before the sweeper reclaims it. It has
	// to exceed uploadURLTTL by a clear margin, or the sweeper would delete rows whose presigned
	// URL is still valid and whose upload is still in flight.
	abandonedUploadAge = time.Hour
)

// AttachmentService manages photos, ticket screenshots and links.
//
// # The one entity with no conflict resolution (D46)
//
// An upload is a one-shot event, not a mergeable field, so two concurrent uploads simply both
// exist and there is nothing to reconcile. Attachments therefore have no version column, no
// field mask worth the name (the add operation's mask is total by rule), and no edit verb: you
// replace an attachment, you do not edit one.
//
// What they DO have is a log entry, and that is the part worth stating. It would have been
// defensible to broadcast attachments without logging them, since there is no merge to replay.
// It would also have been wrong: a member who was offline while a photo was added would never
// learn about it, because the resync path reads the log and nothing else. That is exactly the
// hole Rule 3 exists to close, and it does not stop applying to an entity just because the
// entity is simple.
//
// # Why the API never sees the bytes (D48)
//
// The browser PUTs directly to object storage through a presigned URL. Proxying the file
// through this process would spend memory and bandwidth for nothing. The cost, stated plainly:
// a presigned PUT cannot enforce a size or content-type limit, so the row is created `pending`,
// the object is verified server-side after it lands, and only then does it become `ready` and
// visible to the trip. Rows whose upload never arrives are swept.
type AttachmentService struct {
	authz
	oplog
	attachments domain.AttachmentRepository
	storage     domain.FileStorage
	slots       domain.SlotRepository
	options     domain.SlotOptionRepository
	budget      domain.BudgetRepository
	clock       domain.Clock
	log         *slog.Logger
}

// AttachmentDeps collects AttachmentService's dependencies.
type AttachmentDeps struct {
	Attachments domain.AttachmentRepository
	Storage     domain.FileStorage
	Slots       domain.SlotRepository
	Options     domain.SlotOptionRepository
	Budget      domain.BudgetRepository
	Members     domain.MembershipRepository
	Trips       domain.TripRepository
	Ops         domain.OpLogRepository
	Tx          domain.TxManager
	Pub         domain.OpPublisher
	Clock       domain.Clock
	Logger      *slog.Logger
}

// NewAttachmentService builds an AttachmentService.
func NewAttachmentService(deps AttachmentDeps) *AttachmentService {
	if deps.Clock == nil {
		deps.Clock = domain.SystemClock{}
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &AttachmentService{
		authz:       authz{members: deps.Members},
		oplog:       newOplog(deps.Trips, deps.Ops, deps.Tx, deps.Pub),
		attachments: deps.Attachments,
		storage:     deps.Storage,
		slots:       deps.Slots,
		options:     deps.Options,
		budget:      deps.Budget,
		clock:       deps.Clock,
		log:         deps.Logger,
	}
}

// ListForOwner returns the live attachments on one entity.
func (s *AttachmentService) ListForOwner(ctx context.Context, tripID, userID domain.ID, owner domain.AttachmentOwner) ([]*domain.Attachment, error) {
	if _, err := s.actor(ctx, tripID, userID); err != nil {
		return nil, err
	}
	if err := s.checkOwnerInTrip(ctx, tripID, owner); err != nil {
		return nil, err
	}
	return s.attachments.ListForOwner(ctx, owner)
}

// CreateLinkInput is the input to CreateLink.
type CreateLinkInput struct {
	Owner domain.AttachmentOwner
	URL   string
	Title string

	// ID lets a client name the attachment before the server has seen it (D4).
	ID domain.ID
}

// CreateLink attaches a URL. A link is ready on arrival — there is nothing to upload — so it is
// broadcast in the same transaction that creates it.
func (s *AttachmentService) CreateLink(ctx context.Context, tripID, userID domain.ID, in CreateLinkInput) (*domain.Attachment, error) {
	actor, err := s.require(ctx, tripID, userID, domain.CapUploadAttachments)
	if err != nil {
		return nil, err
	}

	id := in.ID
	if id == domain.NilID {
		id = domain.NewID()
	}
	attachment := &domain.Attachment{
		ID: id, TripID: tripID,
		SlotOptionID: in.Owner.SlotOptionID, BudgetEntryID: in.Owner.BudgetEntryID, SlotID: in.Owner.SlotID,
		Kind: domain.AttachmentKindLink, Status: domain.AttachmentStatusReady,
		ExternalURL:  strings.TrimSpace(in.URL),
		OriginalName: in.Title,
		UploadedBy:   &actor.UserID,
	}

	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		if err := s.checkOwnerInTrip(ctx, tripID, in.Owner); err != nil {
			return err
		}
		if err := attachment.Validate(); err != nil {
			return err
		}
		if err := s.attachments.Create(ctx, attachment); err != nil {
			return err
		}
		return rec.attachment(ctx, domain.OpAttachmentAdd, attachment)
	})
	if err != nil {
		return nil, err
	}
	return attachment, nil
}

// RequestUploadInput is the input to RequestUpload.
type RequestUploadInput struct {
	Owner        domain.AttachmentOwner
	ContentType  string
	OriginalName string
	ID           domain.ID
}

// UploadTicket is what a client needs to perform the upload itself.
type UploadTicket struct {
	Attachment *domain.Attachment
	UploadURL  string
	ExpiresAt  time.Time
}

// RequestUpload reserves an attachment row and returns a presigned PUT.
//
// # Why this records NO operation
//
// A pending row is invisible to everyone but its uploader, and it may never become anything —
// abandoned uploads are the normal failure mode of this design, and the sweeper deletes them.
// Broadcasting a create here would announce a photo that may never exist, and then have to
// un-announce it with a removal for a row nobody ever saw. So the log entry is written at
// CONFIRMATION, when there is a real object to tell people about. "Announce it when it becomes
// visible" is a rule with one exception-free form: a link is visible immediately, a file when
// its bytes land.
//
// It also takes no trip lock and allocates no sequence number, which matters more than it
// looks: presigning is the high-frequency half of an upload, and serialising it behind the
// trip's write lock would put photo uploads in contention with itinerary edits for no benefit.
func (s *AttachmentService) RequestUpload(ctx context.Context, tripID, userID domain.ID, in RequestUploadInput) (*UploadTicket, error) {
	actor, err := s.require(ctx, tripID, userID, domain.CapUploadAttachments)
	if err != nil {
		return nil, err
	}
	if err := s.checkOwnerInTrip(ctx, tripID, in.Owner); err != nil {
		return nil, err
	}

	id := in.ID
	if id == domain.NilID {
		id = domain.NewID()
	}

	contentType := strings.TrimSpace(in.ContentType)
	if contentType == "" {
		ve := &domain.ValidationError{}
		ve.Add("content_type", "required",
			"a content type is required; it is bound into the upload signature")
		return nil, ve
	}

	attachment := &domain.Attachment{
		ID: id, TripID: tripID,
		SlotOptionID: in.Owner.SlotOptionID, BudgetEntryID: in.Owner.BudgetEntryID, SlotID: in.Owner.SlotID,
		Kind: domain.AttachmentKindFile, Status: domain.AttachmentStatusPending,
		StorageKey:   storageKey(tripID, id),
		ContentType:  contentType,
		OriginalName: in.OriginalName,
		UploadedBy:   &actor.UserID,
	}
	if err := attachment.Validate(); err != nil {
		return nil, err
	}

	// The row first, the URL second. A row with no URL is a harmless one the sweeper reclaims;
	// a URL with no row is a write grant against the bucket that nothing will ever clean up.
	if err := s.attachments.Create(ctx, attachment); err != nil {
		return nil, err
	}

	url, err := s.storage.PresignUpload(ctx, attachment.StorageKey, contentType, uploadURLTTL)
	if err != nil {
		return nil, fmt.Errorf("presigning upload for attachment %s: %w", attachment.ID, err)
	}
	return &UploadTicket{
		Attachment: attachment,
		UploadURL:  url,
		ExpiresAt:  s.clock.Now().Add(uploadURLTTL),
	}, nil
}

// ConfirmUpload verifies the uploaded object and makes the attachment visible.
//
// This is the compensating control for the whole presigned-upload design. The signature cannot
// bound the size, so the server checks it here, against what the STORAGE reports rather than
// what the client claims — a client that lies about its size has simply not been asked.
//
// An object that fails verification is marked failed and deleted from storage. That deletion is
// not tidiness: an oversized object that stayed would be an unbounded write primitive for any
// member, since nothing else limits what a presigned PUT accepts.
func (s *AttachmentService) ConfirmUpload(ctx context.Context, tripID, userID, attachmentID domain.ID) (*domain.Attachment, error) {
	actor, err := s.require(ctx, tripID, userID, domain.CapUploadAttachments)
	if err != nil {
		return nil, err
	}

	existing, err := s.getInTrip(ctx, tripID, attachmentID)
	if err != nil {
		return nil, err
	}
	if existing.Kind != domain.AttachmentKindFile {
		ve := &domain.ValidationError{}
		ve.Add("kind", "not_a_file", "only a file upload can be confirmed")
		return nil, ve
	}
	if existing.IsReady() {
		// Idempotent: a client that lost the response and retried gets the same answer rather
		// than an error about a state it already reached.
		return existing, nil
	}

	info, err := s.storage.Stat(ctx, existing.StorageKey)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			ve := &domain.ValidationError{}
			ve.Add("upload", "not_uploaded",
				"no uploaded object was found for this attachment")
			return nil, ve
		}
		return nil, fmt.Errorf("stating uploaded object for attachment %s: %w", attachmentID, err)
	}

	if info.SizeBytes > domain.MaxAttachmentBytes {
		if err := s.rejectUpload(ctx, existing); err != nil {
			return nil, err
		}
		ve := &domain.ValidationError{}
		ve.Add("size_bytes", "too_large", "the uploaded file exceeds the maximum upload size")
		return nil, ve
	}

	now := s.clock.Now()
	var confirmed *domain.Attachment
	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		// The checksum is left NULL, deliberately. Object storage reports an ETag, which for a
		// simple PUT is an MD5 and for a multipart upload is not a hash of the content at all;
		// checksum_sha256 is a SHA-256 column. Writing an MD5 into it would produce a value that
		// is the right length to look trustworthy and wrong for every purpose a checksum has.
		// Computing a real SHA-256 means reading the object back, which is exactly the byte
		// transfer the presigned design exists to avoid — so the column stays null until there
		// is a reason to pay for it, which is what nullable means here (D18).
		if err := s.attachments.Confirm(ctx, attachmentID, info.SizeBytes, nil, now); err != nil {
			return err
		}
		// Re-read rather than reusing the in-memory row: the operation payload must be built
		// from the entity AS PERSISTED (D62), and the confirm statement is what decided the
		// status, size and updated_at that a replaying client will fold.
		fresh, err := s.attachments.GetByID(ctx, attachmentID)
		if err != nil {
			return err
		}
		confirmed = fresh
		return rec.attachment(ctx, domain.OpAttachmentAdd, fresh)
	})
	if err != nil {
		return nil, err
	}
	return confirmed, nil
}

// rejectUpload marks a failed verification and removes the object it refused.
func (s *AttachmentService) rejectUpload(ctx context.Context, a *domain.Attachment) error {
	if err := s.attachments.MarkFailed(ctx, a.ID, s.clock.Now()); err != nil {
		return err
	}
	// Best effort, and deliberately not fatal: the row is already marked failed, so the
	// attachment is unusable either way, and the sweeper will retry the object. Failing the
	// request here would report an error for something the caller cannot act on.
	if err := s.storage.Delete(ctx, a.StorageKey); err != nil {
		s.log.Warn("deleting a rejected upload's object",
			"attachment_id", a.ID, "storage_key", a.StorageKey, "error", err)
	}
	return nil
}

// DownloadURL returns a short-lived signed read URL.
//
// Every read is signed because attachments are private; there is no public-bucket path. The URL
// is minted per request and never stored — which is also why it does not appear in the
// operation log, where a value with a TTL would be false by the time anything replayed it.
func (s *AttachmentService) DownloadURL(ctx context.Context, tripID, userID, attachmentID domain.ID) (string, error) {
	if _, err := s.actor(ctx, tripID, userID); err != nil {
		return "", err
	}
	attachment, err := s.getInTrip(ctx, tripID, attachmentID)
	if err != nil {
		return "", err
	}
	if attachment.Kind == domain.AttachmentKindLink {
		return attachment.ExternalURL, nil
	}
	if !attachment.IsReady() {
		ve := &domain.ValidationError{}
		ve.Add("status", "not_ready", "this upload has not been confirmed yet")
		return "", ve
	}
	return s.storage.PresignDownload(ctx, attachment.StorageKey, downloadURLTTL)
}

// Delete tombstones an attachment and tells the room.
//
// The stored object is deliberately LEFT IN PLACE. A soft delete is reversible by design
// everywhere else in this system, and deleting the bytes would make this the one tombstone that
// destroys data — while also breaking any download already in flight. Reclaiming orphaned
// objects is the sweeper's job, on its own schedule.
func (s *AttachmentService) Delete(ctx context.Context, tripID, userID, attachmentID domain.ID) error {
	actor, err := s.require(ctx, tripID, userID, domain.CapUploadAttachments)
	if err != nil {
		return err
	}

	now := s.clock.Now()
	_, err = s.write(ctx, tripID, actor.UserID, func(ctx context.Context, rec *recorder) error {
		existing, err := s.getInTrip(ctx, tripID, attachmentID)
		if err != nil {
			return err
		}
		if err := s.attachments.SoftDelete(ctx, attachmentID, now); err != nil {
			return err
		}

		tombstone := *existing
		tombstone.DeletedAt = &now
		tombstone.UpdatedAt = now

		// A pending attachment was never announced, so there is nothing to retract and a
		// removal op would target an entity no client has ever seen — which a fold correctly
		// rejects as an operation on an unknown entity.
		if existing.Status == domain.AttachmentStatusPending {
			return nil
		}
		return rec.attachment(ctx, domain.OpAttachmentRemove, &tombstone, domain.FieldDeletedAt)
	})
	return err
}

// SweepAbandonedUploads reclaims rows whose upload never arrived.
//
// Not wired to a scheduler in this slice — it is called by tests and is ready for a maintenance
// job. It records no operations, for the same reason RequestUpload does not: these rows were
// never announced to anybody.
func (s *AttachmentService) SweepAbandonedUploads(ctx context.Context, limit int) (int, error) {
	cutoff := s.clock.Now().Add(-abandonedUploadAge)
	stale, err := s.attachments.ListStalePending(ctx, cutoff, limit)
	if err != nil {
		return 0, err
	}

	swept := 0
	for _, a := range stale {
		if err := s.storage.Delete(ctx, a.StorageKey); err != nil {
			// Leave the row pending so the next sweep retries. Deleting the row while its
			// object survives would strand the object permanently, with nothing left pointing
			// at it to try again.
			s.log.Warn("deleting an abandoned upload's object",
				"attachment_id", a.ID, "storage_key", a.StorageKey, "error", err)
			continue
		}
		if err := s.attachments.SoftDelete(ctx, a.ID, s.clock.Now()); err != nil {
			return swept, err
		}
		swept++
	}
	return swept, nil
}

// storageKey builds the object key for an attachment.
//
// Derived entirely from identifiers the SERVER controls — no part of the client's filename
// reaches it. A key built from user input is a path-traversal and key-collision surface, and
// the original name is preserved in its own column where it is data rather than a path.
func storageKey(tripID, attachmentID domain.ID) string {
	return fmt.Sprintf("trips/%s/attachments/%s", tripID, attachmentID)
}

// checkOwnerInTrip verifies the owning entity exists AND belongs to this trip (D54).
//
// The exclusive arc guarantees exactly one owner column is set; it guarantees nothing about
// whose trip that owner is in. Without this check a member of trip A who learns a trip-B slot id
// could hang an attachment off it while being authorized against trip A.
func (s *AttachmentService) checkOwnerInTrip(ctx context.Context, tripID domain.ID, owner domain.AttachmentOwner) error {
	if !owner.Valid() {
		ve := &domain.ValidationError{}
		ve.Add("owner", "exactly_one_required",
			"an attachment must belong to exactly one of: slot option, budget entry, slot")
		return ve
	}

	switch {
	case owner.SlotID != nil:
		slot, err := s.slots.GetByID(ctx, *owner.SlotID)
		if err != nil {
			return err
		}
		return checkTrip(slot.TripID, tripID)
	case owner.SlotOptionID != nil:
		option, err := s.options.GetByID(ctx, *owner.SlotOptionID)
		if err != nil {
			return err
		}
		return checkTrip(option.TripID, tripID)
	default:
		entry, err := s.budget.GetByID(ctx, *owner.BudgetEntryID)
		if err != nil {
			return err
		}
		return checkTrip(entry.TripID, tripID)
	}
}

func (s *AttachmentService) getInTrip(ctx context.Context, tripID, attachmentID domain.ID) (*domain.Attachment, error) {
	attachment, err := s.attachments.GetByID(ctx, attachmentID)
	if err != nil {
		return nil, err
	}
	if err := checkTrip(attachment.TripID, tripID); err != nil {
		return nil, err
	}
	return attachment, nil
}
