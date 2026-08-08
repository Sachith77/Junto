package security

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/junto/junto/internal/domain"
)

// JWTIssuer implements domain.TokenIssuer using HS256-signed JWTs.
//
// HS256 rather than RS256 because there is exactly one verifier. Asymmetric signing pays off
// when independent services need to verify tokens without holding the signing key, which is
// not this topology; until it is, RS256 would add key management for no benefit. Swapping
// later is a key-distribution change, not an architectural one — which is the point of this
// being behind a port.
type JWTIssuer struct {
	secret []byte
	issuer string
	ttl    time.Duration
}

// NewJWTIssuer builds an issuer. The secret length is validated by configs.Validate, which
// requires at least 32 bytes to match HS256's 256-bit key.
func NewJWTIssuer(secret, issuer string, ttl time.Duration) *JWTIssuer {
	return &JWTIssuer{secret: []byte(secret), issuer: issuer, ttl: ttl}
}

var _ domain.TokenIssuer = (*JWTIssuer)(nil)

// claims is the wire representation. Standard registered claims are used wherever one
// exists, so any JWT tooling can inspect a token without knowing our schema.
type claims struct {
	jwt.RegisteredClaims
	// SessionID ties the access token to a refresh-token family, so revoking a session can
	// be checked without a separate lookup table. It is a private claim because no
	// registered claim means this.
	SessionID string `json:"sid"`
}

// Issue mints an access token for a user on a session.
func (j *JWTIssuer) Issue(userID, sessionID domain.ID, now time.Time) (string, time.Time, error) {
	expiresAt := now.Add(j.ttl)

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			Issuer:    j.issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			// NotBefore matches IssuedAt rather than being omitted: it makes a token minted
			// by a clock-skewed instance fail closed instead of being accepted early.
			NotBefore: jwt.NewNumericDate(now),
			// A unique id per token, so a future revocation list has something to key on.
			ID: domain.NewID().String(),
		},
		SessionID: sessionID.String(),
	})

	signed, err := token.SignedString(j.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("security: signing access token: %w", err)
	}
	return signed, expiresAt, nil
}

// Parse verifies and decodes an access token.
func (j *JWTIssuer) Parse(raw string) (*domain.AccessTokenClaims, error) {
	var c claims

	_, err := jwt.ParseWithClaims(raw, &c, func(t *jwt.Token) (any, error) {
		// Pinning the algorithm is not optional. Without it, a token whose header says
		// "alg": "none" — or an RS256 token verified with the public key as an HMAC secret —
		// would be accepted. This check is the entire defence against algorithm confusion.
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return j.secret, nil
	},
		jwt.WithIssuer(j.issuer),
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		// No leeway. Access tokens live 15 minutes and refresh is cheap; accepting expired
		// tokens "just a little" extends the window a stolen one stays useful.
		jwt.WithLeeway(0),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, fmt.Errorf("%w: access token expired", domain.ErrTokenExpired)
		}
		return nil, fmt.Errorf("%w: %v", domain.ErrTokenInvalid, err)
	}

	userID, err := domain.ParseID("sub", c.Subject)
	if err != nil {
		return nil, fmt.Errorf("%w: subject is not a valid id", domain.ErrTokenInvalid)
	}
	sessionID, err := domain.ParseID("sid", c.SessionID)
	if err != nil {
		return nil, fmt.Errorf("%w: session id is not a valid id", domain.ErrTokenInvalid)
	}
	if c.ExpiresAt == nil || c.IssuedAt == nil {
		return nil, fmt.Errorf("%w: missing iat or exp", domain.ErrTokenInvalid)
	}

	return &domain.AccessTokenClaims{
		UserID:    userID,
		SessionID: sessionID,
		IssuedAt:  c.IssuedAt.Time,
		ExpiresAt: c.ExpiresAt.Time,
	}, nil
}
