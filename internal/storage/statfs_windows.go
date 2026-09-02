//go:build windows

package storage

import "golang.org/x/sys/windows"

// statfsFree reports total and free bytes of the filesystem containing path.
// Windows variant via GetDiskFreeSpaceEx — keeps the package compilable (and
// unit-testable) on dev Windows boxes; the NVR itself targets Linux.
func statfsFree(path string) (total, free int64, err error) {
	var freeCaller, totalC, freeC uint64
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	if err := windows.GetDiskFreeSpaceEx(p, &freeCaller, &totalC, &freeC); err != nil {
		return 0, 0, err
	}
	return int64(totalC), int64(freeC), nil
}
