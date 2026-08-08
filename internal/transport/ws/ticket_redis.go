package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/junto/junto/internal/domain"
	"github.com/junto/junto/pkg/secrets"
)

// ticketKeyPrefix namespaces handshake tickets in a possibly shared Redis.
const ticketKeyPrefix = "junto:wsticket:"

// RedisTicketStore keeps handshake tickets in Redis.
//
// # Why this had to land with the second instance, not after it
//
// MemoryTicketStore is correct and fast, and it is unusable the moment a second instance
// exists: a ticket minted on instance A cannot be redeemed on instance B, so a load balancer
// without sticky sessions fails handshakes at random — and *at random* is the worst kind of
// broken, because it looks like flaky networking rather than a design limit. That was recorded
// as a Slice 1 limitation and it is the reason this is a prerequisite for the two-instance
// test rather than a tidy-up after it: the test would otherwise be measuring whether the
// clients happened to land on the right instances.
//
// # Why Redis and still not Postgres
//
// The reasoning that kept tickets out of Postgres has not changed. A ticket is a write and a
// read per handshake, on the hottest connection path in the system, for data that is garbage
// thirty seconds later. It is cache, not record — and unlike the operation log, nothing needs
// to survive a Redis restart: an unredeemed ticket costs the client one extra round trip.
type RedisTicketStore struct {
	client *redis.Client
	clock  domain.Clock
}

var _ TicketStore = (*RedisTicketStore)(nil)

// NewRedisTicketStore builds an instance-independent ticket store.
func NewRedisTicketStore(client *redis.Client, clock domain.Clock) *RedisTicketStore {
	if clock == nil {
		clock = domain.SystemClock{}
	}
	return &RedisTicketStore{client: client, clock: clock}
}

// storedTicket is the value shape. Declared separately from Ticket so that adding a field to
// the in-memory type cannot silently change what is written to a shared store that other
// instances — possibly on an older build — are reading.
type storedTicket struct {
	UserID    string    `json:"user_id"`
	SessionID string    `json:"session_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Issue mints a single-use ticket with a Redis-enforced TTL.
//
// Keyed by the ticket's HASH, not its raw value, for the same reason every other token in this
// codebase is stored hashed: a Redis dump, a MONITOR session or a replica should not yield
// usable credentials.
func (s *RedisTicketStore) Issue(ctx context.Context, userID, sessionID domain.ID) (string, time.Time, error) {
	token, err := secrets.New()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := s.clock.Now().Add(TicketTTL)

	value, err := json.Marshal(storedTicket{
		UserID: userID.String(), SessionID: sessionID.String(), ExpiresAt: expiresAt,
	})
	if err != nil {
		return "", time.Time{}, err
	}

	// The TTL is Redis's job as well as ours. Redeem re-checks expiry because the two clocks
	// can disagree, but letting the server sweep its own keys is what stops an unredeemed
	// ticket from living forever — the same leak MemoryTicketStore needs a sweeper goroutine
	// to avoid.
	if err := s.client.Set(ctx, ticketKeyPrefix+key(token.Hash), value, TicketTTL).Err(); err != nil {
		return "", time.Time{}, fmt.Errorf("storing ws ticket: %w", err)
	}
	return token.Raw, expiresAt, nil
}

// Redeem consumes a ticket, or returns ErrTokenInvalid.
//
// GETDEL is the whole point: read and delete are ONE round trip and one atomic server-side
// operation, so "single-use" holds even when two handshakes arrive at the same instant on
// different instances. A GET followed by a DEL would be the classic check-then-act race, and
// it would let a replayed ticket through under exactly the concurrency that makes a
// multi-instance deployment worth having.
//
// Expiry and absence collapse to the SAME error deliberately: distinguishing them tells an
// attacker whether a guessed value ever existed.
func (s *RedisTicketStore) Redeem(ctx context.Context, raw string) (Ticket, error) {
	value, err := s.client.GetDel(ctx, ticketKeyPrefix+key(secrets.Hash(raw))).Bytes()
	if errors.Is(err, redis.Nil) {
		return Ticket{}, domain.ErrTokenInvalid
	}
	if err != nil {
		return Ticket{}, fmt.Errorf("redeeming ws ticket: %w", err)
	}

	var stored storedTicket
	if err := json.Unmarshal(value, &stored); err != nil {
		return Ticket{}, domain.ErrTokenInvalid
	}
	if s.clock.Now().After(stored.ExpiresAt) {
		return Ticket{}, domain.ErrTokenInvalid
	}

	userID, err := domain.ParseID("user_id", stored.UserID)
	if err != nil {
		return Ticket{}, domain.ErrTokenInvalid
	}
	sessionID, err := domain.ParseID("session_id", stored.SessionID)
	if err != nil {
		return Ticket{}, domain.ErrTokenInvalid
	}
	return Ticket{UserID: userID, SessionID: sessionID, ExpiresAt: stored.ExpiresAt}, nil
}

// Close releases nothing: the client pool belongs to whoever built it. Present so the two
// stores are interchangeable at the construction site.
func (s *RedisTicketStore) Close() {}
