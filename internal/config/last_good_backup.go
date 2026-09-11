package config

import (
	"fmt"
	"os"
)

// WriteLastGoodBackup atomically snapshots a config file next to itself as
// "<path>.last-good". main.go calls this after a successful boot-side
// validation so there is always a recovery point for the two config-loss
// gotchas this project has hit repeatedly:
//
//  1. shutdown-persist overwrite — the running NVR re-serializes the YAML on
//     shutdown, clobbering hand edits made while it was up;
//  2. older-binary zeroing — deploying a branch binary that lacks a newer
//     config section's schema rewrites that section to zero values at its
//     shutdown (2026-09-08: trigger.webhook got enabled:false this way).
//
// Best-effort by design: failures return an error for the caller to WARN,
// never block the boot that already validated successfully.
func WriteLastGoodBackup(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config for last-good snapshot: %w", err)
	}
	tmp := path + ".last-good.tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write last-good snapshot: %w", err)
	}
	if err := os.Rename(tmp, path+".last-good"); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename last-good snapshot: %w", err)
	}
	return nil
}
