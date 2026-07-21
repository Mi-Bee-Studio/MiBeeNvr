// Package storage provides migration tools for converting legacy recordings.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// MigrateOptions configures the MJPEG→AVI migration behaviour.
type MigrateOptions struct {
	CameraID    string        // only process recordings for this camera (empty = all)
	Limit       int           // max recordings to process (0 = no limit)
	DryRun      bool          // if true, log what would be done without making changes
	Concurrency int           // max concurrent migration workers (default 1, max 4)
	Cutoff      time.Duration // recordings older than cutoff are old (default 72h)
	PurgeOld    bool          // delete old recordings instead of skipping them
	Resume      bool          // clean orphan .avi files before starting
}

// maxFramesPerRecording bounds the number of JPEG frames migrateOneRecording
// will load into the AVI muxer's in-RAM buffer. The muxer (avi.NewVideoOnlyMuxer)
// accumulates every frame in memory until segment close, so an unbounded frame
// count risks OOM on memory-constrained hosts.
//
// Production hit this: a mis-segmented recording with 24751 frames (~1.2 GB
// of JPEG data) drove the migration process to 1.4 GB RSS on a 4 GB host and
// got OOM-killed mid-run, briefly threatening the NVR service sharing the host.
// Normal recordings are ~500 frames (30s @ 15fps); even 5-minute AVI segments
// cap around 4500 frames. 10000 frames comfortably covers any legitimate
// recording while catching pathological cases.
const maxFramesPerRecording = 10000

