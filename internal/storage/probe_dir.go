package storage

import (
	"fmt"
	"os"
)

// ProbeDir verifies that dir can host RECORDING FILES: creatable, writable,
// readable back, and deletable — the file-level lifecycle a camera root needs.
// Lighter than ProbeRoot (no SQLite): per-camera storage and migration
// targets hold only immutable segments, never the database.
func ProbeDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create %q: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".nvr-dirprobe-*")
	if err != nil {
		return fmt.Errorf("cannot create files in %q: %w", dir, err)
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err := f.Write([]byte("probe")); err != nil {
		f.Close()
		return fmt.Errorf("cannot write in %q: %w", dir, err)
	}
	f.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read back files in %q: %w", dir, err)
	}
	if string(data) != "probe" {
		return fmt.Errorf("read-back verification failed in %q", dir)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("cannot delete files in %q: %w", dir, err)
	}
	return nil
}
