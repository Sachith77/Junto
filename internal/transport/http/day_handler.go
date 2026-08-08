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

// DayHandler exposes day CRUD and reordering.
type DayHandler struct {
	days *service.DayService
	log  *slog.Logger
}

// NewDayHandler builds a DayHandler.
func NewDayHandler(days *service.DayService, log *slog.Logger) *DayHandler {
	if log == nil {
		log = slog.Default()
	}
	return &DayHandler{days: days, log: log}
}

type dayRequest struct {
	Date       *time.Time `json:"date"`
	Label      string     `json:"label"`
	AfterDayID *string    `json:"after_day_id"`
	// Fields is the optional edit mask (D64); Version nil asks for merge semantics (D69).
	Fields  []string `json:"fields"`
	Version *int     `json:"version"`
}

type dayResponse struct {
	ID        string     `json:"id"`
	Date      *time.Time `json:"date"`
	Label     string     `json:"label"`
	Position  string     `json:"position"`
	Version   int        `json:"version"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func toDayResponse(d *domain.Day) dayResponse {
	return dayResponse{
		ID: d.ID.String(), Date: d.Date, Label: d.Label, Position: d.Position,
		Version: d.Version, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

// parseOptionalID parses a nullable UUID path/body reference. An empty pointer or empty
// string both mean "not supplied".
func parseOptionalID(field string, raw *string) (*domain.ID, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	id, err := domain.ParseID(field, *raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// List returns a trip's days in order.
func (h *DayHandler) List(w http.ResponseWriter, r *http.Request) {
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

	days, err := h.days.ListForTrip(r.Context(), tripID, userID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	out := make([]dayResponse, 0, len(days))
	for _, d := range days {
		out = append(out, toDayResponse(d))
	}
	writeData(w, http.StatusOK, out)
}

// Create appends or inserts a day.
func (h *DayHandler) Create(w http.ResponseWriter, r *http.Request) {
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

	var body dayRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}
	afterID, err := parseOptionalID("after_day_id", body.AfterDayID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	day, err := h.days.Create(r.Context(), tripID, userID, service.CreateDayInput{
		Date: body.Date, Label: body.Label, AfterDayID: afterID,
	})
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusCreated, toDayResponse(day))
}

// Update edits a day's date and label.
func (h *DayHandler) Update(w http.ResponseWriter, r *http.Request) {
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
	dayID, err := pathID(r, "dayID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	var body dayRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}

	day, err := h.days.Update(r.Context(), tripID, userID, dayID, service.UpdateDayInput{
		Fields: domain.NewFieldMask(body.Fields...),
		Date:   body.Date, Label: body.Label, Version: body.Version,
	})
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusOK, toDayResponse(day))
}

type moveRequest struct {
	// AfterID is nil to move to the start of the bucket.
	AfterID *string `json:"after_id"`
	// DayID is used only by the slot Move handler, which also supports changing bucket.
	DayID *string `json:"day_id"`
	// Version nil asks for merge semantics; a value asks for a 409 on mismatch (D69).
	Version *int `json:"version"`
}

// Move reorders a day.
func (h *DayHandler) Move(w http.ResponseWriter, r *http.Request) {
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
	dayID, err := pathID(r, "dayID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	var body moveRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}
	afterID, err := parseOptionalID("after_id", body.AfterID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	if err := h.days.Move(r.Context(), tripID, userID, dayID, afterID, body.Version); err != nil {
		writeError(w, r, err, h.log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Delete soft-deletes a day.
func (h *DayHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
	dayID, err := pathID(r, "dayID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	var body versionRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}

	if err := h.days.Delete(r.Context(), tripID, userID, dayID, body.Version); err != nil {
		writeError(w, r, err, h.log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
