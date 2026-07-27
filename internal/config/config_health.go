package config

// Health monitoring, IP self-healing (rediscovery), and auto-discovery config.

// HealthConfig configures the camera health monitoring system.
type HealthConfig struct {
	Enabled         bool                        `yaml:"enabled"`
	EventsRetention string                      `yaml:"events_retention"`
	Alerts          HealthAlertsConfig          `yaml:"alerts"`
	Layer1          HealthLayer1Config          `yaml:"layer1"`
	Layer2          HealthLayer2Config          `yaml:"layer2"`
	Layer2_5        HealthLayer2_5Config        `yaml:"layer2_5"`
	AutoRemediation HealthAutoRemediationConfig `yaml:"auto_remediation"`
	// Rediscovery triggers IP re-discovery (ONVIF unicast scan) when a camera is
	// blacklisted by auto-remediation — i.e. the IP has permanently changed.
	Rediscovery RediscoveryConfig `yaml:"rediscovery"`
}

// RediscoveryConfig controls the IP self-healing engine (internal/rediscovery/).
// When a camera's IP changes (e.g. after an AP reboot across per-subnet DHCP),
// the engine re-discovers the camera by its ONVIF serial number via unicast
// probing (cross-subnet; does NOT rely on multicast WS-Discovery).
type RediscoveryConfig struct {
	// Enabled is a *bool so the feature defaults to ON when unset, but can be
	// explicitly turned off with `rediscovery: { enabled: false }`. Use
	// RediscoveryEnabled() to read the effective value.
	Enabled     *bool `yaml:"enabled"`
	MaxParallel int   `yaml:"max_parallel"` // concurrent unicast probes (default 16, RPi-3B friendly)
	// ProbeTimeout is the per-IP probe timeout (default "2s").
	ProbeTimeout string `yaml:"probe_timeout"`
	// MaxDuration bounds a single full scan (default "30s") so a wide subnet_hints
	// list cannot pin the heal loop forever.
	MaxDuration string `yaml:"max_duration"`
}

// RediscoveryEnabled reports the effective enabled state (defaults to true when
// the pointer is nil, i.e. when the user did not explicitly set it).
func (r RediscoveryConfig) RediscoveryEnabled() bool {
	if r.Enabled == nil {
		return true
	}
	return *r.Enabled
}

// AutoDiscoverConfig controls the background auto-discovery engine
// (internal/autodiscover/). When enabled, the NVR continuously listens for
// WS-Discovery Hello messages (passive, zero-latency — a camera is seen the
// moment it powers on) and periodically issues multicast Probe sweeps (active,
// catches devices that do not send Hello, e.g. after a silent IP change).
// Discovered ONVIF devices are added automatically: unauthenticated ones
// (e.g. ESP32 MiBeeCam) are activated immediately; authenticated ones that
// match the default credentials are activated, otherwise they are persisted in
// "pending_activation" state awaiting user-supplied credentials.
//
// Mirrors the Hikvision NVR "plug-and-play" experience. Defaults to OFF so
// existing deployments are unchanged until the user opts in.
type AutoDiscoverConfig struct {
	// Enabled is *bool so the feature defaults to OFF when unset (unlike
	// Rediscovery which defaults ON). Use AutoDiscoverEnabled() to read the
	// effective value — never dereference the pointer directly.
	Enabled *bool `yaml:"enabled"`
	// ScanIntervalSeconds is the period between active Probe sweeps (default 60).
	// Must be >= 30 to respect RPi-3B constraints.
	ScanIntervalSeconds int `yaml:"scan_interval"`
	// ListenForHello controls the passive mode: a resident UDP 3702 listener
	// that reacts to WS-Discovery Hello the instant a device announces itself.
	// Defaults to true when AutoDiscover is enabled. Can be turned off to use
	// active-sweep-only mode (lower resource, higher latency).
	ListenForHello *bool `yaml:"listen_for_hello"`
	// NetworkInterface binds the discovery sockets to a specific interface
	// (e.g. "end0", "eth0"). Empty = default multicast interface (the kernel's
	// default route). Set this when the NVR is multi-homed and cameras live on a
	// non-default interface, otherwise multicast Probe/Hello may go out the wrong NIC.
	NetworkInterface string `yaml:"network_interface"`
	// DefaultUsername/DefaultPassword are tried against authenticated ONVIF
	// devices during discovery. If they succeed, the device is activated
	// immediately; if they fail or are unset, the device is added in
	// "pending_activation" state. Leave blank to require manual activation for
	// every authenticated device.
	DefaultUsername string `yaml:"default_username"`
	DefaultPassword string `yaml:"default_password"`
	// IgnoreScopes is a deny-list of ONVIF scope substrings. A device whose
	// scopes contain any entry is skipped (never auto-added). Useful to exclude
	// e.g. a specific hardware line: ["hardware/LegacyCam"].
	IgnoreScopes []string `yaml:"ignore_scopes"`
}

// AutoDiscoverEnabled reports the effective enabled state. Unlike most *bool
// config fields in this package, auto-discover defaults to OFF (the zero value
// of the pointer is nil → false) so existing deployments are not surprised by
// cameras appearing automatically.
func (a AutoDiscoverConfig) AutoDiscoverEnabled() bool {
	if a.Enabled == nil {
		return false
	}
	return *a.Enabled
}

