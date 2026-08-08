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

// SlotOptionHandler exposes candidate-proposal CRUD under a slot.
type SlotOptionHandler struct {
	options *service.SlotOptionService
	log     *slog.Logger
}

// NewSlotOptionHandler builds a SlotOptionHandler.
func NewSlotOptionHandler(options *service.SlotOptionService, log *slog.Logger) *SlotOptionHandler {
	if log == nil {
		log = slog.Default()
	}
	return &SlotOptionHandler{options: options, log: log}
}

type placeRequest struct {
	Name       string   `json:"name"`
	Address    string   `json:"address"`
	Lat        *float64 `json:"lat"`
	Lng        *float64 `json:"lng"`
	ProviderID string   `json:"provider_id"`
}

func (p placeRequest) toDomain() domain.Place {
	return domain.Place{Name: p.Name, Address: p.Address, Lat: p.Lat, Lng: p.Lng, ProviderID: p.ProviderID}
}

type optionRequest struct {
	Title              string       `json:"title"`
	Notes              string       `json:"notes"`
	ExternalURL        string       `json:"external_url"`
	EstimatedCostMinor *int64       `json:"estimated_cost_minor"`
	Place              placeRequest `json:"place"`
	// Fields is the optional edit mask (D64); Version nil asks for merge semantics (D69).
	Fields  []string `json:"fields"`
	Version *int     `json:"version"`
}

type placeResponse struct {
	Name       string   `json:"name"`
	Address    string   `json:"address"`
	Lat        *float64 `json:"lat,omitempty"`
	Lng        *float64 `json:"lng,omitempty"`
	ProviderID string   `json:"provider_id,omitempty"`
}

type optionResponse struct {
	ID                 string        `json:"id"`
	SlotID             string        `json:"slot_id"`
	Title              string        `json:"title"`
	Notes              string        `json:"notes"`
	ExternalURL        string        `json:"external_url"`
	EstimatedCostMinor *int64        `json:"estimated_cost_minor,omitempty"`
	Place              placeResponse `json:"place"`
	ProposedBy         *string       `json:"proposed_by,omitempty"`
	Version            int           `json:"version"`
	CreatedAt          time.Time     `json:"created_at"`
	UpdatedAt          time.Time     `json:"updated_at"`
}

func toOptionResponse(o *domain.SlotOption) optionResponse {
	out := optionResponse{
		ID: o.ID.String(), SlotID: o.SlotID.String(), Title: o.Title, Notes: o.Notes,
		ExternalURL: o.ExternalURL, EstimatedCostMinor: o.EstimatedCostMinor,
		Place: placeResponse{
			Name: o.Place.Name, Address: o.Place.Address,
			Lat: o.Place.Lat, Lng: o.Place.Lng, ProviderID: o.Place.ProviderID,
		},
		Version: o.Version, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt,
	}
	if o.ProposedBy != nil {
		id := o.ProposedBy.String()
		out.ProposedBy = &id
	}
	return out
}

// List returns a slot's candidates.
func (h *SlotOptionHandler) List(w http.ResponseWriter, r *http.Request) {
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

	options, err := h.options.ListForSlot(r.Context(), tripID, userID, slotID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	out := make([]optionResponse, 0, len(options))
	for _, o := range options {
		out = append(out, toOptionResponse(o))
	}
	writeData(w, http.StatusOK, out)
}

// Create proposes a new candidate.
func (h *SlotOptionHandler) Create(w http.ResponseWriter, r *http.Request) {
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

	var body optionRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}

	option, err := h.options.Create(r.Context(), tripID, userID, slotID, service.CreateOptionInput{
		Title: body.Title, Notes: body.Notes, ExternalURL: body.ExternalURL,
		EstimatedCostMinor: body.EstimatedCostMinor, Place: body.Place.toDomain(),
	})
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusCreated, toOptionResponse(option))
}

// Update edits a candidate's content.
func (h *SlotOptionHandler) Update(w http.ResponseWriter, r *http.Request) {
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
	optionID, err := pathID(r, "optionID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	var body optionRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}

	option, err := h.options.Update(r.Context(), tripID, userID, optionID, service.UpdateOptionInput{
		Fields: domain.NewFieldMask(body.Fields...),
		Title:  body.Title, Notes: body.Notes, ExternalURL: body.ExternalURL,
		EstimatedCostMinor: body.EstimatedCostMinor, Place: body.Place.toDomain(),
		Version: body.Version,
	})
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusOK, toOptionResponse(option))
}

// Delete withdraws a candidate. If it was the slot's selection, the slot's selection is
// cleared in the same transaction — see SlotOptionService.Delete.
func (h *SlotOptionHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
	optionID, err := pathID(r, "optionID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	var body versionRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}

	if err := h.options.Delete(r.Context(), tripID, userID, optionID, body.Version); err != nil {
		writeError(w, r, err, h.log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