// MigrateMJPEGToAVI migrates legacy MJPEG (jpg-directory) recordings to AVI format.
//
// Algorithm:
//  1. If Resume is true, scan camera directories for orphan .avi files not in the DB
//     and delete them (leftover .avi.tmp from a previous SIGINT are also cleaned).
//  2. If not DryRun, create a DB backup via VACUUM INTO.
//  3. Query eligible recordings: format='mjpeg' AND started_at > (now - cutoff).
//  4. For each eligible recording:
//     a. Read sorted .jpg files from the recording directory.
//     b. Extract JPEG dimensions from the first frame.
//     c. Create a .avi.tmp file and write all frames via avi.NewVideoOnlyMuxer.
//     d. Close the muxer.
//     e. Verify by demuxing and sampling first/mid/last frames for JPEG validity.
//     f. If valid: rename .avi.tmp → .avi, update DB (file_path, format, file_size),
//     remove original directory.
//     g. If invalid: delete temp file, log error, continue (never delete original).
//  5. If PurgeOld is set, delete any format='mjpeg' recordings older than cutoff.
//
// The function is interrupt-safe via ctx cancellation: the current recording finishes
// before the function returns. Leftover .avi.tmp files can be cleaned by Resume on
// the next invocation.
//
// DryRun is the safe default — pass DryRun=false to execute writes.
func MigrateMJPEGToAVI(ctx context.Context, db *DB, store *Manager, opts MigrateOptions) error {
	if opts.Concurrency < 1 {
		opts.Concurrency = 1
	}
	if opts.Concurrency > 4 {
		opts.Concurrency = 4
	}
	if opts.Cutoff <= 0 {
		opts.Cutoff = 72 * time.Hour
	}

	log := slog.With("component", "migrate")
	log.Info("starting MJPEG→AVI migration",
		"dry_run", opts.DryRun,
		"concurrency", opts.Concurrency,
		"cutoff", opts.Cutoff,
		"purge_old", opts.PurgeOld,
		"resume", opts.Resume,
		"camera", optionalString(opts.CameraID, "all"),
	)

	// Step 1: Resume — clean orphan .avi files
	if opts.Resume {
		if err := cleanOrphanAVIs(ctx, db, store, log); err != nil {
			return fmt.Errorf("resume cleanup: %w", err)
		}
	}

	// Build filter for eligible recordings (newer than cutoff).
	cutoffTime := time.Now().Add(-opts.Cutoff)
	filter := model.RecordingFilter{
		Format:    model.FormatMJPEG,
		StartTime: cutoffTime,
	}
	if opts.CameraID != "" {
		filter.CameraID = opts.CameraID
	}
	if opts.Limit > 0 {
		filter.Limit = opts.Limit
	}

	eligible, err := db.ListRecordings(ctx, filter)
	if err != nil {
		return fmt.Errorf("query eligible recordings: %w", err)
	}

	if len(eligible) == 0 {
		log.Info("no eligible MJPEG recordings found")
	} else {
		log.Info("found eligible recordings", "count", len(eligible))
	}

	// Step 5: PurgeOld — delete old MJPEG recordings before migration.
	if opts.PurgeOld {
		purgeFilter := model.RecordingFilter{
			Format:  model.FormatMJPEG,
			EndTime: cutoffTime,
		}
		if opts.CameraID != "" {
			purgeFilter.CameraID = opts.CameraID
		}

		oldRecs, err := db.ListRecordings(ctx, purgeFilter)
		if err != nil {
			return fmt.Errorf("query old recordings for purge: %w", err)
		}

		if len(oldRecs) == 0 {
			log.Info("no old recordings to purge")
		} else {
			log.Info("purging old recordings", "count", len(oldRecs))
			for _, rec := range oldRecs {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if opts.DryRun {
					log.Info("[dry-run] would purge recording",
						"id", rec.ID, "camera", rec.CameraID,
						"path", rec.FilePath, "started", rec.StartedAt,
					)
					continue
				}
				if err := purgeOneRecording(ctx, db, store, rec, log); err != nil {
					log.Warn("failed to purge recording", "id", rec.ID, "error", err)
				}
			}
		}
	}

	if opts.DryRun {
		log.Info("dry-run complete — no changes made")
		return nil
	}

	// Step 2: DB backup before any writes.
	// Remove any stale backup from a prior run first — VACUUM INTO fails if
	// the destination file already exists, which would block repeat invocations
	// (common when a previous migration was interrupted and re-run).
	backupPath := db.path + ".migrate-backup"
	if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale backup: %w", err)
	}
	if err := db.Backup(ctx, backupPath); err != nil {
		return fmt.Errorf("db backup: %w", err)
	}
	log.Info("database backed up", "path", backupPath)

	// Steps 3-4: migrate eligible recordings.
	if len(eligible) == 0 {
		return nil
	}

	// Process with bounded concurrency.
	sem := make(chan struct{}, opts.Concurrency)
	var wg sync.WaitGroup
	results := make(chan migrateResult, len(eligible))

	for _, rec := range eligible {
		select {
		case <-ctx.Done():
			// Wait for in-flight workers, then return.
			wg.Wait()
			close(results)
			return ctx.Err()
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(r model.Recording) {
			defer wg.Done()
			defer func() { <-sem }()

			err := migrateOneRecording(ctx, db, store, r, log)
			results <- migrateResult{id: r.ID, err: err}
		}(rec)
	}

	// Wait for all workers and close results channel.
	wg.Wait()
	close(results)

	var migrated, failed, skipped int
	for res := range results {
		if res.err == nil {
			migrated++
		} else if errors.Is(res.err, errSkipped) {
			skipped++
		} else {
			failed++
			log.Warn("migration failed", "id", res.id, "error", res.err)
		}
	}

	log.Info("MJPEG→AVI migration complete",
		"migrated", migrated,
		"failed", failed,
		"skipped", skipped,
	)

	return nil
}

// migrateResult carries the outcome of a single recording migration.
type migrateResult struct {
	id  string
	err error
}

// errSkipped is returned when a recording is skipped (not migrated, not failed).
var errSkipped = fmt.Errorf("skipped")

