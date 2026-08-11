package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Probe is one dependency's health check.
//
// The interface is declared here, in transport, and satisfied by closures from cmd/api. That
// direction is forced by the arch rules — transport may not import a repository or a driver —
// but it is also the right shape: this package needs to know that a dependency is checkable
// and whether the API can serve without it, and nothing else about what it is.
type Probe struct {
	// Name appears in the /healthz body, so it must be a stable label an operator recognises
	// ("postgres", "redis") and must not embed an address or a credential.
	Name string

	// Critical marks a dependency the API cannot serve traffic without. Only a critical
	// failure makes an instance unready; see the class comment on HealthHandler.
	Critical bool

	Check func(context.Context) error
}

// HealthHandler serves the three probe endpoints.
//
// The split between them is the entire point, so it is stated here rather than left to be
// inferred from three short methods:
//
//   - /livez  answers "is this process wedged and worth restarting". It checks NOTHING
//     external. A liveness probe that pings the database turns a database blip into a restart
//     storm across every instance at once — and restarting the API cannot fix the database,
//     it only discards the warm connection pool and every in-flight request, making recovery
//     strictly slower. This is the most common way a health check makes an outage worse.
//   - /readyz answers "should the load balancer send traffic here". It fails on a CRITICAL
//     dependency and while the process is draining.
//   - /healthz is the human/dashboard view: per-component status, for answering "what is
//     actually wrong" without ssh.
//
// Non-critical failures are reported but never fail readiness. If Redis being unreachable
// made every instance unready, a Redis outage would pull every instance out of rotation
// simultaneously and become a total outage — while the system is explicitly designed to
// survive it: with no REDIS_URL at all the process runs single-instance with in-memory
// fan-out, and D75/D76's log-backed gap fill plus the reconcile tick turn a dropped broadcast
// into a latency blip rather than a lost write. Postgres has no such degraded mode — op_seq
// allocation, every read and every write go through it — which is what makes it the one
// critical probe.
type HealthHandler struct {
	probes  []Probe
	log     *slog.Logger
	version string
	timeout time.Duration

	// draining is set once, at the start of shutdown, and never cleared. Readiness must go
	// false BEFORE the server stops accepting connections, or the load balancer keeps routing
	// to a process that is refusing them — which is a graceful shutdown that still 502s.
	draining atomic.Bool
}

// HealthConfig configures a HealthHandler.
type HealthConfig struct {
	Probes []Probe
	// Version is reported by /healthz. Disclosed deliberately on an unauthenticated endpoint:
	// confirming which build is actually serving is the main reason a human opens this URL,
	// and the alternative is ssh. A build identifier is not a credential.
	Version string
	// Timeout bounds ALL probes collectively, not each one. Probes run concurrently against
	// one deadline, so adding a dependency cannot push the endpoint past the orchestrator's
	// own probe timeout — which, if it did, would look exactly like the instance being down.
	Timeout time.Duration
	Logger  *slog.Logger
}

// NewHealthHandler builds a HealthHandler.
func NewHealthHandler(cfg HealthConfig) *HealthHandler {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 2 * time.Second
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	return &HealthHandler{
		probes:  cfg.Probes,
		log:     cfg.Logger,
		version: cfg.Version,
		timeout: cfg.Timeout,
	}
}

// BeginDraining flips readiness to false. Call it on the shutdown signal, before
// server.Shutdown, and then wait out the drain delay so the load balancer observes it.
func (h *HealthHandler) BeginDraining() { h.draining.Store(true) }

// Draining reports whether shutdown has begun. Exported for tests and for the shutdown log.
func (h *HealthHandler) Draining() bool { return h.draining.Load() }

const (
	statusOK       = "ok"
	statusDown     = "down"
	statusDraining = "draining"
)

