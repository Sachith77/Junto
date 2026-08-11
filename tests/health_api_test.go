package tests

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/junto/junto/internal/middleware"
	juntohttp "github.com/junto/junto/internal/transport/http"
)

// The probe endpoints (Stage 4 Slice 3, D110–D113).
//
// These tests are deliberately about the DIFFERENCES between the three endpoints rather than
// about any one of them answering 200 on a good day. Every interesting property here is a
// property of what an endpoint does when something is wrong, and each is a decision that a
// plausible "simplification" would silently undo — one health handler answering everything,
// or a readiness check that dutifully verifies every dependency it can reach.
//
// Verified against planted breaks; each test names its own below.

type probeBody struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Checks  []struct {
		Name     string `json:"name"`
		Status   string `json:"status"`
		Critical bool   `json:"critical"`
	} `json:"checks"`
}

// healthServer builds an isolated router whose probes are controlled by the returned toggles.
// Nothing else is wired: a probe endpoint must not depend on any service being present, which
// is itself part of the contract — an instance that failed to build its services should still
// be able to say so.
func healthServer(t *testing.T, probes ...juntohttp.Probe) (*httptest.Server, *juntohttp.HealthHandler) {
	t.Helper()

	health := juntohttp.NewHealthHandler(juntohttp.HealthConfig{
		Version: "test-build",
		Timeout: 2 * time.Second,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Probes:  probes,
	})
	router, cleanup := juntohttp.NewRouter(juntohttp.Deps{
		Auth:   testAuthService,
		Health: health,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: juntohttp.RouterConfig{AllowedOrigins: []string{"http://localhost:3000"}},
	})
	srv := httptest.NewServer(router)
	t.Cleanup(func() {
		srv.Close()
		cleanup()
	})
	return srv, health
}

func getProbe(t *testing.T, srv *httptest.Server, path string) (int, probeBody) {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var body probeBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decoding %s (%q): %v", path, raw, err)
	}
	return resp.StatusCode, body
}

// failingProbe returns a probe whose health is controlled by the returned flag.
func failingProbe(name string, critical bool) (juntohttp.Probe, *atomic.Bool) {
	broken := &atomic.Bool{}
	return juntohttp.Probe{
		Name:     name,
		Critical: critical,
		Check: func(context.Context) error {
			if broken.Load() {
				return errors.New("dial tcp 10.4.2.7:5432: connection refused")
			}
			return nil
		},
	}, broken
}

// TestLivenessIgnoresDependencies is the most important test in this file, because the
// behaviour it protects is the one every "obvious improvement" removes.
//
// A liveness probe that checks the database looks strictly more thorough and is strictly
// worse: when Postgres blips, every instance fails liveness simultaneously and the
// orchestrator restarts all of them. Restarting cannot fix the database — it discards warm
// pools and in-flight work and makes recovery slower — so the check converts a recoverable
// dependency outage into a self-inflicted restart storm.
//
// Verified against a planted break: adding the critical probe's result to Live's response
// (the one-line "make liveness accurate" change) fails this test.
func TestLivenessIgnoresDependencies(t *testing.T) {
	db, broken := failingProbe("postgres", true)
	srv, _ := healthServer(t, db)

	broken.Store(true)

	status, body := getProbe(t, srv, "/livez")
	if status != http.StatusOK {
		t.Errorf("livez with a dead critical dependency = %d, want 200: a liveness probe that "+
			"checks dependencies turns an outage into a restart storm", status)
	}
	if body.Status != "ok" {
		t.Errorf("livez status = %q, want ok", body.Status)
	}

	// The paired assertion, without which the one above could pass on a handler that always
	// returns 200 for everything: readiness MUST notice the same failure.
	if status, _ := getProbe(t, srv, "/readyz"); status != http.StatusServiceUnavailable {
		t.Errorf("readyz with a dead critical dependency = %d, want 503", status)
	}
}

