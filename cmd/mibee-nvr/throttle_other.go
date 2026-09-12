//go:build !linux

package main

import "errors"

// selfThrottle is a no-op on platforms without nice/ioprio (dev builds).
func selfThrottle() error {
	return errors.New("self-throttle not supported on this platform")
}
