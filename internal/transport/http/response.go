// Package http is the HTTP transport. It translates between the wire and the service layer
// and does nothing else: no business rules, no SQL, no direct repository access.
//
// The one-way rule this package exists to preserve is that services never learn what an HTTP
// status code is. Domain errors travel outward and are mapped to statuses in exactly one
// place — writeError below.
package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/junto/junto/internal/domain"
)

// Response shapes.
//
// Success bodies are enveloped: {"data": ..., "meta": {...}}. Errors are RFC 7807 problem
// documents, which are their own top-level shape rather than the envelope with an error
// field. That asymmetry is deliberate, not an oversight: RFC 7807 defines a complete media
// type (application/problem+json) that existing tooling already understands, and wrapping it
// in a custom envelope would forfeit that for the sake of looking uniform.

// Envelope wraps a successful response body.
type Envelope struct {
	Data any   `json:"data"`
	Meta *Meta `json:"meta,omitempty"`
}

// Meta carries pagination and other out-of-band information.
type Meta struct {
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

// Problem is an RFC 7807 problem document.
type Problem struct {
	// Type is a URI identifying the problem class. Stable and dereferenceable-looking, so
	// clients can switch on it rather than on the status code or the prose.
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
	// Instance identifies this specific occurrence. We put the request id here so a user
	// reporting an error gives us something that finds the exact log line.
	Instance string `json:"instance,omitempty"`

	// Errors is an RFC 7807 extension member carrying field-level validation failures, so a
	// client gets every problem in one round trip instead of discovering them one at a time.
	Errors []domain.FieldViolation `json:"errors,omitempty"`
}

// Problem type URIs. Relative to a documentation base; the exact host does not matter as
// long as the values are stable, because clients match on them as opaque identifiers.
const (
	typeValidation     = "/problems/validation-failed"
	typeUnauthorized   = "/problems/unauthorized"
	typeForbidden      = "/problems/forbidden"
	typeNotFound       = "/problems/not-found"
	typeConflict       = "/problems/conflict"
	typeVersionConflic = "/problems/version-conflict"
	typeRateLimited    = "/problems/rate-limited"
	typeBadRequest     = "/problems/bad-request"
	typeInternal       = "/problems/internal-error"
	typeUnsupported    = "/problems/unsupported-media-type"
	typePayloadTooBig  = "/problems/payload-too-large"
)

// writeJSON writes a success response.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Defence in depth: this API never serves user-supplied HTML, but a browser that sniffs
	// a JSON response as HTML would turn a stored string into stored XSS.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)

	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already on the wire, so this cannot become an error response.
		// Log it and move on; the client will see a truncated body and fail its own decode.
		slog.Error("encoding response body failed", "error", err)
	}
}

// writeData writes an enveloped success response.
func writeData(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, Envelope{Data: data})
}

// writePage writes an enveloped, paginated success response.
func writePage(w http.ResponseWriter, status int, data any, nextCursor domain.Cursor, hasMore bool) {
	writeJSON(w, status, Envelope{
		Data: data,
		Meta: &Meta{NextCursor: string(nextCursor), HasMore: hasMore},
	})
}

// writeProblem writes an RFC 7807 problem document.
func writeProblem(w http.ResponseWriter, r *http.Request, p Problem) {
	p.Instance = RequestIDFrom(r.Context())

	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(p.Status)

	if err := json.NewEncoder(w).Encode(p); err != nil {
		slog.Error("encoding problem document failed", "error", err)
	}
}

// writeError maps a domain error to a problem document.
//
// This function is the ONLY place in the codebase that decides what HTTP status a business
// failure deserves. That is what keeps services free of transport concerns: they return
// domain.ErrForbidden, and the knowledge that "forbidden means 403" lives out here.
//
// Note what is NOT distinguished. Every credential failure — unknown email, wrong password,
// unverified address, invalid token, expired token, replayed token — collapses to a single
// 401 with one message. Internally these are very different and are logged as such; to the
// caller they must be identical, or the endpoint becomes an oracle for probing which
// accounts exist and what happened to a stolen link.
func writeError(w http.ResponseWriter, r *http.Request, err error, log *slog.Logger) {
	switch {
	case err == nil:
		return

	case errors.Is(err, domain.ErrValidation):
		p := Problem{
			Type:   typeValidation,
			Title:  "Validation failed",
			Status: http.StatusUnprocessableEntity,
			Detail: "One or more fields are invalid.",
		}
		if ve, ok := domain.AsValidationError(err); ok {
			p.Errors = ve.Violations
		}
		writeProblem(w, r, p)

	case errors.Is(err, domain.ErrInvalidCredentials),
		errors.Is(err, domain.ErrUnauthenticated),
		errors.Is(err, domain.ErrTokenInvalid),
		errors.Is(err, domain.ErrTokenExpired),
		errors.Is(err, domain.ErrTokenConsumed):
		// Logged with the real reason; answered with none of it.
		log.InfoContext(r.Context(), "authentication rejected",
			"path", r.URL.Path, "reason", err.Error())
		writeProblem(w, r, Problem{
			Type:   typeUnauthorized,
			Title:  "Unauthorized",
			Status: http.StatusUnauthorized,
			Detail: "Your credentials are missing, invalid, or expired.",
		})

	case errors.Is(err, domain.ErrEmailNotVerified):
		// The one credential failure that IS distinguished, because the client must be able
		// to offer "resend verification link" and cannot guess when to do so. The trade is
		// accepted knowingly: it confirms an address is registered, which is why the
		// rate limiter on this endpoint matters.
		writeProblem(w, r, Problem{
			Type:   typeForbidden,
			Title:  "Email not verified",
			Status: http.StatusForbidden,
			Detail: "Confirm your email address before signing in.",
		})

	case errors.Is(err, domain.ErrForbidden):
		writeProblem(w, r, Problem{
			Type:   typeForbidden,
			Title:  "Forbidden",
			Status: http.StatusForbidden,
			Detail: "You do not have permission to perform this action.",
		})

	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, Problem{
			Type:   typeNotFound,
			Title:  "Not found",
			Status: http.StatusNotFound,
			Detail: "The requested resource does not exist.",
		})

	case errors.Is(err, domain.ErrVersionConflict):
		writeProblem(w, r, Problem{
			Type:   typeVersionConflic,
			Title:  "Version conflict",
			Status: http.StatusConflict,
			Detail: "This resource changed since you loaded it. Reload and try again.",
		})

	case errors.Is(err, domain.ErrAlreadyExists):
		writeProblem(w, r, Problem{
			Type:   typeConflict,
			Title:  "Conflict",
			Status: http.StatusConflict,
			Detail: "That resource already exists.",
		})

	default:
		// Unmapped errors are bugs or infrastructure failures. The real error goes to the
		// log with the request id; the client gets the id and nothing else, because internal
		// error strings routinely contain table names, query fragments and hostnames.
		log.ErrorContext(r.Context(), "unhandled error",
			"path", r.URL.Path, "method", r.Method, "error", err)
		writeProblem(w, r, Problem{
			Type:   typeInternal,
			Title:  "Internal server error",
			Status: http.StatusInternalServerError,
			Detail: fmt.Sprintf("Something went wrong. Quote request id %s if you contact support.",
				RequestIDFrom(r.Context())),
		})
	}
}
