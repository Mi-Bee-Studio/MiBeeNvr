package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

// Server, storage, cleanup, auth, observability, and miscellaneous config.

type ServerConfig struct {
	Listen string `yaml:"listen"` // default ":9090"
	// TLSListen enables a second HTTPS listener alongside the plain-HTTP one.
	// Required for browser WebRTC (WHEP) which needs a Secure Context, and for
	// secure WebUI access when not behind a TLS-terminating reverse proxy.
	// Empty = no HTTPS listener (plain HTTP only). e.g. ":9443".
	TLSListen string `yaml:"tls_listen"`
	// CertFile / KeyFile are the TLS certificate and private key paths. Required
	// when TLSListen is set. For production use a real CA-signed cert (e.g. via
	// Caddy/Let's Encrypt or an internal CA); for LAN testing a self-signed cert
	// works (browsers will warn). See deploy/AGENTS.md.
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	// DeviceID is a stable per-install identity (UUIDv4). Generated on first
	// load and persisted so it survives restarts and IP changes (#330).
	// Exposed via GET /api/health for LAN clients that anchor on an ID
	// instead of an IP address.
	DeviceID   string `yaml:"device_id,omitempty"`
	DeviceName string `yaml:"device_name,omitempty"`
	// UnixSocket starts a second HTTP listener on a Unix domain socket at this
	// path, alongside the TCP listener. NAS platforms (fnOS unified gateway)
	// validate their own login session, then forward authenticated requests to
	// the socket with trusted X-Trim-* user headers; the gateway-auth middleware
	// is mounted ONLY on this listener, so those headers are ignored on TCP.
	// Empty = no socket listener. Overridable via NVR_UNIX_SOCKET.
	UnixSocket string `yaml:"unix_socket"`
	// BasePath is the URL prefix the app is served under when fronted by a
	// reverse proxy or NAS unified gateway (e.g. "/app/mibee-nvr"). Incoming
	// request paths carrying the prefix are stripped before routing, and the
	// prefix is injected into index.html so the SPA can build absolute URLs
	// (assets, API, stream endpoints). Empty = served at "/". Overridable via
	// NVR_BASE_PATH.
	BasePath string `yaml:"base_path"`
	// Discovery controls how the NVR announces itself on the LAN for clients
	// that cannot rely on subnet scanning or multicast (mDNS).
	Discovery DiscoveryConfig `yaml:"discovery"`
}

// DiscoveryConfig groups LAN self-announcement mechanisms.
type DiscoveryConfig struct {
	UDP  UDPDiscoveryConfig  `yaml:"udp"`
	MDNS MDNSDiscoveryConfig `yaml:"mdns"`
}

// MDNSDiscoveryConfig is the mDNS/DNS-SD service registration
// (_mibee-nvr._tcp.local) — the fast discovery path for LAN clients; the UDP
// responder covers multicast-restricted networks (#333).
type MDNSDiscoveryConfig struct {
	Enabled *bool `yaml:"enabled"` // default true
}

// UDPDiscoveryConfig is the UDP broadcast responder (MIBEE-NVR-DISC/v1, #334):
// listens on 0.0.0.0:49090 and answers the exact probe "MIBEE-NVR-DISCv1?"
// with a unicast JSON identity payload. This is the discovery fallback for
// multicast-restricted Wi-Fi where mDNS does not travel.
type UDPDiscoveryConfig struct {
	Enabled *bool `yaml:"enabled"` // default true
	Port    int   `yaml:"port"`    // default DefaultUDPPort
}

// DefaultUDPPort is the default UDP listen port for the discovery responder.
const DefaultUDPPort = 49090

// NormalizeBasePath canonicalizes a URL base path: ensured leading "/", no
// trailing "/" ("/app/x/" and "app/x" both become "/app/x"). Returns "" for
// "/" and blank input (serving at the root needs no prefix).
func NormalizeBasePath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimRight(p, "/")
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

type StorageConfig struct {
	RootDir         string `yaml:"root_dir"`         // default "/mnt/data/nvr"
	SegmentDuration string `yaml:"segment_duration"` // default "30s"
	// Candidates lists additional storage locations made available to the NVR
	// by the host platform (#395): on fnOS these are the user-authorized
	// directories (TRIM_DATA_ACCESSIBLE_PATHS), mounted into the container and
	// exposed here so the settings UI can offer them as recording-root choices.
	// Populated from the NVR_STORAGE_CANDIDATES env var; purely informational
	// for the backend — the recording root remains root_dir.
	Candidates []string `yaml:"candidates,omitempty"`
}

type CleanupConfig struct {
	RetentionDays        int    `yaml:"retention_days"`         // default 30
	CheckInterval        string `yaml:"check_interval"`         // default "1h"
	DiskThresholdPercent int    `yaml:"disk_threshold_percent"` // default 85 (HDD perf cliff near 90%+ full)
	// MotionAwareDiskCleanup orders disk-threshold deletion boring-first
	// (issue #435): static segments (motion_score≈0) are deleted before
	// active ones; unanalyzed segments rank neutrally. Default ON (nil=true)
	// because the disk-pressure path is a best-effort eviction — the user's
	// "keep N days" expectation is governed by the time-retention path, which
	// this flag does not touch.
	MotionAwareDiskCleanup *bool `yaml:"motion_aware_disk_cleanup,omitempty"`
}

