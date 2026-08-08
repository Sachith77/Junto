package security

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/junto/junto/internal/domain"
)

const testSecret = "a-test-signing-secret-of-at-least-32-bytes"

func newTestIssuer() *JWTIssuer {
	return NewJWTIssuer(testSecret, "junto-test", 15*time.Minute)
}

func TestIssueAndParseRoundTrip(t *testing.T) {
	issuer := newTestIssuer()
	userID, sessionID := domain.NewID(), domain.NewID()
	now := time.Now().UTC().Truncate(time.Second)

	token, expiresAt, err := issuer.Issue(userID, sessionID, now)
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}
	if !expiresAt.Equal(now.Add(15 * time.Minute)) {
		t.Errorf("expiry = %v, want %v", expiresAt, now.Add(15*time.Minute))
	}

	claims, err := issuer.Parse(token)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if claims.UserID != userID {
		t.Errorf("user id = %v, want %v", claims.UserID, userID)
	}
	if claims.SessionID != sessionID {
		t.Errorf("session id = %v, want %v", claims.SessionID, sessionID)
	}
	if !claims.ExpiresAt.Equal(expiresAt) {
		t.Errorf("claims expiry = %v, want %v", claims.ExpiresAt, expiresAt)
	}
}

// TestAlgorithmConfusionIsRejected is the most important test in this file.
//
// "alg": "none" is the canonical JWT vulnerability: a library that trusts the header will
// accept an unsigned token as valid. The other half is signing an HS256 token with a public
// key that the server treats as an HMAC secret. Pinning the method — and passing
// WithValidMethods — is the entire defence, and this proves it is actually in place.
func TestAlgorithmConfusionIsRejected(t *testing.T) {
	issuer := newTestIssuer()
	userID, sessionID := domain.NewID(), domain.NewID()

	t.Run("alg none", func(t *testing.T) {
		unsigned := jwt.NewWithClaims(jwt.SigningMethodNone, claims{
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   userID.String(),
				Issuer:    "junto-test",
				IssuedAt:  jwt.NewNumericDate(time.Now()),
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			},
			SessionID: sessionID.String(),
		})
		raw, err := unsigned.SignedString(jwt.UnsafeAllowNoneSignatureType)
		if err != nil {
			t.Fatalf("building unsigned token: %v", err)
		}

		if _, err := issuer.Parse(raw); !errors.Is(err, domain.ErrTokenInvalid) {
			t.Errorf("an unsigned token must be rejected, got %v", err)
		}
	})

	t.Run("wrong hmac size", func(t *testing.T) {
		// HS512 rather than HS256, signed with the same secret. Without method pinning this
		// would verify, and an attacker who could influence the algorithm could weaken it.
		other := jwt.NewWithClaims(jwt.SigningMethodHS512, claims{
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   userID.String(),
				Issuer:    "junto-test",
				IssuedAt:  jwt.NewNumericDate(time.Now()),
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			},
			SessionID: sessionID.String(),
		})
		raw, err := other.SignedString([]byte(testSecret))
		if err != nil {
			t.Fatalf("building HS512 token: %v", err)
		}
		if _, err := issuer.Parse(raw); !errors.Is(err, domain.ErrTokenInvalid) {
			t.Errorf("a token signed with a different algorithm must be rejected, got %v", err)
		}
	})
}

func TestTokenSignedWithAnotherSecretIsRejected(t *testing.T) {
	issuer := newTestIssuer()
	attacker := NewJWTIssuer("a-completely-different-secret-32-bytes-long", "junto-test", time.Hour)

	forged, _, err := attacker.Issue(domain.NewID(), domain.NewID(), time.Now())
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}
	if _, err := issuer.Parse(forged); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Errorf("a token from another signing key must be rejected, got %v", err)
	}
}

func TestExpiredTokenIsReportedAsExpired(t *testing.T) {
	issuer := newTestIssuer()

	// Issued far enough in the past that its 15-minute TTL has elapsed.
	token, _, err := issuer.Issue(domain.NewID(), domain.NewID(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}

	_, err = issuer.Parse(token)
	// Distinguished from ErrTokenInvalid internally so the middleware can log the real
	// reason; both still collapse to one opaque 401 at the transport boundary.
	if !errors.Is(err, domain.ErrTokenExpired) {
		t.Errorf("expected ErrTokenExpired, got %v", err)
	}
}

// TestNoClockLeeway pins the deliberate choice not to accept slightly-expired tokens.
//
// Access tokens live 15 minutes and refresh is cheap, so leeway would only extend the window
// a stolen token stays useful.
func TestNoClockLeeway(t *testing.T) {
	issuer := NewJWTIssuer(testSecret, "junto-test", time.Second)

	token, _, err := issuer.Issue(domain.NewID(), domain.NewID(), time.Now().Add(-2*time.Second))
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}
	if _, err := issuer.Parse(token); err == nil {
		t.Error("a token expired one second ago must be rejected; there is no leeway")
	}
}

func TestIssuerIsValidated(t *testing.T) {
	// A token minted for a different application must not be accepted, even with a shared
	// secret — the scenario when one signing key is reused across environments.
	other := NewJWTIssuer(testSecret, "some-other-service", time.Hour)
	token, _, err := other.Issue(domain.NewID(), domain.NewID(), time.Now())
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}
	if _, err := newTestIssuer().Parse(token); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Errorf("a token from another issuer must be rejected, got %v", err)
	}
}

func TestTamperedTokenIsRejected(t *testing.T) {
	issuer := newTestIssuer()
	token, _, err := issuer.Issue(domain.NewID(), domain.NewID(), time.Now())
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWT segments, got %d", len(parts))
	}

	cases := map[string]string{
		"tampered payload":   parts[0] + "." + parts[1] + "x." + parts[2],
		"tampered signature": parts[0] + "." + parts[1] + "." + parts[2] + "x",
		"stripped signature": parts[0] + "." + parts[1] + ".",
		"not a jwt":          "definitely-not-a-token",
		"empty":              "",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := issuer.Parse(raw); err == nil {
				t.Error("a tampered or malformed token must be rejected")
			}
		})
	}
}

func TestMalformedClaimsAreRejected(t *testing.T) {
	// A correctly signed token whose subject is not a UUID. Signature validity is not the
	// same as claim validity, and treating it as such would let a valid signature carry
	// nonsense straight into the authorization layer.
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "not-a-uuid",
			Issuer:    "junto-test",
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
		SessionID: domain.NewID().String(),
	})
	raw, err := token.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("signing: %v", err)
	}

	if _, err := newTestIssuer().Parse(raw); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Errorf("a token with a non-UUID subject must be rejected, got %v", err)
	}
}

// TestEveryTokenHasAUniqueID covers the jti claim, which exists so a future revocation list
// has something to key on.
func TestEveryTokenHasAUniqueID(t *testing.T) {
	issuer := newTestIssuer()
	userID, sessionID := domain.NewID(), domain.NewID()
	now := time.Now()

	seen := map[string]bool{}
	for i := 0; i < 10; i++ {
		raw, _, err := issuer.Issue(userID, sessionID, now)
		if err != nil {
			t.Fatalf("issuing: %v", err)
		}
		var c claims
		if _, err := jwt.ParseWithClaims(raw, &c, func(*jwt.Token) (any, error) {
			return []byte(testSecret), nil
		}); err != nil {
			t.Fatalf("parsing: %v", err)
		}
		if c.ID == "" {
			t.Fatal("every token must carry a jti")
		}
		if seen[c.ID] {
			t.Fatalf("duplicate jti %q", c.ID)
		}
		seen[c.ID] = true
	}
}
