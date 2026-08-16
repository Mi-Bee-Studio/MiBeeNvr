package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestKeyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := APIKeyNameFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(name))
	})
}

func TestAPIKeyAuthMiddlewareBearer(t *testing.T) {
	store := NewAPIKeyStore()
	store.SetKeys(map[string]string{"mbv_test123": "dad-phone"})

	h := APIKeyAuthMiddleware(store, newTestKeyHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("Authorization", "Bearer mbv_test123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid key: got status %d, want 200", rec.Code)
	}
	if rec.Body.String() != "dad-phone" {
		t.Fatalf("context key name: got %q, want %q", rec.Body.String(), "dad-phone")
	}

	req = httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("Authorization", "Bearer mbv_wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid key: got status %d, want 401", rec.Code)
	}

	// Non-Bearer requests pass through untouched (BasicAuth handles them).
	req = httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("basic auth passthrough: got status %d, want 200", rec.Code)
	}
}

func TestAPIKeyAuthMiddlewareQueryParam(t *testing.T) {
	store := NewAPIKeyStore()
	store.SetKeys(map[string]string{"mbv_test123": "vision"})

	h := APIKeyAuthMiddleware(store, newTestKeyHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/events?api_key=mbv_test123", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("query-param key: got status %d, want 200", rec.Code)
	}
	if rec.Body.String() != "vision" {
		t.Fatalf("context key name: got %q, want %q", rec.Body.String(), "vision")
	}
}

// Hot reload (#335): key set changes must apply on the next request — both
// revocation (immediate denial) and minting (immediate acceptance).
func TestAPIKeyAuthMiddlewareHotReload(t *testing.T) {
	store := NewAPIKeyStore()
	store.SetKeys(map[string]string{"mbv_old": "old-key", "mbv_keep": "keep-key"})

	h := APIKeyAuthMiddleware(store, newTestKeyHandler())

	assertStatus := func(token string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("token %q: got status %d, want %d", token, rec.Code, want)
		}
	}

	assertStatus("mbv_old", http.StatusOK)

	// Simulate handleRevokeAPIKey: rebuild the valid set without mbv_old.
	store.SetKeys(map[string]string{"mbv_keep": "keep-key", "mbv_new": "new-key"})
	assertStatus("mbv_old", http.StatusUnauthorized) // revoked → next request denied
	assertStatus("mbv_new", http.StatusOK)           // minted → next request accepted
}

func TestAPIKeyStoreLookupRecordsLastUsed(t *testing.T) {
	store := NewAPIKeyStore()
	store.SetKeys(map[string]string{"mbv_a": "phone-a"})

	if _, ok := store.Lookup("mbv_a"); !ok {
		t.Fatal("expected lookup to succeed")
	}
	first := store.LastUsed()["phone-a"]
	if first.IsZero() {
		t.Fatal("expected last-used to be recorded on first lookup")
	}

	// Within the throttle window the timestamp is not rewritten.
	time.Sleep(5 * time.Millisecond)
	if _, ok := store.Lookup("mbv_a"); !ok {
		t.Fatal("expected second lookup to succeed")
	}
	if got := store.LastUsed()["phone-a"]; !got.Equal(first) {
		t.Fatalf("last-used within throttle window: got %v, want unchanged %v", got, first)
	}

	// An unknown token must not create entries.
	if _, ok := store.Lookup("mbv_missing"); ok {
		t.Fatal("unknown token unexpectedly matched")
	}
	if _, ok := store.LastUsed()[""]; ok {
		t.Fatal("unknown token created a last-used entry")
	}
}

func TestAPIKeyAuthMiddlewareNilStore(t *testing.T) {
	h := APIKeyAuthMiddleware(nil, newTestKeyHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("Authorization", "Bearer mbv_anything")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("nil store should pass through, got status %d", rec.Code)
	}
}
