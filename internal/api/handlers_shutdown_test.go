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

// The shutdown tests mutate package-global state (shutdownFunc and the
// shutdownDone sync.Once) — deliberately NOT parallel.
func TestHandleSystemShutdown_LocalTriggersOnce(t *testing.T) {
	var fired atomic.Int32
	prev := shutdownFunc
	t.Cleanup(func() { shutdownMu.Lock(); shutdownFunc = prev; shutdownMu.Unlock() })
	shutdownMu.Lock()
	shutdownFunc = func() { fired.Add(1) }
	shutdownMu.Unlock()

	body := []byte("{}")
	rec := httptest.NewRecorder()
	handleSystemShutdown(rec, localReq(http.MethodPost, "/api/system/shutdown", body))
	require.Equal(t, http.StatusOK, rec.Code)

	// Second + third calls must not fire again (sync.Once semantics).
	for range 2 {
		rec := httptest.NewRecorder()
		handleSystemShutdown(rec, localReq(http.MethodPost, "/api/system/shutdown", body))
		require.Equal(t, http.StatusOK, rec.Code)
	}

	deadline := time.Now().Add(2 * time.Second)
	for fired.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	require.Equal(t, int32(1), fired.Load(), "shutdown func must fire exactly once")
}

func TestHandleSystemShutdown_RemoteRefused(t *testing.T) {
	var fired atomic.Int32
	prev := shutdownFunc
	t.Cleanup(func() { shutdownMu.Lock(); shutdownFunc = prev; shutdownMu.Unlock() })
	shutdownMu.Lock()
	shutdownFunc = func() { fired.Add(1) }
	shutdownMu.Unlock()

	req := httptest.NewRequest(http.MethodPost, "/api/system/shutdown", bytes.NewReader([]byte("{}")))
	req.RemoteAddr = "192.168.63.30:52000"
	req.Host = "192.168.63.30:9090"
	rec := httptest.NewRecorder()
	handleSystemShutdown(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Zero(t, fired.Load())
}
