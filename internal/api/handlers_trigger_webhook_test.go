package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

// webhookSign produces the X-MiBee-Signature header value for a body:
// t=<unix>,v1=<hex HMAC-SHA256(secret, "<t>.<body>")>. Mirrors the scheme the
// webhook endpoint verifies (issue #709, Stripe-style signed payload).
func webhookSign(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

// webhookNow pins the "server clock" for pure verification tests.
var webhookNow = time.Unix(1731000000, 0)

// dispatchRecorder captures dispatcher invocations (thread-safe — the handler
// may dispatch from the request goroutine, but tests must not race the read).
type dispatchRecorder struct {
	mu     sync.Mutex
	calls  [][2]string
	called chan struct{} // closed on first dispatch, size 1 buffered
}

func newDispatchRecorder() *dispatchRecorder {
	return &dispatchRecorder{called: make(chan struct{}, 1)}
}

func (d *dispatchRecorder) record(cameraID, action string, _ time.Duration) {
	d.mu.Lock()
	d.calls = append(d.calls, [2]string{cameraID, action})
	d.mu.Unlock()
	select {
	case d.called <- struct{}{}:
	default:
	}
}

func (d *dispatchRecorder) snapshot() [][2]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([][2]string, len(d.calls))
	copy(out, d.calls)
	return out
}

// setupWebhookHandler builds a Handler whose camera manager knows one camera
// (no Start — GetCameraConfig reads the constructor-built snapshot, so no
// recorder goroutines spin up) plus a recording dispatcher stub.
func setupWebhookHandler(t *testing.T, enabled bool) (*Handler, *dispatchRecorder) {
	t.Helper()
	db, store := setupTestDB(t)
	cfg := &config.Config{
		Version: "1.0",
		Storage: config.StorageConfig{SegmentDuration: "30s"},
		Cameras: []config.CameraConfig{{
			ID:       "cam-webhook-1",
			Name:     "webhook test cam",
			Protocol: "rtsp",
			Encoding: "h264",
			URL:      "rtsp://127.0.0.1:1/none",
		}},
		Trigger: config.TriggerConfig{
			Webhook: config.WebhookTriggerConfig{
				Enabled:       enabled,
				Secret:        "whsec_test",
				ReplayWindowS: 300,
			},
		},
	}
	camMgr := camera.NewCameraManager(cfg, store, db, "")
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, nil, "", nil, nil, nil, nil, nil, nil)
	rec := newDispatchRecorder()
	h.SetTriggerDispatcher(rec.record)
	return h, rec
}

func postWebhook(h http.Handler, cameraID, action, sigHeader string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/trigger/webhook/"+cameraID+"?action="+action, bytes.NewReader(body))
	if sigHeader != "" {
		req.Header.Set("X-MiBee-Signature", sigHeader)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestTriggerWebhookHandler(t *testing.T) {
	t.Parallel()

	newServing := func(t *testing.T) (http.Handler, *dispatchRecorder) {
		t.Helper()
		h, rec := setupWebhookHandler(t, true)
		return h.Routes(), rec
	}
	body := []byte(`{"source":"front-door-sensor"}`)
	ts := time.Now().Unix()

	t.Run("valid signature dispatches action", func(t *testing.T) {
		t.Parallel()
		serving, rec := newServing(t)
		resp := postWebhook(serving, "cam-webhook-1", "record", webhookSign("whsec_test", ts, body), body)
		require.Equal(t, http.StatusAccepted, resp.Code)
		calls := rec.snapshot()
		require.Len(t, calls, 1)
		require.Equal(t, "cam-webhook-1", calls[0][0])
		require.Equal(t, "record", calls[0][1])
	})

	t.Run("snapshot and stop actions accepted", func(t *testing.T) {
		t.Parallel()
		serving, _ := newServing(t)
		for _, action := range []string{"snapshot", "stop"} {
			resp := postWebhook(serving, "cam-webhook-1", action, webhookSign("whsec_test", ts, body), body)
			require.Equal(t, http.StatusAccepted, resp.Code, "action %s", action)
		}
	})

	t.Run("expired timestamp rejected", func(t *testing.T) {
		t.Parallel()
		serving, rec := newServing(t)
		stale := webhookSign("whsec_test", time.Now().Add(-10*time.Minute).Unix(), body)
		resp := postWebhook(serving, "cam-webhook-1", "record", stale, body)
		require.Equal(t, http.StatusUnauthorized, resp.Code)
		require.Empty(t, rec.snapshot())
	})

	t.Run("wrong signature rejected", func(t *testing.T) {
		t.Parallel()
		serving, rec := newServing(t)
		resp := postWebhook(serving, "cam-webhook-1", "record", webhookSign("whsec_wrong", ts, body), body)
		require.Equal(t, http.StatusUnauthorized, resp.Code)
		require.Empty(t, rec.snapshot())
	})

	t.Run("missing signature rejected", func(t *testing.T) {
		t.Parallel()
		serving, rec := newServing(t)
		resp := postWebhook(serving, "cam-webhook-1", "record", "", body)
		require.Equal(t, http.StatusUnauthorized, resp.Code)
		require.Empty(t, rec.snapshot())
	})

	t.Run("unknown action rejected", func(t *testing.T) {
		t.Parallel()
		serving, rec := newServing(t)
		resp := postWebhook(serving, "cam-webhook-1", "reboot", webhookSign("whsec_test", ts, body), body)
		require.Equal(t, http.StatusBadRequest, resp.Code)
		require.Empty(t, rec.snapshot())
	})

	t.Run("missing action rejected", func(t *testing.T) {
		t.Parallel()
		serving, _ := newServing(t)
		req := httptest.NewRequest(http.MethodPost, "/api/trigger/webhook/cam-webhook-1", bytes.NewReader(body))
		req.Header.Set("X-MiBee-Signature", webhookSign("whsec_test", ts, body))
		recorder := httptest.NewRecorder()
		serving.ServeHTTP(recorder, req)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})

	t.Run("unknown camera rejected", func(t *testing.T) {
		t.Parallel()
		serving, rec := newServing(t)
		resp := postWebhook(serving, "cam-ghost", "record", webhookSign("whsec_test", ts, body), body)
		require.Equal(t, http.StatusNotFound, resp.Code)
		require.Empty(t, rec.snapshot())
	})

	t.Run("disabled webhook not routed", func(t *testing.T) {
		t.Parallel()
		h, rec := setupWebhookHandler(t, false)
		resp := postWebhook(h.Routes(), "cam-webhook-1", "record", webhookSign("whsec_test", ts, body), body)
		require.Equal(t, http.StatusNotFound, resp.Code)
		require.Empty(t, rec.snapshot())
	})

	t.Run("dispatcher not wired degrades to 503", func(t *testing.T) {
		t.Parallel()
		h, _ := setupWebhookHandler(t, true)
		h.SetTriggerDispatcher(nil)
		resp := postWebhook(h.Routes(), "cam-webhook-1", "record", webhookSign("whsec_test", ts, body), body)
		require.Equal(t, http.StatusServiceUnavailable, resp.Code)
	})
}
