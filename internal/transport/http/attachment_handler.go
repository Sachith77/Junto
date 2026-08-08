package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/middleware"
	"github.com/junto/junto/internal/service"
)

// AttachmentHandler exposes the two-phase upload and the link attachment.
//
// # Why uploads are REST-only, while attachment operations arrive over the socket
//
// An upload is not a message. It is a presign, a direct PUT from the browser to object storage,
// and a server-side confirmation — a three-party exchange with a URL in the middle, which a
// WebSocket frame has no way to express. So attachments are WRITTEN here and only BROADCAST
// through the sync engine, which is what "broadcast-only" (D46) means in the most literal
// sense: the engine delivers attachment operations and never accepts them.
//
// The log entry is still what makes them visible to a resyncing client, so this asymmetry costs
// nothing in completeness — a REST-originated attachment reaches an offline member through
// exactly the same replay as a REST-originated day change, which is the whole point of Rule 3.
type AttachmentHandler struct {
	files *service.AttachmentService
	log   *slog.Logger
}

// NewAttachmentHandler builds an AttachmentHandler.
func NewAttachmentHandler(files *service.AttachmentService, log *slog.Logger) *AttachmentHandler {
	if log == nil {
		log = slog.Default()
	}
	return &AttachmentHandler{files: files, log: log}
}