// migrateOneRecording converts a single MJPEG directory recording to AVI.
func migrateOneRecording(ctx context.Context, db *DB, store *Manager, rec model.Recording, log *slog.Logger) error {
	// Sanity check: recording must have a directory path (no .avi extension).
	if filepath.Ext(rec.FilePath) == ".avi" {
		return errSkipped
	}

	// Check that the source directory still exists.
	info, err := os.Stat(rec.FilePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Warn("recording directory missing, skipping", "id", rec.ID, "path", rec.FilePath)
			return errSkipped
		}
		return fmt.Errorf("stat source dir: %w", err)
	}
	if !info.IsDir() {
		log.Warn("recording path is not a directory, skipping", "id", rec.ID, "path", rec.FilePath)
		return errSkipped
	}

	// Read and sort .jpg files from the directory.
	entries, err := os.ReadDir(rec.FilePath)
	if err != nil {
		return fmt.Errorf("read recording dir: %w", err)
	}

	if len(entries) == 0 {
		log.Warn("recording directory empty, skipping", "id", rec.ID, "path", rec.FilePath)
		return errSkipped
	}

	// Filter for .jpg files and sort by name (which is chronological).
	var jpgFiles []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".jpg" {
			jpgFiles = append(jpgFiles, filepath.Join(rec.FilePath, e.Name()))
		}
	}

	if len(jpgFiles) == 0 {
		log.Warn("recording has no .jpg files, skipping", "id", rec.ID, "path", rec.FilePath)
		return errSkipped
	}

	// Bound the frame count to protect the AVI muxer's in-RAM buffer from
	// pathological recordings (e.g. a mis-segmented clip with tens of thousands
	// of frames). The muxer holds every frame in memory until close, so without
	// this cap a single huge recording can OOM the migration process — and on
	// a shared host, take the NVR service down with it. See maxFramesPerRecording
	// for the threshold rationale.
	if len(jpgFiles) > maxFramesPerRecording {
		log.Warn("recording has too many frames, skipping (would OOM AVI muxer)",
			"id", rec.ID, "path", rec.FilePath,
			"frame_count", len(jpgFiles), "limit", maxFramesPerRecording)
		return errSkipped
	}

	sort.Strings(jpgFiles)

	// Read the first frame to get JPEG dimensions.
	firstFrame, err := os.ReadFile(jpgFiles[0])
	if err != nil {
		return fmt.Errorf("read first frame: %w", err)
	}

	width, height, ok := jpegDimensions(firstFrame)
	if !ok {
		return fmt.Errorf("cannot determine JPEG dimensions from first frame")
	}

	if width <= 0 || height <= 0 {
		return fmt.Errorf("invalid JPEG dimensions: %dx%d", width, height)
	}

	// Build the output .avi path: original path + .avi
	cameraDir := filepath.Dir(rec.FilePath)
	recordingBase := filepath.Base(rec.FilePath)
	aviPath := filepath.Join(cameraDir, recordingBase+".avi")

	// Use a unique temp path for the in-progress file.
	tmpAVI := aviPath + ".tmp"

	// Save original path before updating rec.FilePath.
	originalPath := rec.FilePath

	cleanupTemp := true

	// Ensure we clean up temp file if we error out.
	defer func() {
		if cleanupTemp {
			os.Remove(tmpAVI)
		}
	}()

	// Create temp file for AVI output.
	f, err := os.Create(tmpAVI)
	if err != nil {
		return fmt.Errorf("create temp avi: %w", err)
	}

	mux := avi.NewVideoOnlyMuxer(f, width, height)
	frameCount := 0
	const ptsPerFrame = 33333 // ~30 fps

	for i, jpgPath := range jpgFiles {
		if i%100 == 0 {
			// Check for cancellation every 100 frames.
			if err := ctx.Err(); err != nil {
				f.Close()
				return fmt.Errorf("cancelled after %d frames: %w", i, err)
			}
		}

		frame, err := os.ReadFile(jpgPath)
		if err != nil {
			f.Close()
			return fmt.Errorf("read frame %d (%s): %w", i, jpgPath, err)
		}

		pts := int64(i) * ptsPerFrame
		if err := mux.WriteVideo(frame, pts); err != nil {
			f.Close()
			return fmt.Errorf("write video frame %d: %w", i, err)
		}
		frameCount++
	}

	// Close the muxer (flushes to the underlying writer).
	if err := mux.Close(); err != nil {
		f.Close()
		return fmt.Errorf("close avi muxer: %w", err)
	}

	// Get the file size before closing.
	aviFileSize, err := getFileSize(tmpAVI)
	if err != nil {
		f.Close()
		return fmt.Errorf("stat temp avi: %w", err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("close avi file: %w", err)
	}

	// Step 4e: Verify the AVI file by demuxing and sampling frames.
	if err := verifyAVI(tmpAVI); err != nil {
		// Verification failed — delete temp file, log error, keep original.
		cleanupTemp = true
		return fmt.Errorf("avi verification failed: %w", err)
	}

	// Step 4f: All good — atomic rename .avi.tmp → .avi.
	if err := os.Rename(tmpAVI, aviPath); err != nil {
		return fmt.Errorf("rename avi: %w", err)
	}
	cleanupTemp = false // temp file is now the .avi file.

	// Update DB record.
	rec.FilePath = aviPath
	rec.Format = model.FormatAVI
	rec.FileSize = aviFileSize
	if err := db.UpdateRecording(ctx, &rec); err != nil {
		return fmt.Errorf("update db: %w", err)
	}

	// Remove original MJPEG directory.
	if err := store.DeleteFile(originalPath); err != nil {
		// Non-fatal: AVI file exists, DB updated, just log warning.
		log.Warn("failed to remove original MJPEG directory", "path", originalPath, "error", err)
	} else {
		log.Debug("removed original MJPEG directory", "path", originalPath)
	}

	log.Info("migrated recording", "id", rec.ID, "camera", rec.CameraID,
		"frames", frameCount, "size", aviFileSize, "avi", aviPath)

	return nil
}

