package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteError(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusNotFound, "camera not found")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if body["error"] != "camera not found" {
		t.Fatalf("error = %q", body["error"])
	}
}

func TestWriteJSON(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusOK, map[string]int{"total": 3})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if body["total"] != 3 {
		t.Fatalf("total = %d", body["total"])
	}
}

// TestWriteErrorCodedTrack pins the unified error shape (#1009): every error
// response carries {"error": ..., "code": ...} — hand-written messages get a
// status-derived code, matching what the typed-error track emits.
func TestWriteErrorCodedTrack(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status   int
		msg      string
		wantCode string
	}{
		{http.StatusBadRequest, "bad", "BAD_REQUEST"},
		{http.StatusUnauthorized, "who", "UNAUTHORIZED"},
		{http.StatusForbidden, "no", "FORBIDDEN"},
		{http.StatusNotFound, "gone", "NOT_FOUND"},
		{http.StatusConflict, "clash", "CONFLICT"},
		{http.StatusTooManyRequests, "slow", "RATE_LIMITED"},
		{http.StatusInternalServerError, "boom", "INTERNAL"},
		{http.StatusBadGateway, "upstream", "BAD_GATEWAY"},
		{http.StatusServiceUnavailable, "down", "SERVICE_UNAVAILABLE"},
		{418, "teapot", "CLIENT_ERROR"}, // unmapped 4xx falls back
		{599, "weird", "INTERNAL"},      // unmapped 5xx falls back
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		WriteError(rec, tc.status, tc.msg)
		if rec.Code != tc.status {
			t.Fatalf("status = %d, want %d", rec.Code, tc.status)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("status %d: body not JSON: %v", tc.status, err)
		}
		if body["error"] != tc.msg {
			t.Fatalf("error = %q", body["error"])
		}
		if body["code"] != tc.wantCode {
			t.Fatalf("status %d: code = %q, want %q", tc.status, body["code"], tc.wantCode)
		}
	}
}
