//go:build linux

package fadvise

import "golang.org/x/sys/unix"

func platformAdvise(fd, off, length, advice int) error {
	return unix.Fadvise(fd, int64(off), int64(length), advice)
}
