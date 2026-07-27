package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
//
// This is the original single-case regression test, kept for its detailed
// narrative. The exhaustive table-driven coverage lives in
// TestStreamingGzip_BinaryContentTypesNoCorruption below.
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

// TestStreamingGzip_BinaryContentTypesNoCorruption is the table-driven
// regression suite for issue #124's lesson: the gzip middleware is a global
// cross-cutting concern, and the #109/#124 trailer-corruption bug affected
// FAR more than just ONNX models — any binary download (video segments,
// snapshots, audio) served through the middleware could be silently
// corrupted at the tail. Each case asserts the response is byte-for-byte
// identical to what the handler wrote (not just the first 16 bytes — the
// #109 corruption hid at the very end of the payload).
//
// Both WriteHeader modes are exercised because #124's root cause was that
// the middleware mirrored net/http's implicit-WriteHeader-on-first-Write
// semantics: a handler that sets Content-Type then writes without an
// explicit WriteHeader must still get content-type detection.
func TestStreamingGzip_BinaryContentTypesNoCorruption(t *testing.T) {
	// A non-trivial payload with a recognizable tail so trailer corruption
	// (which appends ~20 bytes after the real end) is detectable.
	payload := bytes.Repeat([]byte{0xAB, 0xCD, 0xEF, 0x01}, 256) // 1024 bytes, distinct pattern

	cases := []struct {
		name        string
		contentType string
		// explicitHeader: true → handler calls WriteHeader(200) before Write;
		// false → handler relies on implicit WriteHeader (the #124 scenario).
		explicitHeader bool
	}{
		// Binary content types that shouldSkipCompression returns true for —
		// these MUST pass through raw, byte-for-byte, with NO Content-Encoding
		// and NO appended gzip trailer.
		{"octet-stream/implicit-header", "application/octet-stream", false},
		{"octet-stream/explicit-header", "application/octet-stream", true},
		{"video-mp4/implicit-header", "video/mp4", false},
		{"video-mp4/explicit-header", "video/mp4", true},
		{"image-jpeg/implicit-header", "image/jpeg", false},
		{"image-jpeg/explicit-header", "image/jpeg", true},
		{"audio-wav/implicit-header", "audio/wav", false},
		{"audio-wav/explicit-header", "audio/wav", true},
		{"application-zip", "application/zip", false},
		{"application-gzip", "application/gzip", false},
		{"application-x-tar", "application/x-tar", false},
		// Content-Type with parameters — must still be recognized after the
		// "; charset=..." is stripped by shouldSkipCompression.
		{"video-mp4-with-charset", "video/mp4; charset=binary", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := StreamingGzip(5)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				if tc.explicitHeader {
					w.WriteHeader(http.StatusOK)
				}
				if _, err := w.Write(payload); err != nil {
					t.Fatalf("handler Write failed: %v", err)
				}
			}))

			req := httptest.NewRequest(http.MethodGet, "/binary", nil)
			req.Header.Set("Accept-Encoding", "gzip")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			resp := rec.Result()
			body, _ := io.ReadAll(resp.Body)

			// Core assertion #1: byte-for-byte equality over the FULL payload.
			// #109's corruption was at the tail only, so a prefix check would miss it.
			if !bytes.Equal(body, payload) {
				t.Errorf("body corrupted: got %d bytes, want %d.\n  got tail: % x\n  want tail: % x",
					len(body), len(payload),
					body[max(0, len(body)-24):], payload[max(0, len(payload)-24):])
			}

			// Core assertion #2: no Content-Encoding header for skipped types.
			// A stale "Content-Encoding: gzip" header on a raw body makes
			// browsers/clients attempt gzip decode → corruption / decode error.
			if ce := resp.Header.Get("Content-Encoding"); ce != "" {
				t.Errorf("Content-Encoding should be empty for %q, got %q", tc.contentType, ce)
			}

			// Core assertion #3: no gzip trailer appended (the literal #109 bug).
			// An empty gzip frame is ~20 bytes starting with magic 1f 8b.
			if len(body) > len(payload) && bytes.HasPrefix(body[len(payload):], []byte{0x1f, 0x8b}) {
				t.Errorf("trailing gzip frame detected after payload — the exact #109 corruption")
			}
		})
	}
}

