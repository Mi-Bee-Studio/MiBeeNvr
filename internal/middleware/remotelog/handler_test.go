package remotelog

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
)

// testCollector tracks requests received by the test server.
type testCollector struct {
	Bodies []string
	Count  atomic.Int64
	mu     sync.Mutex // guards Bodies
}

func (tc *testCollector) getBodies() []string {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	out := make([]string, len(tc.Bodies))
	copy(out, tc.Bodies)
	return out
}

// startTestServer creates an httptest server that captures POST bodies.
func startTestServer(statusCode int) (*httptest.Server, *testCollector) {
	tc := &testCollector{}
	if statusCode == 0 {
		statusCode = http.StatusNoContent
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/insert/jsonline", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		tc.mu.Lock()
		tc.Bodies = append(tc.Bodies, string(body))
		tc.mu.Unlock()
		// See handler() above — Count must be incremented after body capture.
		tc.Count.Add(1)
		w.WriteHeader(statusCode)
	})
	srv := httptest.NewServer(mux)
	return srv, tc
}

func TestHandler_Enabled(t *testing.T) {
	t.Parallel()
	h := New("http://localhost/insert/jsonline", "jsonline", slog.LevelInfo, nil)
	defer h.Close()

	if !h.Enabled(context.TODO(), slog.LevelInfo) {
		t.Fatal("expected INFO level to be enabled")
	}
	if !h.Enabled(context.TODO(), slog.LevelError) {
		t.Fatal("expected ERROR level to be enabled")
	}
	if h.Enabled(context.TODO(), slog.LevelDebug) {
		t.Fatal("expected DEBUG level to be disabled")
	}
}

func TestHandler_Handle_BatchFlush(t *testing.T) {
	t.Parallel()
	srv, tc := startTestServer(0)
	defer srv.Close()

	h := New(srv.URL+"/insert/jsonline", "jsonline", slog.LevelDebug, nil)
	defer h.Close()

	// Send enough logs to trigger buffer-full flush
	for i := range defaultBufferSize {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "test message", 0)
		r.AddAttrs(slog.Int("index", i))
		_ = h.Handle(context.TODO(), r)
	}

	// Wait for async flush
	waitForRequests(t, &tc.Count, 1, 2*time.Second)

	bodies := tc.getBodies()
	if len(bodies) == 0 {
		t.Fatal("expected at least one batch to be sent")
	}

	// Verify NDJSON: each line should be valid JSON
	for _, body := range bodies {
		for _, line := range splitNDJSON(body) {
			if line == "" {
				continue
			}
			var obj map[string]any
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				t.Errorf("invalid JSON line: %s\nerror: %v", line, err)
			}
		}
	}
}

func TestHandler_Handle_FailureTolerance(t *testing.T) {
	t.Parallel()

	var reqCount atomic.Int64
	// Server that always returns 500
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	m := metrics.NewMetrics()
	h := New(srv.URL+"/insert/jsonline", "jsonline", slog.LevelDebug, m)
	defer h.Close()

	for range defaultBufferSize {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "fail test", 0)
		_ = h.Handle(context.TODO(), r)
	}

	waitForRequests(t, &reqCount, 1, 2*time.Second)
}

func TestHandler_Close_FlushesRemaining(t *testing.T) {
	t.Parallel()
	srv, tc := startTestServer(0)
	defer srv.Close()

	h := New(srv.URL+"/insert/jsonline", "jsonline", slog.LevelDebug, nil)

	// Send fewer than buffer size — won't trigger automatic flush
	for i := range 50 {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "remaining msg", 0)
		r.AddAttrs(slog.Int("idx", i))
		_ = h.Handle(context.TODO(), r)
	}

	// Close should flush remaining
	h.Close()

	// Wait a bit for final flush
	time.Sleep(200 * time.Millisecond)

	bodies := tc.getBodies()
	if len(bodies) == 0 {
		t.Fatal("expected Close() to flush remaining logs")
	}

	// Count lines in the body
	totalLines := 0
	for _, body := range bodies {
		totalLines += len(splitNDJSON(body))
	}
	if totalLines < 50 {
		t.Errorf("expected at least 50 log lines, got %d", totalLines)
	}
}

