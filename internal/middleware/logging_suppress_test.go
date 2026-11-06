package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestLogSuppressorWindow pins the #46 collapsing contract: one logged line
// per key per window, the swallowed count riding the first line after the
// window rolls.
func TestLogSuppressorWindow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 19, 48, 0, 0, time.UTC)
	s := newLogSuppressor(time.Minute)
	s.now = func() time.Time { return now }

	ok, sup := s.allow("/api/recordings/{id}/ai-status|404|192.168.63.110")
	require.True(t, ok)
	require.Zero(t, sup, "first occurrence always logs")

	for range 3 {
		ok, _ = s.allow("/api/recordings/{id}/ai-status|404|192.168.63.110")
		require.False(t, ok, "duplicates inside the window are swallowed")
	}

	// A different key is independent (distinct shape, status, or host).
	ok, _ = s.allow("/api/recordings/{id}|404|192.168.63.110")
	require.True(t, ok)
	ok, _ = s.allow("/api/recordings/{id}/ai-status|404|10.0.0.9")
	require.True(t, ok)

	// Window rolls: next occurrence logs again and carries the count.
	now = now.Add(90 * time.Second)
	ok, sup = s.allow("/api/recordings/{id}/ai-status|404|192.168.63.110")
	require.True(t, ok)
	require.Equal(t, 3, sup, "swallowed count surfaces on the next logged line")

	// Counts accumulate across windows until a line is actually emitted.
	for range 2 {
		s.allow("/api/recordings/{id}/ai-status|404|192.168.63.110")
	}
	now = now.Add(2 * time.Minute)
	ok, sup = s.allow("/api/recordings/{id}/ai-status|404|192.168.63.110")
	require.True(t, ok)
	require.Equal(t, 2, sup)
}

// TestRequestLoggerSuppressesRepeated4xx: a same-shape 4xx burst from one
// client produces exactly one INFO request line (the #46 storm was 1593/h —
// distinct recording IDs, identical normalized shape, same host). 5xx is
// never suppressed.
func TestRequestLoggerSuppressesRepeated4xx(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	boom := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/recordings/111/ai-status", "/api/recordings/222/ai-status":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	handler := RequestLogger(logger)(boom)

	do := func(path string) {
		req := httptest.NewRequest(http.MethodPatch, path, nil)
		req.RemoteAddr = "192.168.63.110:41234"
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}

	for range 5 {
		do("/api/recordings/111/ai-status")
	}
	do("/api/recordings/222/ai-status")
	do("/api/other")
	do("/api/other")

	out := buf.String()
	require.Equal(t, 1, strings.Count(out, "path=/api/recordings/{id}/ai-status"),
		"same-shape 404 burst (distinct ids) must collapse to one line")
	require.Equal(t, 2, strings.Count(out, "path=/api/other"),
		"a 500 line is never suppressed, even back-to-back")
}
