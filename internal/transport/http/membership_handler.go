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

// MembershipHandler exposes member management and invitations.
type MembershipHandler struct {
	members *service.MembershipService
	log     *slog.Logger
}

// NewMembershipHandler builds a MembershipHandler.
func NewMembershipHandler(members *service.MembershipService, log *slog.Logger) *MembershipHandler {
	if log == nil {
		log = slog.Default()
	}
	return &MembershipHandler{members: members, log: log}
}

type memberResponse struct {
	UserID      string    `json:"user_id"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	InvitedBy   *string   `json:"invited_by,omitempty"`
	JoinedAt    time.Time `json:"joined_at"`
	Version     int       `json:"version"`
}

func toMemberResponse(m *domain.Member) memberResponse {
	out := memberResponse{
		UserID: m.UserID.String(), Role: string(m.Role), JoinedAt: m.JoinedAt, Version: m.Version,
	}
	if m.InvitedBy != nil {
		id := m.InvitedBy.String()
		out.InvitedBy = &id
	}
	return out
}

func toMemberProfileResponse(m *domain.MemberProfile) memberResponse {
	out := toMemberResponse(&m.Member)
	out.DisplayName = m.DisplayName
	return out
}

// ListMembers returns a trip's roster.
func (h *MembershipHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
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

	members, err := h.members.ListMemberProfiles(r.Context(), tripID, userID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	out := make([]memberResponse, 0, len(members))
	for _, m := range members {
		out = append(out, toMemberProfileResponse(m))
	}
	writeData(w, http.StatusOK, out)
}

type updateRoleRequest struct {
	Role    string `json:"role"`
	Version int    `json:"version"`
}

// UpdateMemberRole changes a member's role.
func (h *MembershipHandler) UpdateMemberRole(w http.ResponseWriter, r *http.Request) {
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
	targetID, err := pathID(r, "userID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	var body updateRoleRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}

	member, err := h.members.UpdateRole(r.Context(), tripID, userID, targetID, domain.Role(body.Role), body.Version)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusOK, toMemberResponse(member))
}

// RemoveMember removes a member from a trip.
func (h *MembershipHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
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
	targetID, err := pathID(r, "userID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	if err := h.members.RemoveMember(r.Context(), tripID, userID, targetID); err != nil {
		writeError(w, r, err, h.log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type createInvitationRequest struct {
	Email   *string `json:"email"`
	Role    string  `json:"role"`
	MaxUses *int    `json:"max_uses"`
}

type invitationResponse struct {
	ID        string     `json:"id"`
	Email     *string    `json:"email,omitempty"`
	Role      string     `json:"role"`
	MaxUses   *int       `json:"max_uses,omitempty"`
	UseCount  int        `json:"use_count"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// createInvitationResponse is the create-only projection. It carries one field the list
// projection must never carry, which is the whole reason it is a separate type rather than
// an omitempty on the shared one — a future field added to the wrong struct would publish
// live invite links to everyone who can list them.
type createInvitationResponse struct {
	invitationResponse

	// AcceptURL is present for link invites only, and only here. It is not recoverable
	// afterwards (only the hash is stored), so a client that means to show it must show it
	// on this response.
	AcceptURL string `json:"accept_url,omitempty"`
}

func toInvitationResponse(inv *domain.Invitation) invitationResponse {
	// Deliberately no token or token hash in this projection: the raw token is shown once,
	// in the email or the create response below, and never again. The hash is a database
	// implementation detail with no legitimate use on the wire.
	return invitationResponse{
		ID: inv.ID.String(), Email: inv.Email, Role: string(inv.Role),
		MaxUses: inv.MaxUses, UseCount: inv.UseCount,
		ExpiresAt: inv.ExpiresAt, RevokedAt: inv.RevokedAt, CreatedAt: inv.CreatedAt,
	}
}

// CreateInvitation issues an invitation. For a link invite (no email), the raw token is
// returned once in the response, since there is no inbox to deliver it to instead.
func (h *MembershipHandler) CreateInvitation(w http.ResponseWriter, r *http.Request) {
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

	var body createInvitationRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}

	maxUses := body.MaxUses
	if body.Email != nil && maxUses == nil {
		// Email invites default to a single use; the address was given a specific
		// destination. Link invites default to unlimited (nil), for sharing in a group chat.
		one := 1
		maxUses = &one
	}

	created, err := h.members.CreateInvitation(r.Context(), tripID, userID, service.CreateInvitationInput{
		Email: body.Email, Role: domain.Role(body.Role), MaxUses: maxUses,
	})
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusCreated, createInvitationResponse{
		invitationResponse: toInvitationResponse(created.Invitation),
		AcceptURL:          created.AcceptURL,
	})
}

// ListInvitations returns a trip's outstanding invitations.
func (h *MembershipHandler) ListInvitations(w http.ResponseWriter, r *http.Request) {
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

	invitations, err := h.members.ListInvitations(r.Context(), tripID, userID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	out := make([]invitationResponse, 0, len(invitations))
	for _, inv := range invitations {
		out = append(out, toInvitationResponse(inv))
	}
	writeData(w, http.StatusOK, out)
}

// RevokeInvitation ends an invitation early.
func (h *MembershipHandler) RevokeInvitation(w http.ResponseWriter, r *http.Request) {
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
	invID, err := pathID(r, "invitationID", chi.URLParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	if err := h.members.RevokeInvitation(r.Context(), tripID, userID, invID); err != nil {
		writeError(w, r, err, h.log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type redeemInvitationRequest struct {
	Token string `json:"token"`
}

// AcceptInvitation redeems an invitation link for the authenticated caller.
func (h *MembershipHandler) AcceptInvitation(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFrom(r.Context())
	if !ok {
		writeError(w, r, domain.ErrUnauthenticated, h.log)
		return
	}

	var body redeemInvitationRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}
	if body.Token == "" {
		ve := &domain.ValidationError{}
		ve.Add("token", "required", "token is required")
		writeError(w, r, ve, h.log)
		return
	}

	trip, err := h.members.RedeemInvitation(r.Context(), userID, body.Token)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusOK, toTripResponse(trip))
}
