package main

// retryOnBusy re-runs fn while it fails with SQLITE_BUSY — a repair CLI
// shares the recordings DB with the live server (WAL allows one writer),
// and under disk saturation the server's big merge-batch transactions can
// outlast even a 15s busy_timeout. Bounded attempts with linear backoff;
// any other error passes through untouched.

import (
	"fmt"
	"strings"
	"time"
)

func retryOnBusy(fn func() error, attempts int, wait time.Duration) error {
	var err error
	for i := range max(1, attempts) {
		if i > 0 {
			time.Sleep(wait * time.Duration(i))
		}
		if err = fn(); err == nil {
			return nil
		}
		if !strings.Contains(err.Error(), "database is locked") {
			return err
		}
	}
	return fmt.Errorf("still busy after %d attempts: %w", attempts, err)
}
