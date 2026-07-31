package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIsNewer(t *testing.T) {
	t.Helper()
	cases := []struct {
		name      string
		current   string
		candidate string
		want      bool
	}{
		{"patch newer", "v0.10.0", "v0.10.1", true},
		{"minor newer", "v0.10.0", "v0.11.0", true},
		{"major newer", "v0.10.0", "v1.0.0", true},
		{"same version", "v0.10.0", "v0.10.0", false},
		{"older candidate (no downgrade)", "v0.10.0", "v0.9.5", false},
		{"no v prefix on current", "0.10.0", "v0.10.1", true},
		{"no v prefix on candidate", "v0.10.0", "0.10.1", true},
		{"prerelease less than release", "v0.10.0-beta.1", "v0.10.0", true},
		{"release greater than prerelease (no downgrade)", "v0.10.0", "v0.10.0-beta.1", false},
		{"dev local build never reports update", "dev", "v0.10.0", false},
		{"empty local build never reports update", "", "v0.10.0", false},
		{"non-semver local build", "dirty-build", "v0.10.0", false},
		{"non-semver remote", "v0.10.0", "latest", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Helper()
			if got := isNewer(tc.current, tc.candidate); got != tc.want {
				t.Fatalf("isNewer(%q, %q) = %v, want %v", tc.current, tc.candidate, got, tc.want)
			}
		})
	}
}

func TestDeploymentEnv(t *testing.T) {
	t.Helper()
	t.Setenv("NVR_DEPLOYMENT", "docker")
	if got := Deployment(); got != "docker" {
		t.Fatalf("Deployment() = %q, want docker", got)
	}
	t.Setenv("NVR_DEPLOYMENT", "")
	// /.dockerenv fallback is environment-dependent; just ensure it returns a
	// non-empty, known value without asserting the host's actual state.
	got := Deployment()
	if got != "docker" && got != "binary" {
		t.Fatalf("Deployment() = %q, want docker or binary", got)
	}
}

// newTestServer returns a mock GitHub releases endpoint and the ETag it honors.
func newTestServer(t *testing.T, tag string, etag string) (*httptest.Server, *int, *int) {
	t.Helper()
	hits, notModified := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if etag != "" && r.Header.Get("If-None-Match") == etag {
			notModified++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		resp := map[string]any{
			"tag_name":     tag,
			"name":         "Release " + tag,
			"html_url":     "https://example.com/release",
			"published_at": "2026-01-01T00:00:00Z",
			"body":         "## What's new\n- fixes",
		}
		w.Header().Set("Content-Type", "application/json")
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		json.NewEncoder(w).Encode(resp)
	}))
	return srv, &hits, &notModified
}

func TestFetchAndCache_DetectsUpdate(t *testing.T) {
	t.Helper()
	srv, hits, _ := newTestServer(t, "v0.11.0", "")
	defer srv.Close()

	c := New("v0.10.0", "owner/repo", "stable", time.Hour)
	c.endpoint = srv.URL
	st, err := c.fetchAndCache(context.Background())
	if err != nil {
		t.Fatalf("fetchAndCache: %v", err)
	}
	if !st.UpdateAvailable {
		t.Fatalf("expected update available for v0.10.0 → v0.11.0")
	}
	if st.Latest != "v0.11.0" {
		t.Fatalf("Latest = %q, want v0.11.0", st.Latest)
	}
	if st.Changelog == "" {
		t.Fatal("changelog empty")
	}
	if *hits != 1 {
		t.Fatalf("hits = %d, want 1", *hits)
	}
	// Cached value should be served by Status() without another fetch.
	if got := c.Status(); !got.UpdateAvailable {
		t.Fatal("Status() did not return cached update")
	}
	if *hits != 1 {
		t.Fatalf("Status() caused extra fetch: hits = %d", *hits)
	}
}

func TestFetchAndCache_ETag304(t *testing.T) {
	t.Helper()
	const etag = `"abc123"`
	srv, hits, notMod := newTestServer(t, "v0.11.0", etag)
	defer srv.Close()

	c := New("v0.10.0", "owner/repo", "stable", time.Hour)
	c.endpoint = srv.URL

	if _, err := c.fetchAndCache(context.Background()); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	// Second fetch carries the stored ETag → server returns 304.
	if _, err := c.fetchAndCache(context.Background()); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if *hits != 2 {
		t.Fatalf("hits = %d, want 2", *hits)
	}
	if *notMod != 1 {
		t.Fatalf("not-modified count = %d, want 1", *notMod)
	}
	// Cache must still hold the original payload after a 304.
	if got := c.Status(); got.Latest != "v0.11.0" || !got.UpdateAvailable {
		t.Fatalf("cache lost payload after 304: %+v", got)
	}
}

func TestFetchAndCache_NoUpdateWhenSameVersion(t *testing.T) {
	t.Helper()
	srv, _, _ := newTestServer(t, "v0.10.0", "")
	defer srv.Close()

	c := New("v0.10.0", "owner/repo", "stable", time.Hour)
	c.endpoint = srv.URL
	st, err := c.fetchAndCache(context.Background())
	if err != nil {
		t.Fatalf("fetchAndCache: %v", err)
	}
	if st.UpdateAvailable {
		t.Fatal("expected no update when versions match")
	}
}

func TestStatusBeforeFirstFetch(t *testing.T) {
	t.Helper()
	c := New("v0.10.0", "owner/repo", "stable", time.Hour)
	st := c.Status()
	if st.Current != "v0.10.0" {
		t.Fatalf("Current = %q, want v0.10.0", st.Current)
	}
	if st.UpdateAvailable {
		t.Fatal("should not report update before any fetch")
	}
	if st.CheckedAt != "" {
		t.Fatal("CheckedAt should be empty before first fetch")
	}
}

func TestFetchAndCache_ErrorOn500(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New("v0.10.0", "owner/repo", "stable", time.Hour)
	c.endpoint = srv.URL
	if _, err := c.fetchAndCache(context.Background()); err == nil {
		t.Fatal("expected error on HTTP 500")
	}
	// Status() still returns a sane pre-fetch value, never panics.
	if st := c.Status(); st.Current != "v0.10.0" {
		t.Fatalf("Current = %q after failed fetch", st.Current)
	}
}
