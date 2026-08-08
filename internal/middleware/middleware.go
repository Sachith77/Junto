package middleware

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/junto/junto/internal/domain"
)

// RequestID assigns every request a unique identifier.
//
// It is echoed in the response header, attached to every log line, and returned as the
// `instance` member of any problem document — so a user reporting "it failed at 14:32" hands
// over a value that finds the exact request.
//
// An inbound X-Request-ID is honoured so a trace survives across services, but it is length-
// capped and stripped of control characters: it lands in logs, and an unsanitised header is
// how log injection happens.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := sanitizeRequestID(r.Header.Get("X-Request-ID"))
		if id == "" {
			id = domain.NewID().String()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(WithRequestID(r.Context(), id)))
	})
}

func sanitizeRequestID(v string) string {
	if len(v) > 128 {
		v = v[:128]
	}
	return strings.Map(func(r rune) rune {
		// Printable ASCII only. Newlines would let a caller forge log entries.
		if r < 0x20 || r > 0x7e {
			return -1
		}
		return r
	}, v)
}

// responseRecorder captures the status and size for logging. Without it the logger can only
// report that a request happened, not how it ended.
type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (rec *responseRecorder) WriteHeader(status int) {
	rec.status = status
	rec.ResponseWriter.WriteHeader(status)
}

func (rec *responseRecorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK // implicit 200 from a bare Write
	}
	n, err := rec.ResponseWriter.Write(b)
	rec.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer, which the WebSocket
// upgrade in Stage 2 will need for hijacking.
func (rec *responseRecorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }

// RequestLogger emits one structured line per completed request.
//
// One line per request, not one per phase: logs are for reconstructing what happened, and
// multi-line-per-request output makes that harder, not easier.
func RequestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &responseRecorder{ResponseWriter: w, status: 0}

			next.ServeHTTP(rec, r)

			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}

			// The query string is deliberately omitted. Password-reset and email-verification
			// links carry live tokens as query parameters, and logging them would make log
			// access equivalent to account takeover.
			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"duration_ms", time.Since(start).Milliseconds(),
				"bytes", rec.bytes,
				"request_id", RequestIDFrom(r.Context()),
			}
			if userID, ok := UserIDFrom(r.Context()); ok {
				attrs = append(attrs, "user_id", userID.String())
			}

			switch {
			case status >= 500:
				log.ErrorContext(r.Context(), "request failed", attrs...)
			case status >= 400:
				log.WarnContext(r.Context(), "request rejected", attrs...)
			default:
				log.InfoContext(r.Context(), "request completed", attrs...)
			}
		})
	}
}

// Recoverer converts a panic into a 500 instead of killing the process.
//
// net/http already recovers panics per connection, but it does so silently from the client's
// perspective: the connection is simply dropped. This turns it into a well-formed problem
// document carrying the request id, and logs the stack once where it can be found.
func Recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				// http.ErrAbortHandler is the documented way for a handler to abandon a
				// response deliberately; it is not an error and must propagate.
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec)
				}

				log.ErrorContext(r.Context(), "panic recovered",
					"panic", rec,
					"path", r.URL.Path,
					"method", r.Method,
					"request_id", RequestIDFrom(r.Context()),
					"stack", string(debug.Stack()),
				)
				writeProblemJSON(w, http.StatusInternalServerError,
					"/problems/internal-error", "Internal server error",
					"Something went wrong.", RequestIDFrom(r.Context()))
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// writeProblemJSON emits a minimal RFC 7807 document.
//
// Duplicated from internal/transport/http rather than imported: middleware must not depend on
// the transport package, or the dependency graph acquires a cycle the moment transport wires
// middleware. Small, deliberate duplication is the cheaper of the two costs.
func writeProblemJSON(w http.ResponseWriter, status int, problemType, title, detail, instance string) {
	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":     problemType,
		"title":    title,
		"status":   status,
		"detail":   detail,
		"instance": instance,
	})
}

// SecurityHeaders sets response headers that cost nothing and close whole bug classes.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// Stops a browser from second-guessing our Content-Type; the reason a JSON string
		// cannot become stored XSS.
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// This API serves only JSON, so the strictest possible CSP applies: nothing may be
		// loaded or executed from a response.
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// CORS handles cross-origin requests against an explicit allowlist.
//
// Reflecting the Origin header unconditionally would defeat the same-origin policy for every
// browser client, and combined with credentialed requests it would let any site read
// authenticated responses. The allowlist is configuration, validated in production to reject
// "*" and plaintext origins.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[strings.TrimRight(o, "/")] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimRight(r.Header.Get("Origin"), "/")

			if origin != "" {
				if _, ok := allowed[origin]; ok {
					h := w.Header()
					h.Set("Access-Control-Allow-Origin", origin)
					// Required for the refresh cookie to be sent at all.
					h.Set("Access-Control-Allow-Credentials", "true")
					h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
					h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
					h.Set("Access-Control-Expose-Headers", "X-Request-ID")
					h.Set("Access-Control-Max-Age", "600")
					// Responses differ by Origin, so caches must key on it.
					h.Add("Vary", "Origin")
				}
			}

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
