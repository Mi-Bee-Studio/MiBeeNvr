package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func quickAlert(t string, sev string) Alert {
	a := newAlert(timeNow())
	a.Type = t
	a.Severity = sev
	a.SourceRef = "cam-1"
	return a
}

// waitSent polls the sink's sent counter until it reaches want (or times out).
func waitSent(t *testing.T, s *WebhookSink, want uint64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.sent.Load() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("sent counter stuck at %d, want %d", s.sent.Load(), want)
}

func startSink(t *testing.T, opts SinkOptions) *WebhookSink {
	t.Helper()
	if len(opts.RetryDelays) == 0 {
		opts.RetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	}
	s, err := NewWebhookSink(opts)
	if err != nil {
		t.Fatalf("NewWebhookSink: %v", err)
	}
	s.Start(context.Background())
	t.Cleanup(s.Stop)
	return s
}

func TestSinkDeliversAlertEnvelope(t *testing.T) {
	var mu sync.Mutex
	var got Alert
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	s := startSink(t, SinkOptions{
		Name:  "test",
		URL:   srv.URL + "/api/ingest/webhook",
		Token: "secret-token",
	})
	s.Enqueue(quickAlert(TypeCameraConnection, SeverityError), false)
	waitSent(t, s, 1)

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/api/ingest/webhook/secret-token" {
		t.Errorf("token should be the final path segment, got %q", gotPath)
	}
	if got.Schema != SchemaVersion || got.Type != TypeCameraConnection {
		t.Errorf("delivered envelope wrong: %+v", got)
	}
}

func TestSink_4xxNoRetry(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	s := startSink(t, SinkOptions{Name: "t", URL: srv.URL, Token: "tok"})
	s.Enqueue(quickAlert(TypeCameraMotion, SeverityWarning), false)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && (s.failed.Load() == 0 || hits.Load() == 0) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // let any (wrong) retry land
	if hits.Load() != 1 {
		t.Errorf("404 must not be retried, got %d requests", hits.Load())
	}
	if s.failed.Load() != 1 {
		t.Errorf("failed counter=%d, want 1", s.failed.Load())
	}
}

func TestSink_5xxRetriesThenSucceeds(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := startSink(t, SinkOptions{Name: "t", URL: srv.URL, Token: "tok"})
	s.Enqueue(quickAlert(TypeCameraMotion, SeverityWarning), false)
	waitSent(t, s, 1)

	if hits.Load() != 3 {
		t.Errorf("expected 3 attempts, got %d", hits.Load())
	}
	if s.retried.Load() != 2 {
		t.Errorf("retried counter=%d, want 2", s.retried.Load())
	}
}

func TestSink_5xxExhaustsRetries(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	s := startSink(t, SinkOptions{Name: "t", URL: srv.URL, Token: "tok"})
	s.Enqueue(quickAlert(TypeCameraMotion, SeverityWarning), false)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && s.failed.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if s.failed.Load() != 1 {
		t.Fatalf("failed counter=%d, want 1", s.failed.Load())
	}
	if hits.Load() != 4 { // initial + 3 retries
		t.Errorf("expected 4 attempts, got %d", hits.Load())
	}
}

func TestSink_Filters(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := startSink(t, SinkOptions{
		Name: "t", URL: srv.URL, Token: "tok",
		Events:      []string{TypeCameraConnection, TypeCameraFreeze},
		SeverityMin: SeverityWarning,
	})
	// Type not in the filter.
	s.Enqueue(quickAlert(TypeCameraMotion, SeverityCritical), false)
	// Severity below the floor.
	s.Enqueue(quickAlert(TypeCameraConnection, SeverityInfo), false)
	// Passes both.
	s.Enqueue(quickAlert(TypeCameraConnection, SeverityError), false)

	waitSent(t, s, 1)
	time.Sleep(30 * time.Millisecond)
	if hits.Load() != 1 {
		t.Errorf("only the passing alert may be delivered, got %d requests", hits.Load())
	}
	if s.dropFilt.Load() != 2 {
		t.Errorf("dropped_filter=%d, want 2", s.dropFilt.Load())
	}
}

