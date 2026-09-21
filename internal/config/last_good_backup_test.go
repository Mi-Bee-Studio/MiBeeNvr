package config

import (
	"os"
	"path/filepath"
	"strings"
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

const (
	restoreGoodYAML = "cleanup:\n  retention_days: 30\n  disk_threshold_percent: 85\n"
	// parses; fails Validate (#867)
	restoreBadYAML = "cleanup:\n  retention_days: 30\n  disk_threshold_percent: 20\n"
)

// TestRestoreLastGood covers the consumer half of the recovery point
// (#867): a config that fails boot validation is replaced by the
// ".last-good" snapshot, the rejected file is preserved as ".bad", and —
// critically — recovery refuses to run when the snapshot is missing or
// itself invalid, so the original error still surfaces.
func TestRestoreLastGood(t *testing.T) {
	t.Run("restores snapshot and preserves rejected file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "mibee-nvr.yaml")
		if err := os.WriteFile(path, []byte(restoreGoodYAML), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := WriteLastGoodBackup(path); err != nil {
			t.Fatal(err)
		}
		// The bad runtime write (settings PUT accepted it pre-#867).
		if err := os.WriteFile(path, []byte(restoreBadYAML), 0o600); err != nil {
			t.Fatal(err)
		}

		cfg, err := RestoreLastGood(path)
		if err != nil {
			t.Fatalf("RestoreLastGood() errored: %v", err)
		}
		if cfg.Cleanup.DiskThresholdPercent != 85 {
			t.Fatalf("restored threshold = %d, want 85", cfg.Cleanup.DiskThresholdPercent)
		}
		// Save() re-serializes the full defaulted config, so assert on
		// semantics (the restored value + a clean reload), not bytes.
		onDisk, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(onDisk), "disk_threshold_percent: 85") {
			t.Fatalf("config file not restored to the snapshot value: %s", onDisk)
		}
		if _, err := Load(path); err != nil {
			t.Fatalf("restored config does not reload: %v", err)
		}
		bad, err := os.ReadFile(path + ".bad")
		if err != nil || string(bad) != restoreBadYAML {
			t.Fatalf("rejected file not preserved as .bad: %q err=%v", bad, err)
		}
	})

	t.Run("no snapshot means no recovery", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "mibee-nvr.yaml")
		if err := os.WriteFile(path, []byte(restoreBadYAML), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := RestoreLastGood(path); err == nil {
			t.Fatal("expected an error when no snapshot exists")
		}
	})

	t.Run("invalid snapshot refuses to recover", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "mibee-nvr.yaml")
		if err := os.WriteFile(path, []byte(restoreGoodYAML), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := WriteLastGoodBackup(path); err != nil {
			t.Fatal(err)
		}
		// Both the live file AND the snapshot carry the bad value.
		if err := os.WriteFile(path, []byte(restoreBadYAML), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+".last-good", []byte(restoreBadYAML), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := RestoreLastGood(path); err == nil {
			t.Fatal("recovery must refuse an invalid snapshot")
		}
	})
}
