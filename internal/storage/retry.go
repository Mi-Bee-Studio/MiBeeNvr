package storage

import (
	"context"
	"strings"
	"time"
)

// MaxDBRetries is the maximum number of retry attempts for SQLITE_BUSY errors.
const MaxDBRetries = 3

// RetryInitialDelay is the base delay for exponential backoff on SQLITE_BUSY.
const RetryInitialDelay = 100 * time.Millisecond

// busyErrorHook is invoked on each SQLITE_BUSY retry when set via SetBusyErrorHook.
// Defaults to no-op so RetryOnBusy remains usable in tests without metrics wired.
var busyErrorHook = func() {}

// SetBusyErrorHook installs a callback fired on each SQLITE_BUSY retry (typically
// increments nvr_sqlite_busy_errors_total). Intended to be called once at startup.
func SetBusyErrorHook(fn func()) {
	if fn != nil {
		busyErrorHook = fn
	}
}

// IsBusyError returns true if the error indicates SQLITE_BUSY contention.
func IsBusyError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "busy") || strings.Contains(msg, "SQLITE_BUSY")
}

// RetryOnBusy retries fn on SQLITE_BUSY errors with exponential backoff.
func RetryOnBusy(ctx context.Context, fn func() error) error {
	var err error
	delay := RetryInitialDelay
	for attempt := 0; attempt <= MaxDBRetries; attempt++ {
		if err = fn(); err == nil || !IsBusyError(err) {
			return err
		}
		// Record the contention event (no-op if no hook is installed).
		busyErrorHook()
		if attempt < MaxDBRetries {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			delay *= 2
		}
	}
	return err
}
