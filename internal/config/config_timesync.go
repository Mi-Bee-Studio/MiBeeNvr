package config

import (
	"strings"
)

// Camera time synchronization configuration (#time-sync).
//
// Two cooperating mechanisms:
//   - SNTP server (path B, the long-term heal): the NVR answers NTP requests
//     on the LAN; cameras are pointed at it once (SetNTP) and then keep
//     themselves in sync — including after power loss with a dead RTC battery.
//   - Auto correct (path A, the active fix): periodically measure each ONVIF
//     camera's clock via the pre-auth GetSystemDateAndTime and, when the skew
//     exceeds the threshold, write the NVR's time to the camera
//     (SetSystemDateAndTime). Requires admin credentials on the camera.

// TimeSyncConfig is the top-level `time_sync:` yaml section.
type TimeSyncConfig struct {
	// SNTP server settings (path B).
	SNTP SNTPServerConfig `yaml:"sntp" json:"sntp"`
	// Auto-correction loop settings (path A).
	Auto AutoTimeSyncConfig `yaml:"auto" json:"auto"`
}

// SNTPServerConfig configures the built-in SNTP server.
type SNTPServerConfig struct {
	// Enabled defaults to TRUE: the UDP listener is harmless on a LAN and the
	// "point cameras at the NVR" flow depends on it. nil = enabled.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Listen is the UDP bind address. Default ":123" (standard NTP port; needs
	// privileges on some hosts — systemd root or docker host-network/ mapped
	// port both work; docker bridge requires the port to be published).
	Listen string `yaml:"listen,omitempty" json:"listen,omitempty"`
}

// EnabledOrDefault resolves the three-state enabled flag.
func (s SNTPServerConfig) EnabledOrDefault() bool {
	return s.Enabled == nil || *s.Enabled
}

// AutoTimeSyncConfig configures the automatic camera clock correction loop.
type AutoTimeSyncConfig struct {
	// Enabled defaults to FALSE: writing to cameras' clocks changes their OSD
	// timestamps and camera-side event ordering, so the loop is opt-in.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// IntervalMinutes between checks per camera. Default 60 (1h).
	IntervalMinutes int `yaml:"interval_minutes,omitempty" json:"interval_minutes,omitempty"`
	// ThresholdSeconds: skews at or above this magnitude get corrected.
	// Default 30 — small skews are cosmetic, correction has a visible effect
	// on the camera (OSD jump), so don't chase seconds.
	ThresholdSeconds int `yaml:"threshold_seconds,omitempty" json:"threshold_seconds,omitempty"`
	// MaxCorrectionSeconds: refusals beyond this magnitude (default 3600).
	// A camera an hour off is a broken RTC/timezone; silently "fixing" it may
	// mask the real problem — surface it in status instead and let a human
	// trigger the manual sync.
	MaxCorrectionSeconds int `yaml:"max_correction_seconds,omitempty" json:"max_correction_seconds,omitempty"`
	// Timezone is the POSIX TZ string written to cameras on correction
	// (e.g. "CST-8"). Empty = derive from the NVR's local timezone.
	Timezone string `yaml:"timezone,omitempty" json:"timezone,omitempty"`
	// NTPPush: after a successful correction, also point the camera at the
	// NVR's SNTP server (SetNTP + DateTimeType=NTP) so it self-heals from
	// then on. Only effective with the SNTP server enabled. Default false
	// (the manual per-camera button does this on demand).
	NTPPush bool `yaml:"ntp_push,omitempty" json:"ntp_push,omitempty"`
}

// IntervalOrDefault returns the check interval with the default applied.
func (a AutoTimeSyncConfig) IntervalOrDefault() int {
	if a.IntervalMinutes <= 0 {
		return 60
	}
	return a.IntervalMinutes
}

// ThresholdOrDefault returns the correction threshold with the default applied.
func (a AutoTimeSyncConfig) ThresholdOrDefault() int {
	if a.ThresholdSeconds <= 0 {
		return 30
	}
	return a.ThresholdSeconds
}

