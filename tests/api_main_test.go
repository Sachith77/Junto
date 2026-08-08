package tests

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/internal/middleware"
	"github.com/junto/junto/internal/repository"
	"github.com/junto/junto/internal/service"
	"github.com/junto/junto/internal/storage"
	"github.com/junto/junto/internal/testsupport"
	juntohttp "github.com/junto/junto/internal/transport/http"
	"github.com/junto/junto/internal/transport/ws"
)

// Full-stack HTTP tests.
//
// These wire the REAL router, middleware, service, repositories and Postgres — everything
// except the SMTP server. That is the point: unit tests prove each layer in isolation, and
// these prove the layers were assembled correctly. A handler that forgets to set a cookie, a
// middleware registered in the wrong order, a domain error mapped to the wrong status — none
// of those are visible to a test that stops at the service boundary.
//
// They live in package `tests` rather than beside the transport code so that wiring a
// repository into a handler does not have to be exempted from the layering rules in a way
// that could mask a real violation.

var (
	testPG     *testsupport.Postgres
	testServer *httptest.Server
	testMailer *recordingMailer

	// Repositories exposed to the convergence test, which asserts the four-way equality
	//
	//	fold(trip_ops) == database state == client A's state == client B's state
	//
	// Reading the database directly is the point: a test that only compared the two clients
	// would pass just as happily if both were wrong in the same way.
	testOpLog   domain.OpLogRepository
	testTrips   domain.TripRepository
	testSlots   domain.SlotRepository
	testOptions domain.SlotOptionRepository
	testVotes   domain.VoteRepository
	testBudget  domain.BudgetRepository

	// Slice 3's two entities. testAttachments is the concrete repository because the
	// fold-versus-database comparison needs ListForTrip, which is deliberately not on the
	// domain port. testStorage is the in-process object store the attachment service was
	// wired with: simulating the browser's direct PUT is the ONLY way to reach the confirm
	// path, because the API never sees the bytes.
	testAttachments *repository.AttachmentRepository
	testStorage     *storage.MemoryStorage

	// testConnections is the shared instance's socket registry, so a revocation test can ask
	// the SERVER how many connections it is holding rather than inferring it from a client
	// that stopped receiving — which a dropped subscription also produces.
	testConnections *ws.Registry
)

