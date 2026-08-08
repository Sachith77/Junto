// Package security holds cryptographic adapters that implement domain ports.
//
// It sits in the same architectural position as internal/repository: an outer layer that
// depends on the domain and is depended on by nobody inside it. It is separate from
// repository because "how a password is hashed" and "how a row is stored" are different
// concerns that change for different reasons.
package security

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/junto/junto/configs"
	"github.com/junto/junto/internal/domain"
)

// Argon2Hasher implements domain.PasswordHasher using Argon2id.
//
// Argon2id rather than bcrypt: bcrypt is CPU-hard but only mildly memory-hard, which makes
// it comparatively cheap to attack on GPUs and FPGAs. Argon2id is the current OWASP
// recommendation and its memory cost is the parameter that actually degrades that hardware
// advantage. It also avoids bcrypt's 72-byte silent truncation, where two different long
// passwords can hash identically.
type Argon2Hasher struct {
	params configs.Argon2Config
}

// NewArgon2Hasher builds a hasher from configuration.
func NewArgon2Hasher(params configs.Argon2Config) *Argon2Hasher {
	return &Argon2Hasher{params: params}
}

var _ domain.PasswordHasher = (*Argon2Hasher)(nil)

// Errors returned when a stored hash cannot be interpreted.
var (
	ErrInvalidHashFormat = errors.New("security: password hash is not a valid PHC string")
	ErrUnsupportedAlgo   = errors.New("security: unsupported password hashing algorithm")
	ErrIncompatibleVer   = errors.New("security: incompatible argon2 version")
)

// Hash produces a PHC-format encoded hash:
//
//	$argon2id$v=19$m=65536,t=3,p=4$<base64 salt>$<base64 hash>
//
// The parameters are stored INSIDE the string rather than only in configuration. That is
// what makes raising the cost later safe: existing hashes keep verifying with the settings
// they were created under, and Verify reports that they should be upgraded. A hash that
// only recorded its digest would be permanently frozen at whatever cost was configured the
// day it was written.
func (h *Argon2Hasher) Hash(ctx context.Context, plaintext string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if plaintext == "" {
		return "", errors.New("security: refusing to hash an empty password")
	}

	salt := make([]byte, h.params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("security: generating salt: %w", err)
	}

	digest := argon2.IDKey(
		[]byte(plaintext), salt,
		h.params.Iterations, h.params.MemoryKiB, h.params.Parallelism, h.params.KeyLength,
	)

	return encodeHash(h.params, salt, digest), nil
}

// Verify reports whether plaintext matches encodedHash.
//
// needsRehash is true when the stored hash used weaker parameters than the current
// configuration, so a successful login can transparently upgrade it. That is the only
// moment the plaintext is available to re-hash with, which is why it is reported here
// rather than discovered by a background job.
func (h *Argon2Hasher) Verify(ctx context.Context, encodedHash, plaintext string) (bool, bool, error) {
	if err := ctx.Err(); err != nil {
		return false, false, err
	}

	stored, salt, digest, err := decodeHash(encodedHash)
	if err != nil {
		return false, false, err
	}

	candidate := argon2.IDKey(
		[]byte(plaintext), salt,
		stored.Iterations, stored.MemoryKiB, stored.Parallelism, uint32(len(digest)),
	)

	// Constant-time comparison. A byte-by-byte compare leaks, through timing, how many
	// leading bytes matched, which is enough to reconstruct the digest one byte at a time.
	if subtle.ConstantTimeCompare(digest, candidate) != 1 {
		return false, false, nil
	}

	return true, h.needsRehash(stored), nil
}

// needsRehash compares a hash's own parameters against the current configuration.
func (h *Argon2Hasher) needsRehash(stored configs.Argon2Config) bool {
	return stored.MemoryKiB < h.params.MemoryKiB ||
		stored.Iterations < h.params.Iterations ||
		stored.Parallelism < h.params.Parallelism ||
		stored.KeyLength < h.params.KeyLength
}

func encodeHash(p configs.Argon2Config, salt, digest []byte) string {
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		p.MemoryKiB, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(digest),
	)
}

func decodeHash(encoded string) (configs.Argon2Config, []byte, []byte, error) {
	var zero configs.Argon2Config

	parts := strings.Split(encoded, "$")
	// A PHC string leads with an empty segment because it starts with '$':
	// ["", "argon2id", "v=19", "m=...,t=...,p=...", salt, hash]
	if len(parts) != 6 {
		return zero, nil, nil, ErrInvalidHashFormat
	}
	if parts[1] != "argon2id" {
		return zero, nil, nil, fmt.Errorf("%w: %q", ErrUnsupportedAlgo, parts[1])
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return zero, nil, nil, ErrInvalidHashFormat
	}
	if version != argon2.Version {
		return zero, nil, nil, fmt.Errorf("%w: got %d, want %d", ErrIncompatibleVer, version, argon2.Version)
	}

	var p configs.Argon2Config
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Iterations, &p.Parallelism); err != nil {
		return zero, nil, nil, ErrInvalidHashFormat
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return zero, nil, nil, ErrInvalidHashFormat
	}
	digest, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return zero, nil, nil, ErrInvalidHashFormat
	}

	p.SaltLength = uint32(len(salt))
	p.KeyLength = uint32(len(digest))
	return p, salt, digest, nil
}

// TestHasher is a deliberately weak hasher for tests.
//
// Argon2id at production settings costs 64 MiB and tens of milliseconds per call by design.
// A test suite that creates a few hundred users would spend nearly all its time in the KDF,
// which is why domain.PasswordHasher is a port at all. This keeps the same PHC format and
// the same code paths, at a cost low enough to be free.
//
// It lives in the non-test file so integration tests in other packages can use it, and its
// parameters are far below the configuration floor Validate() enforces, so it cannot be
// selected by any valid production configuration.
func TestHasher() *Argon2Hasher {
	return &Argon2Hasher{params: configs.Argon2Config{
		MemoryKiB:   8,
		Iterations:  1,
		Parallelism: uint8(min(runtime.NumCPU(), 2)),
		SaltLength:  16,
		KeyLength:   32,
	}}
}
