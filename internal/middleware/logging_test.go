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
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello"))
	})

	handler := RequestLogger(logger)(next)

	req := httptest.NewRequest("GET", "/api/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, buf.String(), "method=GET")
	require.Contains(t, buf.String(), "path=/api/test")
	require.Contains(t, buf.String(), "status=200")
}

func TestRequestLoggerSkipPaths(t *testing.T) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := RequestLogger(logger, "/api/health", "/api/readyz")(next)

	// Request to skipped path — no log output
	req := httptest.NewRequest("GET", "/api/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, buf.String(), "skipped path should produce no log output")

	// Request to non-skipped path — should log
	req = httptest.NewRequest("GET", "/api/recordings", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEmpty(t, buf.String(), "non-skipped path should produce log output")
	require.Contains(t, buf.String(), "method=GET")
}

func TestRequestLoggerNormalizesPath(t *testing.T) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := RequestLogger(logger)(next)

	req := httptest.NewRequest("GET", "/api/recordings/123456789", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Contains(t, buf.String(), "path=/api/recordings/{id}")
}

func TestStatusRecorderCapturesStatus(t *testing.T) {
	t.Helper()
	rec := httptest.NewRecorder()
	sr := &StatusRecorder{ResponseWriter: rec, Status: http.StatusOK}
	sr.WriteHeader(http.StatusCreated)
	require.Equal(t, http.StatusCreated, sr.Status)
	require.Equal(t, http.StatusCreated, rec.Code)
}

func TestStatusRecorderCapturesBytes(t *testing.T) {
	t.Helper()
	rec := httptest.NewRecorder()
	sr := &StatusRecorder{ResponseWriter: rec}
	n, err := sr.Write([]byte("hello"))
	require.NoError(t, err)
	require.Equal(t, 5, n)
	require.Equal(t, 5, sr.Bytes)
}

func TestStatusRecorderDefaultStatus(t *testing.T) {
	t.Helper()
	rec := httptest.NewRecorder()
	sr := &StatusRecorder{ResponseWriter: rec}
	// Write without explicit WriteHeader should default to 200
	sr.Write([]byte("data"))
	require.Equal(t, http.StatusOK, sr.Status)
}

func TestRequestLoggerLogsPostRequest(t *testing.T) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	handler := RequestLogger(logger)(next)

	req := httptest.NewRequest("POST", "/api/cameras", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.Contains(t, buf.String(), "method=POST")
	require.Contains(t, buf.String(), "status=201")
}
