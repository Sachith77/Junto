package domain

import (
	"context"
	"time"
)

// Session revocation, and why it needs a channel of its own (D73, closed by D91).
//
// # The gap this closes
//
// A WebSocket's session is verified once, at the handshake, by redeeming a ticket. Nothing
// re-checked it afterwards. Membership and capability WERE still enforced on every frame — a
// member removed from a trip lost access immediately — but session liveness was not: a user who
// logged out, or whose sessions were revoked by a password reset, kept a working socket for
// trips they were still a member of, and could read and write through it until it closed or the
// 12-hour lifetime cap fired.
//
// That was the one place in this codebase where a credential outlived its revocation, and it
// was left open deliberately rather than patched badly. The only fix available before Redis was
// polling the session table once per connection per interval — a query per socket on the
// hottest resource in the system, to shrink a window the lifetime cap already bounded. The
// correct fix needs a way to tell EVERY instance that a session died, which is exactly what the
// peer channel added in Slice 2 provides.
//
// # Why this is a separate port rather than another op kind
//
// It would have been possible to push revocations through OpTransport as a pseudo-operation.
// That would be wrong in a way worth naming: the operation log is a per-trip, gapless,
// replayable record of ITINERARY facts, and a revocation is none of those things. It is not
// scoped to a trip, it must never be replayed to a resyncing client, and folding it would mean
// the Replica had to know what a session is. Two channels carrying two different kinds of fact
// is the honest shape.

// RevokeScope distinguishes "this one session" from "everything this user has".
type RevokeScope string

const (
	// RevokeScopeSession ends one session — a logout, a user revoking a device, or refresh
	// token reuse detection killing one family.
	RevokeScopeSession RevokeScope = "session"

	// RevokeScopeUser ends every session a user holds. This is what a password reset means,
	// and it is the case where getting the fan-out wrong matters most: a reset that leaves an
	// attacker's socket alive tells the victim they have fixed a problem they have not.
	RevokeScopeUser RevokeScope = "user"
)

// RevocationEvent announces that a credential is no longer valid.
//
// SessionID is set only for RevokeScopeSession. UserID is always set, because every listener
// needs it: a user-scoped revocation has nothing else to match on, and a session-scoped one
// still wants it for logging and for the cheap pre-filter.
type RevocationEvent struct {
	Scope     RevokeScope
	UserID    ID
	SessionID ID
	Reason    string
	At        time.Time
}

// Matches reports whether a connection held by (userID, sessionID) is killed by this event.
//
// The matching rule lives here, in the layer with the strictest import allowlist, rather than
// in the transport that holds the sockets. It is one line, but it is the line that decides
// whether a logged-out credential keeps working — and a second copy of it living somewhere else
// is how the two would eventually disagree.
func (e RevocationEvent) Matches(userID, sessionID ID) bool {
	if e.UserID != userID {
		return false
	}
	return e.Scope == RevokeScopeUser || e.SessionID == sessionID
}

// RevocationPublisher is told that a session died, so live connections using it can be closed.
//
// # Why this returns no error
//
// Same reasoning as OpPublisher, reached from the opposite direction. There, a failed broadcast
// is recoverable because the log is the real guarantee. Here there is no log to fall back on —
// but a revocation that cannot be delivered must still not fail the LOGOUT, because refusing to
// log a user out on the grounds that their socket could not be closed leaves them strictly
// worse off: session revoked in the database is what stops the next request, and it has already
// happened by the time this is called.
//
// The compensating control for an undelivered revocation is unchanged and stated plainly: the
// connection lifetime cap in internal/transport/ws/conn.go. It is a bound on a failure of this
// channel, not on the normal case.
type RevocationPublisher interface {
	Publish(ctx context.Context, ev RevocationEvent)
}

// NoopRevocationPublisher is the wiring for a deployment with no sync transport at all — a
// REST-only API, and most service tests. Named rather than nil so that "nothing listens for
// revocations here" is a decision at the construction site instead of a nil check on every
// revocation path.
type NoopRevocationPublisher struct{}

// Publish discards the event. The session is already revoked in the database, which is what
// stops the next HTTP request either way.
func (NoopRevocationPublisher) Publish(context.Context, RevocationEvent) {}

// RevocationTransport carries revocations BETWEEN server instances.
//
// The socket to close may be on any instance, and the one processing the logout has no way to
// know which — so unlike OpTransport, whose per-trip channels let an instance subscribe only to
// what it hosts, this is unavoidably a broadcast to everyone. That is affordable precisely
// because it is rare: revocations happen at human frequency, not at edit frequency.
//
// Implementations must not deliver an instance its own published events. The publisher closes
// its local sockets synchronously before publishing, so a loopback would be redundant work on
// the one instance that has already finished.
type RevocationTransport interface {
	Publish(ctx context.Context, ev RevocationEvent) error
	Run(ctx context.Context, fn func(RevocationEvent)) error
	Close() error
}

// NoopRevocationTransport is the single-instance wiring: every socket is in this process, so
// local closure is the whole job and there are no peers to tell.
type NoopRevocationTransport struct{}

// Publish discards the event; there is no peer that could be holding the socket.
func (NoopRevocationTransport) Publish(context.Context, RevocationEvent) error { return nil }

// Run blocks until the context is done, so a caller can treat it uniformly with a real
// transport rather than branching on whether Redis is configured.
func (NoopRevocationTransport) Run(ctx context.Context, _ func(RevocationEvent)) error {
	<-ctx.Done()
	return ctx.Err()
}

// Close releases nothing.
func (NoopRevocationTransport) Close() error { return nil }
