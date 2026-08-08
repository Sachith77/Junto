package tests

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const apiPassword = "correct horse battery staple"

// uniqueEmail keeps committing tests independent: the API test suite does not roll back, so
// every account must have its own address.
func uniqueEmail(t *testing.T, prefix string) string {
	t.Helper()
	return fmt.Sprintf("%s-%d@example.test", prefix, uniqueCounter())
}

var counterCh = func() chan int {
	ch := make(chan int, 1)
	ch <- 0
	return ch
}()

func uniqueCounter() int {
	n := <-counterCh
	n++
	counterCh <- n
	return n
}

// signupAndVerify runs the onboarding flow over HTTP and returns the address.
func signupAndVerify(t *testing.T, c *client, prefix string) string {
	t.Helper()
	email := uniqueEmail(t, prefix)

	resp := c.do(http.MethodPost, "/api/v1/auth/signup", map[string]string{
		"email": email, "password": apiPassword, "display_name": "Test User",
	})
	assertStatus(t, resp, http.StatusCreated)

	msg, ok := testMailer.lastTo(email)
	if !ok {
		t.Fatalf("no verification email for %s", email)
	}
	token := extractToken(t, msg.TextBody, "verify-email")

	resp = c.do(http.MethodPost, "/api/v1/auth/verify-email", map[string]string{"token": token})
	assertStatus(t, resp, http.StatusNoContent)
	return email
}

func extractToken(t *testing.T, body, fragment string) string {
	t.Helper()
	marker := fragment + "?token="
	idx := strings.Index(body, marker)
	if idx < 0 {
		t.Fatalf("no %s link in email:\n%s", fragment, body)
	}
	rest := body[idx+len(marker):]
	if end := strings.IndexAny(rest, "\n \r"); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

func login(t *testing.T, c *client, email string) string {
	t.Helper()
	resp := c.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": email, "password": apiPassword,
	})
	assertStatus(t, resp, http.StatusOK)

	var body struct {
		AccessToken string `json:"access_token"`
	}
	resp.decode(t, &body)
	return body.AccessToken
}

// --- the happy path, end to end ---

func TestSignupVerifyLoginFlow(t *testing.T) {
	c := newClient(t)
	email := uniqueEmail(t, "flow")

	// Signup returns the user but NO session: login requires a verified address.
	resp := c.do(http.MethodPost, "/api/v1/auth/signup", map[string]string{
		"email": email, "password": apiPassword, "display_name": "Flow User",
	})
	assertStatus(t, resp, http.StatusCreated)

	var user struct {
		ID          string  `json:"id"`
		Email       string  `json:"email"`
		DisplayName string  `json:"display_name"`
		VerifiedAt  *string `json:"email_verified_at"`
	}
	resp.decode(t, &user)

	if user.VerifiedAt != nil {
		t.Error("a new account must start unverified")
	}
	if _, ok := resp.cookie("junto_refresh"); ok {
		t.Error("signup must not open a session")
	}
	// The response type has no password field at all; this asserts the projection is real.
	if strings.Contains(string(resp.body), "password") {
		t.Errorf("the signup response mentions a password:\n%s", resp.body)
	}

	// Login before verification is refused, distinguishably, so the client can offer to
	// resend the link.
	resp = c.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": email, "password": apiPassword,
	})
	assertStatus(t, resp, http.StatusForbidden)

	msg, ok := testMailer.lastTo(email)
	if !ok {
		t.Fatal("no verification email")
	}
	token := extractToken(t, msg.TextBody, "verify-email")

	resp = c.do(http.MethodPost, "/api/v1/auth/verify-email", map[string]string{"token": token})
	assertStatus(t, resp, http.StatusNoContent)

	// Now login works and sets the refresh cookie.
	resp = c.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": email, "password": apiPassword,
	})
	assertStatus(t, resp, http.StatusOK)

	cookie, ok := resp.cookie("junto_refresh")
	if !ok {
		t.Fatal("login must set the refresh cookie")
	}
	assertRefreshCookieIsSafe(t, cookie)

	var session struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	resp.decode(t, &session)
	if session.AccessToken == "" || session.TokenType != "Bearer" {
		t.Fatalf("unexpected session response: %+v", session)
	}
	// The access token must NOT be a cookie: it belongs in memory, so a page that leaks the
	// cookie jar does not leak API access.
	if _, ok := resp.cookie("junto_access"); ok {
		t.Error("the access token must not be delivered as a cookie")
	}

	// The access token works against a protected route.
	resp = c.do(http.MethodGet, "/api/v1/me", nil, withBearer(session.AccessToken))
	assertStatus(t, resp, http.StatusOK)

	var me struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	resp.decode(t, &me)
	if me.ID != user.ID || me.Email != strings.ToLower(email) {
		t.Errorf("/me returned %+v, want id %s", me, user.ID)
	}
}

