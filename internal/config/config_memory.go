package config

// MemoryConfig tunes the process's own memory discipline (#756).
//
// The Go runtime by default grows the heap until GOGC=100 doubling targets
// are hit, with no regard for the page cache that recording I/O depends on.
// On 1GB-class boards that is an OOM express; on bigger boards it silently
// evicts the cache and amplifies I/O. The NVR sets a conservative
// GOMEMLIMIT at startup (min(45% physical, 1GiB), or ~80% of a cgroup limit
// when tighter) unless disabled here.
type MemoryConfig struct {
	// SoftLimitBytes overrides the automatic GOMEMLIMIT computation with an
	// explicit value. 0 (default) = automatic heuristic. Must be at least
	// 64MiB — below that the runtime itself would thrash.
	SoftLimitBytes int64 `yaml:"soft_limit_bytes"`

	// DisableAutoLimit turns the automatic GOMEMLIMIT heuristic off,
	// restoring the Go runtime default. The native GOMEMLIMIT env var always
	// wins over everything (it is applied by the runtime before main).
	DisableAutoLimit bool `yaml:"disable_auto_limit"`

	// Auto-heuristic knobs (#756) — defaults materialized by config defaults
	// (45 / 1GiB / 80); every value is operator-tunable, nothing is hardcoded
	// in wiring code.
	AutoPhysicalPercent int   `yaml:"auto_physical_percent"` // share of physical RAM for the heap tier (default 45)
	AutoCapBytes        int64 `yaml:"auto_cap_bytes"`        // cap of the physical tier on big hosts (default 1GiB)
	AutoCgroupPercent   int   `yaml:"auto_cgroup_percent"`   // share taken from a cgroup ceiling (default 80)
}
