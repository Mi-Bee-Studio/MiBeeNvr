package timelapse

import (
	"log/slog"
	"sync"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
)

var (
	mu         sync.Mutex
	cachedTier MergeTier
	cached     bool
)

// DetectMergeTier probes the system and selects the best merge implementation tier.
// Results are cached — only one probe per app lifecycle unless ResetDetectTier is called.
// Pass ffmpegPath to proactively check a specific FFmpeg binary path,
// or "" to let the probe search the system PATH.
// preferFFmpeg=true opts into the FFmpeg tier when available (default false → pure Go).
func DetectMergeTier(ffmpegPath string, preferFFmpeg ...bool) MergeTier {
	mu.Lock()
	if cached {
		defer mu.Unlock()
		return cachedTier
	}
	mu.Unlock()

	optIn := len(preferFFmpeg) > 0 && preferFFmpeg[0]
	caps := transcoding.ProbeHardwareCapabilities(ffmpegPath)
	tier := selectTier(caps, optIn)

	mu.Lock()
	defer mu.Unlock()
	if !cached {
		cachedTier = tier
		cached = true
		slog.Info(
			"Merge tier detected",
			"tier", cachedTier,
			"reason", tierReason(cachedTier, caps),
		)
	}
	return cachedTier
}

// tierReason returns a human-readable reason for the selected tier.
func tierReason(tier MergeTier, caps *transcoding.HardwareCapabilities) string {
	switch tier {
	case TierFFmpeg:
		return "FFmpeg explicitly enabled (use_ffmpeg=true)"
	case TierGo:
		if caps.FFmpegAvailable {
			return "Go merge (default; FFmpeg available but not preferred)"
		}
		return "sufficient resources for Go merge"
	case TierJPEG:
		return "insufficient resources, using JPEG fallback"
	default:
		return "unknown"
	}
}

// selectTier selects the merge tier from hardware capabilities.
//
// Default is TierGo (pure-Go, no external dependency). FFmpeg (TierFFmpeg) is
// only selected when the caller explicitly opts in via preferFFmpeg=true — this
// keeps FFmpeg as an opt-in accelerator rather than a default dependency, so
// systems without FFmpeg installed produce identical timelapse output.
// Exported for testing — consumers should use DetectMergeTier.
func selectTier(caps *transcoding.HardwareCapabilities, preferFFmpeg bool) MergeTier {
	if preferFFmpeg && caps.FFmpegAvailable {
		return TierFFmpeg
	}
	if caps.TotalCores >= 2 && caps.TotalMemoryMB >= 100 {
		return TierGo
	}
	return TierJPEG
}

// AvailableMergeTier returns the cached merge tier.
// Returns the zero value ("") if DetectMergeTier has not been called yet.
func AvailableMergeTier() MergeTier {
	mu.Lock()
	defer mu.Unlock()
	return cachedTier
}

// IsMergeAvailable returns true if the cached merge tier is better than JPEG.
// Returns false if DetectMergeTier has not been called yet.
func IsMergeAvailable() bool {
	mu.Lock()
	defer mu.Unlock()
	return cachedTier != TierJPEG && cachedTier != ""
}

// ResetDetectTier clears the cached detection result.
func ResetDetectTier() {
	mu.Lock()
	defer mu.Unlock()
	cached = false
	cachedTier = ""
}

// setDetectTier sets the cached detection tier (for testing).
func setDetectTier(tier MergeTier) {
	mu.Lock()
	defer mu.Unlock()
	cachedTier = tier
	cached = true
}
