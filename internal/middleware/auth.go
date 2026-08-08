package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/junto/junto/internal/domain"
)

// Authenticator verifies an access token and attaches its claims to the request context.
type Authenticator interface {
	// Authenticate verifies the token AND that its session is still live. The session check
	// is what makes logout effective before the token's own expiry.
	Authenticate(ctx context.Context, accessToken string) (*domain.AccessTokenClaims, error)
}

// RequireAuth rejects requests without a valid access token.
//
// The token is read from the Authorization header only, never from a cookie or query
// parameter. That is a deliberate CSRF property: a browser attaches cookies to cross-site
// requests automatically but will never attach an Authorization header, so an authenticated
// state-changing request cannot be forged by a third-party page. It also keeps credentials
// out of access logs, which is where query-string tokens end up.
func RequireAuth(auth Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r)
			if !ok {
				unauthorized(w, r, "Provide a bearer access token in the Authorization header.")
				return
			}

			claims, err := auth.Authenticate(r.Context(), token)
			if err != nil {
				// Every failure — bad signature, expired, revoked session, unknown session —
				// answers identically. Distinguishing them tells a holder of a stolen token
				// precisely what went wrong with it.
				unauthorized(w, r, "Your credentials are missing, invalid, or expired.")
				return
			}

			next.ServeHTTP(w, r.WithContext(WithClaims(r.Context(), claims)))
		})
	}
}

// bearerToken extracts the token from an Authorization header.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", false
	}
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}
	return token, true
}

func unauthorized(w http.ResponseWriter, r *http.Request, detail string) {
	// WWW-Authenticate is required by RFC 9110 on a 401 and tells a client which scheme to
	// use rather than leaving it to guess.
	w.Header().Set("WWW-Authenticate", `Bearer realm="junto"`)
	writeProblemJSON(w, http.StatusUnauthorized,
		"/problems/unauthorized", "Unauthorized", detail, RequestIDFrom(r.Context()))
}
