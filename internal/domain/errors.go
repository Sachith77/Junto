package domain

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors. Services return these (usually wrapped with context); the transport
// layer maps them to HTTP status codes in exactly one place. The domain never knows what
// an HTTP status code is.
var (
	// ErrNotFound — the entity does not exist, or the actor may not know that it does.
	ErrNotFound = errors.New("not found")

	// ErrAlreadyExists — a uniqueness constraint would be violated.
	ErrAlreadyExists = errors.New("already exists")

	// ErrVersionConflict — optimistic concurrency check failed: someone else wrote first.
	//
	// Worth being precise about what this does and does not mean. It gives
	// last-writer-rejected, which is the correct behaviour for the REST path. It is NOT
	// collaborative editing and it does NOT produce convergence — that is what the Stage 2
	// sync engine is for. Do not let this error type stand in for conflict resolution.
	ErrVersionConflict = errors.New("version conflict")

	// ErrForbidden — authenticated, but lacks the required capability.
	ErrForbidden = errors.New("forbidden")

	// ErrUnauthenticated — no valid credentials presented.
	ErrUnauthenticated = errors.New("unauthenticated")

	// ErrInvalidCredentials — email/password mismatch. Deliberately indistinguishable
	// between "no such user" and "wrong password" so it cannot be used to enumerate accounts.
	ErrInvalidCredentials = errors.New("invalid credentials")

	// ErrEmailNotVerified — the account exists but has not completed verification.
	ErrEmailNotVerified = errors.New("email not verified")

	// ErrTokenInvalid — malformed, unknown, or already-superseded token.
	ErrTokenInvalid = errors.New("token invalid")

	// ErrTokenExpired — well-formed but past its expiry.
	ErrTokenExpired = errors.New("token expired")

	// ErrTokenConsumed — a single-use token was presented twice.
	//
	// For refresh tokens this is not merely an error, it is a security signal: the correct
	// response is to revoke the entire session family, because either the token was stolen
	// or the legitimate client has a bug. See AuthSession.
	ErrTokenConsumed = errors.New("token already consumed")

	// ErrValidation — input failed domain validation. Match with errors.Is; the concrete
	// *ValidationError carries the per-field detail.
	ErrValidation = errors.New("validation failed")
)

// FieldViolation is a single field-level validation failure.
//
// Code is a stable, machine-readable identifier (e.g. "required", "too_long"). Clients
// switch on Code; Message is for humans and may change freely.
type FieldViolation struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ValidationError aggregates every field-level problem in one input, so a client gets all
// of its mistakes at once rather than discovering them one round trip at a time.
//
// This maps onto the RFC 7807 problem document's extension members in the transport layer.
type ValidationError struct {
	Violations []FieldViolation
}

func (e *ValidationError) Error() string {
	if len(e.Violations) == 0 {
		return ErrValidation.Error()
	}
	parts := make([]string, 0, len(e.Violations))
	for _, v := range e.Violations {
		parts = append(parts, fmt.Sprintf("%s: %s", v.Field, v.Message))
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// Is makes errors.Is(err, ErrValidation) work for the concrete type, so callers can test
// the category without type-asserting, and type-assert only when they need the detail.
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

// Add records a violation. The zero value of ValidationError is ready to use.
func (e *ValidationError) Add(field, code, message string) {
	e.Violations = append(e.Violations, FieldViolation{Field: field, Code: code, Message: message})
}

// AddIf records a violation only when cond is true. Keeps Validate() methods flat instead
// of a ladder of ifs.
func (e *ValidationError) AddIf(cond bool, field, code, message string) {
	if cond {
		e.Add(field, code, message)
	}
}

// HasViolations reports whether anything failed.
func (e *ValidationError) HasViolations() bool { return len(e.Violations) > 0 }

// OrNil returns nil when there are no violations, so callers can `return v.OrNil()`
// without a nil-pointer-in-non-nil-interface trap.
func (e *ValidationError) OrNil() error {
	if e.HasViolations() {
		return e
	}
	return nil
}

// AsValidationError extracts the concrete validation error from a wrapped chain.
func AsValidationError(err error) (*ValidationError, bool) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve, true
	}
	return nil, false
}
