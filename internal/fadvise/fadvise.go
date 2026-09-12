// Package fadvise issues posix_fadvise page-cache hints for one-pass
// sequential media reads (#754).
//
// Merge sources and timelapse frame-extraction sources are read exactly once
// front-to-back; without hints their pages linger in the page cache and evict
// hot data (SQLite mmap pages, active segment buffers) — on a 1GB-RAM board a
// single large merge window can push the system into reclaim stalls. The
// kernel is free to ignore these hints, and so are we: failures are debug-log
// no-ops, never errors.
package fadvise

import (
	"log/slog"
	"os"
)

// Advice values mirror posix_fadvise(2); declared here so tests can assert
// them portably (the constants live in the linux build-tagged file).
const (
	AdviceSequential = 2 // POSIX_FADV_SEQUENTIAL
	AdviceDontNeed   = 4 // POSIX_FADV_DONTNEED
)

// adviseFD is the seam (call-pattern tests swap it).
var adviseFD = platformAdvise

// Sequential hints that fd will be read front-to-back — the kernel may
// enlarge its readahead window. Call right after opening, before first read.
func Sequential(f *os.File) {
	advise(f, AdviceSequential, "sequential")
}

// DontNeed asks the kernel to drop the file's cached pages. Call only AFTER
// the last byte has been consumed (an earlier call turns later reads into
// second disk passes) — e.g. right before closing a merge/extraction source
// that nothing will read again.
func DontNeed(f *os.File) {
	advise(f, AdviceDontNeed, "dontneed")
}

func advise(f *os.File, advice int, label string) {
	if f == nil {
		return
	}
	// Whole-file scope: offset 0, length 0.
	if err := adviseFD(int(f.Fd()), 0, 0, advice); err != nil {
		slog.Debug("fadvise: hint ignored", "op", label, "error", err)
	}
}

// SwapForTest replaces the platform advise call for the duration of a test
// and returns a restore func. Test-only hook (storage.SetBusyErrorHook
// pattern).
func SwapForTest(fn func(fd, advice int) error) (restore func()) {
	old := adviseFD
	adviseFD = func(fd, off, length, advice int) error {
		_ = off
		_ = length
		return fn(fd, advice)
	}
	return func() { adviseFD = old }
}
