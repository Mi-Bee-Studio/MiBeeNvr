//go:build linux

package prealloc

import (
	"os"

	"golang.org/x/sys/unix"
)

// preallocFn is the seam for failure-injection tests.
var preallocFn = func(f *os.File, size int64) error {
	if size <= 0 {
		return nil
	}
	return unix.Fallocate(int(f.Fd()), 0, 0, size)
}
