package relayapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/relay"
)

type fakeRelay struct {
	presets []relay.Preset
	ffmpeg  bool
}

func (f fakeRelay) ListAllPresets() []relay.Preset { return f.presets }
func (f fakeRelay) GetPreset(n string) (relay.Preset, bool) {
	for _, p := range f.presets {
		if p.Name == n {
			return p, true
		}
	}
	return relay.Preset{}, false
}
func (f fakeRelay) FFmpegAvailable() bool { return f.ffmpeg }

func newRouter(deps Deps) chi.Router {
	r := chi.NewRouter()
	Register(r, deps)
	return r
}

// A nil Relay must serve an empty list rather than 500 — relay is optional.
func TestListPresets_NilManager(t *testing.T) {
	w := httptest.NewRecorder()
	newRouter(Deps{}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/relay-presets", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got []relay.Preset
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %q)", err, w.Body.String())
	}
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %v", got)
	}
	if w.Body.String() != "[]\n" {
		t.Fatalf("nil manager must encode as [] not null, got %q", w.Body.String())
	}
}

func TestListPresets_Sorted(t *testing.T) {
	deps := Deps{Relay: fakeRelay{presets: []relay.Preset{{Name: "b"}, {Name: "a"}}}}
	w := httptest.NewRecorder()
	newRouter(deps).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/relay-presets", nil))

	var got []relay.Preset
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got) != 2 || got[0].Name != "b" {
		t.Fatalf("handler must pass through the manager's order, got %v", got)
	}
}

func TestGetPreset_NotFound(t *testing.T) {
	deps := Deps{Relay: fakeRelay{presets: []relay.Preset{{Name: "x"}}}}
	w := httptest.NewRecorder()
	newRouter(deps).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/relay-presets/missing", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestGetPreset_OK(t *testing.T) {
	deps := Deps{Relay: fakeRelay{presets: []relay.Preset{{Name: "x"}}}}
	w := httptest.NewRecorder()
	newRouter(deps).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/relay-presets/x", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestCapabilities_NilManagerReportsUnavailable(t *testing.T) {
	w := httptest.NewRecorder()
	newRouter(Deps{}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/relay/capabilities", nil))

	var got capabilitiesResponse
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if !got.FFmpegRelaySupported {
		t.Fatal("relay support must stay advertised even without a manager")
	}
	if got.FFmpegAvailable {
		t.Fatal("nil manager must report ffmpeg unavailable")
	}
	if got.MaxTargetsPerCamera != 10 {
		t.Fatalf("max targets = %d, want 10", got.MaxTargetsPerCamera)
	}
}
