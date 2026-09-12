//go:build !linux

package prealloc

import (
	"errors"
	"os"
)

// preallocFn is a no-op on non-Linux platforms (seam for tests).
var preallocFn = func(_ *os.File, _ int64) error {
	return errors.New("prealloc: unsupported platform")
}