type componentStatus struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Critical bool   `json:"critical"`
	// Deliberately no error field. A probe failure's detail — a host, a port, a driver
	// message — is exactly the internal topology an unauthenticated endpoint must not
	// publish. The detail is logged instead, where it is available to whoever can read logs
	// and to nobody else. Same reasoning as D32's identical-401 and D23's constraint-name
	// mapping: report the fact, withhold the internals.
}

type healthResponse struct {
	Status  string            `json:"status"`
	Version string            `json:"version"`
	Checks  []componentStatus `json:"checks,omitempty"`
}

// Live handles GET /livez. No dependency is consulted, deliberately.
//
// It stays 200 during draining. A draining process is not a wedged one, and answering 503
// here would invite the orchestrator to kill it mid-drain — turning a clean handover into
// exactly the dropped-connection event the drain exists to prevent. Readiness is the signal
// that says "stop sending me traffic"; liveness only ever says "restart me".
func (h *HealthHandler) Live(w http.ResponseWriter, r *http.Request) {
	writeProbe(w, http.StatusOK, healthResponse{Status: statusOK, Version: h.version})
}

// Ready handles GET /readyz.
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	if h.draining.Load() {
		writeProbe(w, http.StatusServiceUnavailable, healthResponse{
			Status: statusDraining, Version: h.version,
		})
		return
	}

	checks := h.runProbes(r.Context())
	status := http.StatusOK
	overall := statusOK
	for _, c := range checks {
		if c.Critical && c.Status != statusOK {
			status = http.StatusServiceUnavailable
			overall = statusDown
			break
		}
	}
	// Readiness answers one question, so it does not carry the component list. A load
	// balancer reads the status code; a human reads /healthz.
	writeProbe(w, status, healthResponse{Status: overall, Version: h.version})
}

// Health handles GET /healthz — the detailed view.
func (h *HealthHandler) Health(w http.ResponseWriter, r *http.Request) {
	checks := h.runProbes(r.Context())

	overall := statusOK
	status := http.StatusOK
	if h.draining.Load() {
		overall, status = statusDraining, http.StatusServiceUnavailable
	}
	for _, c := range checks {
		if c.Status == statusOK {
			continue
		}
		if c.Critical {
			overall, status = statusDown, http.StatusServiceUnavailable
			break
		}
		// A non-critical failure is visible here but does not change the code — the instance
		// is genuinely still serving, and a 503 would tell the load balancer otherwise.
		if overall == statusOK {
			overall = "degraded"
		}
	}

	writeProbe(w, status, healthResponse{Status: overall, Version: h.version, Checks: checks})
}

// runProbes runs every probe concurrently against one shared deadline.
func (h *HealthHandler) runProbes(ctx context.Context) []componentStatus {
	if len(h.probes) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	out := make([]componentStatus, len(h.probes))
	var wg sync.WaitGroup
	for i, p := range h.probes {
		wg.Add(1)
		go func(i int, p Probe) {
			defer wg.Done()
			out[i] = componentStatus{Name: p.Name, Status: statusOK, Critical: p.Critical}
			if p.Check == nil {
				return
			}
			if err := p.Check(ctx); err != nil {
				out[i].Status = statusDown
				// The detail the response withholds goes here. A critical failure is an
				// ERROR because it takes the instance out of rotation; a non-critical one is
				// a WARN because the instance keeps serving in a degraded mode it was
				// designed for.
				if p.Critical {
					h.log.ErrorContext(ctx, "health probe failed", "probe", p.Name, "critical", true, "error", err)
				} else {
					h.log.WarnContext(ctx, "health probe failed", "probe", p.Name, "critical", false, "error", err)
				}
			}
		}(i, p)
	}
	wg.Wait()
	return out
}

// writeProbe renders a probe response.
//
// Deliberately NOT the {"data": ...} envelope every /api/v1 response uses. A probe consumer is
// an orchestrator or a human with curl, not an API client, and wrapping the status in an
// envelope would mean a health check had to parse the API's conventions to read one field.
// no-store because a cached readiness answer is worse than no readiness answer.
func writeProbe(w http.ResponseWriter, status int, body healthResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
