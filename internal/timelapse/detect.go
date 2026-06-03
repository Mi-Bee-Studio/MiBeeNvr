package timelapse

import (
	"log/slog"
	"sync"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
)

var (
	detectOnce sync.Once
	cachedTier MergeTier
)

// DetectMergeTier probes the system and selects the best merge implementation tier.
// Results are cached with sync.Once — only one probe per app lifecycle.
// Pass ffmpegPath to proactively check a specific FFmpeg binary path,
// or "" to let the probe search the system PATH.
func DetectMergeTier(ffmpegPath string) MergeTier {
	detectOnce.Do(func() {
		caps := transcoding.ProbeHardwareCapabilities(ffmpegPath)
		cachedTier = selectTier(caps)
		slog.Info("Merge tier detected",
			"tier", cachedTier,
			"reason", tierReason(cachedTier, caps),
		)
	})
	return cachedTier
}

// tierReason returns a human-readable reason for the selected tier.
func tierReason(tier MergeTier, caps *transcoding.HardwareCapabilities) string {
	switch tier {
	case TierFFmpeg:
		return "FFmpeg available"
	case TierGo:
		return "sufficient resources for Go merge"
	case TierJPEG:
		return "insufficient resources, using JPEG fallback"
	default:
		return "unknown"
	}
}

// selectTier selects the merge tier from hardware capabilities.
// Exported for testing — consumers should use DetectMergeTier.
func selectTier(caps *transcoding.HardwareCapabilities) MergeTier {
	if caps.FFmpegAvailable {
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
	return cachedTier
}

// IsMergeAvailable returns true if the cached merge tier is better than JPEG.
// Returns false if DetectMergeTier has not been called yet.
func IsMergeAvailable() bool {
	return cachedTier != TierJPEG && cachedTier != ""
}

// ResetDetectTier clears the cached detection result (for testing).
func ResetDetectTier() {
	detectOnce = sync.Once{}
	cachedTier = ""
}
