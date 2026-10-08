package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func newCredProbeTestDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return db
}

// TestCredentialProbeClaimOnce: the second claim for the same camera must be
// refused — this is the once-only guarantee, including across crashes (the
// claim row survives; the run does not need to).
func TestCredentialProbeClaimOnce(t *testing.T) {
	t.Helper()
	db := newCredProbeTestDB(t)
	ctx := context.Background()

	first, err := db.ClaimCredentialProbe(ctx, "cam-1")
	if err != nil || !first {
		t.Fatalf("first claim = %v, %v; want true, nil", first, err)
	}
	second, err := db.ClaimCredentialProbe(ctx, "cam-1")
	if err != nil || second {
		t.Fatalf("second claim = %v, %v; want false, nil", second, err)
	}
	// A different camera claims fine.
	other, err := db.ClaimCredentialProbe(ctx, "cam-2")
	if err != nil || !other {
		t.Fatalf("other camera claim = %v, %v; want true, nil", other, err)
	}
}

func TestCredentialProbeResultSettle(t *testing.T) {
	t.Helper()
	db := newCredProbeTestDB(t)
	ctx := context.Background()

	if _, err := db.ClaimCredentialProbe(ctx, "cam-1"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := db.SetCredentialProbeResult(ctx, "cam-1", CredProbeStatusRotated, "admin", true); err != nil {
		t.Fatalf("set result: %v", err)
	}
	exists, status, user, rotated, err := db.GetCredentialProbeStatus(ctx, "cam-1")
	if err != nil || !exists {
		t.Fatalf("get = %v, %v; want exists", exists, err)
	}
	if status != CredProbeStatusRotated || user != "admin" || !rotated {
		t.Errorf("got (%q,%q,%v), want (rotated,admin,true)", status, user, rotated)
	}

	// A settled row still blocks re-claiming (once-only survives the outcome).
	again, err := db.ClaimCredentialProbe(ctx, "cam-1")
	if err != nil || again {
		t.Errorf("re-claim after settle = %v, %v; want false, nil", again, err)
	}

	// Unknown camera: exists=false, no error.
	exists, _, _, _, err = db.GetCredentialProbeStatus(ctx, "cam-none")
	if err != nil || exists {
		t.Errorf("unknown camera = %v, %v; want false, nil", exists, err)
	}
}
