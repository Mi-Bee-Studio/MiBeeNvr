package middleware

import (
	"net/http"
	"strings"

	"github.com/klauspost/compress/gzip"
)

// compressWriter wraps http.ResponseWriter with gzip compression while
// preserving Flush() support (critical for SSE streaming). The gzip writer
// is flushed on every explicit Write or Flush call so that SSE events reach
// the client immediately instead of being buffered in the compressor.
type compressWriter struct {
	w             http.ResponseWriter
	gz            *gzip.Writer
	contentType   string // detected from first WriteHeader
	headerWritten bool
	// wroteGzip tracks whether any bytes were written through the gzip writer.
	// It's set to true on the first gz.Write call so that Close() knows whether
	// to finalize the gzip stream. This is critical for correctness: when the
	// response is a skip-compression content type (e.g. application/octet-stream
	// for /models/*.onnx), Write() bypasses gz and writes directly to the
	// underlying ResponseWriter. If Close() still called gz.Close(), the gzip
	// writer would emit an empty-but-valid gzip trailer (a ~20-byte gzip
	// frame: 1f 8b 08 ...) APPENDED to the raw response body — silently
	// corrupting binary payloads (issue #109: ONNX models received by the
	// browser were raw bytes + a spurious gzip trailer, so ORT's protobuf
	// parser failed with INVALID_PROTOBUF). Only finalize gz when we actually
	// used it.
	wroteGzip bool
}

func newCompressWriter(w http.ResponseWriter, level int) *compressWriter {
	gz, _ := gzip.NewWriterLevel(w, level)
	return &compressWriter{
		w:  w,
		gz: gz,
	}
}

func (cw *compressWriter) Header() http.Header {
	return cw.w.Header()
}

func (cw *compressWriter) WriteHeader(code int) {
	if cw.headerWritten {
		return
	}
	cw.headerWritten = true
	// Detect content type for SSE exclusion logic.
	ct := cw.w.Header().Get("Content-Type")
	cw.contentType = ct
	// Don't double-compress already-compressed content types (video, images,
	// archives). gzip on MP4/JPEG wastes CPU with negligible size reduction.
	if shouldSkipCompression(ct) {
		// Remove the Content-Encoding header we set in the middleware and write uncompressed.
		cw.w.Header().Del("Content-Encoding")
		cw.w.Header().Del("Vary")
		cw.w.WriteHeader(code)
		return
	}
	cw.w.WriteHeader(code)
}

func (cw *compressWriter) Write(b []byte) (int, error) {
	// Mirror net/http's implicit WriteHeader(200) on first Write: if the
	// handler never called WriteHeader explicitly, run our content-type
	// detection now so shouldSkipCompression sees the real Content-Type.
	// Without this, a handler that does w.Header().Set("Content-Type",
	// "application/octet-stream"); w.Write(bytes) — skipping WriteHeader —
	// would have cw.contentType=="" here, shouldSkipCompression("")==false,
	// and the binary payload would get gzip-compressed (and mislabeled with
	// Content-Encoding: gzip from the middleware's pre-set header). Real
	// handlers via http.ServeFile/http.ServeContent always call WriteHeader
	// first, but we shouldn't depend on that.
	if !cw.headerWritten {
		cw.WriteHeader(http.StatusOK)
	}
	if shouldSkipCompression(cw.contentType) {
		return cw.w.Write(b)
	}
	cw.wroteGzip = true
	return cw.gz.Write(b)
}

// Flush implements http.Flusher. It flushes the gzip writer (pushing buffered
// data through the compressor) and then flushes the underlying response writer.
// This is essential for SSE: each event must reach the client immediately.
func (cw *compressWriter) Flush() {
	if !shouldSkipCompression(cw.contentType) && cw.wroteGzip {
		_ = cw.gz.Flush()
	}
	if flusher, ok := cw.w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (cw *compressWriter) Close() error {
	// Only finalize the gzip stream if we actually compressed bytes into it.
	// Calling gz.Close() on an untouched gzip.Writer still emits an empty gzip
	// frame (~20 bytes: magic 1f 8b 08 + trailer) to the underlying writer,
	// which corrupts skip-compression responses (binary files served raw).
	// See issue #109 (ONNX INVALID_PROTOBUF).
	if !cw.wroteGzip {
		return nil
	}
	return cw.gz.Close()
}

// shouldSkipCompression returns true for content types that are already
// compressed (video, images, archives) where gzip wastes CPU.
func shouldSkipCompression(contentType string) bool {
	if contentType == "" {
		return false // unknown — compress by default
	}
	ct := strings.ToLower(contentType)
	// Extract the media type (before parameters like "; charset=utf-8").
	if idx := strings.IndexByte(ct, ';'); idx != -1 {
		ct = strings.TrimSpace(ct[:idx])
	}
	switch {
	case strings.HasPrefix(ct, "video/"),
		strings.HasPrefix(ct, "image/"),
		strings.HasPrefix(ct, "audio/"),
		ct == "application/zip",
		ct == "application/gzip",
		ct == "application/x-gzip",
		ct == "application/x-tar",
		ct == "application/octet-stream":
		return true
	}
	return false
}

// StreamingGzip is middleware that compresses responses with gzip. Unlike chi's
// built-in middleware.Compress, this implementation:
//   - Flushes the gzip writer on every Flush() call, making it safe for SSE
//     (text/event-stream) without buffering delays.
//   - Skips already-compressed content types (video, images, archives).
//
// The level parameter controls compression: 1 (BestSpeed) to 9 (BestCompression).
// Level 5 is a good default (close to BestSpeed with better ratio).
func StreamingGzip(level int) func(http.Handler) http.Handler {
	// Validate level.
	if level < gzip.DefaultCompression || level > gzip.BestCompression {
		level = gzip.DefaultCompression
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Only compress if the client supports gzip.
			if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
				next.ServeHTTP(w, r)
				return
			}
			// Skip WebSocket upgrade requests.
			if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
				next.ServeHTTP(w, r)
				return
			}

			cw := newCompressWriter(w, level)
			defer cw.Close()

			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Vary", "Accept-Encoding")
			// Remove Content-Length — it's now wrong (compressed size differs).
			w.Header().Del("Content-Length")

			next.ServeHTTP(cw, r)
		})
	}
}

// Ensure gzip.Writer is used correctly.
var _ http.Handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
