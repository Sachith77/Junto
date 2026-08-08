package ws

import (
	"context"
	"log/slog"
	"sync"

	"github.com/junto/junto/internal/domain"
)

// Registry holds every live connection on this instance and closes the ones whose session has
// been revoked (D91, closing D73).
//
// # Why this lives here and not in the sync engine
//
// It is the same split as the broker's, made for the same reason. The broker owns rooms because
// rooms are a sync concept; this owns sockets because a socket is a transport concept, and
// "close the connections belonging to a dead session" is a statement about sockets. The sync
// engine has no idea sessions exist and must not learn — its arch test forbids it importing
// anything that would let it find out.
//
// # The shape, which mirrors the broker exactly
//
//	Publish(event)  ->  close matching LOCAL sockets synchronously
//	                ->  hand the event to peers, best effort
//	Run()           ->  receive peers' events, close matching LOCAL sockets
//
// Local closure is synchronous and independent of Redis, so a single-instance deployment gets
// the full guarantee with NoopRevocationTransport and a Redis outage degrades cross-instance
// revocation without touching the instance that processed the logout. That is the same
// degradation story as operation fan-out, and it is deliberate: the failure mode of the
// optional infrastructure should be "the other instances are late", never "this one is wrong".
type Registry struct {
	log   *slog.Logger
	peers domain.RevocationTransport

	mu sync.RWMutex
	// conns is keyed by connection id. Indexing by session would be a smaller structure but a
	// worse one: a user-scoped revocation (a password reset) has no session to look up, and a
	// second index kept in step with the first is a bug waiting for a disconnect to race a
	// logout. The connection count per instance is bounded by the connection limit, so a scan
	// on a revocation — an event that happens at human frequency — is not worth optimising.
	conns map[domain.ID]*conn
}

var _ domain.RevocationPublisher = (*Registry)(nil)

// NewRegistry builds a connection registry.
//
// A nil transport means single-instance: local closure only, which is complete for that
// topology rather than degraded.
func NewRegistry(peers domain.RevocationTransport, log *slog.Logger) *Registry {
	if log == nil {
		log = slog.Default()
	}
	if peers == nil {
		peers = domain.NoopRevocationTransport{}
	}
	return &Registry{log: log, peers: peers, conns: map[domain.ID]*conn{}}
}

func (r *Registry) add(c *conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.conns[c.id] = c
}

func (r *Registry) remove(c *conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.conns, c.id)
}

// Count reports how many connections this instance is holding. Used by tests and available for
// a future readiness endpoint.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.conns)
}

// Publish closes matching local sockets, then tells the other instances.
//
// The ordering is the point. Closing first means the instance that handled the logout has
// already honoured it by the time anything touches the network, so the guarantee on THIS
// instance does not depend on Redis being reachable. It also means a publish failure is a
// degraded fan-out rather than a failed revocation, which is why this signature has no error to
// return — see domain.RevocationPublisher.
func (r *Registry) Publish(ctx context.Context, ev domain.RevocationEvent) {
	closed := r.closeMatching(ev)

	if err := r.peers.Publish(ctx, ev); err != nil {
		// WARN, not ERROR on the request path, and deliberately not fatal: the session is
		// already revoked in the database, so every future HTTP request fails regardless. What
		// is lost is the promptness of closing a socket on another instance, which the
		// connection lifetime cap still bounds.
		r.log.Warn("could not tell peer instances about a revocation; their sockets will close "+
			"on the connection lifetime cap instead",
			"scope", ev.Scope, "user_id", ev.UserID, "error", err)
	}
	if closed > 0 {
		r.log.Info("closed connections for a revoked session",
			"scope", ev.Scope, "user_id", ev.UserID, "reason", ev.Reason, "connections", closed)
	}
}

// Run closes local sockets for revocations published by other instances.
func (r *Registry) Run(ctx context.Context) error {
	return r.peers.Run(ctx, func(ev domain.RevocationEvent) {
		if closed := r.closeMatching(ev); closed > 0 {
			r.log.Info("closed connections for a session revoked on another instance",
				"scope", ev.Scope, "user_id", ev.UserID, "reason", ev.Reason, "connections", closed)
		}
	})
}

// closeMatching tells every affected connection to go away, and returns how many it found.
//
// The matching rule itself is domain.RevocationEvent.Matches, not a condition written here.
// That is not ceremony: whether a logged-out credential keeps working is a domain rule, and a
// second copy of it living in the transport is how the two would eventually disagree.
func (r *Registry) closeMatching(ev domain.RevocationEvent) int {
	r.mu.RLock()
	var doomed []*conn
	for _, c := range r.conns {
		if ev.Matches(c.userID, c.sessionID) {
			doomed = append(doomed, c)
		}
	}
	r.mu.RUnlock()

	// Collected under the read lock and closed outside it. Closing calls into the connection,
	// which unregisters itself — taking the write lock — and doing that while still holding the
	// read lock is a deadlock.
	for _, c := range doomed {
		c.revoke(ev.Reason)
	}
	return len(doomed)
}
