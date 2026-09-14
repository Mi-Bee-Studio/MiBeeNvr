package main

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRetryOnBusyRetriesTransientLocks(t *testing.T) {
	calls := 0
	err := retryOnBusy(func() error {
		calls++
		if calls < 3 {
			return errors.New("database is locked (5) (SQLITE_BUSY)")
		}
		return nil
	}, 5, time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, 3, calls)
}

func TestRetryOnBusyPassesThroughOtherErrors(t *testing.T) {
	calls := 0
	sentinel := errors.New("disk I/O error")
	err := retryOnBusy(func() error {
		calls++
		return sentinel
	}, 5, time.Millisecond)
	require.ErrorIs(t, err, sentinel)
	require.Equal(t, 1, calls, "non-BUSY errors must not be retried")
}

func TestRetryOnBusyExhaustsAttempts(t *testing.T) {
	calls := 0
	err := retryOnBusy(func() error {
		calls++
		return errors.New("database is locked (5) (SQLITE_BUSY)")
	}, 3, time.Millisecond)
	require.ErrorContains(t, err, "database is locked")
	require.Equal(t, 3, calls)
}