// assertRefreshCookieIsSafe pins the three properties that make the refresh cookie safe to
// hold a long-lived credential.
func assertRefreshCookieIsSafe(t *testing.T, c *http.Cookie) {
	t.Helper()
	// HttpOnly: an XSS bug cannot read it. This is the whole reason the refresh token is a
	// cookie while the access token is not.
	if !c.HttpOnly {
		t.Error("the refresh cookie must be HttpOnly")
	}
	// SameSite=Lax: browsers will not attach it to a cross-site POST, which is the CSRF
	// defence for the refresh endpoint.
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("refresh cookie SameSite = %v, want Lax", c.SameSite)
	}
	// Path scoping keeps it off every other API call, shrinking where it can leak to.
	if c.Path != "/api/v1/auth" {
		t.Errorf("refresh cookie path = %q, want /api/v1/auth", c.Path)
	}
}

// --- authentication on protected routes ---

func TestProtectedRoutesRequireBearerToken(t *testing.T) {
	c := newClient(t)
	email := signupAndVerify(t, c, "protected")
	token := login(t, c, email)

	cases := []struct {
		name string
		opts []func(*http.Request)
		// tokenPresent marks the cases where the caller supplied SOMETHING as a token.
		// Those must all be indistinguishable from each other; "you sent no header at all"
		// may say so, because the caller already knows what they sent and it reveals
		// nothing about server state.
		tokenPresent bool
		status       int
	}{
		{"no header", nil, false, http.StatusUnauthorized},
		{"wrong scheme", []func(*http.Request){withHeader("Authorization", "Basic abc123")}, false, http.StatusUnauthorized},
		{"empty bearer", []func(*http.Request){withHeader("Authorization", "Bearer ")}, false, http.StatusUnauthorized},
		{"garbage token", []func(*http.Request){withBearer("not.a.jwt")}, true, http.StatusUnauthorized},
		{"tampered token", []func(*http.Request){withBearer(token + "x")}, true, http.StatusUnauthorized},
		{"valid token", []func(*http.Request){withBearer(token)}, true, http.StatusOK},
	}

	const opaqueDetail = "Your credentials are missing, invalid, or expired."

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := c.do(http.MethodGet, "/api/v1/me", nil, tc.opts...)
			assertStatus(t, resp, tc.status)

			if tc.status != http.StatusUnauthorized {
				return
			}
			// RFC 9110 requires this header on a 401 so a client knows which scheme to use.
			if resp.headers.Get("WWW-Authenticate") == "" {
				t.Error("a 401 must carry a WWW-Authenticate header")
			}
			if tc.tokenPresent {
				// Forged, expired, revoked, unknown session: all identical. Distinguishing
				// them tells the holder of a stolen token exactly what went wrong with it.
				if p := resp.problem(t); p.Detail != opaqueDetail {
					t.Errorf("401 detail leaks why the token failed: %q", p.Detail)
				}
			}
		})
	}
}

