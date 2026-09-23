package objectstore

// presign.go — presigned GetObject URLs (issue #874 batch 3): direct-browser
// playback. The playback endpoint 302s to a presigned URL so media bytes
// flow store→browser without transiting the NVR. Signing is LOCAL (query-
// string SigV4) — no request is made here.

import (
	"context"
	"fmt"
	"time"

	aws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Presigner produces time-limited direct GET URLs. Implemented by *S3Store
// (against its own endpoint) and by NewS3Presigner (against an explicitly
// different endpoint — the browser-reachable override).
type Presigner interface {
	PresignGet(ctx context.Context, key string, ttl time.Duration) (url string, err error)
}

// PresignGet signs a GET for key on the store's own bucket/endpoint.
func (s *S3Store) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	req, err := s.presignClient().PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presign %s: %w", key, err)
	}
	return req.URL, nil
}

// presignClient lazily builds the presign client over the same S3 client
// config (same endpoint, credentials, path-style).
func (s *S3Store) presignClient() *s3.PresignClient {
	s.presignOnce.Do(func() {
		s.presign = s3.NewPresignClient(s.client)
	})
	return s.presign
}

// NewS3Presigner builds a Presigner against an explicitly provided endpoint
// (typically storage.remote.playback.endpoint_url — the browser-reachable
// host when the NVR's own client endpoint is a Docker-internal name).
// Validation mirrors NewS3.
func NewS3Presigner(cfg Config) (Presigner, error) {
	store, err := NewS3(cfg)
	if err != nil {
		return nil, fmt.Errorf("presigner: %w", err)
	}
	return store, nil
}
