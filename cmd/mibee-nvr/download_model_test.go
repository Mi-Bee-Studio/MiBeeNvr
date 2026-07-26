package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// makeModelPayload builds a deterministic fake ONNX payload of the given size.
// The first 4 bytes mimic a valid ONNX/protobuf start (0x08 0x0a = field 1,
// length-delimited) so a size check is the only realistic integrity gate.
func makeModelPayload(t *testing.T, size int64) []byte {
	t.Helper()
	buf := make([]byte, size)
	buf[0] = 0x08 // protobuf field 1, wire type 2 (length-delimited)
	buf[1] = 0x0a // length 10
	copy(buf[2:12], []byte("pytorch\x00\x00")) // producer string
	for i := int64(12); i < size; i++ {
		buf[i] = byte(i % 251) // deterministic filler
	}
	return buf
}

// rangeableServer serves `payload`, honoring HTTP Range requests (responding
// 206 Partial Content with the requested byte range). `truncateAfter` forces
// every Nth request to drop the connection mid-stream (simulating CDN
// truncation); set to 0 to disable. Returns the server and a counter of
// completed (non-truncated) requests.
func rangeableServer(t *testing.T, payload []byte, truncateReqNum int64) (*httptest.Server, *int64) {
	t.Helper()
	var reqCount int64
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&reqCount, 1)

		totalLen := int64(len(payload))
		start, end := int64(0), totalLen-1
		if cr := r.Header.Get("Range"); strings.HasPrefix(cr, "bytes=") {
			var from int64
			if _, err := fmt.Sscanf(cr, "bytes=%d-", &from); err == nil && from >= 0 && from < totalLen {
				start = from
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, totalLen))
			w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", totalLen))
			w.WriteHeader(http.StatusOK)
		}

		// Decide how many bytes to actually send. truncateReqNum > 0 and this is
		// the Nth request → send only half the intended range, then close.
		toSend := payload[start : end+1]
		if truncateReqNum > 0 && n == truncateReqNum {
			toSend = toSend[:len(toSend)/2]
		}
		_, _ = io.Copy(w, strings.NewReader(string(toSend)))
		// Implicit connection close on handler return truncates the response.
	})
	return httptest.NewServer(mux), &reqCount
}

// countRequestServer is a simpler server that truncates the FIRST request only
// (forcing a retry), then serves fully on subsequent requests. Used to test the
// retry+resume path end-to-end.
func truncatingThenFullServer(t *testing.T, payload []byte) (*httptest.Server, *int64) {
	t.Helper()
	var reqCount int64
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&reqCount, 1)
		totalLen := int64(len(payload))
		start, end := int64(0), totalLen-1
		status := http.StatusOK
		if cr := r.Header.Get("Range"); strings.HasPrefix(cr, "bytes=") {
			var from int64
			if _, err := fmt.Sscanf(cr, "bytes=%d-", &from); err == nil && from >= 0 && from < totalLen {
				start = from
			}
			status = http.StatusPartialContent
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, totalLen))
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
		w.WriteHeader(status)

		toSend := payload[start : end+1]
		// First request: send only the first 40% then drop (truncation).
		if n == 1 {
			toSend = toSend[:len(toSend)*2/5]
		}
		_, _ = io.Copy(w, strings.NewReader(string(toSend)))
	})
	return httptest.NewServer(mux), &reqCount
}

// fullServerNoRange serves the full payload but ignores Range headers (always
// 200 OK), exercising the "server doesn't support resume" branch.
func fullServerNoRange(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, strings.NewReader(string(payload)))
	})
	return httptest.NewServer(mux)
}

