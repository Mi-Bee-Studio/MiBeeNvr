package middleware

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Helper: create a StatusRecorder backed by httptest.NewRecorder.
func newTestStatusRecorder(t *testing.T) *StatusRecorder {
	t.Helper()
	return &StatusRecorder{ResponseWriter: httptest.NewRecorder()}
}

// Helper: assert the status code.
func assertStatus(t *testing.T, r *StatusRecorder, expected int) {
	t.Helper()
	if r.Status != expected {
		t.Errorf("expected status=%d, got %d", expected, r.Status)
	}
}

// Helper: assert the byte count.
func assertBytes(t *testing.T, r *StatusRecorder, expected int) {
	t.Helper()
	if r.Bytes != expected {
		t.Errorf("expected bytes=%d, got %d", expected, r.Bytes)
	}
}

func TestStatusRecorder_WriteHeader(t *testing.T) {
	t.Parallel()
	r := newTestStatusRecorder(t)
	r.WriteHeader(http.StatusNotFound)

	assertStatus(t, r, http.StatusNotFound)

	// Verify the underlying recorder also has the status.
	rr := r.ResponseWriter.(*httptest.ResponseRecorder)
	if rr.Code != http.StatusNotFound {
		t.Errorf("underlying recorder code = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestStatusRecorder_Write(t *testing.T) {
	t.Parallel()
	r := newTestStatusRecorder(t)

	body := []byte("hello world")
	n, err := r.Write(body)
	if err != nil {
		t.Fatalf("Write returned unexpected error: %v", err)
	}
	if n != len(body) {
		t.Errorf("Write returned n=%d, want %d", n, len(body))
	}

	assertStatus(t, r, http.StatusOK) // default status
	assertBytes(t, r, len(body))
}

func TestStatusRecorder_WriteMultiple(t *testing.T) {
	t.Parallel()
	r := newTestStatusRecorder(t)

	r.Write([]byte("hello "))
	r.Write([]byte("world"))

	assertBytes(t, r, 11)
	assertStatus(t, r, http.StatusOK)
}

func TestStatusRecorder_MultipleWriteHeaderLastCallWinsOnRecorder(t *testing.T) {
	t.Parallel()
	r := newTestStatusRecorder(t)

	// StatusRecorder does not protect against multiple WriteHeader calls.
	// Each call overwrites r.Status. The underlying httptest.ResponseRecorder
	// enforces first-wins internally (only first status reaches response).
	r.WriteHeader(http.StatusOK)
	r.WriteHeader(http.StatusInternalServerError)

	// StatusRecorder's own Status field reflects the last value written.
	assertStatus(t, r, http.StatusInternalServerError)

	// The underlying recorder enforces first-wins (httptest behavior).
	rr := r.ResponseWriter.(*httptest.ResponseRecorder)
	if rr.Code != http.StatusOK {
		t.Errorf("underlying recorder code = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestStatusRecorder_WriteHeaderSetsStatusThenWritePreservesIt(t *testing.T) {
	t.Parallel()
	r := newTestStatusRecorder(t)

	// Write header explicitly, then write body — bytes recorded but status unchanged.
	r.WriteHeader(http.StatusTeapot)
	_, _ = r.Write([]byte("data"))

	assertStatus(t, r, http.StatusTeapot)
	assertBytes(t, r, 4)
}

func TestStatusRecorder_WriteDoesNotOverrideExplicitStatus(t *testing.T) {
	t.Parallel()
	r := newTestStatusRecorder(t)

	// When Write is called after WriteHeader, status should already be set.
	r.WriteHeader(http.StatusBadRequest)
	assertStatus(t, r, http.StatusBadRequest)
	assertBytes(t, r, 0)
}

func TestStatusRecorder_ZeroValueDefault(t *testing.T) {
	t.Parallel()
	// Even with a zero-value StatusRecorder (no WriteHeader), Write sets 200.
	r := &StatusRecorder{ResponseWriter: httptest.NewRecorder()}
	_, _ = r.Write([]byte("test"))

	assertStatus(t, r, http.StatusOK)
	assertBytes(t, r, 4)
}

// hijackableResponseWriter wraps httptest.ResponseRecorder to implement http.Hijacker.
type hijackableResponseWriter struct {
	*httptest.ResponseRecorder
	hijacked bool
}

func (h *hijackableResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	return nil, nil, nil
}

func TestStatusRecorder_Hijack(t *testing.T) {
	t.Parallel()
	underlying := &hijackableResponseWriter{ResponseRecorder: httptest.NewRecorder()}
	r := &StatusRecorder{ResponseWriter: underlying}

	conn, rw, err := r.Hijack()
	if err != nil {
		t.Fatalf("Hijack returned unexpected error: %v", err)
	}
	if conn != nil || rw != nil {
		t.Errorf("expected nil conn/bufio from test hijacker, got conn=%v, rw=%v", conn, rw)
	}
	if !underlying.hijacked {
		t.Error("expected underlying Hijack to be called")
	}
}

func TestStatusRecorder_WriteAfterWriteHeader(t *testing.T) {
	t.Parallel()
	r := newTestStatusRecorder(t)

	r.WriteHeader(http.StatusOK)
	_, _ = r.Write([]byte("body"))

	assertStatus(t, r, http.StatusOK)
	assertBytes(t, r, 4)
}

func TestStatusRecorder_ImplementsResponseWriter(t *testing.T) {
	t.Parallel()
	// Compile-time check: StatusRecorder must implement http.ResponseWriter.
	r := newTestStatusRecorder(t)
	var rw http.ResponseWriter = r
	_ = rw
}

func TestStatusRecorder_ImplementsHijacker(t *testing.T) {
	t.Parallel()
	// Compile-time check: StatusRecorder must implement http.Hijacker
	// when the underlying ResponseWriter does.
	underlying := &hijackableResponseWriter{ResponseRecorder: httptest.NewRecorder()}
	r := &StatusRecorder{ResponseWriter: underlying}
	var h http.Hijacker = r
	_ = h
}
