package relay

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// dumpReadWriter wraps an io.ReadWriter, hexdumping every Write to a file
// (append mode, timestamped, with a running byte offset) while passing Reads
// through unchanged. Enabled only when the NVR_RTMP_DEBUG_DUMP environment
// variable names an output file path.
//
// This is a zero-behavior-change diagnostic: it forwards every byte to the
// underlying connection unchanged and returns the same (n, err). It exists to
// let us diff the exact bytes the Go relay sends against what FFmpeg sends, so
// we can pinpoint which message triggers an RST on strict receivers (Douyu Live
// Companion).
type dumpReadWriter struct {
	rw      io.ReadWriter
	f       *os.File
	mu      sync.Mutex
	written int64
}

// wrapDump returns rw unchanged when NVR_RTMP_DEBUG_DUMP is unset; otherwise it
// wraps rw so every written byte is hexdumped to the named file.
func wrapDump(rw io.ReadWriter) io.ReadWriter {
	path := os.Getenv("NVR_RTMP_DEBUG_DUMP")
	if path == "" {
		return rw
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		engineLogger.Warn("NVR_RTMP_DEBUG_DUMP: cannot open dump file, disabled", "path", path, "err", err)
		return rw
	}
	engineLogger.Info("NVR_RTMP_DEBUG_DUMP: enabled", "path", path)
	return &dumpReadWriter{rw: rw, f: f}
}

func (d *dumpReadWriter) Read(p []byte) (int, error) { return d.rw.Read(p) }

func (d *dumpReadWriter) Write(p []byte) (int, error) {
	n, err := d.rw.Write(p)
	d.mu.Lock()
	// Hexdump + ASCII, 16 bytes per line, with absolute offset and timestamp.
	ts := time.Now().Format("15:04:05.000")
	d.f.WriteString(fmt.Sprintf("\n=== WRITE ts=%s offset=%d len=%d err=%v ===\n", ts, d.written, len(p), err))
	d.f.WriteString(hex.Dump(p[:n]))
	d.written += int64(n)
	d.mu.Unlock()
	return n, err
}
