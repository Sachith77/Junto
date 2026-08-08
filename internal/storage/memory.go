package storage

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/junto/junto/internal/domain"
)

// MemoryStorage is an in-process domain.FileStorage for tests and for running the API with
// no object store at all.
//
// It is the storage equivalent of email.LogSender: same port, no infrastructure. Tests that
// care about attachment BOOKKEEPING — that a row goes pending, that confirm verifies size,
// that the sweeper removes orphans — should not need a container to run, and a real MinIO in
// those tests would be verifying MinIO rather than our logic.
//
// It is NOT a fake of S3's semantics. The presigned URLs it returns are inert strings; there
// is no HTTP endpoint behind them. Anything that genuinely exercises the upload round trip
// belongs in an integration test against the real adapter.
type MemoryStorage struct {
	mu      sync.RWMutex
	objects map[string]memoryObject
	// Uploads records every presign call, so a test can assert that a URL was issued for the
	// key and content type it expected.
	Uploads []PresignedUpload
}

type memoryObject struct {
	data        []byte
	contentType string
	modifiedAt  time.Time
}

// PresignedUpload records one issued upload URL.
type PresignedUpload struct {
	Key         string
	ContentType string
	TTL         time.Duration
}

// NewMemoryStorage builds an empty store.
func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{objects: make(map[string]memoryObject)}
}

var _ domain.FileStorage = (*MemoryStorage)(nil)

// PresignUpload records the request and returns an inert URL.
func (m *MemoryStorage) PresignUpload(_ context.Context, key, contentType string, ttl time.Duration) (string, error) {
	if key == "" {
		return "", fmt.Errorf("storage: empty object key")
	}
	if contentType == "" {
		return "", fmt.Errorf("storage: content type is required for an upload URL")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.Uploads = append(m.Uploads, PresignedUpload{Key: key, ContentType: contentType, TTL: ttl})
	return "memory://upload/" + key, nil
}

// PresignDownload returns an inert URL, or ErrNotFound if nothing was ever put at key.
func (m *MemoryStorage) PresignDownload(_ context.Context, key string, _ time.Duration) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.objects[key]; !ok {
		return "", fmt.Errorf("storage: object %q: %w", key, domain.ErrNotFound)
	}
	return "memory://download/" + key, nil
}

// Stat returns metadata for a stored object.
func (m *MemoryStorage) Stat(_ context.Context, key string) (domain.FileInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	obj, ok := m.objects[key]
	if !ok {
		return domain.FileInfo{}, fmt.Errorf("storage: object %q: %w", key, domain.ErrNotFound)
	}
	sum := md5.Sum(obj.data) //nolint:gosec // content identity, not a security check
	return domain.FileInfo{
		Key:         key,
		SizeBytes:   int64(len(obj.data)),
		ContentType: obj.contentType,
		ChecksumMD5: hex.EncodeToString(sum[:]),
		ModifiedAt:  obj.modifiedAt,
	}, nil
}

// Delete removes an object. Idempotent, matching the port's contract.
func (m *MemoryStorage) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

// Put simulates a client completing an upload. Test-only; not part of the port, because
// production clients upload directly to storage and the API never handles the bytes.
func (m *MemoryStorage) Put(key, contentType string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = memoryObject{
		data:        append([]byte(nil), data...),
		contentType: contentType,
		modifiedAt:  time.Now().UTC(),
	}
}

// Has reports whether an object exists, for assertions about cleanup.
func (m *MemoryStorage) Has(key string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.objects[key]
	return ok
}

// ObjectCount returns how many objects are stored.
func (m *MemoryStorage) ObjectCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.objects)
}
