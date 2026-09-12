// Package prealloc pre-allocates disk space for segment files (#757).
//
// ext4 grows an appending file one extent at a time — each growth pays an
// inode-size metadata transaction. A whole-segment fallocate up front gives
// the allocator one contiguous region (better merge/playback read locality,
// fewer journal commits, less write amplification on SD/eMMC).
//
// Preallocation is a HINT, never a bound: writes past the allocated size
// simply grow the file as before. Filesystems without fallocate (ENOSYS /
// EOPNOTSUPP) degrade to the append-write status quo.
package prealloc

import "os"

// Preallocate extends f's size to size bytes (offset 0, length size) without
// writing data. Failures are the caller's to ignore-and-log — this is an
// optimization, not a correctness step.
func Preallocate(f *os.File, size int64) error {
	return preallocFn(f, size)
}

// SwapForTest replaces the platform implementation for failure-injection
// tests; the returned func restores it.
func SwapForTest(fn func(*os.File, int64) error) (restore func()) {
	prev := preallocFn
	preallocFn = fn
	return func() { preallocFn = prev }
}
