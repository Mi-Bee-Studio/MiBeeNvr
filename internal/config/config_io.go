package config

// IOConfig tunes process-level I/O scheduling for background batch work
// (#751). Background jobs (segment merge, cleanup/repair deletes, timelapse
// frame extraction) share one kernel I/O queue with foreground work
// (recording writes, API file serving, SQLite); by default they compete as
// equals, which on busy media starves the foreground into multi-second
// stalls. A configured byte budget paces them instead.
type IOConfig struct {
	// BudgetBytesPerSec is the shared token-bucket rate for background I/O.
	// 0 (default) disables budgeting entirely — behavior is identical to
	// previous releases. A sensible deployment value is ~25% of the storage
	// medium's sequential write throughput (e.g. 8-16 MiB/s for a slow SD
	// card, 40-60 MiB/s for a HDD on USB).
	BudgetBytesPerSec int64 `yaml:"budget_bytes_per_sec"`

	// BudgetBurstBytes is the token-bucket burst capacity (maximum bytes a
	// single burst can consume without waiting). 0 (default) → one second of
	// budget_bytes_per_sec.
	BudgetBurstBytes int64 `yaml:"budget_burst_bytes"`

	// DeleteUnlinksPerSec caps recursive frame-tree (MJPEG/timelapse)
	// unlinks per second while the I/O budget is enabled (#755) — a
	// metadata-storm guardrail so the ext4 journal (jbd2) cannot saturate
	// and self-sustain after the deleting process exits (#748 lesson).
	// 0 → default 200/s (applied when a budget is configured). Only meaningful together with
	// budget_bytes_per_sec > 0; without a budget the legacy fixed
	// time-slice pacing stays active (no default behavior change).
	DeleteUnlinksPerSec int64 `yaml:"delete_unlinks_per_sec"`
}
