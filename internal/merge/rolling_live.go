package merge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// eventLoop drains SegmentCompleted events and dispatches per-camera merge goroutines.
// It applies debounce per camera: rapid segment closes (e.g. short segment dur with
// reconnects) get batched into a single merge.
//
// All wg.Add calls for the mergeSegments fan-out happen inside this goroutine (in the
// fireCh select case), so once ctx is cancelled the loop exits via ctx.Done and no
// further Add can race with a concurrent Stop's Wait (which would violate sync.WaitGroup's
// "Add with positive delta must happen before Wait when counter is zero" rule).
// The debounce timers only send a non-blocking signal on fireCh; they never touch wg.
func (r *RollingMergeCoordinator) eventLoop(ctx context.Context) {
	defer r.wg.Done() // paired with r.wg.Add(3) in Start
	pendingMu := sync.Mutex{}
	pending := make(map[string][]pendingSegmentInfo) // cameraID → queued segments
	timers := make(map[string]*time.Timer)

	// fireCh carries cameraIDs whose debounce timer elapsed. Buffered so a timer
	// callback virtually never blocks; the per-camera dedup in pending (delete on
	// dispatch) keeps semantics correct if multiple signals queue. Dispatch is
	// non-blocking (only wg.Add + go), so drain is fast and the buffer is rarely
	// more than lightly populated.
	fireCh := make(chan string, 256)

	dispatch := func(cameraID string) {
		pendingMu.Lock()
		segs := pending[cameraID]
		delete(pending, cameraID)
		delete(timers, cameraID)
		pendingMu.Unlock()

		if len(segs) == 0 {
			return
		}

		// Launch async merge — never block the event loop. Tracked by wg so Stop
		// waits for in-flight merges too (prevents merge goroutines from outliving
		// t.TempDir cleanup — issue #143).
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.mergeSegments(ctx, cameraID, segs)
		}()
	}

	for {
		select {
		case <-ctx.Done():
			pendingMu.Lock()
			for _, t := range timers {
				t.Stop()
			}
			pendingMu.Unlock()
			return

		case cameraID := <-fireCh:
			dispatch(cameraID)

		case evt := <-r.eventCh:
			sc, ok := evt.Data.(event.SegmentCompleted)
			if !ok {
				continue
			}

			// Check if rolling merge is enabled for this camera.
			cfg := r.resolveRollingConfig(sc.CameraID)
			if !cfg.Enabled {
				continue
			}

			// Skip timelapse format — it has its own merge pipeline (timelapse package).
			// All other formats (h264, h265, avi, mjpeg) are handled by rolling merge.
			if sc.Format == string(model.FormatTimelapse) {
				continue
			}

			// Parse the segment's startedAt time for window calculation.
			startedAt, err := time.Parse(time.RFC3339Nano, sc.StartedAt)
			if err != nil {
				// Fallback: use now.
				startedAt = time.Now()
			}

			seg := pendingSegmentInfo{
				recordingID: sc.RecordingID,
				filePath:    sc.FilePath,
				format:      sc.Format,
				cameraID:    sc.CameraID,
				startedAt:   startedAt,
				endedAt:     time.Now(),
				fileSize:    sc.FileSize,
			}

			pendingMu.Lock()
			pending[sc.CameraID] = append(pending[sc.CameraID], seg)

			// Reset or create the debounce timer. The timer callback only signals
			// fireCh (non-blocking); the actual wg.Add/dispatch happens in the
			// eventLoop's fireCh select case, keeping all wg mutation on this goroutine.
			if existing, ok := timers[sc.CameraID]; ok {
				existing.Stop()
			}
			camID := sc.CameraID
			t := time.AfterFunc(cfg.Debounce, func() {
				// Non-blocking send: if fireCh is full OR the event loop has exited
				// (ctx cancelled, no reader), the signal is dropped. Dropping is safe
				// during shutdown — pending segments stay in the DB and are picked up
				// by the next process start's backfill. This also guarantees the timer
				// callback never leaks a goroutine blocked on a channel send.
				select {
				case fireCh <- camID:
				default:
				}
			})
			timers[sc.CameraID] = t
			pendingMu.Unlock()
		}
	}
}

// pendingSegmentInfo carries segment metadata from the event loop to the merge worker.

// pendingSegmentInfo carries segment metadata from the event loop to the merge worker.
type pendingSegmentInfo struct {
	recordingID string
	filePath    string
	format      string
	cameraID    string
	startedAt   time.Time
	endedAt     time.Time
	fileSize    int64
}

// mergeSegments performs the rolling merge for a batch of segments on one camera.
// It acquires a per-camera non-blocking lock; if a merge is already in progress,
// the segments are left for the next periodic merge pass (MergeManager) as a fallback.

