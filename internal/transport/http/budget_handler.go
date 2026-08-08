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

// BudgetHandler exposes the trip ledger.
//
// # Why the edit verb is PUT and not PATCH
//
// PATCH means "apply this partial modification", and a partial modification of a budget entry
// is exactly the operation this system refuses to offer (D44): the entry and its complete split
// set are replaced together or not at all. PUT says that accurately. Every other planning
// resource here is PATCH with a field mask, and the difference between them is the conflict
// grain — which is worth being visible in the route table rather than only in a doc comment.
type BudgetHandler struct {
	budget *service.BudgetService
	log    *slog.Logger
}

// NewBudgetHandler builds a BudgetHandler.
func NewBudgetHandler(budget *service.BudgetService, log *slog.Logger) *BudgetHandler {
	if log == nil {
		log = slog.Default()
	}
	return &BudgetHandler{budget: budget, log: log}
}

type splitRequest struct {
	UserID      string `json:"user_id"`
	AmountMinor int64  `json:"amount_minor"`
}

type budgetEntryRequest struct {
	Label        string         `json:"label"`
	Category     string         `json:"category"`
	AmountMinor  int64          `json:"amount_minor"`
	SlotOptionID *string        `json:"slot_option_id"`
	PaidBy       *string        `json:"paid_by"`
	IncurredOn   *string        `json:"incurred_on"`
	Splits       []splitRequest `json:"splits"`

	// Version is REQUIRED on update and delete (D85). There is no field mask, because there is
	// no partial write to describe.
	Version *int `json:"version"`
}

// toInput converts the wire body, resolving every optional identifier.
func (b budgetEntryRequest) toInput() (service.BudgetEntryInput, error) {
	in := service.BudgetEntryInput{
		Label:       b.Label,
		Category:    domain.BudgetCategory(b.Category),
		AmountMinor: b.AmountMinor,
		Version:     b.Version,
	}

	if b.SlotOptionID != nil && *b.SlotOptionID != "" {
		id, err := domain.ParseID("slot_option_id", *b.SlotOptionID)
		if err != nil {
			return in, err
		}
		in.SlotOptionID = &id
	}
	if b.PaidBy != nil && *b.PaidBy != "" {
		id, err := domain.ParseID("paid_by", *b.PaidBy)
		if err != nil {
			return in, err
		}
		in.PaidBy = &id
	}
	if b.IncurredOn != nil && *b.IncurredOn != "" {
		// Date-only, because a cost is incurred on a day, not at an instant — the same
		// reasoning that keeps slot times as a wall clock rather than a timestamptz (D7).
		day, err := time.Parse(time.DateOnly, *b.IncurredOn)
		if err != nil {
			ve := &domain.ValidationError{}
			ve.Add("incurred_on", "invalid_date", "must be YYYY-MM-DD")
			return in, ve
		}
		in.IncurredOn = &day
	}

	splits := make([]domain.BudgetSplit, 0, len(b.Splits))
	for _, s := range b.Splits {
		userID, err := domain.ParseID("splits.user_id", s.UserID)
		if err != nil {
			return in, err
		}
		splits = append(splits, domain.BudgetSplit{UserID: userID, AmountMinor: s.AmountMinor})
	}
	in.Splits = splits
	return in, nil
}

type splitResponse struct {
	UserID      string `json:"user_id"`
	AmountMinor int64  `json:"amount_minor"`
}

type budgetEntryResponse struct {
	ID           string          `json:"id"`
	Label        string          `json:"label"`
	Category     string          `json:"category"`
	AmountMinor  int64           `json:"amount_minor"`
	SlotOptionID *string         `json:"slot_option_id,omitempty"`
	PaidBy       *string         `json:"paid_by,omitempty"`
	IncurredOn   *string         `json:"incurred_on,omitempty"`
	Splits       []splitResponse `json:"splits"`
	CreatedBy    *string         `json:"created_by,omitempty"`
	Version      int             `json:"version"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

func toBudgetEntryResponse(e *domain.BudgetEntry) budgetEntryResponse {
	out := budgetEntryResponse{
		ID: e.ID.String(), Label: e.Label, Category: string(e.Category),
		AmountMinor: e.AmountMinor, Version: e.Version,
		CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
		// Never nil: an entry nobody has split yet renders as [] rather than null, so a client
		// can iterate it without a special case.
		Splits: make([]splitResponse, 0, len(e.Splits)),
	}
	for _, s := range e.Splits {
		out.Splits = append(out.Splits, splitResponse{
			UserID: s.UserID.String(), AmountMinor: s.AmountMinor,
		})
	}
	if e.SlotOptionID != nil {
		id := e.SlotOptionID.String()
		out.SlotOptionID = &id
	}
	if e.PaidBy != nil {
		id := e.PaidBy.String()
		out.PaidBy = &id
	}
	if e.CreatedBy != nil {
		id := e.CreatedBy.String()
		out.CreatedBy = &id
	}
	if e.IncurredOn != nil {
		day := e.IncurredOn.Format(time.DateOnly)
		out.IncurredOn = &day
	}
	return out
}

// List returns the trip ledger.
func (h *BudgetHandler) List(w http.ResponseWriter, r *http.Request) {
	userID, tripID, ok := h.scope(w, r)
	if !ok {
		return
	}

	entries, err := h.budget.List(r.Context(), tripID, userID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	out := make([]budgetEntryResponse, 0, len(entries))
	for _, e := range entries {
		out = append(out, toBudgetEntryResponse(e))
	}
	writeData(w, http.StatusOK, out)
}

// Get returns one entry with its splits.
func (h *BudgetHandler) Get(w http.ResponseWriter, r *http.Request) {
	userID, tripID, ok := h.scope(w, r)
	if !ok {
		return
	}
	entryID, err := pathID(r, "entryID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	entry, err := h.budget.Get(r.Context(), tripID, userID, entryID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusOK, toBudgetEntryResponse(entry))
}

// Create adds a ledger entry and its splits.
func (h *BudgetHandler) Create(w http.ResponseWriter, r *http.Request) {
	userID, tripID, ok := h.scope(w, r)
	if !ok {
		return
	}

	var body budgetEntryRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}
	in, err := body.toInput()
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	entry, err := h.budget.Create(r.Context(), tripID, userID, in)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusCreated, toBudgetEntryResponse(entry))
}

// Update replaces an entry and its complete split set.
func (h *BudgetHandler) Update(w http.ResponseWriter, r *http.Request) {
	userID, tripID, ok := h.scope(w, r)
	if !ok {
		return
	}
	entryID, err := pathID(r, "entryID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	var body budgetEntryRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}
	in, err := body.toInput()
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	entry, err := h.budget.Update(r.Context(), tripID, userID, entryID, in)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusOK, toBudgetEntryResponse(entry))
}

// Delete tombstones an entry.
func (h *BudgetHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userID, tripID, ok := h.scope(w, r)
	if !ok {
		return
	}
	entryID, err := pathID(r, "entryID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	var body versionRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}

	if err := h.budget.Delete(r.Context(), tripID, userID, entryID, body.Version); err != nil {
		writeError(w, r, err, h.log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// scope resolves the caller and the trip, writing the error response itself when either fails.
func (h *BudgetHandler) scope(w http.ResponseWriter, r *http.Request) (userID, tripID domain.ID, ok bool) {
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