func TestHandler_WithAttrs(t *testing.T) {
	t.Parallel()
	srv, tc := startTestServer(0)
	defer srv.Close()

	h := New(srv.URL+"/insert/jsonline", "jsonline", slog.LevelDebug, nil)
	defer h.Close()

	// Create a child handler with attrs
	child := h.WithAttrs([]slog.Attr{slog.String("component", "test-comp")})
	for range defaultBufferSize {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "attrs test", 0)
		_ = child.Handle(context.TODO(), r)
	}

	waitForRequests(t, &tc.Count, 1, 2*time.Second)

	bodies := tc.getBodies()
	if len(bodies) == 0 {
		t.Fatal("expected batch to be sent")
	}

	// Check that at least one entry has the "component" field
	found := false
	for _, body := range bodies {
		for _, line := range splitNDJSON(body) {
			if line == "" {
				continue
			}
			var obj map[string]any
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				continue
			}
			if obj["component"] == "test-comp" {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Error("expected 'component=test-comp' attr in at least one log entry")
	}
}

func TestHandler_WithGroup(t *testing.T) {
	t.Parallel()
	srv, tc := startTestServer(0)
	defer srv.Close()

	h := New(srv.URL+"/insert/jsonline", "jsonline", slog.LevelDebug, nil)
	defer h.Close()

	grouped := h.WithGroup("server")
	for range defaultBufferSize {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "grouped test", 0)
		r.AddAttrs(slog.String("host", "localhost"))
		_ = grouped.Handle(context.TODO(), r)
	}

	waitForRequests(t, &tc.Count, 1, 2*time.Second)

	bodies := tc.getBodies()
	found := false
	for _, body := range bodies {
		for _, line := range splitNDJSON(body) {
			if line == "" {
				continue
			}
			var obj map[string]any
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				continue
			}
			if obj["server.host"] == "localhost" {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Error("expected 'server.host=localhost' group prefix in log entry")
	}
}

func TestHandler_Close_MultipleTimes(t *testing.T) {
	t.Parallel()
	h := New("http://localhost/insert/jsonline", "jsonline", slog.LevelInfo, nil)
	h.Close()
	h.Close() // should not panic
}

func TestHandler_NilMetrics(t *testing.T) {
	t.Parallel()
	srv, tc := startTestServer(0)
	defer srv.Close()

	h := New(srv.URL+"/insert/jsonline", "jsonline", slog.LevelDebug, nil)
	defer h.Close()

	for range defaultBufferSize {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "nil metrics", 0)
		_ = h.Handle(context.TODO(), r)
	}

	waitForRequests(t, &tc.Count, 1, 2*time.Second)
	// No panic = success
}

func TestHandler_WithGroup_Empty(t *testing.T) {
	t.Parallel()
	h := New("http://localhost/insert/jsonline", "jsonline", slog.LevelInfo, nil)
	defer h.Close()

	got := h.WithGroup("")
	if got != h {
		t.Error("WithGroup(\"\") should return the same handler")
	}
}

func TestExtractFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		msg     string
		want    map[string]any
		wantNot []string
	}{
		{
			name: "camera_id and component",
			msg:  "ERROR connection error component=xiaomi-recorder camera_id=cam-123 backoff=2s",
			want: map[string]any{
				"component": "xiaomi-recorder",
				"camera_id": "cam-123",
				"backoff":   "2s",
			},
		},
		{
			name: "quoted value with spaces",
			msg:  `ERROR stream error error="dial tcp: connection refused" attempt=3`,
			want: map[string]any{
				"error":   "dial tcp: connection refused",
				"attempt": "3",
			},
		},
		{
			name:    "no key=value pairs",
			msg:     "simple log message",
			wantNot: []string{"component", "camera_id"},
		},
		{
			name: "prefixed message",
			msg:  "INFO resolved xiaomi MISS URL component=xiaomi-cloud did=1058760647 ip=192.168.1.1 vendor=cs2",
			want: map[string]any{
				"component": "xiaomi-cloud",
				"did":       "1058760647",
				"ip":        "192.168.1.1",
				"vendor":    "cs2",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Helper()
			obj := make(map[string]any)
			extractFields(tt.msg, obj)
			for k, v := range tt.want {
				got, ok := obj[k]
				if !ok {
					t.Errorf("expected key %q not found", k)
				} else if got != v {
					t.Errorf("key %q: got %q, want %q", k, got, v)
				}
			}
			for _, k := range tt.wantNot {
				if _, ok := obj[k]; ok {
					t.Errorf("did not expect key %q", k)
				}
			}
		})
	}
}