// mergeSegments performs the rolling merge for a batch of segments on one camera.
// It acquires a per-camera non-blocking lock; if a merge is already in progress,
// the segments are left for the next periodic merge pass (MergeManager) as a fallback.
func (r *RollingMergeCoordinator) mergeSegments(ctx context.Context, cameraID string, segs []pendingSegmentInfo) {
	// For real-time events, use BLOCKING lock acquisition instead of try-lock.
	// The backfill path uses try-lock (skips if busy), so real-time events
	// will eventually get the lock. We wait with a timeout (2 min) to avoid
	// infinite blocking if something goes wrong.
	lockDeadline := time.Now().Add(2 * time.Minute)
	var release func()
	for {
		var ok bool
		release, ok = r.acquireMergeLock(cameraID)
		if ok {
			break
		}
		if time.Now().After(lockDeadline) {
			// This is expected when periodic merge holds the lock for the same camera.
			// The rolling merge will retry on the next segment close. Demoted from WARN
			// to DEBUG to avoid log noise (was ~80/hour in production with no ill effect).
			rollingLogger.Debug("rolling merge timed out waiting for lock",
				"camera_id", cameraID, "segments", len(segs))
			return
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return
		}
	}
	defer release()

	// Separate MP4 segments from AVI/MJPEG.
	var mp4Segs []pendingSegmentInfo
	var batchRecs []*model.Recording
	batchFormat := ""

	for _, seg := range segs {
		if seg.format == string(model.FormatH264) || seg.format == string(model.FormatH265) {
			mp4Segs = append(mp4Segs, seg)
		} else if seg.format == string(model.FormatAVI) || seg.format == string(model.FormatMJPEG) {
			batchFormat = seg.format
			batchRecs = append(batchRecs, &model.Recording{
				ID:        seg.recordingID,
				CameraID:  seg.cameraID,
				FilePath:  seg.filePath,
				Format:    model.Format(seg.format),
				StartedAt: seg.startedAt,
				EndedAt:   seg.endedAt,
				Duration:  seg.endedAt.Sub(seg.startedAt).Seconds(),
				FileSize:  seg.fileSize,
			})
		}
	}

	// Process MP4 segments.
	// When 2+ segments accumulated (frequent disconnect scenario), use batch merge
	// to produce ONE merged file instead of multiple tiny rolling files.
	// Single segment → per-segment append to rolling bucket (low latency).
	if len(mp4Segs) >= 2 {
		mp4Recs := make([]*model.Recording, len(mp4Segs))
		for i, seg := range mp4Segs {
			mp4Recs[i] = &model.Recording{
				ID:        seg.recordingID,
				CameraID:  seg.cameraID,
				FilePath:  seg.filePath,
				Format:    model.Format(seg.format),
				StartedAt: seg.startedAt,
				EndedAt:   seg.endedAt,
				Duration:  seg.endedAt.Sub(seg.startedAt).Seconds(),
				FileSize:  seg.fileSize,
			}
		}
		if ctx.Err() != nil {
			return
		}
		if n, err := r.mergeBatchMP4(ctx, cameraID, mp4Recs); err != nil {
			rollingLogger.Warn("rolling MP4 batch merge failed",
				"camera_id", cameraID, "segments", len(mp4Recs), "error", err)
			if r.metrics != nil {
				r.metrics.RecordMergeFailure("rolling_error")
			}
		} else {
			rollingLogger.Info("rolling MP4 batch merged",
				"camera_id", cameraID, "segments", n)
			// The batch produced standalone output files that are NOT tracked
			// by the in-memory bucket state. Drop the stale bucket so the next
			// single-segment append builds a fresh bucket instead of appending
			// over the batch's time range (double-covered timeline).
			r.buckets.Delete(cameraID)
		}
	} else {
		for _, seg := range mp4Segs {
			if ctx.Err() != nil {
				return
			}
			if err := r.mergeOneSegment(ctx, seg); err != nil {
				rollingLogger.Warn("rolling merge failed for segment",
					"camera_id", cameraID, "recording_id", seg.recordingID, "error", err)
				if r.metrics != nil {
					r.metrics.RecordMergeFailure("rolling_error")
				}
			}
		}
	}

	// Process AVI/MJPEG segments via batch merge.
	if len(batchRecs) >= 2 && batchFormat != "" {
		if ctx.Err() != nil {
			return
		}
		if _, err := r.mergeBatchSegments(ctx, cameraID, batchRecs, batchFormat); err != nil {
			rollingLogger.Warn("rolling batch merge failed",
				"camera_id", cameraID, "format", batchFormat, "error", err)
			if r.metrics != nil {
				r.metrics.RecordMergeFailure("rolling_error")
			}
		}
	} else if len(batchRecs) == 1 && batchFormat != "" {
		// Singleton — just mark as merged (no merge needed).
		if err := storage.RetryOnBusy(ctx, func() error {
			return r.db.SetMergeStatus(ctx, []string{batchRecs[0].ID}, model.MergeStatusMerged)
		}); err != nil {
			rollingLogger.Warn("failed to mark singleton as merged",
				"camera_id", cameraID, "recording_id", batchRecs[0].ID, "error", err)
		}
	}
}

