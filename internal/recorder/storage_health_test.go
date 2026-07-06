package recorder

import "testing"

// healthAwareStore implements SegmentStore plus the optional StorageFailed(cameraID string)
// bool method, mirroring how *storage.Manager satisfies the duck-typed
// healthHint interface. The original bug was that storage.Manager exposed
// StorageHealth() HealthState while the interface demanded StorageHealth() int
// (Go requires exact return-type matching), so the assertion always failed.
type healthAwareStore struct{ failed bool }

func (s *healthAwareStore) CreateSegment(string, string) (string, string, error) {
	return "", "", nil
}
func (s *healthAwareStore) WriteFrame(string, []byte) (int, error) { return 0, nil }
func (s *healthAwareStore) CloseSegment(string, string) error      { return nil }
func (s *healthAwareStore) StorageFailed(cameraID string) bool     { return s.failed }

// plainStore implements only SegmentStore (no health method) — exercises the
// backward-compatible fallback where isStorageFailed returns false.
type plainStore struct{}

func (s *plainStore) CreateSegment(string, string) (string, string, error) {
	return "", "", nil
}
func (s *plainStore) WriteFrame(string, []byte) (int, error) { return 0, nil }
func (s *plainStore) CloseSegment(string, string) error      { return nil }

// TestIsStorageFailed_HealthAwareStore guards the core fix: a store that
// reports StorageFailed()==true must be detected by isStorageFailed. Before the
// fix this always returned false because of the interface type mismatch.
func TestIsStorageFailed_HealthAwareStore(t *testing.T) {
	t.Helper()
	if isStorageFailed(&healthAwareStore{failed: true}, "cam-1") != true {
		t.Fatal("expected isStorageFailed=true when store reports StorageFailed()=true")
	}
	if isStorageFailed(&healthAwareStore{failed: false}, "cam-1") != false {
		t.Fatal("expected isStorageFailed=false when store reports StorageFailed()=false")
	}
}

// TestIsStorageFailed_PlainStore ensures backward compatibility: stores that do
// not implement StorageFailed() must fall back to false (never panic, never
// block recording for stores without health insight).
func TestIsStorageFailed_PlainStore(t *testing.T) {
	t.Helper()
	if isStorageFailed(&plainStore{}, "cam-1") != false {
		t.Fatal("expected isStorageFailed=false for store without StorageFailed() method")
	}
}
