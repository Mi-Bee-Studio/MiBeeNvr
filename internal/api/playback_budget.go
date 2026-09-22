package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"

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
func (h *Handler) serveFileBudgeted(w http.ResponseWriter, r *http.Request, path string) {
	l := h.playbackBudget.Load()
	if l == nil {
		http.ServeFile(w, r, path)
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
	http.ServeContent(w, r, filepath.Base(path), st.ModTime(), &budgetedReadSeeker{f: f, l: *l})
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