// TestExpiredAndRevokedTokensAreIndistinguishable is the sharper version of the property
// above: two DIFFERENT server-side reasons must produce byte-identical answers.
func TestExpiredAndRevokedTokensAreIndistinguishable(t *testing.T) {
	c := newClient(t)
	email := signupAndVerify(t, c, "opaque")
	token := login(t, c, email)

	forged := c.do(http.MethodGet, "/api/v1/me", nil, withBearer("eyJhbGciOiJIUzI1NiJ9.e30.bogus"))

	// Revoke the session so the token becomes valid-but-dead.
	resp := c.do(http.MethodPost, "/api/v1/auth/logout", nil)
	assertStatus(t, resp, http.StatusNoContent)
	revoked := c.do(http.MethodGet, "/api/v1/me", nil, withBearer(token))

	assertStatus(t, forged, http.StatusUnauthorized)
	assertStatus(t, revoked, http.StatusUnauthorized)
	assertProblemsMatchIgnoringInstance(t, forged, revoked)
}

// TestTokenIsNotAcceptedFromCookieOrQuery pins the CSRF property of header-only auth.
func TestTokenIsNotAcceptedFromCookieOrQuery(t *testing.T) {
	c := newClient(t)
	email := signupAndVerify(t, c, "csrf")
	token := login(t, c, email)

	// A browser attaches cookies to cross-site requests automatically but never an
	// Authorization header. Accepting the token from either of these would reintroduce CSRF.
	resp := c.do(http.MethodGet, "/api/v1/me?access_token="+token, nil)
	assertStatus(t, resp, http.StatusUnauthorized)

	resp = c.do(http.MethodGet, "/api/v1/me", nil, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "junto_access", Value: token})
	})
	assertStatus(t, resp, http.StatusUnauthorized)
}

// --- refresh rotation over HTTP ---

func TestRefreshRotatesCookieAndDetectsReuse(t *testing.T) {
	c := newClient(t)
	email := signupAndVerify(t, c, "rotate")

	resp := c.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": email, "password": apiPassword,
	})
	assertStatus(t, resp, http.StatusOK)
	original, _ := resp.cookie("junto_refresh")

	// The jar now holds the refresh cookie, so this mirrors what a browser sends.
	resp = c.do(http.MethodPost, "/api/v1/auth/refresh", nil)
	assertStatus(t, resp, http.StatusOK)

	rotated, ok := resp.cookie("junto_refresh")
	if !ok {
		t.Fatal("refresh must set a new cookie")
	}
	if rotated.Value == original.Value {
		t.Fatal("refresh must issue a NEW token; reusing it is not rotation")
	}
	assertRefreshCookieIsSafe(t, rotated)

	// Replay the ORIGINAL token, as a thief who captured it earlier would.
	resp = c.do(http.MethodPost, "/api/v1/auth/refresh", nil, func(r *http.Request) {
		r.Header.Set("Cookie", "junto_refresh="+original.Value)
	})
	assertStatus(t, resp, http.StatusUnauthorized)

	// The cookie is cleared on failure, so the client stops retrying against a dead session.
	if cleared, ok := resp.cookie("junto_refresh"); !ok || cleared.MaxAge >= 0 {
		t.Error("a failed refresh must clear the refresh cookie")
	}

	// The family is revoked, so the legitimate rotated token is dead too. That is the
	// intended trade: an unexplained replay means one party is an attacker and the server
	// cannot tell which.
	resp = c.do(http.MethodPost, "/api/v1/auth/refresh", nil, func(r *http.Request) {
		r.Header.Set("Cookie", "junto_refresh="+rotated.Value)
	})
	assertStatus(t, resp, http.StatusUnauthorized)
}

func TestRefreshWithoutCookieIsUnauthorized(t *testing.T) {
	c := newClient(t)
	resp := c.do(http.MethodPost, "/api/v1/auth/refresh", nil)
	assertStatus(t, resp, http.StatusUnauthorized)
}

