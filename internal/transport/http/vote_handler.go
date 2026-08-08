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

// VoteHandler exposes voting on slot options.
type VoteHandler struct {
	votes *service.VoteService
	log   *slog.Logger
}

// NewVoteHandler builds a VoteHandler.
func NewVoteHandler(votes *service.VoteService, log *slog.Logger) *VoteHandler {
	if log == nil {
		log = slog.Default()
	}
	return &VoteHandler{votes: votes, log: log}
}

type castVoteRequest struct {
	// OptionID nil retracts the caller's vote.
	OptionID *string `json:"option_id"`
}

type voteResponse struct {
	UserID    string    `json:"user_id"`
	OptionID  *string   `json:"option_id,omitempty"`
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toVoteResponse(v *domain.Vote) voteResponse {
	out := voteResponse{UserID: v.UserID.String(), Version: v.Version, UpdatedAt: v.UpdatedAt}
	if v.OptionID != nil {
		id := v.OptionID.String()
		out.OptionID = &id
	}
	return out
}

type tallyResponse struct {
	OptionID string `json:"option_id"`
	Count    int    `json:"count"`
}

// Cast records or changes the caller's own vote on a slot.
func (h *VoteHandler) Cast(w http.ResponseWriter, r *http.Request) {
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

	var body castVoteRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}
	optionID, err := parseOptionalID("option_id", body.OptionID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	vote, err := h.votes.Cast(r.Context(), tripID, userID, slotID, optionID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusOK, toVoteResponse(vote))
}

// List returns every member's vote on a slot.
func (h *VoteHandler) List(w http.ResponseWriter, r *http.Request) {
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

	votes, err := h.votes.ListForSlot(r.Context(), tripID, userID, slotID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	out := make([]voteResponse, 0, len(votes))
	for _, v := range votes {
		out = append(out, toVoteResponse(v))
	}
	writeData(w, http.StatusOK, out)
}

// Tally returns per-option vote counts for a slot.
func (h *VoteHandler) Tally(w http.ResponseWriter, r *http.Request) {
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

	tallies, err := h.votes.Tally(r.Context(), tripID, userID, slotID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	out := make([]tallyResponse, 0, len(tallies))
	for _, t := range tallies {
		out = append(out, tallyResponse{OptionID: t.OptionID.String(), Count: t.Count})
	}
	writeData(w, http.StatusOK, out)
}
