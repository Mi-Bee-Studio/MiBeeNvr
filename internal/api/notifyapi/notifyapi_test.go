package notifyapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/notify"
)

type fakeSvc struct {
	calls int
	sev   string
	title string
}

func (f *fakeSvc) SendTest(severity, title string) {
	f.calls++
	f.sev, f.title = severity, title
}
func (f *fakeSvc) Stats() []notify.SinkStats { return []notify.SinkStats{{Name: "test"}} }

func router(deps Deps) chi.Router {
	r := chi.NewRouter()
	Register(r, deps)
	return r
}

// Disabled notifications must still answer /status so the settings panel can
// explain itself, reporting enabled=false and an empty sink list.
func TestStatus_Disabled(t *testing.T) {
	w := httptest.NewRecorder()
	router(Deps{}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/notifications/status", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["enabled"] != false {
		t.Fatalf("enabled = %v, want false", got["enabled"])
	}
	if sinks, ok := got["sinks"].([]any); !ok || len(sinks) != 0 {
		t.Fatalf("sinks = %v, want empty array", got["sinks"])
	}
	if types, ok := got["types"].([]any); !ok || len(types) == 0 {
		t.Fatalf("types vocabulary missing: %v", got["types"])
	}
}

func TestStatus_EnabledReportsSinks(t *testing.T) {
	w := httptest.NewRecorder()
	router(Deps{Service: &fakeSvc{}}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/notifications/status", nil))

	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["enabled"] != true {
		t.Fatalf("enabled = %v, want true", got["enabled"])
	}
	if sinks, ok := got["sinks"].([]any); !ok || len(sinks) != 1 {
		t.Fatalf("sinks = %v, want one entry", got["sinks"])
	}
}

// Test send with notifications disabled is a conflict, not a silent no-op.
func TestTest_Disabled(t *testing.T) {
	w := httptest.NewRecorder()
	router(Deps{}).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/notifications/test", strings.NewReader(`{}`)))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
}

func TestTest_QueuesAndForwardsBody(t *testing.T) {
	svc := &fakeSvc{}
	w := httptest.NewRecorder()
	body := `{"severity":"warning","title":"hello"}`
	router(Deps{Service: svc}).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/notifications/test", strings.NewReader(body)))

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}
	if svc.calls != 1 || svc.sev != "warning" || svc.title != "hello" {
		t.Fatalf("forwarded (%d, %q, %q), want (1, warning, hello)", svc.calls, svc.sev, svc.title)
	}
}

func TestTest_RejectsUnknownSeverity(t *testing.T) {
	w := httptest.NewRecorder()
	body := `{"severity":"bogus"}`
	router(Deps{Service: &fakeSvc{}}).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/notifications/test", strings.NewReader(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// An empty body is legal (defaults apply) — only malformed JSON is rejected.
func TestTest_EmptyBodyAllowed(t *testing.T) {
	svc := &fakeSvc{}
	w := httptest.NewRecorder()
	router(Deps{Service: svc}).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/notifications/test", strings.NewReader("")))

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 for empty body", w.Code)
	}
	if svc.calls != 1 {
		t.Fatalf("SendTest calls = %d, want 1", svc.calls)
	}
}
