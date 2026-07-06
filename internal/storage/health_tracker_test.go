package storage

import (
	"testing"
)

// TestStorageFailed_ReflectsHealthState verifies per-camera StorageFailed()
// correctly mirrors the internal health state across transitions.
func TestStorageFailed_ReflectsHealthState(t *testing.T) {
	t.Helper()
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Fresh manager is healthy.
	if m.StorageFailed("cam-1") {
		t.Fatal("fresh manager should not report storage failed")
	}

	// A single write failure escalates only to Degraded, not Failed.
	m.recordWriteFailure("cam-1")
	if m.StorageFailed("cam-1") {
		t.Fatal("degraded state must not be reported as failed (only >= HealthFailed)")
	}

	// Reaching maxConsecutiveFailures escalates to HealthFailed.
	for i := 1; i < maxConsecutiveFailures; i++ { // already had 1 failure above
		m.recordWriteFailure("cam-1")
	}
	if !m.StorageFailed("cam-1") {
		t.Fatal("expected StorageFailed()=true after maxConsecutiveFailures write failures")
	}

	// A successful write restores health.
	m.recordWriteSuccess("cam-1")
	if m.StorageFailed("cam-1") {
		t.Fatal("expected StorageFailed()=false after recordWriteSuccess")
	}
}

// TestPerCameraIsolation verifies that one camera's write failures don't
// affect another camera's health state.
func TestPerCameraIsolation(t *testing.T) {
	t.Helper()
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Both cameras start healthy.
	if m.StorageFailed("cam-a") {
		t.Fatal("cam-a should start healthy")
	}
	if m.StorageFailed("cam-b") {
		t.Fatal("cam-b should start healthy")
	}

	// Fail cam-a 3 times.
	for i := 0; i < maxConsecutiveFailures; i++ {
		m.recordWriteFailure("cam-a")
	}

	// cam-a should be Failed, cam-b should remain Healthy.
	if !m.StorageFailed("cam-a") {
		t.Fatal("cam-a should be Failed after 3 write failures")
	}
	if m.StorageFailed("cam-b") {
		t.Fatal("cam-b should remain Healthy despite cam-a's failures")
	}

	// Recover cam-a.
	m.recordWriteSuccess("cam-a")
	if m.StorageFailed("cam-a") {
		t.Fatal("cam-a should be Healthy after recordWriteSuccess")
	}
}

// TestGlobalMountFailure verifies that mount-point failure affects ALL
// cameras — sets them to HealthFailed.
func TestGlobalMountFailure(t *testing.T) {
	t.Helper()
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Simulate write failures on different cameras.
	m.recordWriteFailure("cam-a")
	m.recordWriteFailure("cam-b")

	if m.StorageFailed("cam-a") {
		t.Fatal("cam-a should be Degraded (only 1 failure)")
	}
	if m.StorageFailed("cam-b") {
		t.Fatal("cam-b should be Degraded (only 1 failure)")
	}

	// Set root dir to nonexistent to trigger mount failure.
	origRoot := m.rootDir
	m.rootDir = "/tmp/nonexistent_mibee_nvr_dir_xyz"
	defer func() { m.rootDir = origRoot }()

	m.performHealthCheck()

	// Both cameras should now be Failed due to mount issue.
	if !m.StorageFailed("cam-a") {
		t.Fatal("cam-a should be Failed after mount failure")
	}
	if !m.StorageFailed("cam-b") {
		t.Fatal("cam-b should be Failed after mount failure")
	}
}

// TestUnknownCameraDefaultsHealthy verifies that an unknown cameraID defaults
// to healthy, never blocking new cameras.
func TestUnknownCameraDefaultsHealthy(t *testing.T) {
	t.Helper()
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Unknown camera should be reported as healthy.
	if m.StorageFailed("unknown-camera") {
		t.Fatal("unknown camera should default to healthy, not failed")
	}

	if m.StorageHealth("unknown-camera") != HealthHealthy {
		t.Fatal("unknown camera should default to HealthHealthy")
	}

	// Even after a known camera fails, unknown camera stays healthy.
	m.recordWriteFailure("known-cam")
	m.recordWriteFailure("known-cam")
	m.recordWriteFailure("known-cam")

	if !m.StorageFailed("known-cam") {
		t.Fatal("known-cam should be Failed after 3 failures")
	}
	if m.StorageFailed("unknown-camera") {
		t.Fatal("unknown camera should still be healthy despite known-cam failures")
	}
}

// TestStorageFailedLegacy verifies that StorageFailedLegacy returns true when
// ANY camera is in a failed state.
func TestStorageFailedLegacy(t *testing.T) {
	t.Helper()
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Initially no cameras, should be false.
	if m.StorageFailedLegacy() {
		t.Fatal("StorageFailedLegacy should be false initially")
	}

	// Fail one camera.
	for i := 0; i < maxConsecutiveFailures; i++ {
		m.recordWriteFailure("cam-1")
	}

	if !m.StorageFailedLegacy() {
		t.Fatal("StorageFailedLegacy should be true when any camera is Failed")
	}

	// Recover that camera.
	m.recordWriteSuccess("cam-1")

	if m.StorageFailedLegacy() {
		t.Fatal("StorageFailedLegacy should be false after camera recovers")
	}
}
