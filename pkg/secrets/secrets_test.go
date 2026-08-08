package secrets

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestNewProducesUniqueHighEntropyTokens(t *testing.T) {
	const runs = 1000
	seen := make(map[string]bool, runs)

	for i := 0; i < runs; i++ {
		tok, err := New()
		if err != nil {
			t.Fatalf("generating token %d: %v", i, err)
		}
		if seen[tok.Raw] {
			t.Fatalf("duplicate token at iteration %d; the entropy source is broken", i)
		}
		seen[tok.Raw] = true

		if len(tok.Hash) != HashSize {
			t.Fatalf("hash is %d bytes, want %d (the DB CHECK requires exactly this)", len(tok.Hash), HashSize)
		}

		// URL-safe base64 without padding, because these values travel in email links and
		// query strings where '+', '/' and '=' get escaped or mangled by mail clients.
		if strings.ContainsAny(tok.Raw, "+/=") {
			t.Fatalf("token %q contains characters unsafe for URLs", tok.Raw)
		}
		decoded, err := base64.RawURLEncoding.DecodeString(tok.Raw)
		if err != nil {
			t.Fatalf("token is not valid raw-url base64: %v", err)
		}
		if len(decoded) != TokenBytes {
			t.Fatalf("token carries %d bytes of entropy, want %d", len(decoded), TokenBytes)
		}
	}
}

func TestHashIsDeterministicAndNotTheRawValue(t *testing.T) {
	tok, err := New()
	if err != nil {
		t.Fatalf("generating: %v", err)
	}

	// Lookup is by hash, so hashing must be deterministic — which is why it is unsalted.
	// That is only safe because the input is full-entropy random; a password could not be
	// treated this way.
	again := Hash(tok.Raw)
	if !Equal(tok.Hash, again) {
		t.Error("hashing the same token twice must produce the same result")
	}

	if string(tok.Hash) == tok.Raw {
		t.Error("the stored hash must not be the raw token")
	}
	if strings.Contains(string(tok.Hash), tok.Raw) {
		t.Error("the raw token must not be recoverable from its hash")
	}
}

func TestDifferentTokensHashDifferently(t *testing.T) {
	a, err := New()
	if err != nil {
		t.Fatalf("generating: %v", err)
	}
	b, err := New()
	if err != nil {
		t.Fatalf("generating: %v", err)
	}
	if Equal(a.Hash, b.Hash) {
		t.Fatal("distinct tokens produced the same hash")
	}
}

func TestEqualComparesFullValue(t *testing.T) {
	a := Hash("some-token")
	b := Hash("some-token")
	c := Hash("another-token")

	if !Equal(a, b) {
		t.Error("identical hashes must compare equal")
	}
	if Equal(a, c) {
		t.Error("different hashes must not compare equal")
	}
	// Length mismatch must be false, not a panic: a truncated value reaching this from a
	// corrupted row should fail closed.
	if Equal(a, a[:16]) {
		t.Error("a truncated hash must not compare equal")
	}
	if Equal(nil, nil) != true && len(a) > 0 {
		// subtle.ConstantTimeCompare returns 1 for two empty slices; documenting the edge
		// rather than relying on it implicitly.
		t.Log("note: empty-vs-empty comparison is true")
	}
}
