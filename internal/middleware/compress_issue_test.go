package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStreamingGzip_BinaryNoGzipTrailer reproduces and guards issue #109:
// serving an application/octet-stream payload (e.g. /models/*.onnx) through
// the gzip middleware must NOT append a spurious gzip trailer. The bug was:
// even though shouldSkipCompression(octet-stream) routes Write() around the
// gzip writer, Close() unconditionally called gz.Close(), which emits an
// empty-but-valid gzip frame (~20 bytes: 1f 8b 08 ...) appended to the raw
// body — corrupting binary downloads. ONNX models received by the browser
// were raw bytes + a gzip trailer, so ORT's protobuf parser failed with
// INVALID_PROTOBUF.
func TestStreamingGzip_BinaryNoGzipTrailer(t *testing.T) {
	payload := strings.Repeat("A", 1000)
	handler := StreamingGzip(5)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set Content-Type but do NOT call WriteHeader explicitly — mirrors
		// handlers that rely on Go's implicit WriteHeader(200) on first Write.
		// The middleware MUST detect the content type from the header even in
		// this case (it synthesizes the WriteHeader inside Write) so that
		// octet-stream responses are still served raw, not gzip-corrupted.
		w.Header().Set("Content-Type", "application/octet-stream")
		io.WriteString(w, payload)
	}))

	req := httptest.NewRequest(http.MethodGet, "/models/x.onnx", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != payload {
		t.Errorf("body corrupted: got %d bytes (expected %d). First extra byte at index %d",
			len(body), len(payload), len(payload))
	}
	if ce := resp.Header.Get("Content-Encoding"); ce != "" {
		t.Errorf("Content-Encoding should be empty for octet-stream, got %q", ce)
	}
	// Specifically check no gzip magic byte appears at the end of the body.
	if len(body) >= 2 && body[len(body)-20] == 0x1f && body[len(body)-19] == 0x8b {
		t.Errorf("body ends with a gzip trailer (1f 8b ...) — the exact corruption from issue #109")
	}
}