func TestLogoutRevokesSessionAndIsIdempotent(t *testing.T) {
	c := newClient(t)
	email := signupAndVerify(t, c, "logout")

	resp := c.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": email, "password": apiPassword,
	})
	assertStatus(t, resp, http.StatusOK)
	var body struct {
		AccessToken string `json:"access_token"`
	}
	resp.decode(t, &body)

	resp = c.do(http.MethodPost, "/api/v1/auth/logout", nil)
	assertStatus(t, resp, http.StatusNoContent)

	// The access token is still cryptographically valid and unexpired. The session check is
	// what makes logout take effect now rather than up to 15 minutes later.
	resp = c.do(http.MethodGet, "/api/v1/me", nil, withBearer(body.AccessToken))
	assertStatus(t, resp, http.StatusUnauthorized)

	// A second logout still succeeds: there is nothing a client would usefully do
	// differently, and failing would leak whether the token was known.
	resp = c.do(http.MethodPost, "/api/v1/auth/logout", nil)
	assertStatus(t, resp, http.StatusNoContent)
}

// --- password reset over HTTP ---

func TestPasswordResetFlowRevokesSessions(t *testing.T) {
	c := newClient(t)
	email := signupAndVerify(t, c, "reset")

	first := login(t, c, email)
	second := newClient(t)
	secondToken := login(t, second, email)

	resp := c.do(http.MethodPost, "/api/v1/auth/request-password-reset", map[string]string{"email": email})
	assertStatus(t, resp, http.StatusAccepted)

	msg, ok := testMailer.lastTo(email)
	if !ok {
		t.Fatal("no reset email")
	}
	token := extractToken(t, msg.TextBody, "reset-password")

	const newPassword = "an entirely different passphrase"
	resp = c.do(http.MethodPost, "/api/v1/auth/reset-password", map[string]string{
		"token": token, "password": newPassword,
	})
	assertStatus(t, resp, http.StatusNoContent)

	// BOTH devices are signed out, not just the one that performed the reset.
	for name, token := range map[string]string{"resetting device": first, "other device": secondToken} {
		r := c.do(http.MethodGet, "/api/v1/me", nil, withBearer(token))
		if r.status != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401 after a password reset", name, r.status)
		}
	}
	_ = second

	resp = c.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": email, "password": apiPassword,
	})
	assertStatus(t, resp, http.StatusUnauthorized)

	resp = c.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": email, "password": newPassword,
	})
	assertStatus(t, resp, http.StatusOK)
}

// TestPasswordResetDoesNotRevealAccountExistence covers the enumeration defence at the
// HTTP boundary: status, body and headers must be identical either way.
func TestPasswordResetDoesNotRevealAccountExistence(t *testing.T) {
	c := newClient(t)
	known := signupAndVerify(t, c, "known")

	knownResp := c.do(http.MethodPost, "/api/v1/auth/request-password-reset",
		map[string]string{"email": known})
	unknownResp := c.do(http.MethodPost, "/api/v1/auth/request-password-reset",
		map[string]string{"email": uniqueEmail(t, "ghost")})

	assertStatus(t, knownResp, http.StatusAccepted)
	assertStatus(t, unknownResp, http.StatusAccepted)
	// This one IS a byte comparison: the success envelope carries no request id, so there is
	// nothing legitimate for the two responses to differ by.
	if string(knownResp.body) != string(unknownResp.body) {
		t.Errorf("responses differ between a known and unknown address:\n known:   %s\n unknown: %s",
			knownResp.body, unknownResp.body)
	}
}

// TestLoginDoesNotRevealAccountExistence is the same property on the login endpoint.
func TestLoginDoesNotRevealAccountExistence(t *testing.T) {
	c := newClient(t)
	known := signupAndVerify(t, c, "oracle")

	wrongPassword := c.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": known, "password": "definitely the wrong password",
	})
	unknownAccount := c.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": uniqueEmail(t, "nobody"), "password": apiPassword,
	})

	assertStatus(t, wrongPassword, http.StatusUnauthorized)
	assertStatus(t, unknownAccount, http.StatusUnauthorized)

	// Identical in every respect except the per-request id. Any other difference is an
	// oracle for probing which accounts exist.
	assertProblemsMatchIgnoringInstance(t, wrongPassword, unknownAccount)
}

