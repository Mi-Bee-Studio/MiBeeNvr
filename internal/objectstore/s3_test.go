package objectstore

// s3_test.go — wire-level tests against an httptest server. The server
// asserts path-style addressing and SigV4 auth presence and replies with
// minimal valid S3 responses; no real S3/MinIO needed for the contract.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T, h http.Handler) (Store, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	store, err := NewS3(Config{
		EndpointURL:     srv.URL,
		Region:          "auto",
		Bucket:          "nvr",
		PathStyle:       true,
		AccessKeyID:     "testkey",
		SecretAccessKey: "testsecret",
	})
	require.NoError(t, err)
	return store, srv
}

func TestS3PutWire(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	var gotLen int64
	store, _ := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotLen = r.ContentLength
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("ETag", `"etag-put-1"`)
		w.WriteHeader(http.StatusOK)
	}))

	etag, err := store.Put(context.Background(), "cam1/2026/09/22/a.mp4", strings.NewReader("hello-offload"), int64(len("hello-offload")))
	require.NoError(t, err)
	assert.Equal(t, `"etag-put-1"`, etag)
	assert.Equal(t, "/nvr/cam1/2026/09/22/a.mp4", gotPath, "path-style addressing")
	assert.True(t, strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256"), "SigV4 auth header, got %q", gotAuth)
	assert.Equal(t, int64(len("hello-offload")), gotLen)
	assert.Equal(t, "hello-offload", gotBody)
}

func TestS3HeadFoundAndMissing(t *testing.T) {
	store, _ := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodHead, r.Method)
		if strings.HasSuffix(r.URL.Path, "/exists.mp4") {
			w.Header().Set("ETag", `"etag-h"`)
			w.Header().Set("Content-Length", "1024")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	info, err := store.Head(context.Background(), "cam1/exists.mp4")
	require.NoError(t, err)
	assert.Equal(t, int64(1024), info.Size)
	assert.Equal(t, `"etag-h"`, info.ETag)

	_, err = store.Head(context.Background(), "cam1/gone.mp4")
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestS3PutServerOverread(t *testing.T) {
	// A 5xx reply must surface as an error, not a silent success.
	var calls int32
	store, _ := newTestStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	_, err := store.Put(context.Background(), "k", strings.NewReader("x"), 1)
	require.Error(t, err)
	assert.GreaterOrEqual(t, atomic.LoadInt32(&calls), int32(1))
}

func TestNewS3ConfigValidation(t *testing.T) {
	_, err := NewS3(Config{EndpointURL: "", Bucket: "b", AccessKeyID: "a", SecretAccessKey: "s"})
	require.ErrorContains(t, err, "endpoint")

	_, err = NewS3(Config{EndpointURL: "http://x", Bucket: "", AccessKeyID: "a", SecretAccessKey: "s"})
	require.ErrorContains(t, err, "bucket")

	_, err = NewS3(Config{EndpointURL: "http://x", Bucket: "b", AccessKeyID: "", SecretAccessKey: "s"})
	require.ErrorContains(t, err, "credential")
}

func TestNewS3ExpandsEnvRefs(t *testing.T) {
	// Not parallel: t.Setenv.
	t.Setenv("OFFLOAD_TEST_AK", "env-key")
	t.Setenv("OFFLOAD_TEST_SK", "env-secret")

	s3i, err := NewS3(Config{
		EndpointURL:     "http://s3.local",
		Bucket:          "b",
		AccessKeyID:     "${OFFLOAD_TEST_AK}",
		SecretAccessKey: "${OFFLOAD_TEST_SK}",
	})
	require.NoError(t, err)
	assert.Equal(t, "env-key", s3i.accessKeyID)
	assert.Equal(t, "env-secret", s3i.secretAccessKey)
}
