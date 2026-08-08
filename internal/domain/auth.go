package domain

import (
	"net/netip"
	"time"
)

// AuthSession is one device/login. It is the refresh-token *family*: every refresh token
// issued during this session descends from it, and revoking the session invalidates all of
// them at once.
//
// The family is what makes reuse detection meaningful. Without it, detecting a replayed
// token would only let you reject that one token — while the attacker who stole it keeps
// rotating happily from whatever token they obtained next.
type AuthSession struct {
	ID         ID
	UserID     ID
	UserAgent  string
	IP         *netip.Addr
	CreatedAt  time.Time
	LastUsedAt time.Time
	ExpiresAt  time.Time

	RevokedAt     *time.Time
	RevokedReason string
}

// Session revocation reasons. Stored rather than inferred, because "why did my session
// die?" is otherwise unanswerable after the fact.
const (
	RevokeReasonLogout        = "logout"
	RevokeReasonTokenReuse    = "refresh_token_reuse_detected"
	RevokeReasonPasswordReset = "password_reset"
	RevokeReasonUserRequest   = "revoked_by_user"
)

// IsActive reports whether the session may still be used at time now.
func (s *AuthSession) IsActive(now time.Time) bool {
	return s.RevokedAt == nil && now.Before(s.ExpiresAt)
}

// RefreshToken is one link in a session's rotation chain.
//
// Opaque random bytes, not a JWT. Refresh requires revocation, revocation requires a
// database lookup, and once that lookup happens a JWT buys nothing while adding a
// forgeable-if-the-key-leaks surface.
//
// Only the hash is stored. Unlike passwords these are full-entropy random values, so a
// plain SHA-256 is correct: they are not guessable, and a deliberately slow KDF would add
// latency to every refresh for no security benefit.
type RefreshToken struct {
	ID        ID
	SessionID ID
	TokenHash []byte
	IssuedAt  time.Time
	ExpiresAt time.Time

	// UsedAt is set when this token is exchanged. Presenting a token that already has
	// UsedAt set is a replay: either it was stolen, or the legitimate client has a bug.
	// Either way the correct response is to revoke the whole family.
	UsedAt *time.Time

	// ReplacedBy points at the token issued in exchange for this one, forming the chain.
	ReplacedBy *ID
}

// IsUsed reports whether the token has already been exchanged.
func (t *RefreshToken) IsUsed() bool { return t.UsedAt != nil }

// IsExpired reports whether the token is past its expiry at time now.
func (t *RefreshToken) IsExpired(now time.Time) bool { return !now.Before(t.ExpiresAt) }

// TokenPurpose discriminates the single-use token kinds.
type TokenPurpose string

const (
	TokenPurposeEmailVerify   TokenPurpose = "email_verify"
	TokenPurposePasswordReset TokenPurpose = "password_reset"
)

// Valid reports whether p is a known purpose. Kept in sync with the user_tokens_purpose
// CHECK constraint.
func (p TokenPurpose) Valid() bool {
	return p == TokenPurposeEmailVerify || p == TokenPurposePasswordReset
}

// UserToken is a single-use, hashed, short-lived token for email verification or password
// reset. One type covers both because the lifecycle — issue, deliver by email, consume
// once, expire — is genuinely identical; only the TTL and the effect differ.
type UserToken struct {
	ID         ID
	UserID     ID
	Purpose    TokenPurpose
	TokenHash  []byte
	ExpiresAt  time.Time
	ConsumedAt *time.Time
	CreatedAt  time.Time
}

// IsUsable reports whether the token may still be consumed at time now.
func (t *UserToken) IsUsable(now time.Time) bool {
	return t.ConsumedAt == nil && now.Before(t.ExpiresAt)
}

// WSTicket is a single-use, very short-lived credential for opening a WebSocket.
//
// Browsers cannot set headers on a WebSocket handshake, which leaves two common options,
// both bad: cookie auth on the upgrade drags in CSRF and cross-origin complications, and
// putting the access JWT in the query string writes a credential into access logs, proxy
// logs, and browser history.
//
// Instead the client calls an authenticated REST endpoint for a ticket and connects with
// `?ticket=...`. A 30-second TTL and single-use redemption make log leakage inert.
//
// Defined in Stage 1 because it is an auth-surface decision; issued and redeemed in
// Stage 2, when there is a hub to connect to. Nothing references it yet by design.
type WSTicket struct {
	ID        ID
	UserID    ID
	TripID    ID
	TokenHash []byte
	ExpiresAt time.Time
	IssuedAt  time.Time
}
