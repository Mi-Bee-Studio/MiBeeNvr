package update

// Coverage for the Checker lifecycle surface (#580): Start/Stop and the
// forced Refresh path, on top of the existing fetchAndCache tests. Uses
// the same mock-GitHub httptest pattern (checker_test.go).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRefreshForcesNetworkFetch(t *testing.T) {
	t.Parallel()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v0.12.0", "name": "R", "html_url": "https://x",
			"published_at": "2026-01-01T00:00:00Z", "body": "notes",
		})
	}))
	defer srv.Close()

	c := New("v0.10.0", "owner/repo", "stable", time.Hour)
	c.endpoint = srv.URL

	// Status before any fetch: current set, everything else zero.
	st := c.Status()
	if st.Current != "v0.10.0" || st.UpdateAvailable {
		t.Fatalf("pre-refresh status = %+v", st)
	}

	st, err := c.Refresh(context.Background())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if !st.UpdateAvailable || st.Latest != "v0.12.0" {
		t.Fatalf("post-refresh status = %+v", st)
	}
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}

	// The cache survives into Status().
	if got := c.Status().Latest; got != "v0.12.0" {
		t.Fatalf("cached latest = %q", got)
	}

	// A failing endpoint refresh returns the error; cache stays intact.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close() // free the port so the dial fails
	c.endpoint = deadURL
	if _, err := c.Refresh(context.Background()); err == nil {
		t.Fatal("refresh against dead endpoint must fail")
	}
	if got := c.Status().Latest; got != "v0.12.0" {
		t.Fatalf("cache lost after failed refresh: %q", got)
	}
}

func TestStartStopLifecycle(t *testing.T) {
	t.Parallel()
	srv, _, _ := newTestServer(t, "v0.11.0", "")
	defer srv.Close()

	c := New("v0.10.0", "owner/repo", "stable", time.Hour)
	c.endpoint = srv.URL

	ctx, cancel := context.WithCancel(context.Background())
	c.Start(ctx)

	// The initial fetch is synchronous inside Start's first iteration —
	// observe the cached status instead of sleeping (#571).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.Status().Latest == "v0.11.0" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := c.Status().Latest; got != "v0.11.0" {
		t.Fatalf("poller never fetched: %+v", c.Status())
	}

	// Stop is idempotent and cancelling the context also ends the loop.
	c.Stop()
	c.Stop()
	cancel()
}