func TestSink_QueueOverflowDropsOldest(t *testing.T) {
	// Sink without a running worker: everything stays queued.
	s, err := NewWebhookSink(SinkOptions{Name: "t", URL: "http://127.0.0.1:1", Token: "tok", QueueSize: 2})
	if err != nil {
		t.Fatalf("NewWebhookSink: %v", err)
	}
	s.Enqueue(quickAlert(TypeCameraMotion, SeverityInfo), false)
	s.Enqueue(quickAlert(TypeCameraMotion, SeverityInfo), false)
	s.Enqueue(quickAlert(TypeCameraConnection, SeverityError), false) // overflows: oldest dropped

	if len(s.queue) != 2 {
		t.Fatalf("queue len=%d, want 2", len(s.queue))
	}
	if s.dropQ.Load() != 1 {
		t.Errorf("dropped_queue=%d, want 1", s.dropQ.Load())
	}
	// Drain: the first motion alert was the one dropped; the newest alert
	// (a stateful connection alert) must survive at the tail.
	var drained []string
	for len(s.queue) > 0 {
		drained = append(drained, (<-s.queue).Type)
	}
	if len(drained) != 2 || drained[1] != TypeCameraConnection {
		t.Errorf("newest alert should survive, drained=%v", drained)
	}
}

func TestSink_RateLimit(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := startSink(t, SinkOptions{Name: "t", URL: srv.URL, Token: "tok", BurstPerMin: 2})
	for range 5 {
		s.Enqueue(quickAlert(TypeCameraMotion, SeverityInfo), false)
	}
	waitSent(t, s, 2)
	time.Sleep(50 * time.Millisecond)
	if hits.Load() != 2 {
		t.Errorf("burst cap should stop delivery at 2, got %d", hits.Load())
	}
	if s.dropRate.Load() != 3 {
		t.Errorf("dropped_rate=%d, want 3", s.dropRate.Load())
	}
}

func TestSinkOptionsValidation(t *testing.T) {
	if _, err := NewWebhookSink(SinkOptions{Name: "t", URL: "not-a-url", Token: "x"}); err == nil {
		t.Error("bad URL must fail")
	}
	if _, err := NewWebhookSink(SinkOptions{Name: "t", URL: "ftp://x", Token: "x"}); err == nil {
		t.Error("non-http scheme must fail")
	}
	if _, err := NewWebhookSink(SinkOptions{Name: "t", URL: "http://x", Token: "  "}); err == nil {
		t.Error("empty token must fail")
	}
	if _, err := NewWebhookSink(SinkOptions{Name: "t", URL: "http://x", Token: "x", Events: []string{"camera.typo"}}); err == nil {
		t.Error("unknown event type must fail")
	}
	if _, err := NewWebhookSink(SinkOptions{Name: "t", URL: "http://x", Token: "x"}); err != nil {
		t.Errorf("valid options must pass: %v", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if d := parseRetryAfter("1200"); d != 2*time.Minute {
		t.Errorf("Retry-After should cap at 2m, got %v", d)
	}
	if d := parseRetryAfter("3"); d != 3*time.Second {
		t.Errorf("got %v", d)
	}
	if d := parseRetryAfter(""); d != 0 {
		t.Errorf("empty should be 0, got %v", d)
	}
}

func TestStatsMasksToken(t *testing.T) {
	s := startSink(t, SinkOptions{Name: "prod-hub", URL: "http://receiver.local:9080/api/ingest/webhook", Token: "supersecret"})
	st := s.Stats()
	if st.URL != "http://receiver.local:9080/api/ingest/webhook/***" {
		t.Errorf("status must mask the token, got %q", st.URL)
	}
	if st.Name != "prod-hub" {
		t.Errorf("name=%q", st.Name)
	}
}
