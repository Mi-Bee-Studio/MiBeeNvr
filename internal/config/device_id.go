package config

import (
	"crypto/rand"
	"fmt"
	"os"
	"time"
)

// newDeviceID returns a random UUIDv4 string (RFC 4122). Built on crypto/rand
// directly — one call site does not justify a third-party UUID dependency.
func newDeviceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is effectively unrecoverable; derive from the
		// clock so the process still starts with a syntactically valid ID.
		seed := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(seed >> (uint(i%8) * 8))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// EnsureDeviceIdentity generates and persists the device ID on first startup.
// Subsequent calls keep the stored value (identity must survive restarts and
// IP changes). Only called from the startup path — deliberately NOT from
// Load(), which is also used read-only (e.g. tests loading the shipped
// config.example.yaml must not rewrite repo files). Skipped when the config
// file does not exist (Load callers always have one; a missing file here
// means the caller constructed cfg out of band). The rewrite is best-effort:
// on failure the in-memory ID is kept and the caller logs a warning rather
// than failing startup.
func EnsureDeviceIdentity(path string, cfg *Config) error {
	if cfg == nil || cfg.Server.DeviceID != "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		//nolint:nilerr // missing config file = caller built cfg out of band; deliberately a no-op
		return nil
	}
	cfg.Server.DeviceID = newDeviceID()
	return Save(path, cfg)
}