// acquireMergeLock attempts a non-blocking per-camera lock.
// Returns a release func and true on success, nil/false if locked.

// acquireMergeLock attempts a non-blocking per-camera lock.
// Returns a release func and true on success, nil/false if locked.
func (r *RollingMergeCoordinator) acquireMergeLock(cameraID string) (release func(), ok bool) {
	lock := &mergeLock{}
	actual, _ := r.mergeLocks.LoadOrStore(cameraID, lock)
	l := actual.(*mergeLock)
	if !l.mu.TryLock() {
		return nil, false
	}
	return func() { l.mu.Unlock() }, true
}

// mergeOneSegment merges a single segment into the camera's current window bucket.

// mergeOneSegment merges a single segment into the camera's current window bucket.
func (r *RollingMergeCoordinator) mergeOneSegment(ctx context.Context, seg pendingSegmentInfo) error {
	mergeStart := time.Now()

	// Parse the new segment (moov-only, skips mdat).
	newInfo, err := ParseSegment(seg.filePath)
	if err != nil {
		return fmt.Errorf("parse new segment: %w", err)
	}

	// Keyframe alignment (#488): a segment that starts mid-GOP (adaptive TL-exit
	// flush, reconnect micro-segment) references frames that get deleted with
	// the previous source file — merging it verbatim produced the gray-screen
	// merged recordings. Align its head to the first keyframe. A keyframe-less
	// segment is undecodable in ANY merged context — mark it incompatible and
	// leave it standalone instead of poisoning the bucket.
	if dropped, ok := AlignToKeyframe(newInfo); !ok {
		rollingLogger.Warn("segment has no keyframe sample, marking incompatible",
			"camera_id", seg.cameraID, "recording_id", seg.recordingID,
			"samples", newInfo.SampleCount)
		if markErr := storage.RetryOnBusy(ctx, func() error {
			return r.db.SetMergeStatus(ctx, []string{seg.recordingID}, model.MergeStatusIncompatible)
		}); markErr != nil {
			rollingLogger.Warn("failed to mark keyframe-less segment",
				"recording_id", seg.recordingID, "error", markErr)
		}
		return nil
	} else if dropped > 0 {
		rollingLogger.Info("keyframe alignment dropped leading samples",
			"camera_id", seg.cameraID, "recording_id", seg.recordingID, "dropped", dropped)
	}

	// Compute SPS/PPS compatibility key.
	h := sha256.New()
	h.Write(newInfo.SPS)
	h.Write(newInfo.PPS)
	h.Write(newInfo.VPS)
	spsKey := hex.EncodeToString(h.Sum(nil))

	// Compute audio compatibility key (see segmentAudioKey).
	audioKey := segmentAudioKey(newInfo)

	// Compute the window for this segment (natural-hour or configured window).
	cfg := r.resolveRollingConfig(seg.cameraID)
	windowStart, windowEnd := computeWindow(seg.startedAt, cfg.Window)

	// Get or create the bucket for this camera.
	bucketAny, _ := r.buckets.LoadOrStore(seg.cameraID, &bucketInfo{
		windowStart: windowStart,
		windowEnd:   windowEnd,
	})
	bucket := bucketAny.(*bucketInfo)

	bucket.mu.Lock()
	defer bucket.mu.Unlock()

	// Check for window rollover, SPS/PPS change, or size limit → close old
	// bucket, start new.
	needNewBucket := false
	if bucket.mergedFilePath != "" {
		if !seg.startedAt.Before(bucket.windowEnd) && !seg.startedAt.Equal(bucket.windowStart) {
			// Segment is in a new window — the old bucket is complete.
			rollingLogger.Debug("window rollover, finalizing bucket",
				"camera_id", seg.cameraID,
				"old_window_end", bucket.windowEnd,
				"new_window_start", windowStart)
			needNewBucket = true
		} else if bucket.spsKey != spsKey {
			// Codec params changed (camera reconnect) — incompatible merge.
			rollingLogger.Info("SPS/PPS changed, starting new bucket",
				"camera_id", seg.cameraID,
				"old_key", bucket.spsKey[:8],
				"new_key", spsKey[:8])
			needNewBucket = true
		} else if bucket.audioKey != audioKey {
			// Audio presence/config changed (audio_enabled toggled, G.711 ↔ AAC
			// renegotiated). Merging across the boundary would trip the
			// mixed-audio policy in MergeMP4Segments and drop audio — and the
			// degraded bucket would poison every later append. Start a new
			// bucket at the boundary instead: each side keeps its own intact
			// audio state.
			rollingLogger.Info("audio config changed, starting new bucket",
				"camera_id", seg.cameraID,
				"old_key", bucket.audioKey,
				"new_key", audioKey)
			needNewBucket = true
		} else if bucket.mergedFileSize > 0 && bucket.mergedFileSize+newInfo.MdatSize > bucketSizeLimit {
			// Bucket approaching the 4 GiB MP4 mdat hard limit. Finalize it
			// and start a fresh bucket within the same window. Without this,
			// high-bitrate cameras (2K云台 ~1.7MB/s → 6GB/hour) accumulate
			// until MergeMP4Segments returns "mdat box size exceeds MaxUint32"
			// and the segment is lost from the merge queue.
			rollingLogger.Info("bucket size limit reached, starting new bucket",
				"camera_id", seg.cameraID,
				"bucket_size_mb", bucket.mergedFileSize>>20,
				"new_segment_mb", newInfo.MdatSize>>20,
				"limit_mb", bucketSizeLimit>>20,
				"segment_count", bucket.segmentCount)
			needNewBucket = true
		}
	}

	if needNewBucket {
		// The old bucket file is already finalized in the DB (each append updates it).
		// Just reset the in-memory state to start fresh.
		bucket.mergedFilePath = ""
		bucket.mergedRecID = ""
		bucket.spsKey = ""
		bucket.audioKey = ""
		bucket.segmentCount = 0
		bucket.mergedFileSize = 0
		bucket.windowStart = windowStart
		bucket.windowEnd = windowEnd
	}

	// Perform the merge.
	var outputPath string
	var mergedRecID string

	if bucket.mergedFilePath == "" {
		// First segment in this bucket — create the bucket file by merging
		// the single segment (this normalizes it into the bucket format and
		// creates the DB row that future appends will UPDATE).
		outputPath, mergedRecID, err = r.createBucket(ctx, seg, newInfo)
	} else {
		// Append to existing bucket: merge [bucketFile + newSegment].
		outputPath, mergedRecID, err = r.appendToBucket(ctx, seg, newInfo, bucket)
		if err != nil && strings.Contains(err.Error(), "bucket keyframe-less") {
			// Legacy corrupt bucket (written before keyframe alignment, no
			// keyframe-bearing sample left): drop the in-memory state and
			// rebuild the bucket from this segment alone.
			rollingLogger.Warn("bucket has no keyframe-bearing samples, rebuilding bucket",
				"camera_id", seg.cameraID, "old_bucket", bucket.mergedFilePath)
			bucket.mergedFilePath = ""
			bucket.mergedRecID = ""
			bucket.spsKey = ""
			bucket.audioKey = ""
			bucket.segmentCount = 0
			bucket.mergedFileSize = 0
			outputPath, mergedRecID, err = r.createBucket(ctx, seg, newInfo)
		}
	}

	if err != nil {
		return err
	}

	// Update bucket state.
	bucket.mergedFilePath = outputPath
	bucket.mergedRecID = mergedRecID
	bucket.spsKey = spsKey
	bucket.audioKey = audioKey
	bucket.segmentCount++
	// Track merged file size for the bucketSizeLimit check on the next append.
	// One stat per merged segment is cheap (the file was just written and its
	// inode is hot in cache), and avoids the need to thread size through every
	// create/append return path.
	if fi, statErr := os.Stat(outputPath); statErr == nil {
		bucket.mergedFileSize = fi.Size()
	} else {
		bucket.mergedFileSize = 0 // unknown — size check will be skipped next cycle
	}

	// Record metrics.
	if r.metrics != nil {
		r.metrics.RecordMergeSuccess(time.Since(mergeStart), newInfo.MdatSize)
		// Rolling-specific metrics: latency from segment close to merge complete.
		latency := time.Since(seg.endedAt)
		r.metrics.RecordRollingMergeLatency(seg.cameraID, latency)
		r.metrics.UpdateRollingMergeBucketSegments(seg.cameraID, bucket.segmentCount)
	}

	rollingLogger.Debug("rolling merge complete",
		"camera_id", seg.cameraID,
		"recording_id", seg.recordingID,
		"bucket_segments", bucket.segmentCount,
		"duration_ms", time.Since(mergeStart).Milliseconds())

	return nil
}

// createBucket creates the initial merged file for a new window bucket.
// It merges the single segment into a new output file and creates a DB row.
// The source segment's DB row is then deleted (replaced by the merged row).
