package storage

import "testing"

// TestStorageFailed_ReflectsHealthState verifies the new StorageFailed() method
// (added to satisfy the recorder package's health-check interface) correctly
// mirrors the internal health state across transitions. This is the storage-side
// half of the isStorageFailed fix.
func TestStorageFailed_ReflectsHealthState(t *testing.T) {
	t.Helper()
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Fresh manager is healthy.
	if m.StorageFailed() {
		t.Fatal("fresh manager should not report storage failed")
	}

	// A single write failure escalates only to Degraded, not Failed.
	m.recordWriteFailure()
	if m.StorageFailed() {
		t.Fatal("degraded state must not be reported as failed (only >= HealthFailed)")
	}

	// Reaching maxConsecutiveFailures escalates to HealthFailed.
	for i := 1; i < maxConsecutiveFailures; i++ { // already had 1 failure above
		m.recordWriteFailure()
	}
	if !m.StorageFailed() {
		t.Fatal("expected StorageFailed()=true after maxConsecutiveFailures write failures")
	}

	// A successful write restores health.
	m.recordWriteSuccess()
	if m.StorageFailed() {
		t.Fatal("expected StorageFailed()=false after recordWriteSuccess")
	}
}