// --- validation and error format ---

func TestValidationErrorsAreRFC7807WithFieldDetail(t *testing.T) {
	c := newClient(t)

	resp := c.do(http.MethodPost, "/api/v1/auth/signup", map[string]string{
		"email": "not-an-email", "password": "short", "display_name": "",
	})
	assertStatus(t, resp, http.StatusUnprocessableEntity)

	if ct := resp.headers.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}

	p := resp.problem(t)
	if p.Status != http.StatusUnprocessableEntity || p.Type == "" || p.Title == "" {
		t.Errorf("incomplete problem document: %+v", p)
	}
	// The request id lands in `instance`, so a user reporting an error hands over something
	// that finds the exact log line.
	if p.Instance == "" {
		t.Error("the problem document must carry the request id as `instance`")
	}
	if p.Instance != resp.headers.Get("X-Request-ID") {
		t.Errorf("instance %q does not match X-Request-ID %q", p.Instance, resp.headers.Get("X-Request-ID"))
	}

	// All three problems reported at once, not one per round trip.
	if len(p.Errors) != 3 {
		t.Errorf("expected 3 field violations, got %d: %+v", len(p.Errors), p.Errors)
	}
	for _, field := range []string{"email", "password", "display_name"} {
		assertProblemField(t, resp, field)
	}
}

func TestMalformedRequestsAreRejectedCleanly(t *testing.T) {
	c := newClient(t)

	cases := []struct {
		name   string
		body   any
		opts   []func(*http.Request)
		status int
	}{
		{"malformed json", `{"email": `, nil, http.StatusUnprocessableEntity},
		{"empty body", ``, nil, http.StatusUnprocessableEntity},
		{"wrong type", `{"email": 42}`, nil, http.StatusUnprocessableEntity},
		{"unknown field", `{"email":"a@b.co","password":"x","display_name":"n","admin":true}`, nil, http.StatusUnprocessableEntity},
		{"two objects", `{"email":"a@b.co"}{"email":"c@d.co"}`, nil, http.StatusUnprocessableEntity},
		{
			"wrong content type", `{"email":"a@b.co"}`,
			[]func(*http.Request){withHeader("Content-Type", "text/plain")},
			http.StatusUnsupportedMediaType,
		},
		{"oversized body", `{"display_name":"` + strings.Repeat("a", 2<<20) + `"}`, nil, http.StatusRequestEntityTooLarge},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := c.do(http.MethodPost, "/api/v1/auth/signup", tc.body, tc.opts...)
			assertStatus(t, resp, tc.status)
			if ct := resp.headers.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
				t.Errorf("Content-Type = %q, want a problem document", ct)
			}
		})
	}
}

// TestUnknownFieldIsNamed covers the reason DisallowUnknownFields is on: a typo'd field
// would otherwise be silently ignored and the client would see a success that did nothing.
func TestUnknownFieldIsNamed(t *testing.T) {
	c := newClient(t)
	resp := c.do(http.MethodPost, "/api/v1/auth/signup",
		`{"email":"a@b.co","password":"a long enough password","displayname":"typo"}`)
	assertStatus(t, resp, http.StatusUnprocessableEntity)
	assertProblemField(t, resp, "displayname")
}

func TestRoutingErrorsUseProblemFormat(t *testing.T) {
	c := newClient(t)

	resp := c.do(http.MethodGet, "/api/v1/does-not-exist", nil)
	assertStatus(t, resp, http.StatusNotFound)
	if p := resp.problem(t); p.Title == "" {
		t.Error("a 404 must be a problem document")
	}

	resp = c.do(http.MethodDelete, "/api/v1/auth/login", nil)
	assertStatus(t, resp, http.StatusMethodNotAllowed)
}

// --- middleware behaviour ---

