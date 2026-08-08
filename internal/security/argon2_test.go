package security

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/junto/junto/configs"
)

// Tests use TestHasher's deliberately weak parameters. Production settings cost 64 MiB and
// tens of milliseconds per call by design; a suite that hashed for real would spend nearly
// all its time in the KDF. The code paths exercised are identical.

func TestHashAndVerifyRoundTrip(t *testing.T) {
	h := TestHasher()
	ctx := context.Background()
	const password = "correct horse battery staple"

	encoded, err := h.Hash(ctx, password)
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}

	ok, needsRehash, err := h.Verify(ctx, encoded, password)
	if err != nil {
		t.Fatalf("verifying: %v", err)
	}
	if !ok {
		t.Error("the correct password must verify")
	}
	if needsRehash {
		t.Error("a hash created with the current parameters must not need rehashing")
	}

	ok, _, err = h.Verify(ctx, encoded, "wrong password")
	if err != nil {
		t.Fatalf("verifying a wrong password should not error: %v", err)
	}
	if ok {
		t.Error("an incorrect password must not verify")
	}
}

// TestHashesAreSalted is the property that makes a stolen password table expensive rather
// than instantly useful: identical passwords must not produce identical hashes, or an
// attacker learns which accounts share a password and can attack them all at once.
func TestHashesAreSalted(t *testing.T) {
	h := TestHasher()
	ctx := context.Background()
	const password = "the same password twice"

	first, err := h.Hash(ctx, password)
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}
	second, err := h.Hash(ctx, password)
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}

	if first == second {
		t.Fatal("identical passwords produced identical hashes: the salt is not random")
	}

	// Both must still verify — a salt that broke verification would be worse than none.
	for i, encoded := range []string{first, second} {
		ok, _, err := h.Verify(ctx, encoded, password)
		if err != nil || !ok {
			t.Errorf("hash %d failed to verify: ok=%v err=%v", i, ok, err)
		}
	}
}

// TestEncodedHashCarriesItsParameters is what makes raising the cost later safe.
func TestEncodedHashCarriesItsParameters(t *testing.T) {
	h := NewArgon2Hasher(configs.Argon2Config{
		MemoryKiB: 32, Iterations: 2, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	encoded, err := h.Hash(context.Background(), "password12345")
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}

	// PHC format: $argon2id$v=19$m=32,t=2,p=1$<salt>$<hash>
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=32,t=2,p=1$") {
		t.Errorf("unexpected encoding: %q", encoded)
	}
	if n := len(strings.Split(encoded, "$")); n != 6 {
		t.Errorf("expected 6 PHC segments, got %d in %q", n, encoded)
	}
}

// TestNeedsRehashOnParameterUpgrade covers the migration path: after raising the cost, old
// hashes keep verifying and are flagged for transparent upgrade at next login — the only
// moment the plaintext is available to re-hash with.
func TestNeedsRehashOnParameterUpgrade(t *testing.T) {
	weak := NewArgon2Hasher(configs.Argon2Config{
		MemoryKiB: 16, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	strong := NewArgon2Hasher(configs.Argon2Config{
		MemoryKiB: 64, Iterations: 3, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})

	const password = "a passphrase that is long enough"
	oldHash, err := weak.Hash(context.Background(), password)
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}

	// The old hash must still verify under the new configuration...
	ok, needsRehash, err := strong.Verify(context.Background(), oldHash, password)
	if err != nil {
		t.Fatalf("verifying: %v", err)
	}
	if !ok {
		t.Fatal("an old hash must still verify after a parameter upgrade")
	}
	if !needsRehash {
		t.Error("a hash weaker than current settings must be flagged for rehash")
	}

	// ...and a freshly created one must not be flagged.
	newHash, err := strong.Hash(context.Background(), password)
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}
	_, needsRehash, err = strong.Verify(context.Background(), newHash, password)
	if err != nil {
		t.Fatalf("verifying: %v", err)
	}
	if needsRehash {
		t.Error("a current-parameter hash must not be flagged for rehash")
	}

	// Downgrading configuration must NOT flag hashes as needing rehash — that would
	// silently weaken every password on next login.
	_, needsRehash, err = weak.Verify(context.Background(), newHash, password)
	if err != nil {
		t.Fatalf("verifying: %v", err)
	}
	if needsRehash {
		t.Error("a stronger-than-configured hash must not be downgraded")
	}
}

func TestMalformedHashesAreRejected(t *testing.T) {
	h := TestHasher()
	ctx := context.Background()

	valid, err := h.Hash(ctx, "a valid password here")
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}
	parts := strings.Split(valid, "$")

	cases := []struct {
		name    string
		encoded string
		want    error
	}{
		{"empty", "", ErrInvalidHashFormat},
		{"not a PHC string", "just-some-text", ErrInvalidHashFormat},
		{"too few segments", "$argon2id$v=19$m=8,t=1,p=1", ErrInvalidHashFormat},
		{"wrong algorithm", "$bcrypt$v=19$m=8,t=1,p=1$" + parts[4] + "$" + parts[5], ErrUnsupportedAlgo},
		{"wrong version", "$argon2id$v=16$m=8,t=1,p=1$" + parts[4] + "$" + parts[5], ErrIncompatibleVer},
		{"unparseable params", "$argon2id$v=19$m=x,t=y,p=z$" + parts[4] + "$" + parts[5], ErrInvalidHashFormat},
		{"corrupt salt", "$argon2id$v=19$m=8,t=1,p=1$!!!!$" + parts[5], ErrInvalidHashFormat},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, _, err := h.Verify(ctx, tc.encoded, "anything")
			if ok {
				t.Error("a malformed hash must never verify")
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("expected %v, got %v", tc.want, err)
			}
		})
	}
}

func TestEmptyPasswordIsRefused(t *testing.T) {
	// Hashing an empty string would produce a valid-looking hash for an account that can
	// never be logged into deliberately — a state worth refusing at the source.
	if _, err := TestHasher().Hash(context.Background(), ""); err == nil {
		t.Error("hashing an empty password must be refused")
	}
}

func TestContextCancellationIsHonoured(t *testing.T) {
	h := TestHasher()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := h.Hash(ctx, "password12345"); !errors.Is(err, context.Canceled) {
		t.Errorf("Hash should honour a cancelled context, got %v", err)
	}
	if _, _, err := h.Verify(ctx, "$argon2id$v=19$m=8,t=1,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaA", "x"); !errors.Is(err, context.Canceled) {
		t.Errorf("Verify should honour a cancelled context, got %v", err)
	}
}

// BenchmarkProductionHash measures the real cost. Argon2id is SUPPOSED to be slow: this
// number is the per-login latency budget and the attacker's per-guess cost, and it is the
// reason domain.PasswordHasher is a port that tests can substitute.
func BenchmarkProductionHash(b *testing.B) {
	h := NewArgon2Hasher(configs.Argon2Config{
		MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 4, SaltLength: 16, KeyLength: 32,
	})
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := h.Hash(ctx, "correct horse battery staple"); err != nil {
			b.Fatal(err)
		}
	}
}
