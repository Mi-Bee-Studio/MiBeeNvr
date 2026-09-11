package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteLastGoodBackupAtomicSnapshot verifies the boot-time recovery
// snapshot: after a successful boot-side validation, main.go snapshots the
// config next to itself as <path>.last-good. This is the recovery point for
// the shutdown-persist overwrite gotcha (a running NVR re-serializes the YAML
// on shutdown — a hand-edited change can be clobbered, and an older binary
// can zero out newly added sections).
func TestWriteLastGoodBackupAtomicSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mibee-nvr.yaml")
	original := []byte("server:\n  listen: :9090\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteLastGoodBackup(path); err != nil {
		t.Fatalf("WriteLastGoodBackup() errored: %v", err)
	}
	got, err := os.ReadFile(path + ".last-good")
	if err != nil {
		t.Fatalf("last-good snapshot missing: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("snapshot content mismatch: got %q, want %q", got, original)
	}
	// No temp file left behind by the atomic write.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 { // original + last-good
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("atomic write must not leave temp files behind: %v", names)
	}
}

// TestWriteLastGoodBackupOverwriteStale ensures a reboot refreshes the
// snapshot instead of failing on an existing one.
func TestWriteLastGoodBackupOverwriteStale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mibee-nvr.yaml")
	if err := os.WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mibee-nvr.yaml.last-good"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteLastGoodBackup(path); err != nil {
		t.Fatalf("WriteLastGoodBackup() must overwrite a stale snapshot: %v", err)
	}
	got, err := os.ReadFile(path + ".last-good")
	if err != nil || string(got) != "new" {
		t.Fatalf("snapshot must refresh, got %q err=%v", got, err)
	}
}
