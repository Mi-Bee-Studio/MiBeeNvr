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
}

// IsEnabled returns whether the update check is enabled (default true).
func (c UpdateConfig) IsEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}
