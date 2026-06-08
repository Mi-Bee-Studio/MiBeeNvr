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
