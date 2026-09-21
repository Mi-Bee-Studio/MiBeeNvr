package config

// config_remote.go — S3-compatible object-storage offload configuration
// (issue #874 batch 1: local recording + async post-merge upload, plan A).
//
// Credentials support ${VAR} environment-variable references so secrets never
// need to live in the YAML. Expansion happens at CONSUMPTION time (client
// construction in internal/objectstore), NOT here: the in-memory config keeps
// the literal "${S3_ACCESS_KEY}" so a Settings-UI save round-trips the
// reference instead of writing the plaintext secret back to disk.

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// RemoteStorageConfig configures offloading merged recordings to an
// S3-compatible object store (AWS S3 / MinIO / R2 / B2 / OSS / COS). Disabled
// by default; the whole subsystem (client, uploader service, evict CLI
// targets) is inert unless Enabled.
type RemoteStorageConfig struct {
	Enabled bool `yaml:"enabled"`
	// EndpointURL is the S3 API endpoint. Empty means AWS default resolution —
	// but batch 1 requires it explicitly (every supported target has one).
	EndpointURL string `yaml:"endpoint_url"`
	Region      string `yaml:"region"`
	Bucket      string `yaml:"bucket"`
	// PathStyle selects path-style addressing (<endpoint>/<bucket>/<key>),
	// required by MinIO and most self-hosted stores. Default true; AWS with
	// virtual-hosted buckets sets it false.
	PathStyle *bool `yaml:"path_style"`
	// AccessKeyID / SecretAccessKey are static credentials. Both support
	// ${VAR} env expansion (see ExpandEnvRefs).
	AccessKeyID     string `yaml:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key"`
	// Prefix is the object-key root for all uploaded objects
	// ("<prefix>/<camera>/<date>/<id>.<ext>"). Default "recordings".
	Prefix string             `yaml:"prefix,omitempty"`
	Upload RemoteUploadConfig `yaml:"upload"`
	Evict  RemoteEvictConfig  `yaml:"evict"`
}

// RemoteUploadConfig tunes the uploader loop.
type RemoteUploadConfig struct {
	// MaxConcurrency bounds parallel PUTs. Default 1 — home uplinks are the
	// bottleneck, not CPU; parallelism only helps multi-bucket fan-out later.
	MaxConcurrency int `yaml:"max_concurrency"`
	// ScanIntervalS is the discovery sweep cadence in seconds (default 60).
	ScanIntervalS int `yaml:"scan_interval_s"`
	// MinAgeS is how long a merged recording must have been closed (ended_at)
	// before it is eligible for upload (default 900 = 15m). Covers the rolling
	// merge debounce + backfill latency so a still-growing window bucket is
	// never uploaded; a late append that still slips through is caught by the
	// stale-requeue check (file size vs uploaded size).
	MinAgeS int `yaml:"min_age_s"`
	// BacklogLimit caps pending+uploading outbox rows. When upstream bandwidth
	// can't keep up with production, enqueueing stops and warns instead of
	// silently queueing forever (default 5000; 0 = unlimited).
	BacklogLimit int `yaml:"backlog_limit"`
}

// RemoteEvictConfig controls local eviction after upload confirmation.
type RemoteEvictConfig struct {
	// AfterDays: days to keep the local file after upload confirmation before
	// auto-eviction. 0 (default) = upload-only, never evict; batch 1 ships the
	// manual CLI (`mibee-nvr offload evict`), auto-eviction lands later.
	AfterDays int `yaml:"after_days"`
}

// envRefPattern matches a single ${VAR} reference (POSIX-ish name).
var envRefPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ExpandEnvRefs replaces every ${VAR} reference in s with the value of the
// environment variable VAR. Unset variables expand to the empty string — so a
// misconfigured reference fails validation (empty credential) loudly instead
// of shipping a literal "${...}" string as a secret. Invalid names are left
// untouched. This is the credential seam: call it at client construction,
// never write the result back into the saved config.
func ExpandEnvRefs(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return envRefPattern.ReplaceAllStringFunc(s, func(m string) string {
		return os.Getenv(m[2 : len(m)-1])
	})
}

// validateRemoteStorage validates the storage.remote block. Returns nil when
// disabled. Called from the central Validate() — no validation outside it.
func validateRemoteStorage(r RemoteStorageConfig) error {
	if !r.Enabled {
		return nil
	}
	if strings.TrimSpace(r.EndpointURL) == "" {
		return fmt.Errorf("storage.remote.endpoint_url required when storage.remote.enabled")
	}
	if u, err := url.Parse(r.EndpointURL); err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("storage.remote.endpoint_url must be a valid URL (e.g. https://s3.example.com)")
	}
	if strings.TrimSpace(r.Bucket) == "" {
		return fmt.Errorf("storage.remote.bucket required when storage.remote.enabled")
	}
	if strings.TrimSpace(r.AccessKeyID) == "" {
		return fmt.Errorf("storage.remote.access_key_id required when storage.remote.enabled")
	}
	if strings.TrimSpace(r.SecretAccessKey) == "" {
		return fmt.Errorf("storage.remote.secret_access_key required when storage.remote.enabled")
	}
	if r.Upload.MaxConcurrency < 0 || r.Upload.MaxConcurrency > 8 {
		return fmt.Errorf("storage.remote.upload.max_concurrency must be in [0, 8] (0 = default 1)")
	}
	if r.Upload.ScanIntervalS < 0 || r.Upload.ScanIntervalS > 3600 {
		return fmt.Errorf("storage.remote.upload.scan_interval_s must be in [0, 3600] (0 = default 60)")
	}
	if r.Upload.MinAgeS < 60 || r.Upload.MinAgeS > 86400 {
		return fmt.Errorf("storage.remote.upload.min_age_s must be in [60, 86400] (at least the merge debounce + backfill latency)")
	}
	if r.Upload.BacklogLimit < 0 {
		return fmt.Errorf("storage.remote.upload.backlog_limit must be >= 0 (0 = unlimited)")
	}
	if r.Evict.AfterDays < 0 {
		return fmt.Errorf("storage.remote.evict.after_days must be >= 0 (0 = upload-only)")
	}
	return nil
}
