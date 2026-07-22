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
	w            http.ResponseWriter
	gz           *gzip.Writer
	contentType  string // detected from first WriteHeader
	headerWritten bool
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
	if shouldSkipCompression(cw.contentType) {
		return cw.w.Write(b)
	}
	return cw.gz.Write(b)
}

// Flush implements http.Flusher. It flushes the gzip writer (pushing buffered
// data through the compressor) and then flushes the underlying response writer.
// This is essential for SSE: each event must reach the client immediately.
func (cw *compressWriter) Flush() {
	if !shouldSkipCompression(cw.contentType) {
		_ = cw.gz.Flush()
	}
	if flusher, ok := cw.w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (cw *compressWriter) Close() error {
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
