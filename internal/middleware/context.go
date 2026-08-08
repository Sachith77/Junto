// Package middleware holds cross-cutting HTTP concerns: request identity, structured
// logging, panic recovery, authentication and rate limiting.
//
// It sits alongside internal/transport rather than inside it because these behaviours are
// transport-level but endpoint-agnostic. Like every outer layer, it depends on
// internal/domain and never on internal/repository.
package middleware

import (
	"context"

	"github.com/junto/junto/internal/domain"
)

// Context keys are unexported struct types so no other package can collide with them or
// forge a value. A string key would let any package write "user_id" into a context and
// impersonate an authenticated caller — the reason the Go docs recommend against them.
type (
	requestIDKey struct{}
	claimsKey    struct{}
)

// WithRequestID stores a request id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFrom returns the request id, or "" if unset.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// WithClaims stores verified access-token claims.
//
// Only the Authenticator middleware calls this, and only after the token's signature,
// expiry and session liveness have all been checked. Anything downstream may therefore
// treat the presence of claims as proof of authentication.
func WithClaims(ctx context.Context, claims *domain.AccessTokenClaims) context.Context {
	return context.WithValue(ctx, claimsKey{}, claims)
}

// ClaimsFrom returns the verified claims, if the request was authenticated.
func ClaimsFrom(ctx context.Context) (*domain.AccessTokenClaims, bool) {
	c, ok := ctx.Value(claimsKey{}).(*domain.AccessTokenClaims)
	return c, ok
}

// UserIDFrom returns the authenticated user's id.
func UserIDFrom(ctx context.Context) (domain.ID, bool) {
	c, ok := ClaimsFrom(ctx)
	if !ok {
		return domain.NilID, false
	}
	return c.UserID, true
}
