package timelapse

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
)

func TestDetectTierFFmpeg(t *testing.T) {
	tier := selectTier(&transcoding.HardwareCapabilities{
		FFmpegAvailable: true,
		FFmpegPath:      "/usr/bin/ffmpeg",
	})
	if tier != TierFFmpeg {
		t.Errorf("selectTier with FFmpegAvailable=true: expected TierFFmpeg, got %q", tier)
	}
}

func TestDetectTierGo(t *testing.T) {
	tier := selectTier(&transcoding.HardwareCapabilities{
		FFmpegAvailable: false,
		TotalCores:      4,
		TotalMemoryMB:   512,
	})
	if tier != TierGo {
		t.Errorf("selectTier with 4 cores, 512MB: expected TierGo, got %q", tier)
	}
}

func TestDetectTierJPEG(t *testing.T) {
	t.Run("low CPU", func(t *testing.T) {
		tier := selectTier(&transcoding.HardwareCapabilities{
			FFmpegAvailable: false,
			TotalCores:      1,
			TotalMemoryMB:   4096,
		})
		if tier != TierJPEG {
			t.Errorf("selectTier with 1 core: expected TierJPEG, got %q", tier)
		}
	})

	t.Run("low memory", func(t *testing.T) {
		tier := selectTier(&transcoding.HardwareCapabilities{
			FFmpegAvailable: false,
			TotalCores:      4,
			TotalMemoryMB:   50,
		})
		if tier != TierJPEG {
			t.Errorf("selectTier with 50MB RAM: expected TierJPEG, got %q", tier)
		}
	})
}

func TestDetectTierGo_BareMinimum(t *testing.T) {
	tier := selectTier(&transcoding.HardwareCapabilities{
		FFmpegAvailable: false,
		TotalCores:      2,
		TotalMemoryMB:   100,
	})
	if tier != TierGo {
		t.Errorf("selectTier with 2 cores, 100MB: expected TierGo, got %q", tier)
	}
}

func TestDetectMergeTier(t *testing.T) {
	t.Run("FFmpeg available via real binary", func(t *testing.T) {
		transcoding.ResetProbe()
		ResetDetectTier()

		tmpDir := t.TempDir()
		ffmpegPath := filepath.Join(tmpDir, "ffmpeg")
		if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\necho fake"), 0755); err != nil {
			t.Fatal(err)
		}

		tier := DetectMergeTier(ffmpegPath)
		if tier != TierFFmpeg {
			t.Errorf("DetectMergeTier with temp ffmpeg: expected TierFFmpeg, got %q", tier)
		}
	})

	t.Run("caching - second call returns same cached result", func(t *testing.T) {
		transcoding.ResetProbe()
		ResetDetectTier()

		tier1 := DetectMergeTier("/nonexistent/ffmpeg")
		// Second call with different path should still return cached result
		// (detectOnce prevents re-execution)
		tier2 := DetectMergeTier("/another/nonexistent/path")

		if tier1 != tier2 {
			t.Errorf("cached tier mismatch: first=%q, second=%q", tier1, tier2)
		}
	})

	t.Run("AvailableMergeTier returns empty before detection", func(t *testing.T) {
		ResetDetectTier()
		if got := AvailableMergeTier(); got != "" {
			t.Errorf("expected empty value before detection, got %q", got)
		}
	})

	t.Run("IsMergeAvailable returns false before detection", func(t *testing.T) {
		ResetDetectTier()
		if IsMergeAvailable() {
			t.Error("expected IsMergeAvailable() == false before detection")
		}
	})

	t.Run("IsMergeAvailable returns false for TierJPEG", func(t *testing.T) {
		ResetDetectTier()
		detectOnce.Do(func() {
			cachedTier = TierJPEG
		})
		if IsMergeAvailable() {
			t.Error("expected IsMergeAvailable() == false for TierJPEG")
		}
	})

	t.Run("IsMergeAvailable returns true for TierFFmpeg", func(t *testing.T) {
		ResetDetectTier()
		detectOnce.Do(func() {
			cachedTier = TierFFmpeg
		})
		if !IsMergeAvailable() {
			t.Error("expected IsMergeAvailable() == true for TierFFmpeg")
		}
	})

	t.Run("IsMergeAvailable returns true for TierGo", func(t *testing.T) {
		ResetDetectTier()
		detectOnce.Do(func() {
			cachedTier = TierGo
		})
		if !IsMergeAvailable() {
			t.Error("expected IsMergeAvailable() == true for TierGo")
		}
	})
}