// TestReadinessFailsOnCriticalButNotOnDegraded pins the other half of the split. A Redis
// outage must not take the instance out of rotation — if it did, it would take EVERY instance
// out at once and turn a degradation the system is designed to absorb (D75/D76 repair
// cross-instance delivery from the log) into a total outage.
//
// Verified against a planted break: dropping the `c.Critical &&` guard in Ready — so any
// failing probe fails readiness — makes the second half of this test fail.
func TestReadinessFailsOnCriticalButNotOnDegraded(t *testing.T) {
	db, dbBroken := failingProbe("postgres", true)
	cache, cacheBroken := failingProbe("redis", false)
	srv, _ := healthServer(t, db, cache)

	if status, body := getProbe(t, srv, "/readyz"); status != http.StatusOK || body.Status != "ok" {
		t.Fatalf("healthy readyz = %d/%q, want 200/ok", status, body.Status)
	}

	cacheBroken.Store(true)
	status, body := getProbe(t, srv, "/readyz")
	if status != http.StatusOK {
		t.Errorf("readyz with only REDIS down = %d, want 200. Failing readiness on a "+
			"non-critical dependency pulls every instance from the pool at the same moment", status)
	}
	if body.Status != "ok" {
		t.Errorf("readyz status with redis down = %q, want ok", body.Status)
	}

	// ...and /healthz must still SAY redis is down, or the degradation is invisible.
	healthStatus, healthBody := getProbe(t, srv, "/healthz")
	if healthStatus != http.StatusOK {
		t.Errorf("healthz with only redis down = %d, want 200", healthStatus)
	}
	if healthBody.Status != "degraded" {
		t.Errorf("healthz status = %q, want degraded — a non-critical failure must be "+
			"reported even though it does not affect routing", healthBody.Status)
	}
	if !componentIs(healthBody, "redis", "down") {
		t.Errorf("healthz checks = %+v, want redis reported down", healthBody.Checks)
	}
	if !componentIs(healthBody, "postgres", "ok") {
		t.Errorf("healthz checks = %+v, want postgres reported ok", healthBody.Checks)
	}

	cacheBroken.Store(false)
	dbBroken.Store(true)
	if status, _ := getProbe(t, srv, "/readyz"); status != http.StatusServiceUnavailable {
		t.Errorf("readyz with POSTGRES down = %d, want 503", status)
	}
}

// TestDrainingFailsReadinessButNotLiveness covers the shutdown ordering that makes a rolling
// deploy silent.
//
// Readiness has to go false while the process is STILL SERVING, so the load balancer removes
// the instance before the listener closes. Liveness must stay true throughout, because a
// draining process is not a wedged one and a 503 there invites the orchestrator to kill it
// mid-drain — reintroducing the dropped connections the drain exists to avoid.
//
// Verified against a planted break: making Live consult h.draining fails this test.
func TestDrainingFailsReadinessButNotLiveness(t *testing.T) {
	srv, health := healthServer(t)

	if status, _ := getProbe(t, srv, "/readyz"); status != http.StatusOK {
		t.Fatalf("readyz before draining = %d, want 200", status)
	}

	health.BeginDraining()

	status, body := getProbe(t, srv, "/readyz")
	if status != http.StatusServiceUnavailable {
		t.Errorf("readyz while draining = %d, want 503", status)
	}
	if body.Status != "draining" {
		t.Errorf("readyz status while draining = %q, want draining", body.Status)
	}

	if status, _ := getProbe(t, srv, "/livez"); status != http.StatusOK {
		t.Errorf("livez while draining = %d, want 200: a draining process is not a wedged "+
			"one, and 503 here asks the orchestrator to kill it mid-handover", status)
	}

	// The process must still SERVE during the drain — that is the entire point of failing
	// readiness first rather than just closing the listener.
	resp, err := http.Post(srv.URL+"/api/v1/auth/login", "application/json",
		strings.NewReader(`{"email":"nobody@example.com","password":"x"}`))
	if err != nil {
		t.Fatalf("a draining server must still answer requests: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusServiceUnavailable {
		t.Error("a draining server returned 503 to a real request; it should keep serving " +
			"until the listener closes")
	}
}

// TestProbeResponsesLeakNoInternals. The probe endpoints are unauthenticated by necessity —
// an orchestrator cannot hold a credential — so anything in the body is public. A driver
// error naming a host and port is exactly the internal topology that must not be, and it is
// the most natural thing in the world for a "helpful" error field to carry.
//
// Verified against a planted break: adding `Error string` to componentStatus and populating
// it from err fails this test.
func TestProbeResponsesLeakNoInternals(t *testing.T) {
	db, broken := failingProbe("postgres", true)
	srv, _ := healthServer(t, db)
	broken.Store(true)

	for _, path := range []string{"/livez", "/readyz", "/healthz"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		// The probe's error text contains an address and a port. Neither may appear.
		for _, secret := range []string{"10.4.2.7", "5432", "connection refused", "dial tcp"} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("%s body leaked %q: %s", path, secret, raw)
			}
		}
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s Cache-Control = %q, want no-store: a cached readiness answer is "+
				"worse than none", path, got)
		}
	}
}

