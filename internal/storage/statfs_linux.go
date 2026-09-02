//go:build linux || darwin || freebsd || netbsd || openbsd

package storage

import "syscall"

// statfsFree reports total and free bytes of the filesystem containing path.
// Unix variant (Statfs; darwin/bsd share the API with a different stat
// layout, both expose Blocks/Bsize/Bfree as uint64-compatible fields).
func statfsFree(path string) (total, free int64, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, err
	}
	total = int64(stat.Blocks * uint64(stat.Bsize))
	free = int64(stat.Bfree * uint64(stat.Bsize))
	return total, free, nil
}