// TestStreamingGzip_CompressibleTypesActuallyCompressed is the positive
// control for the table-driven suite: content types that SHOULD be compressed
// must still come out gzipped (with magic bytes + Content-Encoding header).
// This guards against an over-broad "fix" that skips compression for
// everything.
func TestStreamingGzip_CompressibleTypesActuallyCompressed(t *testing.T) {
	// A highly compressible payload so gzip produces a real frame, not stored mode.
	payload := strings.Repeat(`{"id":"cam1","name":"Front Door","protocol":"rtsp"}`, 64)

	cases := []struct {
		name        string
		contentType string
	}{
		{"application-json", "application/json"},
		{"text-event-stream", "text/event-stream"},
		{"text-html", "text/html; charset=utf-8"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := StreamingGzip(5)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				io.WriteString(w, payload)
			}))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Accept-Encoding", "gzip")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			resp := rec.Result()
			body, _ := io.ReadAll(resp.Body)

			require.Equal(t, "gzip", resp.Header.Get("Content-Encoding"),
				"%q must be compressed", tc.contentType)
			require.Greater(t, len(body), 2, "compressed body must be non-empty")
			assert.Equal(t, byte(0x1f), body[0], "gzip magic byte 0")
			assert.Equal(t, byte(0x8b), body[1], "gzip magic byte 1")
			// And the compressed body must NOT equal the raw payload (it's actually compressed).
			assert.NotEqual(t, payload, string(body), "body should be compressed, not raw")
		})
	}
}

// TestStreamingGzip_RangeRequestNotCorrupted guards Range requests
// (GET /api/recordings/{id}/download with Range header → http.ServeFile
// returns 206 Partial Content with raw video bytes). The middleware must not
// set Content-Encoding on a 206 video response, nor corrupt the partial body.
// This was flagged in issue #134 as a class of download affected by the same
// global-middleware surface as #109/#124.
func TestStreamingGzip_RangeRequestNotCorrupted(t *testing.T) {
	// Simulate a video segment served with a Range request → 206 Partial Content.
	full := bytes.Repeat([]byte{0x00, 0x01, 0x02, 0x03}, 512) // 2048 bytes
	rangeStart, rangeEnd := 512, 1023
	expectedPartial := full[rangeStart : rangeEnd+1]

	handler := StreamingGzip(5)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", "bytes 512-1023/2048")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusPartialContent)
		if _, err := w.Write(expectedPartial); err != nil {
			t.Fatalf("handler Write failed: %v", err)
		}
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/recordings/abc/download", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=512-1023")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)

	assert.Equal(t, http.StatusPartialContent, resp.StatusCode, "206 must pass through")
	assert.Empty(t, resp.Header.Get("Content-Encoding"), "206 video must not get Content-Encoding")
	assert.True(t, bytes.Equal(body, expectedPartial),
		"partial body corrupted: got %d bytes, want %d", len(body), len(expectedPartial))
}

// TestStreamingGzip_BinaryNoCorruption_GuardsAgainstRegression is a
// meta-test: it verifies that the skip-list logic in shouldSkipCompression
// is what actually protects binary responses. If someone narrows the skip
// list (e.g. removes "application/octet-stream"), this test goes red,
// confirming the table-driven tests above are meaningful guards and not
// vacuously passing.
func TestStreamingGzip_BinaryNoCorruption_GuardsAgainstRegression(t *testing.T) {
	// Every content type asserted in the corruption suite must be in the skip list.
	mustSkip := []string{
		"application/octet-stream",
		"video/mp4",
		"image/jpeg",
		"audio/wav",
		"application/zip",
		"application/gzip",
		"application/x-tar",
	}
	for _, ct := range mustSkip {
		if !shouldSkipCompression(ct) {
			t.Errorf("shouldSkipCompression(%q) = false; the corruption suite relies on it being true", ct)
		}
	}
}
