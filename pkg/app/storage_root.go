package app

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// ensureStorageRoot makes sure cfg.Storage.RootDir exists, creating it when
// missing.
//
// In containerized deployments a root_dir that points at a HOST path —
// classically a config left behind by an fnOS uninstall-keep-data cycle
// (#434) — is not creatable inside the container (no such mount, non-root
// UID), and a fatal error here throws the container into a restart loop with
// no way in. Instead we fall back to the container data volume so the app
// still starts, the misconfiguration stays visible (error log every boot +
// Settings UI showing the effective path), and the user can fix it from the
// web UI.
//
// The fallback is in-memory only: the config file is never rewritten behind
// the user's back, because a custom volume that is temporarily unmounted
// (-v /recs:/recs) must resume working once the mount returns. Outside
// containers (fallback == "") the error stays fatal — on bare metal an
// un-creatable root_dir is a real configuration error.
func ensureStorageRoot(cfg *config.Config, fallback string) error {
	err := os.MkdirAll(cfg.Storage.RootDir, 0o755)
	if err == nil {
		return nil
	}
	if fallback == "" || cfg.Storage.RootDir == fallback {
		return fmt.Errorf("create storage dir %s: %w", cfg.Storage.RootDir, err)
	}
	slog.Error(
		"storage.root_dir is not usable in this environment — falling back to the container data directory",
		"configured_root_dir", cfg.Storage.RootDir,
		"fallback", fallback,
		"hint", "host paths (e.g. /vol1/...) do not exist inside the container; fix storage.root_dir in the web UI Settings",
		"error", err,
	)
	cfg.Storage.RootDir = fallback
	if err := os.MkdirAll(fallback, 0o755); err != nil {
		return fmt.Errorf("create storage dir %s: %w", fallback, err)
	}
	return nil
}
