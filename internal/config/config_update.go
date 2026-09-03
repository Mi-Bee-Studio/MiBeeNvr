package config

// UpdateConfig controls the in-app "new version available" check.
//
// This is the SENSING layer only: the app polls GitHub Releases, compares the
// latest tag to the running version (injected via -ldflags main.appVersion),
// and surfaces the result in the Web UI. It NEVER executes an upgrade — in a
// Docker deployment the container is immutable, so real upgrades are done by an
// external tool (Watchtower) or the NAS app store / Container Manager. The
// default-detect-not-execute policy also matches NAS data-safety expectations
// (user must authorize any change to a box holding recordings).
//
// All fields are optional; zero-value ApplyDefaults fills sensible values.
type UpdateConfig struct {
	// Enabled gates the background check. Default true.
	Enabled *bool `yaml:"enabled"`
	// Channel selects the release stream. "stable" (default) queries
	// /releases/latest which excludes prereleases. "beta" lists releases and
	// includes prereleases. Only "stable" is implemented for now.
	Channel string `yaml:"channel"`
	// CheckInterval is how often the background poller hits GitHub. Parsed with
	// time.ParseDuration. Default "1h".
	CheckInterval string `yaml:"check_interval"`
	// Repo is the "owner/name" GitHub repository to check.
	// Default "Mi-Bee-Studio/MiBeeNvr".
	Repo string `yaml:"repo"`
	// AutoApply opt-in EXECUTION of bare-metal upgrades (#647). Default false:
	// the sensing layer only announces updates. When true AND the deployment
	// is bare-metal systemd (never docker/dev/beta), a newly detected stable
	// release is installed via the mibee-nvr-update.service root helper
	// (polkit-authorized), with sha256+ed25519 verification and automatic
	// rollback to the previous binary if the upgraded service fails its
	// health gate.
	AutoApply *bool `yaml:"auto_apply"`
	// DownloadMirror is a base URL that replaces "https://github.com" for
	// release-artifact downloads (#649) — bare-metal auto-upgrade reliability
	// on networks where GitHub is slow/unreachable. The
	// {repo}/releases/download/... path is preserved underneath it, so a
	// ghproxy-style prefix or a self-hosted path-preserving mirror both fit.
	// The version CHECK still goes to the GitHub API. Empty (default) =
	// GitHub official. All artifacts (binary + checksums + signature) come
	// from the same origin; mirror failures never fall back to GitHub.
	DownloadMirror string `yaml:"download_mirror"`
}

// IsEnabled returns whether the update check is enabled (default true).
func (c UpdateConfig) IsEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// IsAutoApply returns whether bare-metal auto-apply is enabled (default
// false — the sensing layer never executes upgrades unless explicitly opted in).
func (c UpdateConfig) IsAutoApply() bool {
	return c.AutoApply != nil && *c.AutoApply
}
