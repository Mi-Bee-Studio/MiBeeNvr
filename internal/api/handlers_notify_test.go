package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/notify"
)

// fakeNotifyService records SendTest calls; Stats returns canned counters.
type fakeNotifyService struct {
	calls []struct{ severity, title string }
}

func (f *fakeNotifyService) SendTest(severity, title string) {
	f.calls = append(f.calls, struct{ severity, title string }{severity, title})
}

func (f *fakeNotifyService) Stats() []notify.SinkStats {
	return []notify.SinkStats{{Name: "hub", SentTotal: 3, DroppedQueue: 1}}
}

func newNotifyTestHandler(t *testing.T, svc NotifyService) (*Handler, string) {
	t.Helper()
	db, store := setupTestDB(t)
	hash, err := middleware.HashPassword("secret")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	h := testHandlerWithAuth(db, store, "admin", hash)
	if svc != nil {
		h.SetNotifyService(svc)
	}
	return h, "secret"
}

func TestNotifyEndpoints_AuthRequired(t *testing.T) {
	h, _ := newNotifyTestHandler(t, nil)
	for _, ep := range []struct{ method, path string }{
		{http.MethodGet, "/api/notifications/status"},
		{http.MethodPost, "/api/notifications/test"},
	} {
		rr := httptest.NewRecorder()
		h.Routes().ServeHTTP(rr, httptest.NewRequest(ep.method, ep.path, nil))
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: expected 401, got %d", ep.method, ep.path, rr.Code)
		}
	}
}

func TestNotifyStatus(t *testing.T) {
	h, pass := newNotifyTestHandler(t, &fakeNotifyService{})
	rr := doRequest(t, h.Routes(), "GET", "/api/notifications/status", nil, "admin", pass)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d", rr.Code)
	}
	var resp struct {
		Enabled bool               `json:"enabled"`
		Types   []string           `json:"types"`
		Sinks   []notify.SinkStats `json:"sinks"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Enabled || len(resp.Sinks) != 1 || resp.Sinks[0].Name != "hub" {
		t.Errorf("enabled status wrong: %+v", resp)
	}
	if len(resp.Types) == 0 {
		t.Error("type vocabulary missing")
	}
}

func TestNotifyStatus_Disabled(t *testing.T) {
	h, pass := newNotifyTestHandler(t, nil) // no service wired
	rr := doRequest(t, h.Routes(), "GET", "/api/notifications/status", nil, "admin", pass)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d", rr.Code)
	}
	var resp struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Enabled {
		t.Error("enabled must be false without a service")
	}
}

// TestNotifyStatus_TypedNilService pins the live 2026-11-02 regression: the
// app wiring passed a nil *notify.Service pointer into the NotifyService
// interface parameter, so the handler's `!= nil` check saw a non-nil
// interface and Stats() panicked on the nil receiver (status endpoint 500
// on every notifications-disabled deployment). The nil-receiver guards in
// notify.Service must keep this endpoint panic-free even if a typed nil
// sneaks in again.
func TestNotifyStatus_TypedNilService(t *testing.T) {
	h, pass := newNotifyTestHandler(t, nil)
	h.SetNotifyService((*notify.Service)(nil))

	rr := doRequest(t, h.Routes(), "GET", "/api/notifications/status", nil, "admin", pass)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: expected 200 with typed-nil service, got %d %s", rr.Code, rr.Body.String())
	}

	rr = doRequest(t, h.Routes(), "POST", "/api/notifications/test", nil, "admin", pass)
	if rr.Code != http.StatusAccepted {
		t.Errorf("test: expected 202 (no-op) with typed-nil service, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestNotifyTest(t *testing.T) {
	fake := &fakeNotifyService{}
	h, pass := newNotifyTestHandler(t, fake)

	body := bytes.NewBufferString(`{"severity":"warning","title":"check"}`)
	rr := doRequest(t, h.Routes(), "POST", "/api/notifications/test", body, "admin", pass)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("test: %d %s", rr.Code, rr.Body.String())
	}
	if len(fake.calls) != 1 || fake.calls[0].severity != "warning" || fake.calls[0].title != "check" {
		t.Errorf("SendTest args wrong: %+v", fake.calls)
	}

	// Invalid severity is rejected before anything is queued.
	rr = doRequest(t, h.Routes(), "POST", "/api/notifications/test",
		bytes.NewBufferString(`{"severity":"loud"}`), "admin", pass)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("invalid severity: expected 400, got %d", rr.Code)
	}
	if len(fake.calls) != 1 {
		t.Error("invalid request must not enqueue")
	}
}

func TestNotifyTest_DisabledConflict(t *testing.T) {
	h, pass := newNotifyTestHandler(t, nil)
	rr := doRequest(t, h.Routes(), "POST", "/api/notifications/test", nil, "admin", pass)
	if rr.Code != http.StatusConflict {
		t.Errorf("expected 409 with forwarding disabled, got %d", rr.Code)
	}
}
