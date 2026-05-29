package transcoding

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// DBTaskLister abstracts the database operations needed for orphan cleanup.
type DBTaskLister interface {
	ListTranscodeTasks(ctx context.Context, f storage.TranscodeTaskFilter) ([]storage.TranscodeTask, int, error)
}

// TranscodeCleaner handles post-transcode verification and cleanup.
// It verifies the transcoded output file, then either replaces the original
// (delete original, keep output) or keeps both files.
type TranscodeCleaner struct {
	ffprobePath string
}

// NewTranscodeCleaner creates a cleaner that uses the given ffprobe binary path.
// If ffprobePath is empty, "ffprobe" is used (must be on PATH).
func NewTranscodeCleaner(ffprobePath string) *TranscodeCleaner {
	return &TranscodeCleaner{ffprobePath: ffprobePath}
}

// VerifyAndClean verifies the transcoded output and performs cleanup.
//
// When replaceOriginal is true:
//   - Original file/directory is deleted after successful verification
//   - The caller should update the recording's file_path and format in the DB
//
// When replaceOriginal is false:
//   - Original file/directory is left untouched
//   - The caller should create a new recording entry for the output file
//
// On verification failure, the partial output file is cleaned up (rollback).
func (c *TranscodeCleaner) VerifyAndClean(
	ctx context.Context,
	inputPath string,
	outputPath string,
	inputFormat string,
	replaceOriginal bool,
) error {
	// Verify output first — never delete original until output is confirmed valid.
	if err := c.verifyOutput(outputPath, inputPath); err != nil {
		// Rollback: remove partial/invalid output.
		c.RollbackFailedTranscode(outputPath)
		return fmt.Errorf("verify output: %w", err)
	}

	if !replaceOriginal {
		return nil
	}

	// Replace mode: delete original file or directory.
	if err := c.replaceOriginal(inputPath, inputFormat); err != nil {
		return fmt.Errorf("replace original: %w", err)
	}

	return nil
}

// verifyOutput checks that the transcoded file is valid:
//  1. File exists and has non-zero size
//  2. FFprobe validation passes (has a video stream)
//  3. Output duration matches input duration within ±5% tolerance
func (c *TranscodeCleaner) verifyOutput(outputPath, inputPath string) error {
	// 1. File exists and is non-empty.
	info, err := os.Stat(outputPath)
	if err != nil {
		return fmt.Errorf("output file missing: %w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("output file is empty")
	}

	// 2. FFprobe validation.
	if err := ValidateOutput(c.ffprobePath, outputPath); err != nil {
		return fmt.Errorf("ffprobe validation: %w", err)
	}

	// 3. Duration check — only if we can probe both files.
	inputMedia, errIn := GetMediaInfo(c.ffprobePath, inputPath)
	outputMedia, errOut := GetMediaInfo(c.ffprobePath, outputPath)

	if errIn == nil && errOut == nil && inputMedia.Duration > 0 {
		ratio := outputMedia.Duration / inputMedia.Duration
		if ratio < 0.95 || ratio > 1.05 {
			return fmt.Errorf(
				"duration mismatch: input=%.1fs output=%.1fs (%.0f%%)",
				inputMedia.Duration, outputMedia.Duration, ratio*100,
			)
		}
	}

	return nil
}

// replaceOriginal deletes the original file or directory.
// For MJPEG format, inputPath is a directory of JPEG frames → use RemoveAll.
// For H.264/H.265, inputPath is a single MP4 file → use Remove.
func (c *TranscodeCleaner) replaceOriginal(inputPath, inputFormat string) error {
	if inputFormat == "mjpeg" {
		if err := os.RemoveAll(inputPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove MJPEG directory %q: %w", inputPath, err)
		}
		return nil
	}
	// Single file (h264, h265, etc.).
	if err := os.Remove(inputPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove original file %q: %w", inputPath, err)
	}
	return nil
}

// RollbackFailedTranscode removes a partial or invalid transcode output file.
// Errors are logged as warnings; missing files are silently ignored.
func (c *TranscodeCleaner) RollbackFailedTranscode(outputPath string) {
	if err := os.Remove(outputPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to cleanup partial transcode output",
			"path", outputPath, "error", err)
	}
}

// CheckDiskSpaceForTranscode verifies the output filesystem has enough free
// space to hold the transcoded output. The safety factor multiplies the input
// size to account for possible output expansion.
// Returns nil if sufficient space is available or if the input size cannot be
// determined (e.g., MJPEG directory).
func CheckDiskSpaceForTranscode(inputPath, outputDir string, safetyFactor float64) error {
	if safetyFactor <= 0 {
		safetyFactor = 2.0
	}

	inputSize, err := dirSize(inputPath)
	if err != nil {
		// Cannot determine input size — skip check.
		return nil
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(outputDir, &stat); err != nil {
		return fmt.Errorf("statfs %s: %w", outputDir, err)
	}

	freeBytes := stat.Bavail * uint64(stat.Bsize)
	required := uint64(float64(inputSize) * safetyFactor)

	if freeBytes < required {
		return fmt.Errorf("insufficient disk space: need %d bytes (%.1fx safety), have %d bytes free",
			required, safetyFactor, freeBytes)
	}

	return nil
}

// dirSize returns the total size of a file or directory in bytes.
func dirSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return info.Size(), nil
	}
	var size int64
	err = filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			size += fi.Size()
		}
		return nil
	})
	return size, err
}

// CleanOrphanedTranscodes walks dataDir for files matching *.transcoded.mp4 and
// deletes any that do not have a corresponding task in the database.
// This handles crash recovery: orphaned output files left from tasks that were
// never recorded in DB (e.g., process died mid-enqueue) or whose tasks were
// cleaned up by DeleteCompletedTasks.
func CleanOrphanedTranscodes(ctx context.Context, dataDir string, db DBTaskLister) error {
	// Build set of all known output paths from DB
	tasks, _, err := db.ListTranscodeTasks(ctx, storage.TranscodeTaskFilter{Limit: 200})
	if err != nil {
		return fmt.Errorf("list transcode tasks: %w", err)
	}
	activePaths := make(map[string]struct{}, len(tasks))
	for _, t := range tasks {
		if t.OutputPath != "" {
			activePaths[t.OutputPath] = struct{}{}
		}
	}

	// Walk dataDir for orphaned .transcoded.mp4 files
	var deleted int
	err = filepath.WalkDir(dataDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".transcoded.mp4") {
			return nil
		}

		// Check if context cancelled
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if _, ok := activePaths[path]; ok {
			return nil // has active task, keep it
		}

		// Orphaned — delete it
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.Warn("failed to delete orphaned transcoded file", "path", path, "error", err)
			return nil // non-fatal, continue walking
		}
		slog.Info("deleted orphaned transcoded file", "path", path)
		deleted++
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk data dir: %w", err)
	}

	if deleted > 0 {
		slog.Info("cleaned up orphaned transcoded files", "count", deleted)
	}
	return nil
}
