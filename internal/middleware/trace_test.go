package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGenerateTraceID(t *testing.T) {
	t.Helper()
	id1 := GenerateTraceID()
	id2 := GenerateTraceID()
	if id1 == "" {
		t.Fatal("expected non-empty trace ID")
	}
	if len(id1) != 16 {
		t.Errorf("expected 16-char hex trace ID, got %d chars: %q", len(id1), id1)
	}
	if id1 == id2 {
		t.Error("expected unique trace IDs")
	}
}

func TestContextWithTraceID(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	tid := TraceIDFromContext(ctx)
	if tid != "" {
		t.Errorf("expected empty trace ID from bare context, got %q", tid)
	}

	ctx = ContextWithTraceID(ctx, "abc123")
	tid = TraceIDFromContext(ctx)
	if tid != "abc123" {
		t.Errorf("expected 'abc123', got %q", tid)
	}
}

func TestLoggerFromContext(t *testing.T) {
	t.Helper()
	// Without trace ID — should return default logger (no panic).
	logger := LoggerFromContext(context.Background())
	if logger == nil {
		t.Fatal("expected non-nil logger")
	}

	// With trace ID — logger should include it.
	ctx := ContextWithTraceID(context.Background(), "test-tid")
	logger = LoggerFromContext(ctx)
	if logger == nil {
		t.Fatal("expected non-nil logger")
	}
}

func TestTraceMiddleware(t *testing.T) {
	t.Helper()
	var capturedTID string
	handler := TraceMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedTID = TraceIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	// No incoming X-Request-Id — middleware should generate one.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	handler.ServeHTTP(rec, req)
	if capturedTID == "" {
		t.Error("expected trace ID in context")
	}
	if rec.Header().Get(RequestIDHeader) == "" {
		t.Error("expected X-Request-Id in response header")
	}
	if rec.Header().Get(RequestIDHeader) != capturedTID {
		t.Error("response header trace ID should match context trace ID")
	}
}

func TestTraceMiddleware_PropagatesIncomingID(t *testing.T) {
	t.Helper()
	var capturedTID string
	handler := TraceMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedTID = TraceIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	// Incoming request with X-Request-Id — middleware should reuse it.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set(RequestIDHeader, "upstream-id-123")
	handler.ServeHTTP(rec, req)
	if capturedTID != "upstream-id-123" {
		t.Errorf("expected upstream trace ID to be reused, got %q", capturedTID)
	}
	if rec.Header().Get(RequestIDHeader) != "upstream-id-123" {
		t.Error("response header should carry upstream trace ID")
	}
}

func TestRequestLogger_IncludesTraceID(t *testing.T) {
	t.Helper()
	var capturedTID string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedTID = TraceIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	mw := RequestLogger(ComponentLogger("test"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/test", nil)
	mw(inner).ServeHTTP(rec, req)

	if capturedTID == "" {
		t.Error("expected trace ID injected into context by RequestLogger")
	}
	if rec.Header().Get(RequestIDHeader) == "" {
		t.Error("expected X-Request-Id in response header from RequestLogger")
	}
}
