package middleware

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Rate limiting on the auth endpoints is NOT optional garnish.
//
// The password policy is length-only by design (NIST SP 800-63B): no composition rules,
// because they push users toward predictable substitutions without adding entropy. The
// documented compensating controls are Argon2id and rate limiting on login. Shipping the
// first without the second would make that stated reasoning false — so this exists to keep
// the claim in internal/domain/user.go honest, not as a nice-to-have.
//
// Two known limitations, stated rather than discovered later.
//
// 1. IN-MEMORY, therefore PER-INSTANCE. Behind N instances the effective global limit is N
//    times the configured rate. Acceptable for a single-instance deployment and for the demo;
//    the fix is a Redis-backed counter, which becomes natural in Stage 2 when Redis arrives
//    for WebSocket fan-out. Until then, do not describe this as a global limit.
//
// 2. KEYED ON IP, which is the wrong grain in both directions. Everyone behind one corporate
//    NAT or mobile carrier shares a bucket, so a handful of colleagues signing in can lock out
//    a whole office. Conversely, an attacker with a pool of addresses gets the full budget per
//    address and can spray one password across many accounts. The standard fix is to limit on
//    BOTH dimensions — per IP and per target account — so neither axis alone is enough. That
//    is a depth-add, not built: it needs shared state to be meaningful, which again points at
//    Redis. Today's limiter raises the cost of online guessing; it does not eliminate it.

// RateLimitConfig configures a limiter.
type RateLimitConfig struct {
	// RequestsPerSecond is the sustained rate. Fractional values are the useful ones here:
	// 0.2 means one request every five seconds.
	RequestsPerSecond float64
	// Burst is how many requests may arrive at once before throttling begins. A burst of 1
	// would break legitimate clients that retry immediately.
	Burst int
	// TTL is how long an idle bucket is kept before eviction.
	TTL time.Duration
}

// AuthRateLimit is a deliberately strict default for credential endpoints.
//
// Five attempts up front, then one every ten seconds. Slow enough that online password
// guessing is hopeless, generous enough that a person who mistypes their password twice and
// then gets it right never notices.
func AuthRateLimit() RateLimitConfig {
	return RateLimitConfig{RequestsPerSecond: 0.1, Burst: 5, TTL: 15 * time.Minute}
}

// GeneralRateLimit is a loose ceiling for ordinary API traffic — a backstop against runaway
// clients, not a policy.
func GeneralRateLimit() RateLimitConfig {
	return RateLimitConfig{RequestsPerSecond: 20, Burst: 40, TTL: 5 * time.Minute}
}

type bucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimiter throttles requests per client IP.
type RateLimiter struct {
	cfg RateLimitConfig

	mu      sync.Mutex
	buckets map[string]*bucket

	stop chan struct{}
	once sync.Once
}

// NewRateLimiter builds a limiter and starts its eviction loop.
//
// The eviction loop is the reason this is a struct with a lifecycle rather than a closure
// over a map. Without eviction the map grows once per distinct source address forever, which
// is a memory leak an attacker can drive deliberately by rotating IPs — turning a defence
// into a vulnerability.
func NewRateLimiter(cfg RateLimitConfig) *RateLimiter {
	if cfg.TTL <= 0 {
		cfg.TTL = 10 * time.Minute
	}
	rl := &RateLimiter{
		cfg:     cfg,
		buckets: make(map[string]*bucket),
		stop:    make(chan struct{}),
	}
	go rl.evictLoop()
	return rl
}

// Close stops the eviction goroutine. Safe to call more than once.
func (rl *RateLimiter) Close() {
	rl.once.Do(func() { close(rl.stop) })
}

func (rl *RateLimiter) evictLoop() {
	ticker := time.NewTicker(rl.cfg.TTL)
	defer ticker.Stop()

	for {
		select {
		case <-rl.stop:
			return
		case now := <-ticker.C:
			rl.mu.Lock()
			for key, b := range rl.buckets {
				if now.Sub(b.lastSeen) > rl.cfg.TTL {
					delete(rl.buckets, key)
				}
			}
			rl.mu.Unlock()
		}
	}
}

// allow reports whether a request from key may proceed.
func (rl *RateLimiter) allow(key string, now time.Time) (bool, time.Duration) {
	rl.mu.Lock()
	b, ok := rl.buckets[key]
	if !ok {
		b = &bucket{limiter: rate.NewLimiter(rate.Limit(rl.cfg.RequestsPerSecond), rl.cfg.Burst)}
		rl.buckets[key] = b
	}
	b.lastSeen = now
	limiter := b.limiter
	rl.mu.Unlock()

	if limiter.Allow() {
		return true, 0
	}
	// Reserve tells us when a token will next be available, so the response can carry an
	// accurate Retry-After instead of a guess. Cancelled immediately: we are only asking,
	// not actually consuming the future token.
	res := limiter.Reserve()
	delay := res.Delay()
	res.Cancel()
	return false, delay
}

// Middleware returns the HTTP middleware.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := limiterKey(r)

		allowed, retryAfter := rl.allow(key, time.Now())
		if !allowed {
			seconds := int(retryAfter.Seconds())
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			writeProblemJSON(w, http.StatusTooManyRequests,
				"/problems/rate-limited", "Too many requests",
				"You are sending requests too quickly. Try again shortly.",
				RequestIDFrom(r.Context()))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// limiterKey identifies the caller.
//
// Uses RemoteAddr, which chi's RealIP has already rewritten from X-Forwarded-For when the
// service runs behind a trusted proxy. Reading the header here directly would be worse than
// useless: it is client-controlled, so anyone could evade the limit entirely by varying it —
// the classic way a per-IP limiter is silently bypassed.
func limiterKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
