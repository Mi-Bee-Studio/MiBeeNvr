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
}