func TestDownloadModelFile_Success(t *testing.T) {
	payload := makeModelPayload(t, 6*1024*1024) // 6MB, above minModelSize
	srv, reqCount := rangeableServer(t, payload, 0)
	defer srv.Close()

	dir := t.TempDir()
	if err := downloadModelFile(dir, "yolo11n.onnx", srv.URL); err != nil {
		t.Fatalf("downloadModelFile failed: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "yolo11n.onnx"))
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if len(got) != len(payload) {
		t.Fatalf("size mismatch: got %d, want %d", len(got), len(payload))
	}
	if string(got[:12]) != string(payload[:12]) {
		t.Fatalf("content prefix mismatch")
	}
	if n := atomic.LoadInt64(reqCount); n != 1 {
		t.Fatalf("expected exactly 1 request for clean download, got %d", n)
	}

	// No leftover .part / .part.size files.
	if _, err := os.Stat(filepath.Join(dir, "yolo11n.onnx.part")); !os.IsNotExist(err) {
		t.Fatalf(".part file should be removed after success, got err=%v", err)
	}
}

func TestDownloadModelFile_RetryAndResume(t *testing.T) {
	payload := makeModelPayload(t, 6 * 1024 * 1024)
	srv, reqCount := truncatingThenFullServer(t, payload)
	defer srv.Close()

	dir := t.TempDir()
	if err := downloadModelFile(dir, "yolo11n.onnx", srv.URL); err != nil {
		t.Fatalf("downloadModelFile should succeed after retry+resume, got: %v", err)
	}

	// First request truncated, second resumed and completed → at least 2 requests.
	if n := atomic.LoadInt64(reqCount); n < 2 {
		t.Fatalf("expected >=2 requests (truncation then resume), got %d", n)
	}

	got, err := os.ReadFile(filepath.Join(dir, "yolo11n.onnx"))
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if len(got) != len(payload) {
		t.Fatalf("final file truncated: got %d, want %d", len(got), len(payload))
	}
	// Verify the resumed prefix + suffix are correct (resume appends, doesn't corrupt).
	for i, b := range payload {
		if got[i] != b {
			t.Fatalf("byte mismatch at %d after resume: got %d, want %d", i, got[i], b)
		}
	}
}

func TestDownloadModelFile_ServerNoRangeSupport(t *testing.T) {
	payload := makeModelPayload(t, 6 * 1024 * 1024)
	srv := fullServerNoRange(t, payload)
	defer srv.Close()

	dir := t.TempDir()
	if err := downloadModelFile(dir, "yolo11n.onnx", srv.URL); err != nil {
		t.Fatalf("downloadModelFile failed when server ignores Range: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "yolo11n.onnx"))
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if len(got) != len(payload) {
		t.Fatalf("size mismatch: got %d, want %d", len(got), len(payload))
	}
}

func TestDownloadModelFile_PersistentTruncation_FailsAfterRetries(t *testing.T) {
	// Server truncates EVERY request → all retries fail → function returns error
	// AND cleans up the .part (the exact bug we're fixing: a stale partial must
	// not be mistaken for a complete file on the next run).
	payload := makeModelPayload(t, 6 * 1024 * 1024)
	srv, reqCount := rangeableServer(t, payload, 1) // truncate request #1 always
	// Override: truncate ALL requests by setting truncateReqNum to a sentinel
	// that matches every request. Rebuild with a custom handler.
	srv.Close()
	var allReqCount int64
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&allReqCount, 1)
		totalLen := int64(len(payload))
		start := int64(0)
		if cr := r.Header.Get("Range"); strings.HasPrefix(cr, "bytes=") {
			var from int64
			if _, err := fmt.Sscanf(cr, "bytes=%d-", &from); err == nil && from >= 0 && from < totalLen {
				start = from
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, totalLen-1, totalLen))
			w.Header().Set("Content-Length", fmt.Sprintf("%d", (totalLen-start)/2)) // lie: claim half
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", totalLen))
			w.WriteHeader(http.StatusOK)
		}
		toSend := payload[start:]
		toSend = toSend[:len(toSend)/2] // only send half
		_, _ = io.Copy(w, strings.NewReader(string(toSend)))
	}))
	defer srv.Close()
	_ = reqCount // silence unused (we replaced the server)

	dir := t.TempDir()
	err := downloadModelFile(dir, "yolo11n.onnx", srv.URL)
	if err == nil {
		t.Fatal("expected error when every attempt truncates, got nil")
	}
	// All 5 backoff slots attempted.
	if n := atomic.LoadInt64(&allReqCount); n != int64(len(downloadBackoff)) {
		t.Fatalf("expected %d attempts, got %d", len(downloadBackoff), n)
	}
	// CRITICAL: .part and .part.size must NOT survive — otherwise a later run
	// would see a stale partial and think the file exists.
	if _, err := os.Stat(filepath.Join(dir, "yolo11n.onnx.part")); !os.IsNotExist(err) {
		t.Fatalf(".part file must be cleaned up after all retries fail, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "yolo11n.onnx.part.size")); !os.IsNotExist(err) {
		t.Fatalf(".part.size sidecar must be cleaned up, got err=%v", err)
	}
	// And the final file must not exist.
	if _, err := os.Stat(filepath.Join(dir, "yolo11n.onnx")); !os.IsNotExist(err) {
		t.Fatalf("final model file must not exist after failure, got err=%v", err)
	}
}

func TestDownloadModelFile_TooSmall_Fails(t *testing.T) {
	// A 1MB payload is below minModelSize even if fully downloaded.
	payload := makeModelPayload(t, 1*1024*1024)
	srv := fullServerNoRange(t, payload)
	defer srv.Close()

	dir := t.TempDir()
	err := downloadModelFile(dir, "yolo11n.onnx", srv.URL)
	if err == nil {
		t.Fatal("expected error for undersized model, got nil")
	}
	if !strings.Contains(err.Error(), "too small") && !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("expected size-related error, got: %v", err)
	}
}

func TestDownloadModelFile_HTTPError_Retries(t *testing.T) {
	// Server returns 500 on every request → all retries fail with HTTP error.
	var reqCount int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&reqCount, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir := t.TempDir()
	err := downloadModelFile(dir, "yolo11n.onnx", srv.URL)
	if err == nil {
		t.Fatal("expected error for HTTP 500, got nil")
	}
	if n := atomic.LoadInt64(&reqCount); n != int64(len(downloadBackoff)) {
		t.Fatalf("expected %d attempts for persistent 500, got %d", len(downloadBackoff), n)
	}
}

func TestParseContentRangeTotal(t *testing.T) {
	cases := []struct {
		header string
		want   int64
	}{
		{"bytes 0-1023/2048", 2048},
		{"bytes 1024-2047/5400000", 5400000},
		{"bytes 0-1023/*", 0}, // unknown total
		{"", 0},
		{"malformed", 0},
	}
	for _, c := range cases {
		if got := parseContentRangeTotal(c.header); got != c.want {
			t.Errorf("parseContentRangeTotal(%q) = %d, want %d", c.header, got, c.want)
		}
	}
}

func TestDownloadBackoffIsBounded(t *testing.T) {
	// Sanity: the backoff schedule must be bounded and reasonable so the CLI
	// doesn't hang forever on a bad network. Total worst-case ~31s.
	var total time.Duration
	for _, d := range downloadBackoff {
		total += d
	}
	if total > 2*time.Minute {
		t.Fatalf("downloadBackoff total %v exceeds 2min — too long for an interactive CLI", total)
	}
	if len(downloadBackoff) < 3 {
		t.Fatalf("downloadBackoff has only %d entries — too few retries", len(downloadBackoff))
	}
}
