// Package secrets generates and hashes the opaque, high-entropy tokens used for refresh
// tokens, email verification, password resets, and invitation links.
//
// It is deliberately separate from password hashing. Passwords are low-entropy and
// human-chosen, so they need a deliberately slow, memory-hard KDF (see internal/security).
// These tokens are 256 bits of machine-generated randomness: they cannot be guessed or
// brute-forced, so a slow KDF would add latency to every request for no security benefit.
// SHA-256 is the correct choice here precisely because it is fast.
//
// stdlib only, so it satisfies the pkg/ layering rule.
package secrets

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
)

// TokenBytes is the raw entropy per token.
//
// 32 bytes = 256 bits. The threat model is an attacker guessing a valid token by brute
// force against a live endpoint; 256 bits makes that impossible regardless of how long the
// token lives or how fast the endpoint is.
const TokenBytes = 32

// HashSize is the length of the stored hash, matching the octet_length(...) = 32 CHECK
// constraint on every token_hash column.
const HashSize = sha256.Size

// Token is a freshly generated secret.
//
// Raw is shown to the user exactly once — in an email link or a cookie — and is never
// stored. Hash is what goes in the database, so a leaked table yields nothing usable.
type Token struct {
	Raw  string
	Hash []byte
}

// New generates a token.
//
// Encoded with URL-safe base64 without padding, because these values travel in email links
// and query strings where '+', '/' and '=' would need escaping and frequently get mangled
// by mail clients that helpfully "fix" URLs.
func New() (Token, error) {
	buf := make([]byte, TokenBytes)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failing means the system entropy source is broken. There is no
		// meaningful recovery: issuing a predictable token would be worse than failing.
		return Token{}, fmt.Errorf("secrets: reading random bytes: %w", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(buf)
	return Token{Raw: raw, Hash: Hash(raw)}, nil
}

// Hash returns the stored form of a raw token.
//
// Lookup is by hash, so this must be deterministic — which also means it must never be
// salted. That is safe here only because the input is full-entropy random; salting a
// low-entropy secret is what passwords need and this is not that.
func Hash(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// Equal compares two token hashes in constant time.
//
// Database lookups by hash already avoid a timing side channel, but any in-process
// comparison of secret material should not leak how many bytes matched.
func Equal(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}