func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		log.Println("skipping API integration tests in -short mode (they require Docker)")
		return
	}

	code, err := runAPISuite(m)
	if err != nil {
		log.Printf("API test setup failed: %v", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runAPISuite(m *testing.M) (int, error) {
	ctx := context.Background()

	pg, err := testsupport.StartPostgres(ctx)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := pg.Close(); err != nil {
			log.Printf("closing test database: %v", err)
		}
	}()
	testPG = pg

	testMailer = &recordingMailer{}

	// The shared instance. Single-instance by configuration — no ticket store or transport is
	// supplied, so it gets the in-memory ones and behaves exactly as Slice 1 did. The
	// multi-instance tests build their own pair with Redis behind both.
	//
	// ReconcileInterval is shortened from the production default so that the fan-out repair
	// path is exercised by the suite at all rather than only in the tests that force it.
	s, err := newStack(stackConfig{Pool: pg.Pool, ReconcileInterval: time.Second})
	if err != nil {
		return 0, err
	}
	defer s.Close()

	testServer = s.Server
	testAuthService = s.Auth
	testOpLog = s.OpLog
	testTrips = s.Trips
	testSlots = s.Slots
	testOptions = s.Options
	testVotes = s.Votes
	testBudget = s.Budget
	testAttachments = s.Attachments
	testStorage = s.Storage
	testConnections = s.Connections

	return m.Run(), nil
}

// testAuthService is exposed so a test can build a second router with different settings.
var testAuthService *service.AuthService

// newStrictlyLimitedServer builds an isolated server with production-grade throttles, so the
// rate limiter can be proven without the shared suite tripping over it.
func newStrictlyLimitedServer(t *testing.T) *httptest.Server {
	t.Helper()

	strict := middleware.AuthRateLimit()
	router, cleanup := juntohttp.NewRouter(juntohttp.Deps{
		Auth:   testAuthService,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: juntohttp.RouterConfig{
			AllowedOrigins: []string{"http://localhost:3000"},
			AuthRateLimit:  &strict,
		},
	})
	srv := httptest.NewServer(router)
	t.Cleanup(func() {
		srv.Close()
		cleanup()
	})
	return srv
}

// recordingMailer captures outbound email so tests can follow a link like a user would.
type recordingMailer struct {
	mu   sync.Mutex
	sent []domain.EmailMessage
}

func (m *recordingMailer) Send(_ context.Context, msg domain.EmailMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

// lastTo returns the most recent message sent to an address.
func (m *recordingMailer) lastTo(addr string) (domain.EmailMessage, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.sent) - 1; i >= 0; i-- {
		if strings.EqualFold(m.sent[i].To, addr) {
			return m.sent[i], true
		}
	}
	return domain.EmailMessage{}, false
}

// --- HTTP helpers ---

// client is a per-test HTTP client with its own cookie jar, so each test gets an independent
// browser-like session. A shared jar would leak refresh cookies between tests.
type client struct {
	t    *testing.T
	http *http.Client
	base string
}

func newClient(t *testing.T) *client {
	t.Helper()
	return newClientFor(t, testServer)
}

// newClientFor targets a specific server, for tests that need their own router configuration.
func newClientFor(t *testing.T, srv *httptest.Server) *client {
	t.Helper()
	jar := &simpleJar{cookies: map[string]*http.Cookie{}}
	return &client{t: t, base: srv.URL, http: &http.Client{
		Jar: jar,
		// Do not follow redirects: this API never issues them, and a silent redirect would
		// mask a routing mistake.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

type response struct {
	status  int
	body    []byte
	headers http.Header
	cookies []*http.Cookie
}

// decode unmarshals the `data` member of a success envelope.
func (r response) decode(t *testing.T, dst any) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(r.body, &env); err != nil {
		t.Fatalf("decoding envelope: %v\nbody: %s", err, r.body)
	}
	if err := json.Unmarshal(env.Data, dst); err != nil {
		t.Fatalf("decoding data: %v\nbody: %s", err, r.body)
	}
}

// problem unmarshals an RFC 7807 problem document.
func (r response) problem(t *testing.T) juntohttp.Problem {
	t.Helper()
	var p juntohttp.Problem
	if err := json.Unmarshal(r.body, &p); err != nil {
		t.Fatalf("decoding problem document: %v\nbody: %s", err, r.body)
	}
	return p
}

func (r response) cookie(name string) (*http.Cookie, bool) {
	for _, c := range r.cookies {
		if c.Name == name {
			return c, true
		}
	}
	return nil, false
}

func (c *client) do(method, path string, body any, opts ...func(*http.Request)) response {
	c.t.Helper()

	var reader io.Reader
	if body != nil {
		switch v := body.(type) {
		case string:
			reader = strings.NewReader(v)
		default:
			encoded, err := json.Marshal(v)
			if err != nil {
				c.t.Fatalf("encoding request body: %v", err)
			}
			reader = strings.NewReader(string(encoded))
		}
	}

	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatalf("building request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, opt := range opts {
		opt(req)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("reading response body: %v", err)
	}

	return response{
		status:  resp.StatusCode,
		body:    data,
		headers: resp.Header,
		cookies: resp.Cookies(),
	}
}

func withBearer(token string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

func withHeader(key, value string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(key, value) }
}

// simpleJar is a minimal cookie jar.
//
// net/http/cookiejar enforces a public-suffix policy that rejects cookies for "127.0.0.1",
// which is exactly where httptest listens. This stores by name and ignores domain rules,
// which is all these tests need — and it makes cookie handling explicit rather than
// depending on jar behaviour the tests are not actually about.
type simpleJar struct {
	mu      sync.Mutex
	cookies map[string]*http.Cookie
}

func (j *simpleJar) SetCookies(_ *url.URL, cookies []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range cookies {
		if c.MaxAge < 0 {
			delete(j.cookies, c.Name)
			continue
		}
		j.cookies[c.Name] = c
	}
}

func (j *simpleJar) Cookies(_ *url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]*http.Cookie, 0, len(j.cookies))
	for _, c := range j.cookies {
		out = append(out, c)
	}
	return out
}

// assertStatus fails with the response body attached, so a mismatch is diagnosable without
// re-running under a debugger.
func assertStatus(t *testing.T, r response, want int) {
	t.Helper()
	if r.status != want {
		t.Fatalf("status = %d, want %d\nbody: %s", r.status, want, r.body)
	}
}

// assertProblemsMatchIgnoringInstance compares two problem documents for everything except
// the request id.
//
// `instance` is unique per request by design — that is what makes it useful for support — so
// a raw byte comparison would always differ and prove nothing. Everything else must be
// identical, because any other difference is an oracle the caller can probe.
func assertProblemsMatchIgnoringInstance(t *testing.T, a, b response) {
	t.Helper()

	pa, pb := a.problem(t), b.problem(t)
	pa.Instance, pb.Instance = "", ""

	if pa.Instance == pb.Instance && a.headers.Get("X-Request-ID") == b.headers.Get("X-Request-ID") {
		t.Error("the two responses share a request id; they were not distinct requests")
	}

	encodedA, err := json.Marshal(pa)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	encodedB, err := json.Marshal(pb)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if string(encodedA) != string(encodedB) {
		t.Errorf("responses differ beyond the request id:\n  %s\n  %s", encodedA, encodedB)
	}
	if a.status != b.status {
		t.Errorf("status codes differ: %d vs %d", a.status, b.status)
	}
}

func assertProblemField(t *testing.T, r response, field string) {
	t.Helper()
	p := r.problem(t)
	for _, v := range p.Errors {
		if v.Field == field {
			return
		}
	}
	t.Errorf("expected a violation on %q, got %+v", field, p.Errors)
}