// ListenForHelloEnabled reports whether the passive Hello listener should run.
// Defaults to true (when the pointer is nil) whenever auto-discover is enabled.
func (a AutoDiscoverConfig) ListenForHelloEnabled() bool {
	if a.ListenForHello == nil {
		return true
	}
	return *a.ListenForHello
}

type HealthAlertsConfig struct {
	Cooldown string `yaml:"cooldown"`
	MQTT     bool   `yaml:"mqtt"`
}

type HealthLayer1Config struct {
	OfflineThreshold string `yaml:"offline_threshold"`
}

type HealthLayer2Config struct {
	BitrateChangeThreshold float64 `yaml:"bitrate_change_threshold"`
	MinFPS                 int     `yaml:"min_fps"`
	MaxIDRInterval         string  `yaml:"max_idr_interval"`
}

type HealthLayer2_5Config struct {
	FreezeTimeout string `yaml:"freeze_timeout"`
}

type HealthAutoRemediationConfig struct {
	Enabled            bool `yaml:"enabled"`
	MaxRestartsPerHour int  `yaml:"max_restarts_per_hour"`
	CooldownMinutes    int  `yaml:"cooldown_minutes"`
	BlacklistHours     int  `yaml:"blacklist_hours"`
	GlobalMaxPerMin    int  `yaml:"global_max_per_min"`
	// ReconnectingTimeoutMinutes is how long a recorder may stay in the
	// "reconnecting" state before auto-remediation treats it as a dead-end and
	// triggers a hard restart (which can then escalate to blacklist + IP
	// rediscovery). A recorder's own reconnect loop never escalates to
	// StatusError, so without this gate a camera whose IP changed would loop
	// forever and rediscovery would never fire. 0 = use default (10 min).
	ReconnectingTimeoutMinutes int `yaml:"reconnecting_timeout_minutes"`
	// RediscoveryRescanMinutes is how often to re-attempt IP rediscovery for a
	// blacklisted camera while the blacklist is still active. Without this, a
	// camera that comes back online during the blacklist window (e.g. power
	// restored) is not recovered until the full BlacklistHours elapses, because
	// rediscovery only scans once at the moment of blacklisting. 0 = disabled
	// (legacy behavior: scan only once at blacklisting). Default 5 min.
	RediscoveryRescanMinutes int `yaml:"rediscovery_rescan_minutes"`
	// RediscoveryRescanMaxMinutes caps the exponential backoff interval for
	// periodic blacklist rescans. After each consecutive "device not found"
	// result, the rescan interval is multiplied by RediscoveryRescanBackoff
	// until it hits this ceiling. 0 = use default (60 min). This prevents
	// permanently-dead cameras from sustaining a full-/24 scan every 5 min
	// indefinitely, which hammered disk IO on RPi-class hosts.
	RediscoveryRescanMaxMinutes int `yaml:"rediscovery_rescan_max_minutes"`
	// RediscoveryRescanBackoff is the exponential multiplier applied to the
	// rescan interval after each consecutive miss. Default 2.0 →
	// 5min→10min→20min→40min→60min(cap). Must be >= 1.0.
	RediscoveryRescanBackoff float64 `yaml:"rediscovery_rescan_backoff"`
	// RediscoveryMaxScanMisses, when > 0, stops periodic rescans entirely
	// after this many consecutive "not found" results — the camera is
	// assumed permanently offline and must be recovered via manual
	// POST /api/cameras/{id}/rediscover. 0 = unlimited (backoff caps at
	// RediscoveryRescanMaxMinutes). Default 0.
	RediscoveryMaxScanMisses int `yaml:"rediscovery_max_scan_misses"`
}

// ResolveHealthOverrides returns the effective health thresholds for a camera.
// Per-camera overrides take precedence over global health config when set.
// Duration strings are left as-is (empty string means "use global").
func ResolveHealthOverrides(global HealthConfig, overrides HealthOverrides) ResolvedHealthOverrides {
	result := ResolvedHealthOverrides{
		MaxIDRInterval:         global.Layer2.MaxIDRInterval,
		BitrateChangeThreshold: global.Layer2.BitrateChangeThreshold,
		MinFPS:                 global.Layer2.MinFPS,
		OfflineThreshold:       global.Layer1.OfflineThreshold,
		FreezeTimeout:          global.Layer2_5.FreezeTimeout,
	}
	if overrides.MaxIDRInterval != "" {
		result.MaxIDRInterval = overrides.MaxIDRInterval
	}
	if overrides.BitrateChangeThreshold > 0 {
		result.BitrateChangeThreshold = overrides.BitrateChangeThreshold
	}
	if overrides.MinFPS > 0 {
		result.MinFPS = overrides.MinFPS
	}
	if overrides.OfflineThreshold != "" {
		result.OfflineThreshold = overrides.OfflineThreshold
	}
	if overrides.FreezeTimeout != "" {
		result.FreezeTimeout = overrides.FreezeTimeout
	}
	return result
}

// ResolvedHealthOverrides holds fully-resolved health threshold values
// (duration strings ready for time.ParseDuration).
type ResolvedHealthOverrides struct {
	MaxIDRInterval         string
	BitrateChangeThreshold float64
	MinFPS                 int
	OfflineThreshold       string
	FreezeTimeout          string
}
