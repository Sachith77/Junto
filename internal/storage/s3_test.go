package storage

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"

	"github.com/junto/junto/internal/domain"
)

// The S3 adapter is tested against a real MinIO, for the same reason the repositories are
// tested against a real Postgres: the interesting behaviour is the protocol, not our code.
// A mocked S3 client would prove that we call methods, not that a presigned URL is actually
// accepted by a server — and "the signature was wrong" is exactly the bug that would ship.
//
// `go test -short` skips these; they need Docker.

var testStorage *S3Storage

const testBucket = "junto-test"

func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		log.Println("skipping storage tests in -short mode (they require Docker)")
		return
	}

	code, err := runSuite(m)
	if err != nil {
		log.Printf("storage test setup failed: %v", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runSuite(m *testing.M) (int, error) {
	ctx := context.Background()

	container, err := tcminio.Run(ctx, "minio/minio:latest",
		tcminio.WithUsername("junto"),
		tcminio.WithPassword("junto_dev_password"),
	)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			log.Printf("terminating minio: %v", err)
		}
	}()

	endpoint, err := container.ConnectionString(ctx)
	if err != nil {
		return 0, err
	}

	testStorage, err = NewS3Storage(ctx, S3Config{
		Endpoint:  endpoint,
		AccessKey: "junto",
		SecretKey: "junto_dev_password",
		Bucket:    testBucket,
		Region:    "us-east-1",
	})
	if err != nil {
		return 0, err
	}
	return m.Run(), nil
}

// TestPresignedUploadRoundTrip is the test that matters: a URL this adapter signs must be
// accepted by a real server, and the object must come back with the size and type we expect.
func TestPresignedUploadRoundTrip(t *testing.T) {
	ctx := context.Background()
	key := "trips/abc/options/def/ticket.png"
	body := []byte("not really a png, but bytes are bytes")

	uploadURL, err := testStorage.PresignUpload(ctx, key, "image/png", 5*time.Minute)
	if err != nil {
		t.Fatalf("presigning upload: %v", err)
	}
	if !strings.Contains(uploadURL, key) {
		t.Errorf("upload URL does not reference the key: %s", uploadURL)
	}

	// The client uploads directly to storage; the API never sees the bytes.
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building upload request: %v", err)
	}
	req.Header.Set("Content-Type", "image/png")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("uploading: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload returned %d: %s", resp.StatusCode, detail)
	}

	// Stat is the confirm-path check that compensates for a presigned PUT being unable to
	// enforce a size limit.
	info, err := testStorage.Stat(ctx, key)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.SizeBytes != int64(len(body)) {
		t.Errorf("size = %d, want %d", info.SizeBytes, len(body))
	}
	if info.ContentType != "image/png" {
		t.Errorf("content type = %q, want image/png", info.ContentType)
	}
	if info.ChecksumMD5 == "" {
		t.Error("expected a checksum from the server")
	}

	// Download round trip.
	downloadURL, err := testStorage.PresignDownload(ctx, key, 5*time.Minute)
	if err != nil {
		t.Fatalf("presigning download: %v", err)
	}
	getResp, err := http.Get(downloadURL) //nolint:gosec,noctx // signed URL from the adapter under test
	if err != nil {
		t.Fatalf("downloading: %v", err)
	}
	defer func() { _ = getResp.Body.Close() }()

	got, err := io.ReadAll(getResp.Body)
	if err != nil {
		t.Fatalf("reading download: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("downloaded %q, want %q", got, body)
	}

	if err := testStorage.Delete(ctx, key); err != nil {
		t.Fatalf("deleting: %v", err)
	}
	if _, err := testStorage.Stat(ctx, key); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a deleted object must stat as not-found, got %v", err)
	}
}

// TestContentTypeIsBoundIntoTheSignature covers the property that stops an "image/png"
// upload slot from accepting text/html — which, served back from our domain, is stored XSS.
func TestContentTypeIsBoundIntoTheSignature(t *testing.T) {
	ctx := context.Background()
	key := "trips/abc/options/def/mismatch.png"

	uploadURL, err := testStorage.PresignUpload(ctx, key, "image/png", 5*time.Minute)
	if err != nil {
		t.Fatalf("presigning: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL,
		strings.NewReader("<html>this is not a png</html>"))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	// A different content type than the one that was signed.
	req.Header.Set("Content-Type", "text/html")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("uploading: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		_ = testStorage.Delete(ctx, key)
		t.Fatal("the server accepted a content type that was not the one signed; " +
			"binding Content-Type into the signature is not working")
	}
}

func TestStatOfAbsentObjectIsNotFound(t *testing.T) {
	_, err := testStorage.Stat(context.Background(), "nothing/here.png")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestDeleteIsIdempotent pins the port's contract. Both the failed-upload path and the
// orphan sweeper race with clients that may already be gone, and making every call site
// handle "it was already deleted" would be noise.
func TestDeleteIsIdempotent(t *testing.T) {
	ctx := context.Background()
	if err := testStorage.Delete(ctx, "never/existed.png"); err != nil {
		t.Errorf("deleting an absent object must not error: %v", err)
	}
}

func TestPresignRejectsEmptyInput(t *testing.T) {
	ctx := context.Background()
	if _, err := testStorage.PresignUpload(ctx, "", "image/png", time.Minute); err == nil {
		t.Error("an empty key must be rejected")
	}
	if _, err := testStorage.PresignUpload(ctx, "k", "", time.Minute); err == nil {
		t.Error("an empty content type must be rejected: it is what binds the signature")
	}
	if _, err := testStorage.PresignDownload(ctx, "", time.Minute); err == nil {
		t.Error("an empty key must be rejected")
	}
}

// TestEnsureBucketIsIdempotent covers the startup path: two instances booting at once must
// not fight over creating the bucket, and an existing bucket must be a no-op.
func TestEnsureBucketIsIdempotent(t *testing.T) {
	if err := testStorage.ensureBucket(context.Background(), "us-east-1"); err != nil {
		t.Errorf("ensuring an existing bucket must be a no-op: %v", err)
	}
}
