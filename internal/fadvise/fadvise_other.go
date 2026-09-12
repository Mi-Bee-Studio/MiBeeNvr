//go:build !linux

package fadvise

// posix_fadvise has no portable equivalent off Linux — hints silently no-op
// (behavior unchanged, just without the cache-management benefit).
func platformAdvise(fd, off, length, advice int) error {
	return nil
}