type attachmentResponse struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	Status        string    `json:"status"`
	ExternalURL   string    `json:"external_url,omitempty"`
	ContentType   string    `json:"content_type,omitempty"`
	SizeBytes     *int64    `json:"size_bytes,omitempty"`
	OriginalName  string    `json:"original_name,omitempty"`
	SlotOptionID  *string   `json:"slot_option_id,omitempty"`
	BudgetEntryID *string   `json:"budget_entry_id,omitempty"`
	SlotID        *string   `json:"slot_id,omitempty"`
	UploadedBy    *string   `json:"uploaded_by,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// toAttachmentResponse projects an attachment for the wire.
//
// The storage key is deliberately absent. It is an internal object identifier, not a URL: a
// client that saw it could learn the bucket layout, and it is useless without a signature
// anyway. Reads go through the download endpoint, which mints a short-lived signed URL.
func toAttachmentResponse(a *domain.Attachment) attachmentResponse {
	out := attachmentResponse{
		ID: a.ID.String(), Kind: string(a.Kind), Status: string(a.Status),
		ExternalURL: a.ExternalURL, ContentType: a.ContentType, SizeBytes: a.SizeBytes,
		OriginalName: a.OriginalName, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
	for _, f := range []struct {
		src *domain.ID
		dst **string
	}{
		{a.SlotOptionID, &out.SlotOptionID},
		{a.BudgetEntryID, &out.BudgetEntryID},
		{a.SlotID, &out.SlotID},
		{a.UploadedBy, &out.UploadedBy},
	} {
		if f.src != nil {
			id := f.src.String()
			*f.dst = &id
		}
	}
	return out
}

type ownerRequest struct {
	SlotOptionID  *string `json:"slot_option_id"`
	BudgetEntryID *string `json:"budget_entry_id"`
	SlotID        *string `json:"slot_id"`
}

func (o ownerRequest) toDomain() (domain.AttachmentOwner, error) {
	var owner domain.AttachmentOwner
	for _, f := range []struct {
		raw   *string
		field string
		dst   **domain.ID
	}{
		{o.SlotOptionID, "slot_option_id", &owner.SlotOptionID},
		{o.BudgetEntryID, "budget_entry_id", &owner.BudgetEntryID},
		{o.SlotID, "slot_id", &owner.SlotID},
	} {
		if f.raw == nil || *f.raw == "" {
			continue
		}
		id, err := domain.ParseID(f.field, *f.raw)
		if err != nil {
			return owner, err
		}
		*f.dst = &id
	}
	if !owner.Valid() {
		ve := &domain.ValidationError{}
		ve.Add("owner", "exactly_one_required",
			"name exactly one of: slot_option_id, budget_entry_id, slot_id")
		return owner, ve
	}
	return owner, nil
}

type uploadRequest struct {
	Owner        ownerRequest `json:"owner"`
	ContentType  string       `json:"content_type"`
	OriginalName string       `json:"original_name"`
}

type uploadTicketResponse struct {
	Attachment attachmentResponse `json:"attachment"`
	UploadURL  string             `json:"upload_url"`
	ExpiresAt  time.Time          `json:"expires_at"`
}

type linkRequest struct {
	Owner ownerRequest `json:"owner"`
	URL   string       `json:"url"`
	Title string       `json:"title"`
}

type downloadURLResponse struct {
	URL string `json:"url"`
}

// List returns the attachments on one owning entity.
//
// The owner arrives as query parameters rather than as a nested route, because an attachment
// can hang off three different kinds of entity and three parallel route trees would triple the
// surface to say one thing.
func (h *AttachmentHandler) List(w http.ResponseWriter, r *http.Request) {
	userID, tripID, ok := h.scope(w, r)
	if !ok {
		return
	}

	q := r.URL.Query()
	owner, err := ownerRequest{
		SlotOptionID:  queryPtr(q.Get("slot_option_id")),
		BudgetEntryID: queryPtr(q.Get("budget_entry_id")),
		SlotID:        queryPtr(q.Get("slot_id")),
	}.toDomain()
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	attachments, err := h.files.ListForOwner(r.Context(), tripID, userID, owner)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	out := make([]attachmentResponse, 0, len(attachments))
	for _, a := range attachments {
		out = append(out, toAttachmentResponse(a))
	}
	writeData(w, http.StatusOK, out)
}

// RequestUpload reserves an attachment and returns a presigned PUT.
//
// 201 with a pending attachment: the row exists, and the client's next call is the PUT to
// upload_url followed by the confirm endpoint. Nothing is visible to the rest of the trip until
// that confirmation succeeds.
func (h *AttachmentHandler) RequestUpload(w http.ResponseWriter, r *http.Request) {
	userID, tripID, ok := h.scope(w, r)
	if !ok {
		return
	}

	var body uploadRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}
	owner, err := body.Owner.toDomain()
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	ticket, err := h.files.RequestUpload(r.Context(), tripID, userID, service.RequestUploadInput{
		Owner: owner, ContentType: body.ContentType, OriginalName: body.OriginalName,
	})
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusCreated, uploadTicketResponse{
		Attachment: toAttachmentResponse(ticket.Attachment),
		UploadURL:  ticket.UploadURL,
		ExpiresAt:  ticket.ExpiresAt,
	})
}

// ConfirmUpload verifies the uploaded object and makes the attachment visible.
func (h *AttachmentHandler) ConfirmUpload(w http.ResponseWriter, r *http.Request) {
	userID, tripID, ok := h.scope(w, r)
	if !ok {
		return
	}
	attachmentID, err := pathID(r, "attachmentID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	attachment, err := h.files.ConfirmUpload(r.Context(), tripID, userID, attachmentID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusOK, toAttachmentResponse(attachment))
}

// CreateLink attaches a URL, which is ready and visible immediately.
func (h *AttachmentHandler) CreateLink(w http.ResponseWriter, r *http.Request) {
	userID, tripID, ok := h.scope(w, r)
	if !ok {
		return
	}

	var body linkRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}
	owner, err := body.Owner.toDomain()
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	link, err := h.files.CreateLink(r.Context(), tripID, userID, service.CreateLinkInput{
		Owner: owner, URL: body.URL, Title: body.Title,
	})
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusCreated, toAttachmentResponse(link))
}

// DownloadURL returns a short-lived signed read URL.
//
// A fresh URL per request, never stored and never cached in a response that outlives it — which
// is the same reason it is absent from the operation log: a value with a TTL is false by the
// time anything replays it.
func (h *AttachmentHandler) DownloadURL(w http.ResponseWriter, r *http.Request) {
	userID, tripID, ok := h.scope(w, r)
	if !ok {
		return
	}
	attachmentID, err := pathID(r, "attachmentID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	url, err := h.files.DownloadURL(r.Context(), tripID, userID, attachmentID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusOK, downloadURLResponse{URL: url})
}

// Delete tombstones an attachment. No version: attachments have none (D46).
func (h *AttachmentHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userID, tripID, ok := h.scope(w, r)
	if !ok {
		return
	}
	attachmentID, err := pathID(r, "attachmentID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	if err := h.files.Delete(r.Context(), tripID, userID, attachmentID); err != nil {
		writeError(w, r, err, h.log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AttachmentHandler) scope(w http.ResponseWriter, r *http.Request) (userID, tripID domain.ID, ok bool) {
	userID, found := middleware.UserIDFrom(r.Context())
	if !found {
		writeError(w, r, domain.ErrUnauthenticated, h.log)
		return domain.NilID, domain.NilID, false
	}
	tripID, err := pathID(r, "tripID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return domain.NilID, domain.NilID, false
	}
	return userID, tripID, true
}

// queryPtr converts an absent query parameter to nil, so the owner decoder can treat query and
// body identically.
func queryPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