// TestProbesAreNotRateLimited. Probes arrive from one source address, forever, at the
// platform's polling rate. Mounted inside /api/v1 they would share the general per-IP bucket
// and eventually 429 — which the platform reads as an unhealthy instance, so the rate limiter
// would be capable of taking a perfectly healthy deployment down.
//
// Verified against a planted break: moving the three routes inside the /api/v1 group (where
// generalLimiter.Middleware is mounted) fails this test.
func TestProbesAreNotRateLimited(t *testing.T) {
	strict := middleware.RateLimitConfig{RequestsPerSecond: 0.1, Burst: 3, TTL: time.Minute}
	health := juntohttp.NewHealthHandler(juntohttp.HealthConfig{
		Version: "test-build",
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	router, cleanup := juntohttp.NewRouter(juntohttp.Deps{
		Auth:   testAuthService,
		Health: health,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: juntohttp.RouterConfig{
			AllowedOrigins:   []string{"http://localhost:3000"},
			GeneralRateLimit: &strict,
		},
	})
	srv := httptest.NewServer(router)
	defer func() {
		srv.Close()
		cleanup()
	}()

	// Comfortably more than the burst: a limiter in the path would have refused most of these.
	//
	// Asserting 200 rather than merely "not 429" is deliberate, and was NOT the first version
	// of this loop. The planted break for this test moves the routes inside the /api/v1 group,
	// which relocates them to /api/v1/livez — so every request 404'd, no request was throttled,
	// and a test whose whole purpose is to detect that move sailed through it. "Not 429" is
	// satisfied by an endpoint that does not exist. The status the probe is supposed to give is
	// the only assertion that isn't.
	for i := range 25 {
		resp, err := http.Get(srv.URL + "/readyz")
		if err != nil {
			t.Fatalf("probe %d: %v", i, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			t.Fatalf("probe %d was rate limited: an orchestrator polling forever would be "+
				"throttled into looking unhealthy", i)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("probe %d returned %d, want 200 — the endpoint must be reachable at the "+
				"root for this test to be measuring an exemption at all", i, resp.StatusCode)
		}
	}

	// The paired assertion: the strict limiter must genuinely be in place for API routes, or
	// this test proves nothing at all — it would pass just as happily against a router with
	// no rate limiting anywhere.
	throttled := false
	for range 25 {
		resp, err := http.Get(srv.URL + "/api/v1/trips")
		if err != nil {
			t.Fatalf("api request: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			throttled = true
			break
		}
	}
	if !throttled {
		t.Error("the general limiter never fired on /api/v1, so this test's probe assertion " +
			"is vacuous — it is not measuring an exemption from anything")
	}
}

// TestProbePathsExistWithoutAHealthHandler. A nil Health must still mount the routes. A 404
// and a 503 mean entirely different things to an orchestrator, and "nobody wired the handler"
// should not be indistinguishable from "this instance is unhealthy".
func TestProbePathsExistWithoutAHealthHandler(t *testing.T) {
	router, cleanup := juntohttp.NewRouter(juntohttp.Deps{
		Auth:   testAuthService,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: juntohttp.RouterConfig{AllowedOrigins: []string{"http://localhost:3000"}},
	})
	srv := httptest.NewServer(router)
	defer func() {
		srv.Close()
		cleanup()
	}()

	for _, path := range []string{"/livez", "/readyz", "/healthz"} {
		if status, _ := getProbe(t, srv, path); status != http.StatusOK {
			t.Errorf("%s with no Health configured = %d, want 200", path, status)
		}
	}
}

func componentIs(body probeBody, name, status string) bool {
	for _, c := range body.Checks {
		if c.Name == name {
			return c.Status == status
		}
	}
	return false
}
