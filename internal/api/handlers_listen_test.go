package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The listen-change tests mutate package-global state (listenChangeFn) —
// deliberately NOT parallel, mirroring the shutdown tests.
func TestHandleListenChange_LocalAccepted(t *testing.T) {
	var got atomic.Value // string
	prev := listenChangeFn
	t.Cleanup(func() { listenChangeFn = prev })
	listenChangeFn = func(addr string) error {
		got.Store(addr)
		return nil
	}

	body := []byte(`{"listen":"0.0.0.0:9090"}`)
	rec := httptest.NewRecorder()
	handleListenChange(rec, localReq(http.MethodPut, "/api/system/listen", body))
	require.Equal(t, http.StatusAccepted, rec.Code)

	deadline := time.Now().Add(2 * time.Second)
	for {
		if v, ok := got.Load().(string); ok && v == "0.0.0.0:9090" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("listen change func was not called with the requested address")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHandleListenChange_RemoteRefused(t *testing.T) {
	var fired atomic.Int32
	prev := listenChangeFn
	t.Cleanup(func() { listenChangeFn = prev })
	listenChangeFn = func(string) error { fired.Add(1); return nil }

	req := httptest.NewRequest(http.MethodPut, "/api/system/listen",
		bytes.NewReader([]byte(`{"listen":"0.0.0.0:9090"}`)))
	req.RemoteAddr = "192.168.63.30:52000"
	req.Host = "192.168.63.30:9090"
	rec := httptest.NewRecorder()
	handleListenChange(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Zero(t, fired.Load())
}

func TestHandleListenChange_BadShape(t *testing.T) {
	prev := listenChangeFn
	t.Cleanup(func() { listenChangeFn = prev })
	listenChangeFn = func(string) error { return nil }

	for _, body := range []string{
		`{"listen":""}`,
		`{"listen":"   "}`,
		`{"listen":"9090"}`,
		`{"listen":"127.0.0.1"}`,
		`{"listen":":0"}`,
		`{"listen":":65536"}`,
		`{"listen":"http://127.0.0.1:9090"}`,
		`not-json`,
	} {
		rec := httptest.NewRecorder()
		handleListenChange(rec, localReq(http.MethodPut, "/api/system/listen", []byte(body)))
		require.Equal(t, http.StatusBadRequest, rec.Code, "body %q", body)
	}
}

func TestHandleListenChange_NotWired(t *testing.T) {
	prev := listenChangeFn
	t.Cleanup(func() { listenChangeFn = prev })
	listenChangeFn = nil

	rec := httptest.NewRecorder()
	handleListenChange(rec, localReq(http.MethodPut, "/api/system/listen", []byte(`{"listen":"127.0.0.1:9091"}`)))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
