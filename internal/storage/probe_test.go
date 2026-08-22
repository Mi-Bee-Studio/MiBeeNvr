package storage

import (
	"os"
	"path/filepath"
	"testing"
)

// ProbeRoot must accept an ordinary writable directory and leave no artifacts
// behind, and reject a path that cannot host a SQLite database.
func TestProbeRoot(t *testing.T) {
	t.Run("writable dir passes and leaves no artifacts", func(t *testing.T) {
		dir := t.TempDir()
		if err := ProbeRoot(dir); err != nil {
			t.Fatalf("ProbeRoot(%q) = %v, want nil", dir, err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("probe left artifacts: %v", entries)
		}
	})

	t.Run("path through a file fails", func(t *testing.T) {
		blocked := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		// A database path UNDER a regular file cannot be opened by SQLite.
		if err := ProbeRoot(filepath.Join(blocked, "sub")); err == nil {
			t.Fatal("ProbeRoot under a file should fail")
		}
	})
}
