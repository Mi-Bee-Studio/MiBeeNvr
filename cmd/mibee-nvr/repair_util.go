package main

// repair.go — `mibee-nvr repair` CLI: on-demand data repair for operational issues
// that don't belong in the hot path of the long-running server.
//
// Subcommands:
//   repair duration     — re-probe video files to fix recordings stuck at duration=0
//   repair merge-status — reset merge_status for recordings whose merged file is missing
//
// Both mirror the migrate-mjpeg CLI shape (--dry-run default, --execute to apply,
// --config / --camera / --limit). They open their own DB connection from the config
// and are safe to run while the server is stopped (preferred) or running (WAL mode
// allows concurrent readers, but prefer stopping the server for large repairs).

import (
	"fmt"
	"os"
)

// humanBytes formats a byte count as a human-readable string (KB/MB/GB).
func humanBytes(b int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.2f GB", float64(b)/GB)
	case b >= MB:
		return fmt.Sprintf("%.2f MB", float64(b)/MB)
	case b >= KB:
		return fmt.Sprintf("%.2f KB", float64(b)/KB)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// splitCSV splits a comma-separated string, trimming whitespace and dropping empties.

// splitCSV splits a comma-separated string, trimming whitespace and dropping empties.
func splitCSV(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			field := s[start:i]
			// trim spaces
			for len(field) > 0 && (field[0] == ' ' || field[0] == '\t') {
				field = field[1:]
			}
			for len(field) > 0 && (field[len(field)-1] == ' ' || field[len(field)-1] == '\t') {
				field = field[:len(field)-1]
			}
			if field != "" {
				out = append(out, field)
			}
			start = i + 1
		}
	}
	return out
}

// estimateMJpegDirDuration estimates the duration of an MJPEG frame-directory
// recording. ESP32 MiBeeCam stores JPEG frames in a directory (not a single MP4).
// We count .jpg/.jpeg files in the directory and multiply by a nominal frame
// interval. If frame_count is recorded in the DB (non-zero), we use that instead
// of counting files (faster).
//
// The frame interval defaults to 0.5s (2fps — typical for ESP32 MiBeeCam at
// the configured segment duration). This is an estimate, not exact, but far
// better than 0 for timeline display purposes.
const mjpegNominalFrameInterval = 0.5 // seconds per frame (2fps)

func estimateMJpegDirDuration(filePath string, frameCount int) (float64, error) {
	// If frame_count is recorded and > 0, use it (avoids directory scan).
	if frameCount > 0 {
		return float64(frameCount) * mjpegNominalFrameInterval, nil
	}
	// Otherwise count JPEG files in the directory.
	entries, err := os.ReadDir(filePath)
	if err != nil {
		return 0, fmt.Errorf("read mjpeg dir: %w", err)
	}
	count := 0
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && (hasSuffix(name, ".jpg") || hasSuffix(name, ".jpeg")) {
			count++
		}
	}
	if count == 0 {
		return 0, fmt.Errorf("no jpeg frames in %s", filePath)
	}
	return float64(count) * mjpegNominalFrameInterval, nil
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

// ---------------------------------------------------------------------------
// Usage
// ---------------------------------------------------------------------------

// parseInt is a small helper to avoid importing strconv just for one call.
func parseInt(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}