func TestSend_UsesMsgField(t *testing.T) {
	t.Parallel()
	srv, tc := startTestServer(0)
	defer srv.Close()

	h := New(srv.URL+"/insert/jsonline", "jsonline", slog.LevelDebug, nil)
	defer h.Close()

	for range defaultBufferSize {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "test message content", 0)
		_ = h.Handle(context.TODO(), r)
	}

	waitForRequests(t, &tc.Count, 1, 2*time.Second)

	bodies := tc.getBodies()
	if len(bodies) == 0 {
		t.Fatal("expected batch to be sent")
	}

	var obj map[string]any
	for _, line := range splitNDJSON(bodies[0]) {
		if line == "" {
			continue
		}
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		break
	}

	// Must have _msg (not message) as the primary message field
	if obj["_msg"] != "test message content" {
		t.Errorf("expected _msg='test message content', got %v", obj["_msg"])
	}
	// Should NOT have 'message' field
	if _, ok := obj["message"]; ok {
		t.Error("expected no 'message' field, only '_msg'")
	}
}

func TestSend_ExtractsFieldsFromMessage(t *testing.T) {
	t.Parallel()
	srv, tc := startTestServer(0)
	defer srv.Close()

	h := New(srv.URL+"/insert/jsonline", "jsonline", slog.LevelDebug, nil)
	defer h.Close()

	for range defaultBufferSize {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "connection error component=recorder camera_id=cam-abc", 0)
		_ = h.Handle(context.TODO(), r)
	}

	waitForRequests(t, &tc.Count, 1, 2*time.Second)

	bodies := tc.getBodies()
	var obj map[string]any
outer:
	for _, body := range bodies {
		for _, line := range splitNDJSON(body) {
			if line == "" {
				continue
			}
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				continue
			}
			break outer
		}
	}

	if obj == nil {
		t.Fatal("no valid JSON entry found")
	}
	// Extracted fields should be present
	if obj["component"] != "recorder" {
		t.Errorf("expected component='recorder', got %v", obj["component"])
	}
	if obj["camera_id"] != "cam-abc" {
		t.Errorf("expected camera_id='cam-abc', got %v", obj["camera_id"])
	}
}

func TestExtractFields_DoesNotOverwrite(t *testing.T) {
	t.Parallel()
	srv, tc := startTestServer(0)
	defer srv.Close()

	h := New(srv.URL+"/insert/jsonline", "jsonline", slog.LevelDebug, nil)
	defer h.Close()

	// Send with explicit slog attr camera_id=explicit-value
	for range defaultBufferSize {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "msg camera_id=extracted-value", 0)
		r.AddAttrs(slog.String("camera_id", "explicit-value"))
		_ = h.Handle(context.TODO(), r)
	}

	waitForRequests(t, &tc.Count, 1, 2*time.Second)

	bodies := tc.getBodies()
	var obj map[string]any
outer:
	for _, body := range bodies {
		for _, line := range splitNDJSON(body) {
			if line == "" {
				continue
			}
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				continue
			}
			break outer
		}
	}

	if obj == nil {
		t.Fatal("no valid JSON entry found")
	}
	// Explicit slog attr should win over extracted value
	if obj["camera_id"] != "explicit-value" {
		t.Errorf("explicit attr should win: got %v, want 'explicit-value'", obj["camera_id"])
	}
}

// --- helpers ---

func waitForRequests(t *testing.T, count *atomic.Int64, want int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if count.Load() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("timed out waiting for %d requests, got %d", want, count.Load())
}

func splitNDJSON(body string) []string {
	lines := make([]string, 0)
	start := 0
	for i := 0; i <= len(body); i++ {
		if i == len(body) || body[i] == '\n' {
			if i > start {
				lines = append(lines, body[start:i])
			}
			start = i + 1
		}
	}
	return lines
}
