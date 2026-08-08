// Package ws is the WebSocket transport for the sync engine.
//
// It is the ONLY package in the module that knows WebSockets exist. Its whole job is:
// decode a frame into a syncengine.Intent, call the engine, and encode the engine's events
// back into frames. The engine's two entry points take and return domain types, so replacing
// this package with Server-Sent Events or a long poll would not change a line of the engine
// — which is what "transport-agnostic" has to mean to be worth claiming.
package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/time/rate"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/middleware"
	"github.com/junto/junto/internal/syncengine"
)

// Config tunes the transport.
type Config struct {
	// AllowedOrigins is checked on the handshake. Empty means same-origin only.
	AllowedOrigins []string

	// OpsPerSecond and OpsBurst throttle a single connection's operation submissions.
	// Configurable rather than constant for the same reason the HTTP limiter is: the right
	// numbers depend on the deployment, and a production-strict limiter throttles the test
	// suite itself.
	OpsPerSecond float64
	OpsBurst     int
}

func (c Config) withDefaults() Config {
	if c.OpsPerSecond <= 0 {
		c.OpsPerSecond = 25
	}
	if c.OpsBurst <= 0 {
		c.OpsBurst = 50
	}
	return c
}

// Handlers exposes the two HTTP endpoints the transport needs.
type Handlers struct {
	engine   *syncengine.Engine
	tickets  TicketStore
	registry *Registry
	log      *slog.Logger
	cfg      Config
}

// NewHandlers builds the WebSocket endpoints.
//
// registry may be nil, which means nothing can close a socket on revocation and the connection
// lifetime cap is the only bound again. That is the correct wiring for a test that is not about
// revocation, and the wrong one for production — so cmd/api always passes one.
func NewHandlers(engine *syncengine.Engine, tickets TicketStore, registry *Registry, log *slog.Logger, cfg Config) *Handlers {
	if log == nil {
		log = slog.Default()
	}
	return &Handlers{engine: engine, tickets: tickets, registry: registry, log: log, cfg: cfg.withDefaults()}
}

type ticketResponse struct {
	Ticket    string    `json:"ticket"`
	ExpiresAt time.Time `json:"expires_at"`
}

// IssueTicket mints a single-use handshake credential for the authenticated caller.
//
// Mounted behind the normal bearer-token middleware, which is the point: the ticket derives
// its authority from an Authorization header that a cross-site attacker cannot produce. That
// is what makes the query-string credential on the handshake safe, and it is why this endpoint
// must never be reachable without RequireAuth.
func (h *Handlers) IssueTicket(w http.ResponseWriter, r *http.Request) {
	claims, ok := middleware.ClaimsFrom(r.Context())
	if !ok {
		http.Error(w, `{"error":"unauthenticated"}`, http.StatusUnauthorized)
		return
	}

	raw, expiresAt, err := h.tickets.Issue(r.Context(), claims.UserID, claims.SessionID)
	if err != nil {
		h.log.Error("issuing ws ticket", "user_id", claims.UserID, "error", err)
		http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	// A credential must never be cached, by the browser or by anything between.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(ticketResponse{Ticket: raw, ExpiresAt: expiresAt})
}

// Connect upgrades the request to a WebSocket after redeeming its ticket.
//
// Deliberately NOT behind RequireAuth: a browser cannot set an Authorization header on a
// WebSocket handshake, which is the entire reason tickets exist (D10). The ticket is the
// credential here, and it is single-use and thirty seconds old.
func (h *Handlers) Connect(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("ticket")
	if raw == "" {
		http.Error(w, `{"error":"ticket required"}`, http.StatusUnauthorized)
		return
	}

	ticket, err := h.tickets.Redeem(r.Context(), raw)
	if err != nil {
		// Expired, unknown, and already-redeemed all answer identically. Distinguishing them
		// tells a holder of a stolen or guessed value exactly what went wrong with it.
		http.Error(w, `{"error":"invalid ticket"}`, http.StatusUnauthorized)
		return
	}

	sock, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Origin is checked as defence in depth. WebSocket handshakes are not subject to
		// CORS, so a cookie-authenticated socket would be hijackable from any origin (CSWSH).
		// The ticket design already defeats that — an attacker's page cannot mint one — but
		// "already defeated by something else" is not a reason to leave the check out.
		OriginPatterns:     h.cfg.AllowedOrigins,
		InsecureSkipVerify: false,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		h.log.Warn("ws handshake rejected", "error", err)
		return
	}

	c := &conn{
		id:     domain.NewID(),
		userID: ticket.UserID,
		// Carried for the whole life of the connection so a revocation can find it (D91). This
		// is the only thing the ticket's session id is kept for after the handshake.
		sessionID: ticket.SessionID,
		ws:        sock,
		engine:    h.engine,
		log:       h.log,
		limiter:   rate.NewLimiter(rate.Limit(h.cfg.OpsPerSecond), h.cfg.OpsBurst),
		send:      make(chan []byte, sendBuffer),
		subs:      map[domain.ID]*syncengine.Subscription{},
		closed:    make(chan struct{}),
	}

	// Registered BEFORE the connection starts running, and unregistered after it stops. The
	// order matters in one direction only: a socket that is live but not yet registered would
	// survive a revocation arriving in that window, which is precisely the gap being closed.
	if h.registry != nil {
		h.registry.add(c)
		defer h.registry.remove(c)
	}

	h.log.Info("ws connected", "conn_id", c.id, "user_id", c.userID, "session_id", c.sessionID)
	// Deliberately NOT r.Context(): for a hijacked connection that context is tied to the
	// request, and using it would tear the socket down the moment the handshake handler
	// returns. The connection's own lifetime bound is applied inside run.
	c.run(context.WithoutCancel(r.Context()))
	h.log.Info("ws disconnected", "conn_id", c.id, "user_id", c.userID)
}
