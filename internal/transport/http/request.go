package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/junto/junto/internal/domain"
)

// maxBodyBytes caps request bodies.
//
// Without a cap, a single client can stream unbounded data into memory before the handler
// ever runs — a denial of service that costs the attacker one connection. 1 MiB is far above
// any legitimate request this API accepts.
const maxBodyBytes = 1 << 20

// decodeJSON reads and validates a JSON request body.
//
// Every failure becomes a field-level validation problem rather than a bare 400, because
// "which field was wrong" is the only thing a client can act on.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		// Compare the media type only: "application/json; charset=utf-8" is legitimate.
		mediaType := strings.TrimSpace(strings.Split(ct, ";")[0])
		if !strings.EqualFold(mediaType, "application/json") {
			return &unsupportedMediaTypeError{got: mediaType}
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	dec := json.NewDecoder(r.Body)
	// Reject unknown fields. A typo'd field name would otherwise be silently ignored and the
	// client would see a successful response that did not do what they asked.
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}

	// Exactly one JSON value per request. A trailing second object usually means a client
	// bug, and accepting it silently discards data.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		ve := &domain.ValidationError{}
		ve.Add("body", "malformed", "request body must contain a single JSON object")
		return ve
	}
	return nil
}

// decodeError converts a json decode failure into an actionable validation error.
func decodeError(err error) error {
	ve := &domain.ValidationError{}

	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var maxBytesErr *http.MaxBytesError

	switch {
	case errors.As(err, &syntaxErr):
		ve.Add("body", "malformed", fmt.Sprintf("malformed JSON at byte %d", syntaxErr.Offset))

	case errors.As(err, &typeErr):
		// Name the offending field so the client does not have to bisect its payload.
		field := typeErr.Field
		if field == "" {
			field = "body"
		}
		ve.Add(field, "wrong_type", fmt.Sprintf("expected %s", typeErr.Type.String()))

	case errors.As(err, &maxBytesErr):
		return &payloadTooLargeError{limit: maxBytesErr.Limit}

	case errors.Is(err, io.EOF):
		ve.Add("body", "required", "request body must not be empty")

	case strings.HasPrefix(err.Error(), "json: unknown field "):
		name := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
		ve.Add(name, "unknown_field", "this field is not accepted by this endpoint")

	default:
		ve.Add("body", "malformed", "request body could not be parsed")
	}
	return ve
}

// unsupportedMediaTypeError and payloadTooLargeError exist so the two failures that are NOT
// semantically "invalid field" get their own accurate status codes (415 and 413) rather than
// being flattened into 422.

type unsupportedMediaTypeError struct{ got string }

func (e *unsupportedMediaTypeError) Error() string {
	return fmt.Sprintf("unsupported media type %q", e.got)
}

type payloadTooLargeError struct{ limit int64 }

func (e *payloadTooLargeError) Error() string {
	return fmt.Sprintf("request body exceeds %d bytes", e.limit)
}

// writeRequestError handles the transport-level decode failures, delegating everything else
// to writeError so the domain-error mapping stays in one place.
func writeRequestError(w http.ResponseWriter, r *http.Request, err error, log *slog.Logger) {
	var mediaErr *unsupportedMediaTypeError
	var sizeErr *payloadTooLargeError

	switch {
	case errors.As(err, &mediaErr):
		writeProblem(w, r, Problem{
			Type:   typeUnsupported,
			Title:  "Unsupported media type",
			Status: http.StatusUnsupportedMediaType,
			Detail: "Content-Type must be application/json.",
		})
	case errors.As(err, &sizeErr):
		writeProblem(w, r, Problem{
			Type:   typePayloadTooBig,
			Title:  "Payload too large",
			Status: http.StatusRequestEntityTooLarge,
			Detail: fmt.Sprintf("Request body must not exceed %d bytes.", maxBodyBytes),
		})
	default:
		writeError(w, r, err, log)
	}
}

// pathID reads a UUID path parameter.
func pathID(r *http.Request, name string, param func(*http.Request, string) string) (domain.ID, error) {
	return domain.ParseID(name, param(r, name))
}

// queryPage reads keyset pagination parameters.
func queryPage(r *http.Request) (domain.PageRequest, error) {
	page := domain.PageRequest{After: domain.Cursor(r.URL.Query().Get("cursor"))}

	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			ve := &domain.ValidationError{}
			ve.Add("limit", "not_a_number", "limit must be an integer")
			return page, ve
		}
		page.Limit = n
	}
	// Normalize clamps out-of-range values rather than rejecting them: a client asking for
	// 10000 items wants "as many as possible", and failing the request helps nobody.
	return page.Normalize(), nil
}

// pathIDFunc is chi.URLParam, injected so this file does not depend on the router.
type pathIDFunc func(*http.Request, string) string

// clientIP extracts the caller's address for session bookkeeping.
//
// It uses RemoteAddr, which chi's RealIP middleware has already rewritten from X-Forwarded-For
// when configured. Reading the header directly here would be a mistake: the header is
// client-controlled, so an attacker could forge session records or evade a per-IP rate limit
// simply by setting it.
func clientIP(r *http.Request) *netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return nil
	}
	return &addr
}
