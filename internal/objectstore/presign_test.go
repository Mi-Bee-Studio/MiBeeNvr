package objectstore

// presign_test.go — presigned GetObject URL generation (issue #874 batch 3):
// direct-browser playback. Signing is local (no wire) — the test asserts the
// URL shape: endpoint host, path-style bucket/key, SigV4 query params, and
// the requested expiry.

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPresignGetURLShape(t *testing.T) {
	t.Parallel()
	store, err := NewS3(Config{
		EndpointURL: "http://minio.local:9000", Region: "auto", Bucket: "nvr",
		PathStyle: true, AccessKeyID: "k", SecretAccessKey: "s",
	})
	require.NoError(t, err)

	raw, err := store.PresignGet(context.Background(), "cam/a.mp4", time.Hour)
	require.NoError(t, err)

	u, err := url.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, "http", u.Scheme)
	assert.Equal(t, "minio.local:9000", u.Host)
	assert.Equal(t, "/nvr/cam/a.mp4", u.Path, "path-style presigned URL")

	q := u.Query()
	// X-Amz-Credential is "<accessKey>/<date>/<region>/s3/aws4_request" — the
	// access key is the first segment.
	assert.True(t, strings.HasPrefix(q.Get("X-Amz-Credential"), "k/"),
		"credential carries the access key, got %q", q.Get("X-Amz-Credential"))
	assert.NotEmpty(t, q.Get("X-Amz-Signature"))
	assert.NotEmpty(t, q.Get("X-Amz-Expires"), "expiry is part of the signed query")
	assert.NotEmpty(t, q.Get("X-Amz-Date"))
}

func TestPresignGetDistinguishedEndpoint(t *testing.T) {
	t.Parallel()
	// The browser-reachable override endpoint (batch 3): presigning against
	// a different host than the NVR's own client endpoint.
	p, err := NewS3Presigner(Config{
		EndpointURL: "https://minio.example.com", Region: "auto", Bucket: "nvr",
		PathStyle: true, AccessKeyID: "k", SecretAccessKey: "s",
	})
	require.NoError(t, err)
	raw, err := p.PresignGet(context.Background(), "a.mp4", 30*time.Minute)
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, "minio.example.com", u.Host)
	assert.Equal(t, "https", u.Scheme)
}
