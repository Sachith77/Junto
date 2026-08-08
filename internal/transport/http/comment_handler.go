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

// CommentHandler exposes flat, per-slot discussion. Unlike attachments, comments have no
// external dependency (no object storage), so — like votes — these routes mount
// unconditionally rather than being gated behind an optional-subsystem check.
type CommentHandler struct {
	comments *service.CommentService
	log      *slog.Logger
}

// NewCommentHandler builds a CommentHandler.
func NewCommentHandler(comments *service.CommentService, log *slog.Logger) *CommentHandler {
	if log == nil {
		log = slog.Default()
	}
	return &CommentHandler{comments: comments, log: log}
}

type createCommentRequest struct {
	Body string `json:"body"`
}

type commentResponse struct {
	ID        string    `json:"id"`
	SlotID    string    `json:"slot_id"`
	Body      string    `json:"body"`
	AuthorID  *string   `json:"author_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func toCommentResponse(c *domain.Comment) commentResponse {
	out := commentResponse{
		ID: c.ID.String(), SlotID: c.SlotID.String(), Body: c.Body, CreatedAt: c.CreatedAt,
	}
	if c.AuthorID != nil {
		id := c.AuthorID.String()
		out.AuthorID = &id
	}
	return out
}

// Create posts a comment on a slot. The caller is always the author.
func (h *CommentHandler) Create(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFrom(r.Context())
	if !ok {
		writeError(w, r, domain.ErrUnauthenticated, h.log)
		return
	}
	tripID, err := pathID(r, "tripID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	slotID, err := pathID(r, "slotID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	var body createCommentRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}

	comment, err := h.comments.Create(r.Context(), tripID, userID, slotID, service.CreateCommentInput{
		Body: body.Body,
	})
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusCreated, toCommentResponse(comment))
}

// List returns a slot's comments in chronological order.
func (h *CommentHandler) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFrom(r.Context())
	if !ok {
		writeError(w, r, domain.ErrUnauthenticated, h.log)
		return
	}
	tripID, err := pathID(r, "tripID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	slotID, err := pathID(r, "slotID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	comments, err := h.comments.ListForSlot(r.Context(), tripID, userID, slotID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	out := make([]commentResponse, 0, len(comments))
	for _, c := range comments {
		out = append(out, toCommentResponse(c))
	}
	writeData(w, http.StatusOK, out)
}

// Delete removes the caller's OWN comment. No body: comments carry no version (D46-style), and
// the author check happens in the service, not from anything the client supplies.
func (h *CommentHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFrom(r.Context())
	if !ok {
		writeError(w, r, domain.ErrUnauthenticated, h.log)
		return
	}
	tripID, err := pathID(r, "tripID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	commentID, err := pathID(r, "commentID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	if err := h.comments.Delete(r.Context(), tripID, userID, commentID); err != nil {
		writeError(w, r, err, h.log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