// purgeOneRecording deletes a recording from DB and removes its file/dir from disk.
func purgeOneRecording(ctx context.Context, db *DB, store *Manager, rec model.Recording, log *slog.Logger) error {
	// Delete from DB first so if we crash after, the file is orphaned but not
	// incorrectly referenced.
	if err := db.DeleteRecording(ctx, rec.ID); err != nil {
		return fmt.Errorf("delete recording %s from db: %w", rec.ID, err)
	}

	// Remove file/dir from disk.
	if err := store.DeleteFile(rec.FilePath); err != nil {
		if !os.IsNotExist(err) {
			log.Warn("purge: file deletion warning", "id", rec.ID, "path", rec.FilePath, "error", err)
		}
	}

	log.Info("purged old recording", "id", rec.ID, "camera", rec.CameraID, "path", rec.FilePath)
	return nil
}

// cleanOrphanAVIs scans camera directories for .avi files not registered in the DB
// and removes them. Also cleans up leftover .avi.tmp files.
func cleanOrphanAVIs(ctx context.Context, db *DB, store *Manager, log *slog.Logger) error {
	entries, err := os.ReadDir(store.RootDir())
	if err != nil {
		return fmt.Errorf("read root dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		cameraID := entry.Name()
		cameraDir := filepath.Join(store.RootDir(), cameraID)

		dirEntries, err := os.ReadDir(cameraDir)
		if err != nil {
			log.Warn("cannot read camera dir", "dir", cameraDir, "error", err)
			continue
		}

		for _, de := range dirEntries {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			name := de.Name()

			// Clean .avi.tmp files regardless of DB state.
			if stringsHasSuffix(name, ".avi.tmp") {
				path := filepath.Join(cameraDir, name)
				if err := os.Remove(path); err != nil {
					log.Warn("cannot remove .avi.tmp", "path", path, "error", err)
				} else {
					log.Info("removed orphan .avi.tmp", "path", path)
				}
				continue
			}
			// Check .avi files against DB via GetRecordingsByPathSet.
			if stringsHasSuffix(name, ".avi") {
				aviPath := filepath.Join(cameraDir, name)

				exists, err := db.GetRecordingsByPathSet(ctx, []string{aviPath})
				if err != nil {
					log.Warn("cannot query db for path", "path", aviPath, "error", err)
					continue
				}
				if !exists[aviPath] {
					// Not in DB — orphan.
					if err := os.Remove(aviPath); err != nil {
						log.Warn("cannot remove orphan .avi", "path", aviPath, "error", err)
					} else {
						log.Info("removed orphan .avi", "path", aviPath)
					}
				}
			}
		}
	}

	return nil
}

// verifyAVI opens an AVI file with the demuxer, reads all video chunks,
// and samples the first, middle, and last frame to verify they are valid JPEG
// (starting with FF D8 and ending with FF D9).
func verifyAVI(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open avi: %w", err)
	}
	defer f.Close()

	d, err := avi.NewDemuxer(f)
	if err != nil {
		return fmt.Errorf("avi header: %w", err)
	}

	var (
		firstData []byte
		midData   []byte
		lastData  []byte
		count     int
		midStored int = -1
	)

	for {
		ck, err := d.NextChunk()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("avi read: %w", err)
		}
		if ck.Type != avi.ChunkVideo {
			continue
		}

		count++
		if count == 1 {
			firstData = ck.Data
		}

		// Track middle frame: when count crosses a new midpoint floor(count/2),
		// store the current frame.
		midIndex := count / 2
		if midStored < midIndex {
			midData = ck.Data
			midStored = midIndex
		}

		lastData = ck.Data
	}

	if count == 0 {
		return fmt.Errorf("no video chunks found")
	}

	// Verify sampled frames.
	if !validJPEG(firstData) {
		return fmt.Errorf("first frame (%d bytes) is not valid JPEG (missing SOI/EOI)", len(firstData))
	}
	if !validJPEG(midData) {
		return fmt.Errorf("middle frame (index %d, %d bytes) is not valid JPEG (missing SOI/EOI)", count/2, len(midData))
	}
	if count > 1 && !validJPEG(lastData) {
		return fmt.Errorf("last frame (index %d, %d bytes) is not valid JPEG (missing SOI/EOI)", count-1, len(lastData))
	}

	return nil
}

