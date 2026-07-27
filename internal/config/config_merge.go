package config

// Merge configuration types and resolution helpers.

type MergeConfig struct {
	Enabled            bool   `yaml:"enabled"`
	CheckInterval      string `yaml:"check_interval"`
	WindowSize         string `yaml:"window_size"`
	BatchLimit         int    `yaml:"batch_limit"`
	MinSegmentAge      string `yaml:"min_segment_age"`
	MinSegmentsToMerge int    `yaml:"min_segments_to_merge"`

	// Rolling merge (quasi-real-time): event-driven merge on SegmentCompleted.
	// When enabled, each newly-closed segment is merged into a per-camera window
	// bucket within seconds (vs the periodic MergeManager's ~1h latency).
	// Independent of Enabled/CheckInterval — can run alongside the periodic merge.
	//
	// RollingEnabled is a *bool so rolling merge defaults to ON when unset
	// (continuous 24/7 recording otherwise produces thousands of 30s fragments
	// per camera per day), but can be explicitly turned OFF — globally via
	// `merge: { rolling_enabled: false }` or per-camera — to avoid write
	// amplification (e.g. SD-card cameras). Use RollingEnabledValue() to read
	// the effective value; never dereference the pointer directly.
	RollingEnabled  *bool  `yaml:"rolling_enabled" json:"rolling_enabled"`
	RollingDebounce string `yaml:"rolling_debounce" json:"rolling_debounce"` // e.g. "500ms", "2s"
	RollingWindow   string `yaml:"rolling_window" json:"rolling_window"`     // e.g. "1h", "30m"

	// RollingMinDuration is the target minimum duration for merged recordings.
	// Merged files shorter than this are marked merge_quality='short' and can be
	// further consolidated via POST /api/merge/consolidate. Default "5m".
	RollingMinDuration string `yaml:"rolling_min_duration" json:"rolling_min_duration"`

	// RollingBackfill caps the startup backfill so a first boot after enabling
	// rolling merge cannot trigger an IO storm on resource-constrained hosts
	// (RPi 3B). MaxSegments=0 means unlimited (not recommended on RPi).
	// MaxAge bounds backfill to segments newer than this age; older segments are
	// left for the periodic MergeManager to digest gradually.
	RollingBackfillMaxSegments int    `yaml:"rolling_backfill_max_segments" json:"rolling_backfill_max_segments"`
	RollingBackfillMaxAge      string `yaml:"rolling_backfill_max_age" json:"rolling_backfill_max_age"`

	// RollingBackfillInterval governs a periodic background sweep of historical
	// pending segments (in addition to the one-shot startup backfill). The
	// startup backfill is throttled to max_segments/max_age and only runs once;
	// without a periodic sweep, historical pending accumulates whenever the
	// startup backfill can't keep up (e.g. thousands of 30s H265 fragments/day).
	// Default "10m"; "0" disables the periodic sweep (startup backfill only).
	RollingBackfillInterval string `yaml:"rolling_backfill_interval" json:"rolling_backfill_interval"`

	// RollingBackfillBatch caps how many pending segments one periodic sweep
	// processes per cycle, so a single sweep can't monopolize IO for minutes.
	// Default 500. The sweep uses try-locks and yields to real-time events.
	RollingBackfillBatch int `yaml:"rolling_backfill_batch" json:"rolling_backfill_batch"`

	// RollingBackfillConcurrency bounds how many cameras the periodic sweep
	// merges in parallel. Each camera holds its own merge lock, so this controls
	// total disk IO across cameras — the main knob for trading merge throughput
	// against recording-pipeline latency on USB HDD.
	// Default 0 = auto: 1 on devices with ≤2GB RAM (RPi 3B), 3 on larger hosts.
	// Lower it (e.g. 1) if recording drops frames during backlog clearing;
	// raise it on SSD/NVMe hosts where seek contention is not a concern.
	RollingBackfillConcurrency int `yaml:"rolling_backfill_concurrency" json:"rolling_backfill_concurrency"`
}

// RollingEnabledValue reports the effective rolling-merge enabled state,
// defaulting to true when the pointer is nil (user did not explicitly set it).
// This is the only correct way to read RollingEnabled — it preserves the
// "unset → on" / "explicitly false → off" distinction that a bare bool cannot.
func (m MergeConfig) RollingEnabledValue() bool {
	if m.RollingEnabled == nil {
		return true
	}
	return *m.RollingEnabled
}

// ResolveMergeConfig returns the effective MergeConfig for a camera.
// If perCamera is nil, the global config is returned unchanged.
// If perCamera is non-nil, only non-zero fields override the global config.
func ResolveMergeConfig(global MergeConfig, perCamera *MergeConfig) MergeConfig {
	if perCamera == nil {
		return global
	}
	result := global
	if perCamera.Enabled {
		result.Enabled = true
	}
	if perCamera.CheckInterval != "" {
		result.CheckInterval = perCamera.CheckInterval
	}
	if perCamera.WindowSize != "" {
		result.WindowSize = perCamera.WindowSize
	}
	if perCamera.BatchLimit > 0 {
		result.BatchLimit = perCamera.BatchLimit
	}
	if perCamera.MinSegmentAge != "" {
		result.MinSegmentAge = perCamera.MinSegmentAge
	}
	if perCamera.MinSegmentsToMerge > 0 {
		result.MinSegmentsToMerge = perCamera.MinSegmentsToMerge
	}
	// Rolling merge fields: per-camera overrides only when explicitly set.
	// RollingEnabled is a *bool — a non-nil per-camera value (true OR false)
	// overrides the global so users can disable rolling merge per-camera even
	// when the global default is on.
	if perCamera.RollingEnabled != nil {
		result.RollingEnabled = perCamera.RollingEnabled
	}
	if perCamera.RollingDebounce != "" {
		result.RollingDebounce = perCamera.RollingDebounce
	}
	if perCamera.RollingWindow != "" {
		result.RollingWindow = perCamera.RollingWindow
	}
	if perCamera.RollingMinDuration != "" {
		result.RollingMinDuration = perCamera.RollingMinDuration
	}
	if perCamera.RollingBackfillMaxSegments > 0 {
		result.RollingBackfillMaxSegments = perCamera.RollingBackfillMaxSegments
	}
	if perCamera.RollingBackfillMaxAge != "" {
		result.RollingBackfillMaxAge = perCamera.RollingBackfillMaxAge
	}
	return result
}
