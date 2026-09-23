// Package objectstore is the NVR's thin client for S3-compatible object
// storage (AWS S3 / MinIO / Cloudflare R2 / B2 / Aliyun OSS / COS — one wire
// protocol, issue #874). It deliberately exposes only the two operations the
// offload pipeline needs (PutObject for upload, HeadObject for the pre-evict
// existence re-check); batch 2's playback proxy will add ranged GetObject.
//
// The Store interface is the injection seam: internal/offload depends on the
// interface, unit tests run against fakes, and the only production
// implementation is the aws-sdk-go-v2 S3 subset below (pure Go,
// CGO_ENABLED=0-safe, static credentials — no config-chain resolution).
package objectstore

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrObjectNotFound is returned by Head when the remote object does not exist
// (HTTP 404, or 403 from stores that conflate "no ListObjects permission"
// with "no such object"). The evict path treats it as a hard stop.
var ErrObjectNotFound = errors.New("object not found in remote store")

// Config carries everything NewS3 needs. AccessKeyID / SecretAccessKey accept
// ${VAR} environment references (expanded at construction — never written
// back to the config file).
type Config struct {
	EndpointURL     string
	Region          string
	Bucket          string
	PathStyle       bool
	AccessKeyID     string
	SecretAccessKey string
}

// ObjectInfo is the HeadObject result.
type ObjectInfo struct {
	Key          string
	Size         int64
	ETag         string
	LastModified time.Time
}

// Store is the object-storage seam consumed by internal/offload.
type Store interface {
	// Put uploads body (exactly size bytes) to key and returns the store's
	// ETag. Overwriting an existing key is the crash-recovery mechanism —
	// re-uploading after an interrupted run must be idempotent.
	Put(ctx context.Context, key string, body io.Reader, size int64) (etag string, err error)

	// Head fetches object metadata; ErrObjectNotFound when absent.
	Head(ctx context.Context, key string) (ObjectInfo, error)

	// GetRange fetches bytes [start, end] — end INCLUSIVE, matching the S3
	// Range header form; end < 0 means "to EOF" (an open-ended, streamable
	// fetch). info.Size carries the TOTAL object size parsed from
	// Content-Range when the store provides it (0 otherwise). Callers own
	// closing the returned reader.
	GetRange(ctx context.Context, key string, start, end int64) (io.ReadCloser, ObjectInfo, error)
}