// validJPEG checks that data starts with FF D8 (SOI) and ends with FF D9 (EOI).
func validJPEG(data []byte) bool {
	return len(data) >= 4 &&
		data[0] == 0xFF && data[1] == 0xD8 &&
		data[len(data)-2] == 0xFF && data[len(data)-1] == 0xD9
}

// jpegDimensions extracts image dimensions from raw JPEG data by scanning
// for the SOF0 (0xC0), SOF1 (0xC1), or SOF2 (0xC2) marker.
func jpegDimensions(data []byte) (width, height int, ok bool) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 0, 0, false
	}
	idx := 2
	for idx < len(data)-1 {
		if data[idx] != 0xFF {
			return 0, 0, false
		}
		marker := data[idx+1]
		if marker == 0xC0 || marker == 0xC1 || marker == 0xC2 {
			if idx+9 >= len(data) {
				return 0, 0, false
			}
			height = int(data[idx+5])<<8 | int(data[idx+6])
			width = int(data[idx+7])<<8 | int(data[idx+8])
			return width, height, true
		}
		if marker == 0xD9 || marker == 0xDA {
			return 0, 0, false
		}
		if marker == 0xFF || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 {
			idx += 2
		} else {
			if idx+3 >= len(data) {
				return 0, 0, false
			}
			segLen := int(data[idx+2])<<8 | int(data[idx+3])
			if segLen < 2 {
				return 0, 0, false
			}
			idx += 2 + segLen
		}
	}
	return 0, 0, false
}

// getFileSize is a small wrapper for testing purposes.
var getFileSize = func(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// stringsHasSuffix is a wrapper for testability (same as strings.HasSuffix).
var stringsHasSuffix = func(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

// optionalString returns value if non-empty, otherwise fallback.
func optionalString(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
