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

// TripHandler exposes trip CRUD and listing.
type TripHandler struct {
	trips *service.TripService
	log   *slog.Logger
}

// NewTripHandler builds a TripHandler.
func NewTripHandler(trips *service.TripService, log *slog.Logger) *TripHandler {
	if log == nil {
		log = slog.Default()
	}
	return &TripHandler{trips: trips, log: log}
}

// Wire types are declared separately from domain types (D37): serialising a domain struct
// directly means adding an internal field silently publishes it.

type tripRequest struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	TimeZone    string     `json:"time_zone"`
	StartDate   *time.Time `json:"start_date"`
	EndDate     *time.Time `json:"end_date"`
	Version     int        `json:"version"`
}

type tripResponse struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	TimeZone    string     `json:"time_zone"`
	StartDate   *time.Time `json:"start_date"`
	EndDate     *time.Time `json:"end_date"`
	Version     int        `json:"version"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func toTripResponse(t *domain.Trip) tripResponse {
	return tripResponse{
		ID: t.ID.String(), Name: t.Name, Description: t.Description, TimeZone: t.TimeZone,
		StartDate: t.StartDate, EndDate: t.EndDate, Version: t.Version,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

// Create makes a trip. The caller becomes its owner.
func (h *TripHandler) Create(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFrom(r.Context())
	if !ok {
		writeError(w, r, domain.ErrUnauthenticated, h.log)
		return
	}

	var body tripRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}

	trip, err := h.trips.Create(r.Context(), userID, service.CreateTripInput{
		Name: body.Name, Description: body.Description, TimeZone: body.TimeZone,
		StartDate: body.StartDate, EndDate: body.EndDate,
	})
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusCreated, toTripResponse(trip))
}

// List returns the caller's trips, keyset paginated.
func (h *TripHandler) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFrom(r.Context())
	if !ok {
		writeError(w, r, domain.ErrUnauthenticated, h.log)
		return
	}
	page, err := queryPage(r)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	result, err := h.trips.ListForUser(r.Context(), userID, page)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	out := make([]tripResponse, 0, len(result.Items))
	for _, t := range result.Items {
		out = append(out, toTripResponse(t))
	}
	writePage(w, http.StatusOK, out, result.NextCursor, result.HasMore)
}

// Get returns one trip.
func (h *TripHandler) Get(w http.ResponseWriter, r *http.Request) {
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

	trip, err := h.trips.Get(r.Context(), tripID, userID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusOK, toTripResponse(trip))
}

// Update edits trip content.
func (h *TripHandler) Update(w http.ResponseWriter, r *http.Request) {
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

	var body tripRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}

	trip, err := h.trips.Update(r.Context(), tripID, userID, service.UpdateTripInput{
		Name: body.Name, Description: body.Description, TimeZone: body.TimeZone,
		StartDate: body.StartDate, EndDate: body.EndDate, Version: body.Version,
	})
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusOK, toTripResponse(trip))
}

// versionRequest carries an optional optimistic-concurrency precondition.
//
// Version is a pointer because absence is meaningful (D69): omitting it asks for merge
// semantics — no precondition, conflicts resolved by the sync engine's field-level merge —
// while supplying one asks for a 409 on mismatch. Conflict semantics are a property of the
// REQUEST, not of the transport, so a REST client gets the same choice a WebSocket client has.
//
// Trips and memberships are the exception: they are not sync-managed (D72), so there is no
// merge path for them and requireVersion below rejects a missing version rather than silently
// giving them semantics the sync engine is not providing.
type versionRequest struct {
	Version *int `json:"version"`
}

// requireVersion enforces a mandatory precondition for the entities that have no merge path.
func requireVersion(v *int) (int, error) {
	if v == nil {
		ve := &domain.ValidationError{}
		ve.Add("version", "required", "version is required for this resource")
		return 0, ve
	}
	return *v, nil
}

// Delete soft-deletes a trip.
func (h *TripHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
	var body versionRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}

	version, err := requireVersion(body.Version)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	if err := h.trips.Delete(r.Context(), tripID, userID, version); err != nil {
		writeError(w, r, err, h.log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
