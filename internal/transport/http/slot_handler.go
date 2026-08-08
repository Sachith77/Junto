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

// SlotHandler exposes slot CRUD, placement, resolution and coverage.
type SlotHandler struct {
	slots *service.SlotService
	log   *slog.Logger
}

// NewSlotHandler builds a SlotHandler.
func NewSlotHandler(slots *service.SlotService, log *slog.Logger) *SlotHandler {
	if log == nil {
		log = slog.Default()
	}
	return &SlotHandler{slots: slots, log: log}
}

type slotRequest struct {
	DayID       *string `json:"day_id"`
	Kind        string  `json:"kind"`
	Title       string  `json:"title"`
	Notes       string  `json:"notes"`
	StartTime   *string `json:"start_time"`
	EndTime     *string `json:"end_time"`
	AfterSlotID *string `json:"after_slot_id"`

	// Fields is the optional edit mask (D64). Omitted means "replace every editable field",
	// which is what a whole-object PATCH has always meant here. Naming fields explicitly
	// makes a REST edit merge with a concurrent edit to a different field, exactly as a
	// WebSocket one does — the granularity, like the version precondition below, belongs to
	// the request rather than to the transport.
	Fields []string `json:"fields"`

	// Version nil asks for merge semantics; a value asks for a 409 on mismatch (D69).
	Version *int `json:"version"`
}

type slotResponse struct {
	ID               string     `json:"id"`
	DayID            *string    `json:"day_id,omitempty"`
	Kind             string     `json:"kind"`
	Title            string     `json:"title"`
	Notes            string     `json:"notes"`
	StartTime        *string    `json:"start_time,omitempty"`
	EndTime          *string    `json:"end_time,omitempty"`
	Position         string     `json:"position"`
	SelectedOptionID *string    `json:"selected_option_id,omitempty"`
	Status           string     `json:"status"`
	StatusChangedAt  *time.Time `json:"status_changed_at,omitempty"`
	StatusChangedBy  *string    `json:"status_changed_by,omitempty"`
	Version          int        `json:"version"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

func toSlotResponse(s *domain.Slot) slotResponse {
	out := slotResponse{
		ID: s.ID.String(), Kind: string(s.Kind), Title: s.Title, Notes: s.Notes,
		Position: s.Position, Status: string(s.Status), Version: s.Version,
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, StatusChangedAt: s.StatusChangedAt,
	}
	if s.DayID != nil {
		id := s.DayID.String()
		out.DayID = &id
	}
	if s.StartTime != nil {
		v := s.StartTime.String()
		out.StartTime = &v
	}
	if s.EndTime != nil {
		v := s.EndTime.String()
		out.EndTime = &v
	}
	if s.SelectedOptionID != nil {
		id := s.SelectedOptionID.String()
		out.SelectedOptionID = &id
	}
	if s.StatusChangedBy != nil {
		id := s.StatusChangedBy.String()
		out.StatusChangedBy = &id
	}
	return out
}

// parseOptionalTime parses an "HH:MM" wire value, or returns nil for an absent one.
func parseOptionalTime(field string, raw *string) (*domain.TimeOfDay, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	t, err := domain.ParseTimeOfDay(*raw)
	if err != nil {
		ve := &domain.ValidationError{}
		ve.Add(field, "invalid_time", "must be HH:MM")
		return nil, ve
	}
	return &t, nil
}

// List returns every live slot in a trip. Clients group by day_id and sort by position.
func (h *SlotHandler) List(w http.ResponseWriter, r *http.Request) {
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

	slots, err := h.slots.ListForTrip(r.Context(), tripID, userID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	out := make([]slotResponse, 0, len(slots))
	for _, s := range slots {
		out = append(out, toSlotResponse(s))
	}
	writeData(w, http.StatusOK, out)
}

// Get returns one slot.
func (h *SlotHandler) Get(w http.ResponseWriter, r *http.Request) {
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

	slot, err := h.slots.Get(r.Context(), tripID, userID, slotID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusOK, toSlotResponse(slot))
}

// Create adds a slot to a day or the trip backlog.
func (h *SlotHandler) Create(w http.ResponseWriter, r *http.Request) {
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

	var body slotRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}
	dayID, err := parseOptionalID("day_id", body.DayID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	afterID, err := parseOptionalID("after_slot_id", body.AfterSlotID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	start, err := parseOptionalTime("start_time", body.StartTime)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	end, err := parseOptionalTime("end_time", body.EndTime)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	slot, err := h.slots.Create(r.Context(), tripID, userID, service.CreateSlotInput{
		DayID: dayID, Kind: domain.SlotKind(body.Kind), Title: body.Title, Notes: body.Notes,
		StartTime: start, EndTime: end, AfterSlotID: afterID,
	})
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusCreated, toSlotResponse(slot))
}

// Update edits a slot's content.
func (h *SlotHandler) Update(w http.ResponseWriter, r *http.Request) {
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

	var body slotRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}
	start, err := parseOptionalTime("start_time", body.StartTime)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	end, err := parseOptionalTime("end_time", body.EndTime)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	slot, err := h.slots.Update(r.Context(), tripID, userID, slotID, service.UpdateSlotInput{
		Fields: domain.NewFieldMask(body.Fields...),
		Kind:   domain.SlotKind(body.Kind), Title: body.Title, Notes: body.Notes,
		StartTime: start, EndTime: end, Version: body.Version,
	})
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusOK, toSlotResponse(slot))
}

// Move reorders a slot, optionally onto a different day.
func (h *SlotHandler) Move(w http.ResponseWriter, r *http.Request) {
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

	var body moveRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}
	dayID, err := parseOptionalID("day_id", body.DayID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	afterID, err := parseOptionalID("after_id", body.AfterID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	if err := h.slots.Move(r.Context(), tripID, userID, slotID, dayID, afterID, body.Version); err != nil {
		writeError(w, r, err, h.log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type selectOptionRequest struct {
	OptionID *string `json:"option_id"`
	Version  *int    `json:"version"`
}

// SetSelectedOption records (or clears) the group's resolution.
func (h *SlotHandler) SetSelectedOption(w http.ResponseWriter, r *http.Request) {
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

	var body selectOptionRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}
	optionID, err := parseOptionalID("option_id", body.OptionID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	if err := h.slots.SetSelectedOption(r.Context(), tripID, userID, slotID, optionID, body.Version); err != nil {
		writeError(w, r, err, h.log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type setStatusRequest struct {
	Status  string `json:"status"`
	Version *int   `json:"version"`
}

// SetStatus records Live-mode coverage.
func (h *SlotHandler) SetStatus(w http.ResponseWriter, r *http.Request) {
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

	var body setStatusRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}

	if err := h.slots.SetStatus(r.Context(), tripID, userID, slotID, domain.SlotStatus(body.Status), body.Version); err != nil {
		writeError(w, r, err, h.log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Delete soft-deletes a slot.
func (h *SlotHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
	var body versionRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}

	if err := h.slots.Delete(r.Context(), tripID, userID, slotID, body.Version); err != nil {
		writeError(w, r, err, h.log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
