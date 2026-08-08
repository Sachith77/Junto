package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/junto/junto/internal/domain"
)

// MemoryStorage is shipped code, so it gets tests. It is the storage equivalent of
// email.LogSender: same port, no infrastructure, so tests about attachment BOOKKEEPING can
// run without a container.

func TestMemoryStorageRoundTrip(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStorage()

	url, err := m.PresignUpload(ctx, "trips/a/x.png", "image/png", time.Minute)
	if err != nil {
		t.Fatalf("presigning: %v", err)
	}
	if url == "" {
		t.Error("expected a URL, even an inert one")
	}
	// The recorded call is what lets a test assert the service asked for the key and type it
	// intended, which is the part worth checking without a real server.
	if len(m.Uploads) != 1 || m.Uploads[0].Key != "trips/a/x.png" || m.Uploads[0].ContentType != "image/png" {
		t.Errorf("upload not recorded correctly: %+v", m.Uploads)
	}

	// Nothing uploaded yet, so the object does not exist.
	if _, err := m.Stat(ctx, "trips/a/x.png"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("an un-uploaded key must stat as not-found, got %v", err)
	}
	if _, err := m.PresignDownload(ctx, "trips/a/x.png", time.Minute); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("downloading an absent object must be not-found, got %v", err)
	}

	// Simulate the client completing the upload.
	m.Put("trips/a/x.png", "image/png", []byte("hello"))

	info, err := m.Stat(ctx, "trips/a/x.png")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.SizeBytes != 5 || info.ContentType != "image/png" || info.ChecksumMD5 == "" {
		t.Errorf("unexpected info: %+v", info)
	}

	if !m.Has("trips/a/x.png") {
		t.Error("Has should report the stored object")
	}
	if err := m.Delete(ctx, "trips/a/x.png"); err != nil {
		t.Fatalf("deleting: %v", err)
	}
	if m.Has("trips/a/x.png") || m.ObjectCount() != 0 {
		t.Error("the object should be gone")
	}
}

// TestMemoryStorageMatchesThePortContract pins the two behaviours a substitute must share
// with the real adapter, or tests written against it would prove the wrong thing.
func TestMemoryStorageMatchesThePortContract(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStorage()

	// Idempotent delete.
	if err := m.Delete(ctx, "never/existed"); err != nil {
		t.Errorf("deleting an absent object must not error: %v", err)
	}

	// Same input rejection as the S3 adapter.
	if _, err := m.PresignUpload(ctx, "", "image/png", time.Minute); err == nil {
		t.Error("an empty key must be rejected")
	}
	if _, err := m.PresignUpload(ctx, "k", "", time.Minute); err == nil {
		t.Error("an empty content type must be rejected")
	}
}

func TestMemoryStorageSatisfiesThePort(t *testing.T) {
	// Compile-time already guarantees this via the package-level assertion; this makes the
	// intent visible in the test output too.
	var _ domain.FileStorage = NewMemoryStorage()
}
