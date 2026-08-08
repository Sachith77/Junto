package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/middleware"
	"github.com/junto/junto/internal/service"
)

// AuthHandler exposes the authentication endpoints.
//
// Handlers do exactly four things: decode, delegate, render, and set cookies. Any `if` in
// here that is not about the shape of the request is business logic in the wrong layer.
type AuthHandler struct {
	auth   *service.AuthService
	log    *slog.Logger
	cookie CookieConfig
}

// CookieConfig controls how the refresh token is delivered.
type CookieConfig struct {
	Name     string
	Path     string
	Domain   string
	Secure   bool
	SameSite http.SameSite
}

// DefaultCookieConfig returns the refresh-cookie policy.
//
// Three properties, each load-bearing:
//
//   - HttpOnly: JavaScript cannot read it, so an XSS bug cannot exfiltrate the long-lived
//     credential. This is why the refresh token is a cookie while the access token is not.
//   - Path scoped to the auth routes: the browser stops attaching it to every other API call,
//     shrinking where it can leak to.
//   - SameSite=Lax: browsers do not send it on cross-site POSTs, which is the CSRF defence
//     for the refresh endpoint. Strict would break returning to the app from an email link;
//     None would remove the protection entirely.
//
// Secure is configuration rather than a constant only because http://localhost development
// would otherwise never receive the cookie. Production config validation requires HTTPS.
func DefaultCookieConfig(secure bool) CookieConfig {
	return CookieConfig{
		Name:     "junto_refresh",
		Path:     "/api/v1/auth",
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// NewAuthHandler builds an AuthHandler.
func NewAuthHandler(auth *service.AuthService, cookie CookieConfig, log *slog.Logger) *AuthHandler {
	if log == nil {
		log = slog.Default()
	}
	return &AuthHandler{auth: auth, log: log, cookie: cookie}
}

// --- request and response bodies ---
//
// Wire types are declared here, separately from domain types, on purpose. If handlers
// serialised domain structs directly, adding an internal field would silently publish it —
// which is how password hashes end up in API responses.

type signupRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type verifyEmailRequest struct {
	Token string `json:"token"`
}

type requestPasswordResetRequest struct {
	Email string `json:"email"`
}

type resetPasswordRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

type userResponse struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"display_name"`
	VerifiedAt  *time.Time `json:"email_verified_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

// toUserResponse projects a domain user onto the wire.
//
// PasswordHash has no field here and never will. That is the whole reason this projection
// exists rather than tagging the domain struct with json tags.
func toUserResponse(u *domain.User) userResponse {
	return userResponse{
		ID:          u.ID.String(),
		Email:       u.Email,
		DisplayName: u.DisplayName,
		VerifiedAt:  u.EmailVerifiedAt,
		CreatedAt:   u.CreatedAt,
	}
}

type sessionResponse struct {
	AccessToken string       `json:"access_token"`
	ExpiresAt   time.Time    `json:"expires_at"`
	TokenType   string       `json:"token_type"`
	User        userResponse `json:"user"`
}

type authSessionResponse struct {
	ID         string    `json:"id"`
	UserAgent  string    `json:"user_agent"`
	IP         string    `json:"ip,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
	Current    bool      `json:"current"`
}

// --- handlers ---

// Signup creates an account. 201 with the user; no session, because login requires a
// verified email address.
func (h *AuthHandler) Signup(w http.ResponseWriter, r *http.Request) {
	var body signupRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}

	user, err := h.auth.Signup(r.Context(), service.SignupInput{
		Email:       body.Email,
		Password:    body.Password,
		DisplayName: body.DisplayName,
	})
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	writeData(w, http.StatusCreated, toUserResponse(user))
}

// Login authenticates and starts a session.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var body loginRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}

	user, pair, err := h.auth.Login(r.Context(), service.LoginInput{
		Email:    body.Email,
		Password: body.Password,
		Meta: service.RequestMeta{
			UserAgent: r.UserAgent(),
			IP:        clientIP(r),
		},
	})
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	h.setRefreshCookie(w, pair)
	writeData(w, http.StatusOK, sessionResponse{
		AccessToken: pair.AccessToken,
		ExpiresAt:   pair.AccessExpiresAt,
		TokenType:   "Bearer",
		User:        toUserResponse(user),
	})
}

// Refresh rotates the refresh token and issues a new access token.
//
// The refresh token is read ONLY from the cookie, never from the body. Accepting it in a
// body would mean JavaScript had to hold it, which defeats the point of HttpOnly.
func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(h.cookie.Name)
	if err != nil || cookie.Value == "" {
		writeError(w, r, domain.ErrTokenInvalid, h.log)
		return
	}

	pair, err := h.auth.Refresh(r.Context(), cookie.Value, service.RequestMeta{
		UserAgent: r.UserAgent(),
		IP:        clientIP(r),
	})
	if err != nil {
		// Clear the cookie on any failure. The browser is holding a token that is either
		// invalid or has just triggered a family-wide revocation; leaving it in place makes
		// the client retry forever against a dead session.
		h.clearRefreshCookie(w)
		writeError(w, r, err, h.log)
		return
	}

	h.setRefreshCookie(w, pair)
	writeData(w, http.StatusOK, map[string]any{
		"access_token": pair.AccessToken,
		"expires_at":   pair.AccessExpiresAt,
		"token_type":   "Bearer",
	})
}

// Logout revokes the current session.
//
// Always 204, even for an unknown or already-dead token. A logout that can fail gives a
// caller information about a token they may not own, and there is nothing a client would
// usefully do differently.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(h.cookie.Name); err == nil && cookie.Value != "" {
		if err := h.auth.Logout(r.Context(), cookie.Value); err != nil {
			h.log.ErrorContext(r.Context(), "logout failed", "error", err)
		}
	}
	h.clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// VerifyEmail consumes a verification token.
func (h *AuthHandler) VerifyEmail(w http.ResponseWriter, r *http.Request) {
	var body verifyEmailRequest
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

	if err := h.auth.VerifyEmail(r.Context(), body.Token); err != nil {
		writeError(w, r, err, h.log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RequestPasswordReset issues a reset link.
//
// Always 202, whether or not the address exists. Anything else turns this endpoint into a
// free account-enumeration oracle for anyone holding a list of email addresses.
func (h *AuthHandler) RequestPasswordReset(w http.ResponseWriter, r *http.Request) {
	var body requestPasswordResetRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeRequestError(w, r, err, h.log)
		return
	}

	if err := h.auth.RequestPasswordReset(r.Context(), body.Email); err != nil {
		// A malformed address is still reported: that is a client bug, not an account
		// disclosure. Everything else is swallowed by the service itself.
		writeError(w, r, err, h.log)
		return
	}
	writeJSON(w, http.StatusAccepted, Envelope{Data: map[string]string{
		"message": "If that address has an account, a reset link is on its way.",
	}})
}

// ResetPassword consumes a reset token and sets a new password.
func (h *AuthHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var body resetPasswordRequest
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

	if err := h.auth.ResetPassword(r.Context(), body.Token, body.Password); err != nil {
		writeError(w, r, err, h.log)
		return
	}

	// Every session was just revoked, including this browser's. Clearing the cookie keeps
	// the client's state consistent with the server's instead of leaving it to discover the
	// revocation on its next refresh.
	h.clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// Me returns the authenticated user.
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	claims, ok := middleware.ClaimsFrom(r.Context())
	if !ok {
		writeError(w, r, domain.ErrUnauthenticated, h.log)
		return
	}

	user, err := h.auth.User(r.Context(), claims.UserID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}
	writeData(w, http.StatusOK, toUserResponse(user))
}

// ListSessions returns the caller's active sessions for a device-management screen.
func (h *AuthHandler) ListSessions(w http.ResponseWriter, r *http.Request) {
	claims, ok := middleware.ClaimsFrom(r.Context())
	if !ok {
		writeError(w, r, domain.ErrUnauthenticated, h.log)
		return
	}

	sessions, err := h.auth.ListSessions(r.Context(), claims.UserID)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	out := make([]authSessionResponse, 0, len(sessions))
	for _, s := range sessions {
		resp := authSessionResponse{
			ID:         s.ID.String(),
			UserAgent:  s.UserAgent,
			CreatedAt:  s.CreatedAt,
			LastUsedAt: s.LastUsedAt,
			// Marking the current session lets a UI say "this device" and avoid
			// accidentally revoking the session the user is looking at.
			Current: s.ID == claims.SessionID,
		}
		if s.IP != nil {
			resp.IP = s.IP.String()
		}
		out = append(out, resp)
	}
	writeData(w, http.StatusOK, out)
}

// RevokeSession ends one of the caller's sessions.
func (h *AuthHandler) RevokeSession(w http.ResponseWriter, r *http.Request, sessionIDParam string) {
	claims, ok := middleware.ClaimsFrom(r.Context())
	if !ok {
		writeError(w, r, domain.ErrUnauthenticated, h.log)
		return
	}

	sessionID, err := domain.ParseID("session_id", sessionIDParam)
	if err != nil {
		writeError(w, r, err, h.log)
		return
	}

	if err := h.auth.RevokeSession(r.Context(), claims.UserID, sessionID); err != nil {
		writeError(w, r, err, h.log)
		return
	}
	if sessionID == claims.SessionID {
		h.clearRefreshCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- cookies ---

func (h *AuthHandler) setRefreshCookie(w http.ResponseWriter, pair *service.TokenPair) {
	http.SetCookie(w, &http.Cookie{
		Name:     h.cookie.Name,
		Value:    pair.RefreshToken,
		Path:     h.cookie.Path,
		Domain:   h.cookie.Domain,
		Expires:  pair.RefreshExpiresAt,
		MaxAge:   int(time.Until(pair.RefreshExpiresAt).Seconds()),
		HttpOnly: true,
		Secure:   h.cookie.Secure,
		SameSite: h.cookie.SameSite,
	})
}

func (h *AuthHandler) clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     h.cookie.Name,
		Value:    "",
		Path:     h.cookie.Path,
		Domain:   h.cookie.Domain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.cookie.Secure,
		SameSite: h.cookie.SameSite,
	})
}
