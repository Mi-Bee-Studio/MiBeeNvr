package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequestLoggerLogsRequest(t *testing.T) {
	t.Helper()
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello"))
	})

	handler := RequestLogger(logger)(next)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, buf.String(), "level=DEBUG")
	require.Contains(t, buf.String(), "method=GET")
	require.Contains(t, buf.String(), "path=/api/test")
	require.Contains(t, buf.String(), "status=200")
}

// TestRequestLoggerLevels pins the #685 denoising contract: routine success
// is Debug (M5 field evidence: 2xx polling was 26% of the 24h volume at
// INFO), client errors stay visible, server errors escalate to WARN.
func TestRequestLoggerLevels(t *testing.T) {
	t.Helper()
	t.Parallel()

	cases := []struct {
		name       string
		status     int
		wantLevel  string
		wantAtInfo bool
	}{
		{"success-200", http.StatusOK, "DEBUG", false},
		{"not-modified-304", http.StatusNotModified, "DEBUG", false},
		{"not-found-404", http.StatusNotFound, "INFO", true},
		{"boom-500", http.StatusInternalServerError, "WARN", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			})
			req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
			RequestLogger(logger)(next).ServeHTTP(httptest.NewRecorder(), req)
			require.Contains(t, buf.String(), "level="+tc.wantLevel)

			var infoBuf bytes.Buffer
			infoLogger := slog.New(slog.NewTextHandler(&infoBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
			RequestLogger(infoLogger)(next).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/x", nil))
			if tc.wantAtInfo {
				require.NotEmpty(t, infoBuf.String(), "must stay visible at default info level")
			} else {
				require.Empty(t, infoBuf.String(), "routine responses must be silent at default info level")
			}
		})
	}
}

func TestRequestLoggerSkipPaths(t *testing.T) {
	t.Helper()
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := RequestLogger(logger, "/api/health", "/api/readyz")(next)

	// Request to skipped path — no log output
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, buf.String(), "skipped path should produce no log output")

	// Request to non-skipped path — should log
	req = httptest.NewRequest(http.MethodGet, "/api/recordings", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEmpty(t, buf.String(), "non-skipped path should produce log output")
	require.Contains(t, buf.String(), "method=GET")
}

func TestRequestLoggerNormalizesPath(t *testing.T) {
	t.Helper()
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := RequestLogger(logger)(next)

	req := httptest.NewRequest(http.MethodGet, "/api/recordings/123456789", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Contains(t, buf.String(), "path=/api/recordings/{id}")
}

func TestStatusRecorderCapturesStatus(t *testing.T) {
	t.Helper()
	t.Parallel()
	rec := httptest.NewRecorder()
	sr := &StatusRecorder{ResponseWriter: rec, Status: http.StatusOK}
	sr.WriteHeader(http.StatusCreated)
	require.Equal(t, http.StatusCreated, sr.Status)
	require.Equal(t, http.StatusCreated, rec.Code)
}

func TestStatusRecorderCapturesBytes(t *testing.T) {
	t.Helper()
	t.Parallel()
	rec := httptest.NewRecorder()
	sr := &StatusRecorder{ResponseWriter: rec}
	n, err := sr.Write([]byte("hello"))
	require.NoError(t, err)
	require.Equal(t, 5, n)
	require.Equal(t, 5, sr.Bytes)
}

func TestStatusRecorderDefaultStatus(t *testing.T) {
	t.Helper()
	t.Parallel()
	rec := httptest.NewRecorder()
	sr := &StatusRecorder{ResponseWriter: rec}
	// Write without explicit WriteHeader should default to 200
	sr.Write([]byte("data"))
	require.Equal(t, http.StatusOK, sr.Status)
}

func TestRequestLoggerLogsPostRequest(t *testing.T) {
	t.Helper()
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	handler := RequestLogger(logger)(next)

	req := httptest.NewRequest(http.MethodPost, "/api/cameras", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.Contains(t, buf.String(), "method=POST")
	require.Contains(t, buf.String(), "status=201")
}
