// Package memlimit computes a conservative Go runtime soft memory limit
// (GOMEMLIMIT) from the host's actual memory situation (#756).
//
// Design baseline is 1GB-RAM class boards: an unconstrained Go heap (default
// GOGC=100, no GOMEMLIMIT) grows until it competes with the page cache that
// recording I/O depends on — on small boards that is an OOM express and on
// bigger boards a hidden I/O amplifier (cache evicted by heap → more reads).
// The limit set here is deliberately conservative: it trades a little GC CPU
// for headroom, never the other way around.
//
// Precedence: the native GOMEMLIMIT env var (applied by the runtime itself
// before main runs) always wins — callers must check it first. A YAML
// override beats the automatic heuristic. The heuristic prefers the cgroup
// memory limit (~80%) and otherwise uses min(45% of physical RAM, 1GiB).
package memlimit

import (
	"os"
	"strconv"
	"strings"
	"sync/atomic"
)

// minLimitBytes is the floor for any computed limit — below this the Go
// runtime itself (stacks, heaps of goroutine-local buffers, the metadata of
// a dozen recorders) would thrash the GC and threaten recording realtime.
const minLimitBytes = 64 << 20

// Path seams for tests.
var (
	cgroupV2Path = "/sys/fs/cgroup/memory.max"
	cgroupV1Path = "/sys/fs/cgroup/memory/memory.limit_in_bytes"
	meminfoPath  = "/proc/meminfo"
)

// applied stores the limit actually installed via Set (0 = none) so
// observability wiring can publish it after the fact.
var applied atomic.Int64

// ComputeLimit picks the GOMEMLIMIT value from the host situation:
//
//   - override > 0 wins (explicit operator intent);
//   - otherwise the tightest of: 80% of the cgroup limit (when set and
//     sane) and min(45% physical, 1GiB);
//   - 0 when nothing is known → caller leaves the Go default untouched.
//
// A cgroup "limit" far above physical RAM (v1 unlimited sentinel leaks
// through as a huge number on misconfigured hosts) is ignored by the
// cgroup-vs-physical min.
func ComputeLimit(physical, cgroup, override int64) int64 {
	if override > 0 {
		return max(override, minLimitBytes)
	}
	var candidates []int64
	if cgroup > 0 {
		candidates = append(candidates, cgroup*8/10)
	}
	if physical > 0 {
		candidates = append(candidates, min(physical*45/100, int64(1)<<30))
	}
	if len(candidates) == 0 {
		return 0
	}
	limit := candidates[0]
	for _, c := range candidates[1:] {
		limit = min(limit, c)
	}
	if limit < minLimitBytes {
		return minLimitBytes
	}
	return limit
}

// CgroupLimit reads the container/cgroup memory ceiling: cgroup v2
// (memory.max) preferred, v1 (memory.limit_in_bytes) fallback. "max",
// unlimited sentinels and absent files all return 0.
func CgroupLimit() int64 {
	if n := readCgroupV2(cgroupV2Path); n > 0 {
		return n
	}
	return readCgroupV1(cgroupV1Path)
}

func readCgroupV2(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(data))
	if s == "max" || s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func readCgroupV1(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	// v1 "unlimited" = PAGE_COUNTER_MAX, a value no real host could have.
	// Anything beyond 1<<48 bytes is that sentinel, not a real limit.
	if n > 1<<48 {
		return 0
	}
	return n
}

// Physical reads MemTotal from /proc/meminfo (bytes). 0 when unavailable
// (non-Linux / unusual sandbox).
func Physical() int64 {
	data, err := os.ReadFile(meminfoPath)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line[len("MemTotal:"):])
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}

// RecordApplied stores the limit that was installed (0 = none) for
// observability.
func RecordApplied(bytes int64) { applied.Store(bytes) }

// Applied returns the recorded applied limit (0 = none).
func Applied() int64 { return applied.Load() }