// MotionAwareDiskCleanupEnabled resolves the flag with default-on semantics.
func (c CleanupConfig) MotionAwareDiskCleanupEnabled() bool {
	return c.MotionAwareDiskCleanup == nil || *c.MotionAwareDiskCleanup
}

type AuthConfig struct {
	Username     string          `yaml:"username"`
	PasswordHash string          `yaml:"password_hash"`
	Password     string          `yaml:"password"`
	RateLimit    RateLimitConfig `yaml:"rate_limit"`
}

// RateLimitConfig controls auth failure rate limiting.
// When Enabled is false (default), no rate limiting is applied.
type RateLimitConfig struct {
	Enabled       *bool `yaml:"enabled"`        // default false
	MaxFailures   int   `yaml:"max_failures"`   // default 20
	WindowMinutes int   `yaml:"window_minutes"` // default 1
}

type MQTTConfig struct {
	Enabled  bool   `yaml:"enabled"` // default false
	Broker   string `yaml:"broker"`
	Topic    string `yaml:"topic"`
	ClientID string `yaml:"client_id"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// ObservabilityConfig defines observability settings
type ObservabilityConfig struct {
	LogLevel    string `yaml:"log_level"`    // default "info"
	LogFormat   string `yaml:"log_format"`   // default "text"
	EnablePprof bool   `yaml:"enable_pprof"` // default false
}

// RemoteLogConfig defines remote log shipping settings (e.g. VictoriaLogs).
type RemoteLogConfig struct {
	Enabled  bool   `yaml:"enabled"`  // default false
	Endpoint string `yaml:"endpoint"` // VictoriaLogs URL, e.g. "http://localhost:9428/insert/jsonline"
	Format   string `yaml:"format"`   // "jsonline" (default) or "loki"
}

// MetricsAuthConfig defines optional independent authentication for the /metrics endpoint.
// When username and password (or password_hash) are non-empty, /metrics requires BasicAuth.
// When empty, /metrics stays public (backward compatible).
type MetricsAuthConfig struct {
	Username     string `yaml:"username"`
	Password     string `yaml:"password"`
	PasswordHash string `yaml:"password_hash"`
}

// IsConfigured returns true if both username and a password (or hash) are set.
func (c MetricsAuthConfig) IsConfigured() bool {
	return strings.TrimSpace(c.Username) != "" &&
		(strings.TrimSpace(c.Password) != "" || strings.TrimSpace(c.PasswordHash) != "")
}

// SecurityConfig controls HTTP security response headers.
type SecurityConfig struct {
	// FrameAncestors sets the CSP frame-ancestors directive: who may embed the
	// Web UI in an <iframe>. A space-separated list of sources
	// (e.g. "'self' http://192.168.1.10"). Empty/'self' = no cross-origin
	// embedding. Needed for platforms that embed the app from a different origin
	// (e.g. the fnOS desktop, where the desktop page origin differs from the
	// NVR's :9090 origin). Overridable via the NVR_FRAME_ANCESTORS env var.
	FrameAncestors string `yaml:"frame_ancestors"`
}

// APIKeyConfig represents a single API key for MiBeeVision integration.
type APIKeyConfig struct {
	Key     string `yaml:"key" json:"key"`
	Name    string `yaml:"name" json:"name"`
	Revoked bool   `yaml:"revoked,omitempty" json:"revoked,omitempty"`
}

type WebSocketConfig struct {
	MaxViewers   int           `yaml:"max_viewers" json:"maxViewers"`
	WriteBufSize int           `yaml:"write_buf_size" json:"writeBufSize"`
	IdleTimeout  time.Duration `yaml:"idle_timeout" json:"idleTimeout"`
}

// memAvailableMB reports the system's available memory in MB. Used to gate the
// segment-duration cap: the MP4 muxer holds all samples in RAM until a segment
// closes, so available RAM bounds the safe maximum segment duration.
//
// Reads /proc/meminfo (Linux). On non-Linux or parse failure, returns a
// conservative 1024 MB so the low-memory (30s) cap applies — never over-estimates.
func memAvailableMB() int {
	const fallback = 1024 // conservative: assume low memory → 30s cap
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return fallback
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// "MemAvailable:  3123456 kB"
		if strings.HasPrefix(line, "MemAvailable:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				var kb int
				if _, err := fmt.Sscanf(fields[1], "%d", &kb); err == nil {
					return kb / 1024
				}
			}
		}
	}
	return fallback
}

// maxSegmentDurationForMem returns the maximum safe segment duration based on
// available RAM. Low-memory devices (≤2GB, e.g. RPi 3B) are capped at 30s to
// avoid OOM; higher-memory devices (Banana Pi M5, x86) allow up to 2m, which
// halves the fragment count rolling merge must process.
func maxSegmentDurationForMem() time.Duration {
	const (
		lowMemCap  = 30 * time.Second // RPi 3B (≤2GB): MP4 muxer RAM safety
		highMemCap = 2 * time.Minute  // Banana Pi M5 / x86 (≥2GB): fewer fragments
		threshold  = 2048             // MB — 2GB
	)
	if memAvailableMB() > threshold {
		return highMemCap
	}
	return lowMemCap
}
