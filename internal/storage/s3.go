// Package storage contains adapters implementing domain.FileStorage.
//
// Like internal/repository, internal/security and internal/email, it is an outer layer: it
// implements a port the domain declares and nothing depends on it inwardly. The domain knows
// a photo must be storable; it does not know about buckets, regions or signature versions.
package storage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/junto/junto/internal/domain"
)

// S3Storage implements domain.FileStorage against any S3-compatible endpoint.
//
// minio-go rather than aws-sdk-go-v2: presigning, PUT and stat behave identically against
// MinIO, S3 and Cloudflare R2, and it is a far smaller dependency. The choice is invisible
// above this file by construction — the port is what the rest of the application depends on,
// so swapping SDKs would touch this file and nothing else.
type S3Storage struct {
	client *minio.Client
	bucket string
}

// S3Config configures an S3Storage.
type S3Config struct {
	Endpoint  string // host:port, no scheme
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseSSL    bool
}

// NewS3Storage connects and ensures the bucket exists.
//
// Creating the bucket here rather than in an init container keeps Compose simpler, and the
// same code path is a no-op against a real S3 bucket that already exists. It is deliberately
// tolerant of a concurrent creator: two instances starting at once must not fight.
func NewS3Storage(ctx context.Context, cfg S3Config) (*S3Storage, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: connecting to %s: %w", cfg.Endpoint, err)
	}

	s := &S3Storage{client: client, bucket: cfg.Bucket}
	if err := s.ensureBucket(ctx, cfg.Region); err != nil {
		return nil, err
	}
	return s, nil
}

var _ domain.FileStorage = (*S3Storage)(nil)

func (s *S3Storage) ensureBucket(ctx context.Context, region string) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("storage: checking bucket %q: %w", s.bucket, err)
	}
	if exists {
		return nil
	}

	if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: region}); err != nil {
		// Another instance may have won the race between BucketExists and MakeBucket.
		// Re-check rather than failing startup on a benign conflict.
		if exists, checkErr := s.client.BucketExists(ctx, s.bucket); checkErr == nil && exists {
			return nil
		}
		return fmt.Errorf("storage: creating bucket %q: %w", s.bucket, err)
	}
	return nil
}

// PresignUpload returns a URL the client may PUT to, once, within ttl.
//
// The content type is bound into the signature. Without that, a client could obtain a URL
// for "image/png" and upload text/html to it — which, served back to a browser from our
// domain, is stored XSS. Binding it means the upload is rejected at the storage layer if the
// header does not match.
func (s *S3Storage) PresignUpload(ctx context.Context, key, contentType string, ttl time.Duration) (string, error) {
	if key == "" {
		return "", errors.New("storage: empty object key")
	}
	if contentType == "" {
		return "", errors.New("storage: content type is required for an upload URL")
	}

	headers := http.Header{}
	headers.Set("Content-Type", contentType)

	signed, err := s.client.PresignHeader(ctx, http.MethodPut, s.bucket, key, ttl, url.Values{}, headers)
	if err != nil {
		return "", fmt.Errorf("storage: presigning upload for %q: %w", key, err)
	}
	return signed.String(), nil
}

// PresignDownload returns a time-limited read URL.
//
// Every read is signed; there is no public-bucket path. Attachments are trip-private, and a
// permanently readable URL would leak a member's photos to anyone who ever saw the link.
func (s *S3Storage) PresignDownload(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if key == "" {
		return "", errors.New("storage: empty object key")
	}
	signed, err := s.client.PresignedGetObject(ctx, s.bucket, key, ttl, url.Values{})
	if err != nil {
		return "", fmt.Errorf("storage: presigning download for %q: %w", key, err)
	}
	return signed.String(), nil
}

// Stat returns metadata for a stored object.
//
// This is the confirm-path check that compensates for a presigned PUT being unable to
// enforce a size limit: the service compares the real size and content type against what it
// expected, and deletes the object if it does not match.
func (s *S3Storage) Stat(ctx context.Context, key string) (domain.FileInfo, error) {
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return domain.FileInfo{}, fmt.Errorf("storage: object %q: %w", key, domain.ErrNotFound)
		}
		return domain.FileInfo{}, fmt.Errorf("storage: stat %q: %w", key, err)
	}
	return domain.FileInfo{
		Key:         key,
		SizeBytes:   info.Size,
		ContentType: info.ContentType,
		ChecksumMD5: strings.Trim(info.ETag, `"`),
		ModifiedAt:  info.LastModified,
	}, nil
}

// Delete removes an object.
//
// Idempotent by contract: deleting an absent key is not an error. Both the failed-upload
// path and the orphan sweeper race with clients that may already be gone, and making them
// handle "it was already deleted" would be noise at every call site.
func (s *S3Storage) Delete(ctx context.Context, key string) error {
	err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("storage: deleting %q: %w", key, err)
	}
	return nil
}

func isNotFound(err error) bool {
	resp := minio.ToErrorResponse(err)
	return resp.Code == "NoSuchKey" || resp.Code == "NoSuchBucket" || resp.StatusCode == 404
}