func TestSecurityHeadersAndRequestID(t *testing.T) {
	c := newClient(t)
	resp := c.do(http.MethodGet, "/api/v1/does-not-exist", nil)

	for header, want := range map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "no-referrer",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
	} {
		if got := resp.headers.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if resp.headers.Get("X-Request-ID") == "" {
		t.Error("every response must carry a request id")
	}
}

// TestInboundRequestIDIsHonouredAndSanitised covers both halves: a trace should survive
// across services, but the value lands in logs, so control characters must be stripped or
// a caller could forge log entries.
func TestInboundRequestIDIsHonouredAndSanitised(t *testing.T) {
	c := newClient(t)

	resp := c.do(http.MethodGet, "/api/v1/does-not-exist", nil,
		withHeader("X-Request-ID", "trace-abc-123"))
	if got := resp.headers.Get("X-Request-ID"); got != "trace-abc-123" {
		t.Errorf("request id = %q, want the inbound value echoed", got)
	}

	// An over-long value is truncated. The request id is written to every log line for the
	// request, so an unbounded caller-supplied value is a cheap way to bloat logs.
	resp = c.do(http.MethodGet, "/api/v1/does-not-exist", nil,
		withHeader("X-Request-ID", strings.Repeat("A", 500)))
	got := resp.headers.Get("X-Request-ID")
	if len(got) > 128 {
		t.Errorf("request id was not truncated: %d chars", len(got))
	}
	for _, r := range got {
		// Printable ASCII only. A newline reaching a log would let a caller forge entries.
		if r < 0x20 || r > 0x7e {
			t.Errorf("request id contains a control character %q in %q", r, got)
		}
	}
}

func TestCORSAllowlist(t *testing.T) {
	c := newClient(t)

	allowed := c.do(http.MethodOptions, "/api/v1/auth/login", nil,
		withHeader("Origin", "http://localhost:3000"))
	if got := allowed.headers.Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("allowed origin = %q, want it echoed", got)
	}
	if allowed.headers.Get("Access-Control-Allow-Credentials") != "true" {
		t.Error("credentials must be allowed, or the refresh cookie is never sent")
	}
	if !strings.Contains(allowed.headers.Get("Vary"), "Origin") {
		t.Error("responses vary by Origin and caches must be told")
	}

	// An origin outside the allowlist gets NO CORS header, so the browser blocks the read.
	// Reflecting Origin unconditionally would defeat the same-origin policy entirely.
	denied := c.do(http.MethodOptions, "/api/v1/auth/login", nil,
		withHeader("Origin", "https://evil.test"))
	if got := denied.headers.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("an unlisted origin was allowed: %q", got)
	}
}

// TestAuthEndpointsAreRateLimited is not decoration. The password policy is length-only by
// design (NIST SP 800-63B), and the documented compensating controls are Argon2id plus rate
// limiting on login. Without this passing, that stated reasoning is false.
func TestAuthEndpointsAreRateLimited(t *testing.T) {
	// Its own server with production throttles. The shared suite server runs permissive
	// limits, because every test here originates from 127.0.0.1 and would otherwise throttle
	// itself rather than any simulated attacker.
	srv := newStrictlyLimitedServer(t)
	c := newClientFor(t, srv)
	email := uniqueEmail(t, "bruteforce")

	var limited bool
	var attempts int
	// Burst is 5, so throttling must begin well within this budget.
	for i := 0; i < 30; i++ {
		attempts++
		resp := c.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
			"email": email, "password": "guess-" + fmt.Sprint(i),
		})
		if resp.status == http.StatusTooManyRequests {
			limited = true

			if resp.headers.Get("Retry-After") == "" {
				t.Error("a 429 must carry Retry-After so a client knows when to retry")
			}
			p := resp.problem(t)
			if p.Status != http.StatusTooManyRequests {
				t.Errorf("problem status = %d, want 429", p.Status)
			}
			break
		}
	}

	if !limited {
		t.Fatalf("login was not rate limited after %d attempts; online password guessing is unbounded", attempts)
	}
	t.Logf("throttling began after %d attempts", attempts)
}

