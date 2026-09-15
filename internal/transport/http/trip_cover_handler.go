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

type TripCoverHandler struct {
	covers *service.TripCoverService
	log    *slog.Logger
}

func NewTripCoverHandler(covers *service.TripCoverService, log *slog.Logger) *TripCoverHandler {
	if log == nil {
		log = slog.Default()
	}
	return &TripCoverHandler{covers: covers, log: log}
}

type coverSuggestionResponse struct {
	ID               string `json:"id,omitempty"`
	Source           string `json:"source"`
	URL              string `json:"url,omitempty"`
	PhotographerName string `json:"photographer_name,omitempty"`
	PhotographerURL  string `json:"photographer_url,omitempty"`
	PhotoURL         string `json:"photo_url,omitempty"`
}

func toCoverSuggestionResponse(s *domain.TripCoverSuggestion) coverSuggestionResponse {
	out := coverSuggestionResponse{Source: "neutral"}
	if s.ID != domain.NilID {
		out.ID = s.ID.String()
	}
	if s.Found {
		out.Source = "suggested"
		out.URL = s.ImageURL
		out.PhotographerName = s.PhotographerName
		out.PhotographerURL = s.PhotographerURL
		out.PhotoURL = s.PhotoURL
	}
	return out
}

func (h *TripCoverHandler) Suggest(w http.ResponseWriter, r *http.Request) {
	if _, ok := middleware.UserIDFrom(r.Context()); !ok {
		writeError(w, r, domain.ErrUnauthenticated, h.log)
		return
	}
	suggestion, err := h.covers.Suggest(r.Context(), r.URL.Query().Get("query"))
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusOK, toCoverSuggestionResponse(suggestion))
}

type coverUploadRequest struct {
	ContentType  string `json:"content_type"`
	OriginalName string `json:"original_name"`
}

type coverUploadResponse struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	UploadURL string    `json:"upload_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (h *TripCoverHandler) RequestUpload(w http.ResponseWriter, r *http.Request) {
	userID, tripID, ok := h.scope(w, r)
	if !ok {
		return
	}
	var body coverUploadRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}
	ticket, err := h.covers.RequestUpload(r.Context(), tripID, userID, body.ContentType, body.OriginalName)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusCreated, coverUploadResponse{ID: ticket.Upload.ID.String(), Status: string(ticket.Upload.Status), UploadURL: ticket.UploadURL, ExpiresAt: ticket.ExpiresAt})
}

func (h *TripCoverHandler) ConfirmUpload(w http.ResponseWriter, r *http.Request) {
	userID, tripID, ok := h.scope(w, r)
	if !ok {
		return
	}
	uploadID, err := pathID(r, "uploadID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	trip, err := h.covers.ConfirmUpload(r.Context(), tripID, userID, uploadID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusOK, toTripResponse(trip))
}

func (h *TripCoverHandler) scope(w http.ResponseWriter, r *http.Request) (domain.ID, domain.ID, bool) {
	userID, ok := middleware.UserIDFrom(r.Context())
	if !ok {
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
