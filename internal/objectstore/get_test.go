package objectstore

// get_test.go — ranged GetObject wire tests (issue #874 batch 2: playback
// proxy). Asserts the Range header we send and parses 206/Content-Range
// responses; also covers the plain (no-range) streaming form used for
// initial <video> opens.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS3GetRangeWire(t *testing.T) {
	var gotPath, gotRange, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRange = r.Header.Get("Range")
		gotMethod = r.Method
		w.Header().Set("Content-Range", "bytes 4-6/11")
		w.Header().Set("Content-Length", "3")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("567"))
	}))
	defer srv.Close()

	store, err := NewS3(Config{
		EndpointURL: srv.URL, Region: "auto", Bucket: "nvr", PathStyle: true,
		AccessKeyID: "k", SecretAccessKey: "s",
	})
	require.NoError(t, err)

	rc, info, err := store.GetRange(context.Background(), "cam/a.mp4", 4, 6)
	require.NoError(t, err)
	defer rc.Close()
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "567", string(b))
	assert.Equal(t, "/nvr/cam/a.mp4", gotPath)
	assert.Equal(t, "bytes=4-6", gotRange, "S3 Range is inclusive")
	assert.Equal(t, http.MethodGet, gotMethod)
	assert.Equal(t, int64(11), info.Size, "total size parsed from Content-Range")
}

func TestS3GetRangeToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "bytes=4-", r.Header.Get("Range"), "open-ended range form")
		w.Header().Set("Content-Range", "bytes 4-10/11")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("7890!"))
	}))
	defer srv.Close()

	store, err := NewS3(Config{
		EndpointURL: srv.URL, Region: "auto", Bucket: "nvr", PathStyle: true,
		AccessKeyID: "k", SecretAccessKey: "s",
	})
	require.NoError(t, err)
	rc, _, err := store.GetRange(context.Background(), "a", 4, -1)
	require.NoError(t, err)
	defer rc.Close()
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "7890!", string(b))
}

func TestS3GetRangeNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	store, err := NewS3(Config{
		EndpointURL: srv.URL, Region: "auto", Bucket: "nvr", PathStyle: true,
		AccessKeyID: "k", SecretAccessKey: "s",
	})
	require.NoError(t, err)

	_, _, err = store.GetRange(context.Background(), "gone", 0, -1)
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestS3GetRangeServerGarbage(t *testing.T) {
	// 200 with a full body (store ignored the range or object smaller than
	// the window): must surface as ErrRangeNotSatisfiable-ish error, not a
	// silent misaligned stream — the proxy would serve corrupt bytes.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("full-body"))
	}))
	defer srv.Close()
	store, err := NewS3(Config{
		EndpointURL: srv.URL, Region: "auto", Bucket: "nvr", PathStyle: true,
		AccessKeyID: "k", SecretAccessKey: "s",
	})
	require.NoError(t, err)

	rc, _, err := store.GetRange(context.Background(), "a", 4, 6)
	if err == nil {
		rc.Close()
	}
	require.Error(t, err, "non-206 reply to a ranged GET must be an error")
	assert.False(t, strings.Contains(err.Error(), "silent"))
}
