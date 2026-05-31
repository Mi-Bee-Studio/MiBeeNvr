// Package engine implements a local ONNX Runtime AI provider that communicates
// with an external subprocess over stdin/stdout JSON.
package engine

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// ProbeInfo holds the results of a hardware capability probe for AI inference.
type ProbeInfo struct {
	Available   bool   `json:"available"`
	BinaryFound bool   `json:"binary_found"`
	PlatformOK  bool   `json:"platform_ok"`
	MemoryOK    bool   `json:"memory_ok"`
	CpuOK       bool   `json:"cpu_ok"`
	TotalMemory uint64 `json:"total_memory_mb"`
	CpuCores    int    `json:"cpu_cores"`
	GoArch      string `json:"go_arch"`
	Reason      string `json:"reason,omitempty"`
}

var (
	probeOnce  sync.Once
	cachedInfo *ProbeInfo
)

// ResetProbe clears the cached probe result (for testing).
func ResetProbe() {
	probeOnce = sync.Once{}
	cachedInfo = nil
}

// Probe checks system capabilities for AI inference and returns detailed info.
// Uses sync.Once for idempotent caching — subsequent calls return cached result.
// The dataDir parameter points to the application data directory where the
// ONNX Runtime binary is expected at {dataDir}/tools/onnxruntime.
func Probe(dataDir string) ProbeInfo {
	probeOnce.Do(func() {
		cachedInfo = probe(dataDir)
	})
	return *cachedInfo
}

// IsAvailable returns true if the system is capable of running AI inference.
// Shortcut for Probe(dataDir).Available.
func IsAvailable(dataDir string) bool {
	return Probe(dataDir).Available
}

// probe performs the actual hardware checks without caching.
func probe(dataDir string) *ProbeInfo {
	info := &ProbeInfo{
		GoArch:   runtime.GOARCH,
		CpuCores: runtime.NumCPU(),
	}

	// 1. Platform check: only amd64 or arm64 are supported.
	switch info.GoArch {
	case "amd64", "arm64":
		info.PlatformOK = true
	default:
		info.Reason = "unsupported architecture: " + info.GoArch + " (requires amd64 or arm64)"
		return info
	}

	// 2. Binary check: ONNX Runtime subprocess must exist and be executable.
	binPath := filepath.Join(dataDir, "tools", "onnxruntime")
	info.BinaryFound = binaryExists(binPath)
	if !info.BinaryFound {
		info.Reason = "ONNX Runtime binary not found at " + binPath
		return info
	}

	// 3. Memory check: must have at least 2 GB total RAM.
	info.TotalMemory = detectMemory()
	info.MemoryOK = info.TotalMemory >= 2048 // 2048 MB = 2 GB
	if !info.MemoryOK {
		info.Reason = "insufficient memory: " + strconv.FormatUint(info.TotalMemory, 10) + " MB (requires ≥2048 MB)"
		return info
	}

	// 4. CPU cores check: must have at least 2 cores.
	info.CpuOK = info.CpuCores >= 2
	if !info.CpuOK {
		info.Reason = "insufficient CPU cores: " + strconv.Itoa(info.CpuCores) + " (requires ≥2)"
		return info
	}

	info.Available = true
	return info
}

// binaryExists checks if a file exists and is executable by anyone.
func binaryExists(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	// Must be a regular file (not a directory) and have at least one execute bit set.
	return !fi.IsDir() && fi.Mode()&0111 != 0
}

// detectMemory reads /proc/meminfo and returns total RAM in MB.
func detectMemory() uint64 {
	return detectMemoryFromFile("/proc/meminfo")
}

// detectMemoryFromFile reads a meminfo-format file and returns total MB.
// Extracted for testability.
func detectMemoryFromFile(path string) uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("AI probe: failed to read meminfo", "path", path, "error", err)
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if kb, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
					return kb / 1024 // KB → MB
				}
			}
		}
	}
	return 0
}
