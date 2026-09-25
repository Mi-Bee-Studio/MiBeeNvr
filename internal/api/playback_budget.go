package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/iobudget"
)

// SetPlaybackBudget installs the playback I/O budget (#886, gray-release).
// Passing nil disables budgeted serving (plain http.ServeFile).
func (h *Handler) SetPlaybackBudget(l iobudget.Limiter) {
	if l == nil {
		h.playbackBudget.Store(nil)
		return
	}
	h.playbackBudget.Store(&l)
}

// serveFileBudgeted serves a media file. Without a budget installed it is
// http.ServeFile; with one, reads are charged to the "playback" tenant in
// ServeContent-sized chunks (#886). Range/If-Modified-Since semantics are
// http.ServeContent's.
//
// Cache semantics: every media response carries an ETag (mtime+size) and
// Cache-Control: no-cache — the browser may cache but MUST revalidate. Repair
// tooling rewrites recording files IN PLACE (e.g. repair append-tkhd), and
// Go's ServeContent only negotiates If-Modified-Since; without a strong ETag
// plus forced revalidation, browsers kept serving the pre-repair bytes from
// their media cache and playback looked "still broken" after a fix (observed
// 2026-09-25). If-None-Match is answered here because net/http does not
// handle ETag negotiation itself.
func (h *Handler) serveFileBudgeted(w http.ResponseWriter, r *http.Request, path string) {
	if l := h.playbackBudget.Load(); l == nil {
		st, err := os.Stat(path)
		if err != nil {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		if h.setMediaCacheHeaders(w, r, st) {
			http.ServeFile(w, r, path)
		}
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	if h.setMediaCacheHeaders(w, r, st) {
		http.ServeContent(w, r, filepath.Base(path), st.ModTime(), &budgetedReadSeeker{f: f, l: *h.playbackBudget.Load()})
	}
}

// setMediaCacheHeaders stamps the media cache headers and answers
// If-None-Match. Returns false when a 304 was written (caller must not serve
// a body).
func (h *Handler) setMediaCacheHeaders(w http.ResponseWriter, r *http.Request, st os.FileInfo) bool {
	etag := mediaETag(st)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if match := r.Header.Get("If-None-Match"); match != "" {
		for _, cand := range splitETagList(match) {
			if cand == etag || cand == "*" {
				w.WriteHeader(http.StatusNotModified)
				return false
			}
		}
	}
	return true
}

// mediaETag builds a strong validator from mtime+size — both change whenever
// a repair rewrites the file, so caches re-fetch exactly then and never
// before.
func mediaETag(st os.FileInfo) string {
	return fmt.Sprintf(`"%x-%x"`, st.ModTime().UnixNano(), st.Size())
}

// splitETagList splits a comma-separated If-None-Match header value into
// trimmed candidate tags.
func splitETagList(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// budgetedReadSeeker charges every successful Read to the playback budget
// before returning. Charging is blocking — a starved bucket paces the
// download, which is the point of the opt-in.
type budgetedReadSeeker struct {
	f *os.File
	l iobudget.Limiter
}

func (b *budgetedReadSeeker) Read(p []byte) (int, error) {
	n, err := b.f.Read(p)
	if n > 0 {
		// ctx is never canceled; pacing must not abort a transfer.
		_ = b.l.Wait(context.Background(), iobudget.ConsumerPlayback, int64(n))
	}
	return n, err
}

func (b *budgetedReadSeeker) Seek(offset int64, whence int) (int64, error) {
	return b.f.Seek(offset, whence)
}
