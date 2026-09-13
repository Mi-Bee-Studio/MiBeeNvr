package main

// memlimit.go — adaptive Go runtime soft memory limit (GOMEMLIMIT, #756).
//
// The production NVR box ran with MemoryMax=infinity under systemd and no
// GOMEMLIMIT: 1.6GB heap peaks on a 4GB host, squeezing the page cache that
// recording I/O depends on. This runs early in the server startup path
// (subcommands have already exited by then — one-shot CLIs don't need it)
// and installs a conservative limit so the Go heap yields to the cache
// instead of competing with it.

import (
	"log/slog"
	"os"
	"runtime/debug"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/memlimit"
)

// Seams for tests.
var (
	setMemoryLimitFn   = debug.SetMemoryLimit // func(int64) int64 — returns the previous limit
	memlimitPhysicalFn = memlimit.Physical
	memlimitCgroupFn   = memlimit.CgroupLimit
)

// applyMemoryLimit installs the process soft memory limit per #756:
//
//	env GOMEMLIMIT  >  memory.soft_limit_bytes  >  auto heuristic
//	  (native,        (explicit operator value)    (cgroup ~80% vs
//	   runtime                                    min(45% physical, 1GiB),
//	   applied                                    whichever is tighter)
//	   before main)
//
// The soft limit never makes the process OOM by itself — the GC just works
// harder near it; GOGC stays at its default.
func applyMemoryLimit(cfg *config.Config) {
	// The runtime applies the env var natively before main; respect it.
	if os.Getenv("GOMEMLIMIT") != "" {
		slog.Info("GOMEMLIMIT env var set — keeping the runtime's own limit",
			"value", os.Getenv("GOMEMLIMIT"))
		memlimit.RecordApplied(0)
		return
	}

	if cfg.Memory.DisableAutoLimit && cfg.Memory.SoftLimitBytes == 0 {
		slog.Info("memory auto limit disabled by config — using Go runtime default")
		memlimit.RecordApplied(0)
		return
	}

	limit := memlimit.ComputeLimit(memlimitPhysicalFn(), memlimitCgroupFn(), cfg.Memory.SoftLimitBytes, memlimit.AutoParams{
		PhysicalPercent: cfg.Memory.AutoPhysicalPercent,
		CapBytes:        cfg.Memory.AutoCapBytes,
		CgroupPercent:   cfg.Memory.AutoCgroupPercent,
	})
	if limit <= 0 {
		slog.Info("no reliable memory size detected — leaving Go runtime default (no GOMEMLIMIT)")
		memlimit.RecordApplied(0)
		return
	}

	setMemoryLimitFn(limit)
	memlimit.RecordApplied(limit)
	slog.Info("process memory soft limit set (GOMEMLIMIT)",
		"bytes", limit, "mib", limit>>20,
		"source", limitSource(cfg.Memory.SoftLimitBytes))
}

func limitSource(yamlOverride int64) string {
	if yamlOverride > 0 {
		return "memory.soft_limit_bytes"
	}
	return "auto (cgroup/physical heuristic)"
}