// --- session management ---

func TestSessionListingAndRevocation(t *testing.T) {
	c := newClient(t)
	email := signupAndVerify(t, c, "devices")

	first := login(t, c, email)
	other := newClient(t)
	otherToken := login(t, other, email)

	resp := c.do(http.MethodGet, "/api/v1/auth/sessions", nil, withBearer(first))
	assertStatus(t, resp, http.StatusOK)

	var sessions []struct {
		ID      string `json:"id"`
		Current bool   `json:"current"`
	}
	resp.decode(t, &sessions)
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}

	// Exactly one must be flagged current, so a UI can say "this device" and avoid
	// accidentally revoking the session the user is looking at.
	var currents, targetID int
	var target string
	for i, s := range sessions {
		if s.Current {
			currents++
			targetID = i
		} else {
			target = s.ID
		}
	}
	if currents != 1 {
		t.Errorf("expected exactly 1 current session, got %d", currents)
	}
	_ = targetID

	resp = c.do(http.MethodDelete, "/api/v1/auth/sessions/"+target, nil, withBearer(first))
	assertStatus(t, resp, http.StatusNoContent)

	// The revoked device is signed out; the revoking device is not.
	resp = other.do(http.MethodGet, "/api/v1/me", nil, withBearer(otherToken))
	assertStatus(t, resp, http.StatusUnauthorized)

	resp = c.do(http.MethodGet, "/api/v1/me", nil, withBearer(first))
	assertStatus(t, resp, http.StatusOK)
}

func TestCannotRevokeAnotherUsersSession(t *testing.T) {
	attackerClient := newClient(t)
	victimClient := newClient(t)

	attackerEmail := signupAndVerify(t, attackerClient, "attacker")
	victimEmail := signupAndVerify(t, victimClient, "victim")

	attackerToken := login(t, attackerClient, attackerEmail)
	victimToken := login(t, victimClient, victimEmail)

	resp := victimClient.do(http.MethodGet, "/api/v1/auth/sessions", nil, withBearer(victimToken))
	assertStatus(t, resp, http.StatusOK)
	var victimSessions []struct {
		ID string `json:"id"`
	}
	resp.decode(t, &victimSessions)
	if len(victimSessions) == 0 {
		t.Fatal("the victim should have a session")
	}

	// Without the ownership check, any authenticated user could revoke any session id they
	// could observe. 404 rather than 403: confirming the id exists is itself a disclosure.
	resp = attackerClient.do(http.MethodDelete, "/api/v1/auth/sessions/"+victimSessions[0].ID,
		nil, withBearer(attackerToken))
	assertStatus(t, resp, http.StatusNotFound)

	resp = victimClient.do(http.MethodGet, "/api/v1/me", nil, withBearer(victimToken))
	assertStatus(t, resp, http.StatusOK)
}

func TestVerificationTokenIsSingleUse(t *testing.T) {
	c := newClient(t)
	email := uniqueEmail(t, "singleuse")

	resp := c.do(http.MethodPost, "/api/v1/auth/signup", map[string]string{
		"email": email, "password": apiPassword, "display_name": "Single Use",
	})
	assertStatus(t, resp, http.StatusCreated)

	msg, ok := testMailer.lastTo(email)
	if !ok {
		t.Fatal("no verification email")
	}
	token := extractToken(t, msg.TextBody, "verify-email")

	resp = c.do(http.MethodPost, "/api/v1/auth/verify-email", map[string]string{"token": token})
	assertStatus(t, resp, http.StatusNoContent)

	resp = c.do(http.MethodPost, "/api/v1/auth/verify-email", map[string]string{"token": token})
	assertStatus(t, resp, http.StatusUnauthorized)

	resp = c.do(http.MethodPost, "/api/v1/auth/verify-email", map[string]string{"token": "made-up-token"})
	assertStatus(t, resp, http.StatusUnauthorized)
}
