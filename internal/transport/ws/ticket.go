package ws

import (
	"context"
	"encoding/base64"
	"sync"
	"time"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/pkg/secrets"
)

// TicketTTL is how long a handshake ticket stays valid.
//
// Thirty seconds is the entire window between "the client asked for a ticket" and "the client
// opened a socket". Making it longer would buy nothing and would extend the life of a
// credential that travels in a query string.
const TicketTTL = 30 * time.Second

// Ticket is a single-use handshake credential (D10).
//
// # Why a ticket at all
//
// A browser cannot set an Authorization header on a WebSocket handshake — the API simply does
// not allow it. That leaves three options, and two are wrong:
//
//   - Cookies: drag in CSRF. A WebSocket handshake is NOT subject to CORS, so a cookie-
//     authenticated socket is trivially hijackable from any origin (CSWSH).
//   - The access token in the query string: JWTs are long-lived by comparison and land in
//     access logs, proxy logs and browser history, where they stay useful for their whole TTL.
//   - A single-use ticket valid for 30 seconds. A copy in a log is worthless the moment it is
//     redeemed, and it cannot be minted without the bearer token the attacker does not have.
//
// The ticket is USER-scoped, not trip-scoped: one socket serves every trip a user has open,
// and membership is re-authorized per room at subscribe time — which is where it belongs,
// since access can be revoked while a socket is open.
type Ticket struct {
	UserID    domain.ID
	SessionID domain.ID
	ExpiresAt time.Time
}

// TicketStore issues and redeems handshake tickets.
//
// A port rather than a concrete type because the storage choice changes with the deployment,
// and the swap must not touch the handshake code.
type TicketStore interface {
	// Issue mints a ticket and returns its raw value, shown exactly once.
	Issue(ctx context.Context, userID, sessionID domain.ID) (raw string, expiresAt time.Time, err error)
	// Redeem consumes a ticket ATOMICALLY. A second redemption of the same value must fail,
	// whatever the concurrency.
	Redeem(ctx context.Context, raw string) (Ticket, error)
}

// MemoryTicketStore keeps tickets in process memory.
//
// # Why not Postgres
//
// A write and a read per handshake, for data that is garbage thirty seconds later, on the
// hottest connection path in the system. Tickets are cache, not record.
//
// # The limitation, stated rather than discovered
//
// This does not survive a multi-instance deployment: a ticket minted on instance A cannot be
// redeemed on instance B, so a load balancer without sticky sessions would fail handshakes at
// random. Slice 1 is single-instance by definition. Redis replaces this in Slice 2 and the
// swap is one constructor call, which is the entire reason TicketStore is an interface.
type MemoryTicketStore struct {
	mu      sync.Mutex
	tickets map[string]Ticket
	clock   domain.Clock

	stop chan struct{}
	once sync.Once
}

// NewMemoryTicketStore builds a store and starts its sweeper.
func NewMemoryTicketStore(clock domain.Clock) *MemoryTicketStore {
	if clock == nil {
		clock = domain.SystemClock{}
	}
	s := &MemoryTicketStore{
		tickets: map[string]Ticket{},
		clock:   clock,
		stop:    make(chan struct{}),
	}
	go s.sweep()
	return s
}

var _ TicketStore = (*MemoryTicketStore)(nil)

// Issue mints a single-use ticket.
//
// The map is keyed by the ticket's HASH, not its raw value, for the same reason every other
// token in this codebase is stored hashed: a heap dump, a core file or a debugger session
// should not yield usable credentials.
func (s *MemoryTicketStore) Issue(_ context.Context, userID, sessionID domain.ID) (string, time.Time, error) {
	token, err := secrets.New()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := s.clock.Now().Add(TicketTTL)

	s.mu.Lock()
	s.tickets[key(token.Hash)] = Ticket{UserID: userID, SessionID: sessionID, ExpiresAt: expiresAt}
	s.mu.Unlock()

	return token.Raw, expiresAt, nil
}

// Redeem consumes a ticket, or returns ErrTokenInvalid.
//
// Lookup and delete happen under one lock, which is what makes single-use actually single-use
// rather than "single-use unless two handshakes arrive at the same moment". Expiry and absence
// collapse to the SAME error deliberately: distinguishing them tells an attacker whether a
// guessed value ever existed.
func (s *MemoryTicketStore) Redeem(_ context.Context, raw string) (Ticket, error) {
	k := key(secrets.Hash(raw))

	s.mu.Lock()
	t, ok := s.tickets[k]
	delete(s.tickets, k)
	s.mu.Unlock()

	if !ok || s.clock.Now().After(t.ExpiresAt) {
		return Ticket{}, domain.ErrTokenInvalid
	}
	return t, nil
}

// Close stops the sweeper.
func (s *MemoryTicketStore) Close() {
	s.once.Do(func() { close(s.stop) })
}

// sweep evicts expired tickets.
//
// Without it, a client that requests tickets and never connects grows the map without bound —
// a slow memory leak driven entirely by unauthenticated-ish client behaviour. Redeem also
// checks expiry, so this is about memory, not correctness.
func (s *MemoryTicketStore) sweep() {
	ticker := time.NewTicker(TicketTTL)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			now := s.clock.Now()
			s.mu.Lock()
			for k, t := range s.tickets {
				if now.After(t.ExpiresAt) {
					delete(s.tickets, k)
				}
			}
			s.mu.Unlock()
		}
	}
}

func key(hash []byte) string { return base64.RawURLEncoding.EncodeToString(hash) }