// MaxCorrectionOrDefault returns the correction ceiling with the default applied.
func (a AutoTimeSyncConfig) MaxCorrectionOrDefault() int {
	if a.MaxCorrectionSeconds <= 0 {
		return 3600
	}
	return a.MaxCorrectionSeconds
}

// applyTimeSyncDefaults fills zero values in the time_sync section.
func applyTimeSyncDefaults(cfg *Config) {
	if strings.TrimSpace(cfg.TimeSync.SNTP.Listen) == "" {
		cfg.TimeSync.SNTP.Listen = ":123"
	}
}

// --- Camera default-credential probe (#credprobe) ---
//
// When auto-discovery enrolls a NEW camera with NO password configured, the
// NVR tests a small list of industry-factory-default credentials against it
// exactly ONCE per camera (success or failure, never again — tracked in the
// DB). On a match the credential is written into the camera config, enabling
// admin operations (time sync writes, device management). With
// rotate_password set, the matched account is first rotated to that password
// via ONVIF SetUser so every camera ends up on the operator's chosen secret.
//
// Deliberately CONFIG-FILE ONLY: no web UI, no API — a credential-probing
// feature must not itself be reachable through the web surface.

// CredentialProbeConfig is the top-level `credential_probe:` yaml section.
type CredentialProbeConfig struct {
	// Enabled defaults to TRUE — the probe only fires for auto-discovered
	// cameras added without a password, runs once, and uses a tiny list.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Candidates overrides the built-in industry-default list. Each entry is
	// one username/password pair. Empty = built-in list (see below).
	Candidates []CredentialProbeCandidate `yaml:"candidates,omitempty" json:"candidates,omitempty"`
	// RotatePassword: when non-empty, a successful probe immediately changes
	// the matched account's password to this value (ONVIF SetUser) and stores
	// the new password in the camera config — hardening the camera from a
	// factory default to the operator's secret in one step. Empty = keep the
	// matched factory credential as-is.
	RotatePassword string `yaml:"rotate_password,omitempty" json:"rotate_password,omitempty"`
	// AttemptIntervalMs is the pause between candidate attempts
	// (default 2000). Keeps the run gentle on cameras with login-failure
	// lockouts.
	AttemptIntervalMs int `yaml:"attempt_interval_ms,omitempty" json:"attempt_interval_ms,omitempty"`
}

// CredentialProbeCandidate is one credential to test.
type CredentialProbeCandidate struct {
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"password"`
}

// EnabledOrDefault resolves the three-state enabled flag.
func (c CredentialProbeConfig) EnabledOrDefault() bool {
	return c.Enabled == nil || *c.Enabled
}

// AttemptIntervalOrDefault returns the inter-attempt pause with default applied.
func (c CredentialProbeConfig) AttemptIntervalOrDefault() int {
	if c.AttemptIntervalMs <= 0 {
		return 2000
	}
	return c.AttemptIntervalMs
}

// EffectiveCandidates returns the configured candidate list, or the built-in
// industry-default list when none is configured. A user-provided list
// REPLACES the built-in one entirely (append semantics would make the list
// grow on every reload since config round-trips through yaml).
func (c CredentialProbeConfig) EffectiveCandidates() []CredentialProbeCandidate {
	if len(c.Candidates) > 0 {
		return c.Candidates
	}
	return BuiltinDefaultCredentials()
}

// BuiltinDefaultCredentials is the built-in factory-default credential list —
// small by design: each entry is one failed login on the device, and some
// cameras lock the account after a handful of failures. Ordered by estimated
// prevalence in the field.
func BuiltinDefaultCredentials() []CredentialProbeCandidate {
	return []CredentialProbeCandidate{
		{Username: "admin", Password: "admin"},
		{Username: "admin", Password: "12345"},    // Hikvision lineage
		{Username: "admin", Password: "123456"},   // generic OEM
		{Username: "admin", Password: "password"}, // Axis/D-Link lineage
		{Username: "admin", Password: "admin1234"},
		{Username: "admin", Password: "9999"},
		{Username: "root", Password: "pass"}, // Axis legacy
		{Username: "admin", Password: "1111"},
	}
}
