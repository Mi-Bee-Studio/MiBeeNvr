package merge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// Fragment batching rails (#852). Segments shorter than fragmentDuration-
// Threshold are "fragments" (flapping-camera reconnect outputs, ~7s each);
// with merge.rolling_fragment_hold_s > 0 they are HELD in a metadata-only
// queue and folded into the hour bucket in ONE merge, instead of one
// full-bucket rewrite per fragment (#851: a single flapper drove 36
// rewrites/min and saturated the disk — PSI io some 86%). The queue flushes
// on any of: oldest fragment reaches the hold age (timer), depth cap, byte
// cap, window rollover, or a healthy (≥ threshold) segment arriving on the
// same camera (a fold happens for it anyway, so the batch rides along).
const (
	fragmentDurationThreshold = 30 * time.Second
	fragmentHoldMaxCount      = 8
	fragmentHoldMaxBytes      = 64 << 20 // 64 MiB
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
	pending := make(map[string][]pendingSegmentInfo) // cameraID → queued healthy segments
	timers := make(map[string]*time.Timer)

	// Fragment hold queue (#852): fragments wait here (NOT in pending) until a
	// flush condition fires, so the debounce timer of healthy segments never
	// drains them early. fragSince anchors the oldest fragment's enqueue time
	// — the hold deadline is fixed by it, later fragments do NOT extend it.
	fragHold := make(map[string][]pendingSegmentInfo)
	fragSince := make(map[string]time.Time)
	fragBytes := make(map[string]int64)
	fragTimers := make(map[string]*time.Timer)

	// fireCh carries cameraIDs whose debounce timer elapsed. Buffered so a timer
	// callback virtually never blocks; the per-camera dedup in pending (delete on
	// dispatch) keeps semantics correct if multiple signals queue. Dispatch is
	// non-blocking (only wg.Add + go), so drain is fast and the buffer is rarely
	// more than lightly populated.
	fireCh := make(chan string, 256)

	// signalFire enqueues a dispatch without blocking the caller (event loop or
	// timer callback). A dropped signal is safe: every fragment queue also has
	// an armed hold timer (or an in-flight healthy debounce) as a backstop.
	signalFire := func(camID string) {
		select {
		case fireCh <- camID:
		default:
		}
	}

	dispatch := func(cameraID string) {
		pendingMu.Lock()
		// Fragments first — they are older than any pending healthy segment
		// (mergeSegments re-sorts, this just keeps the slice intuitive).
		segs := fragHold[cameraID]
		delete(fragHold, cameraID)
		delete(fragSince, cameraID)
		delete(fragBytes, cameraID)
		if t := fragTimers[cameraID]; t != nil {
			t.Stop()
			delete(fragTimers, cameraID)
		}
		segs = append(segs, pending[cameraID]...)
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
			for _, t := range fragTimers {
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

			// Skip non-main layers — tierrec sub-layer segments (#637) are
			// standalone 60s archives on the sub-stream, never merge inputs.
			// (Field bug 2026-09-01: without this the live bucket consumed
			// every sub segment, splicing 480p frames into 2.5K merged output
			// and deleting the sub rows/files.)
			if sc.Layer != model.LayerMain {
				continue
			}

			// Parse the segment's startedAt time for window calculation.
			// (below) endedAt prefers the event's real end time — time.Now() inflated
			// offline-compensation replays of old segments to hours of phantom wall
			// time and desynced the row's wall axis (#496 append-fix, 2026-09-01).
			startedAt, err := time.Parse(time.RFC3339Nano, sc.StartedAt)
			if err != nil {
				// Fallback: use now.
				startedAt = time.Now()
			}

			endedAt := time.Now()
			if t, perr := time.Parse(time.RFC3339Nano, sc.EndedAt); perr == nil && t.After(startedAt) {
				endedAt = t
			}

			seg := pendingSegmentInfo{
				recordingID: sc.RecordingID,
				filePath:    sc.FilePath,
				format:      sc.Format,
				cameraID:    sc.CameraID,
				startedAt:   startedAt,
				endedAt:     endedAt,
				fileSize:    sc.FileSize,
			}

			pendingMu.Lock()
			// Fragment routing (#852): short MP4 segments enter the hold queue
			// when batching is on; everything else keeps the classic debounce
			// path (and its dispatch drains the hold queue alongside). A
			// non-positive event duration (clock skew, offline replays of
			// future-stamped rows) is NOT a fragment — route it the classic
			// way and let the fold-site guards sort it out.
			isFragment := cfg.FragmentHold > 0 &&
				(sc.Format == string(model.FormatH264) || sc.Format == string(model.FormatH265)) &&
				endedAt.Sub(startedAt) > 0 &&
				endedAt.Sub(startedAt) < fragmentDurationThreshold
			if isFragment {
				fragHold[sc.CameraID] = append(fragHold[sc.CameraID], seg)
				if sc.FileSize > 0 {
					fragBytes[sc.CameraID] += sc.FileSize
				}
				if _, ok := fragSince[sc.CameraID]; !ok {
					fragSince[sc.CameraID] = time.Now()
				}
				// Flush conditions, checked on every arrival.
				queue := fragHold[sc.CameraID]
				flush, reason := false, ""
				if len(queue) > 1 {
					// Window rollover: the oldest held fragment's window has
					// closed — fold now so the bucket timeline doesn't straddle.
					oldWS, _ := computeWindow(queue[0].startedAt, cfg.Window)
					newWS, _ := computeWindow(seg.startedAt, cfg.Window)
					if !oldWS.Equal(newWS) {
						flush, reason = true, "window_rollover"
					}
				}
				if !flush && len(queue) >= fragmentHoldMaxCount {
					flush, reason = true, "depth_cap"
				}
				if !flush && fragBytes[sc.CameraID] >= fragmentHoldMaxBytes {
					flush, reason = true, "byte_cap"
				}
				if flush {
					rollingLogger.Debug("fragment hold flushing",
						"camera_id", sc.CameraID, "segments", len(queue), "reason", reason)
					camID := sc.CameraID
					pendingMu.Unlock()
					signalFire(camID)
					continue
				}
				// Arm the hold timer once, at the OLDEST fragment's deadline —
				// later fragments must not extend it. The callback deletes its
				// own map entry before signaling so a dropped fireCh send (full
				// buffer / shutdown) still allows the next fragment to re-arm.
				if fragTimers[sc.CameraID] == nil {
					camID := sc.CameraID
					deadline := cfg.FragmentHold - time.Since(fragSince[sc.CameraID])
					if deadline < 0 {
						deadline = 0
					}
					fragTimers[sc.CameraID] = time.AfterFunc(deadline, func() {
						pendingMu.Lock()
						delete(fragTimers, camID)
						pendingMu.Unlock()
						signalFire(camID)
					})
				}
				pendingMu.Unlock()
				continue
			}

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
				signalFire(camID)
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
	// Chronological order, always (#698): dispatch order follows lock
	// acquisition, which does NOT follow event order under load — a swapped
	// batch fed recs[0]/recs[last] an inverted range and wrote ended_at <
	// started_at rows. Sorting here fixes the batch path (2+ segments) and
	// the bucket path's create-vs-append sequencing within one dispatch.
	sort.SliceStable(segs, func(i, j int) bool {
		return segs[i].startedAt.Before(segs[j].startedAt)
	})

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
	//
	// #810: segments with a pending/running transcode task are DEFERRED —
	// both the batch path and the bucket append fold and delete their source
	// within one debounce, racing the sequential transcode queue (M5
	// production: flapping H.265 camera, 54 consecutive exit-254s). Deferred
	// segments are left untouched for the backfill sweep (10m cadence,
	// min_segment_age rail), by which time their tasks have finished.
	pendingTranscode, holdOK := r.queryTranscodeHold(ctx, cameraID)
	if !holdOK {
		// Fail-closed (#810 fold-site follow-up): the hold set is unknowable —
		// fold nothing this round; the next dispatch or backfill sweep picks
		// the segments up.
		return
	}
	// Age rail (#810 TOCTOU follow-up): a task row that has not been
	// inserted yet is invisible to the pending-set — production showed
	// the debounce folding a segment ~4s before its task landed. Young
	// segments on transcode-enabled cameras defer unconditionally; the
	// backfill sweep folds them once the window expires.
	grace := r.resolveRollingConfig(cameraID).TranscodeGrace
	graceRail := grace > 0 && r.cameraTranscodeEnabled != nil && r.cameraTranscodeEnabled(cameraID)
	var young func(endedAt time.Time) bool
	if graceRail {
		now := time.Now()
		young = func(endedAt time.Time) bool { return now.Sub(endedAt) < grace }
	}
	if len(pendingTranscode) > 0 || graceRail {
		free := make([]pendingSegmentInfo, 0, len(mp4Segs))
		deferred := 0
		byAge := 0
		for _, seg := range mp4Segs {
			if pendingTranscode[seg.filePath] {
				deferred++
				continue
			}
			if young != nil && young(seg.endedAt) {
				byAge++
				continue
			}
			free = append(free, seg)
		}
		if deferred > 0 || byAge > 0 {
			rollingLogger.Info("deferring segments with pending transcode tasks (backfill merges them after the tasks finish)",
				"camera_id", cameraID, "deferred", deferred, "young", byAge, "processing", len(free))
			mp4Segs = free
		}
	}

	// Process MP4 segments: group into same-key runs and fold each run into
	// its window bucket with ONE MergeMP4Segments pass (#852 true batch fold).
	// This replaces the pre-#852 split (2+ segments → standalone batch merge +
	// bucket-set reset; 1 → per-segment append): every live fold now lands in
	// the retained bucket state, so a fragment batch — or a post-restart
	// dispatch herd — costs one bucket rewrite per key group instead of one
	// per segment. AVI/MJPEG keep the standalone batch path below (no bucket).
	if len(mp4Segs) > 0 {
		r.foldIntoBuckets(ctx, cameraID, mp4Segs)
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

// acquireMergeLockBlocking waits up to timeout for the per-camera merge lock
// (500ms retry cadence; ctx cancels the wait). Returns the release func and
// true, or nil/false on timeout/cancel — the caller decides whether skipping
// is tolerable (mergeSegments: retry on the next segment) or an error
// (BackfillCamera reset, #788).
func (r *RollingMergeCoordinator) acquireMergeLockBlocking(ctx context.Context, cameraID string, timeout time.Duration) (release func(), ok bool) {
	lockDeadline := time.Now().Add(timeout)
	for {
		release, ok := r.acquireMergeLock(cameraID)
		if ok {
			return release, true
		}
		if time.Now().After(lockDeadline) {
			return nil, false
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return nil, false
		}
	}
}

// foldSegment is a parsed, classified segment ready to fold into a bucket.
type foldSegment struct {
	seg      pendingSegmentInfo
	info     *SegmentInfo
	spsKey   string
	audioKey string
	// windowStart/End are the segment's (camera-config) merge window — run
	// grouping breaks on window boundaries so a batch never straddles hours.
	windowStart time.Time
	windowEnd   time.Time
}

// classifyForFold parses one segment and derives its bucket keys: keyframe
// alignment (#488 — a mid-GOP start references frames deleted with the
// previous source file), SPS semantic key (#642), audio key and window.
// ok=false means the segment must NOT fold this round (parse failure → left
// pending for the backfill sweep; keyframe-less → marked incompatible).
func (r *RollingMergeCoordinator) classifyForFold(ctx context.Context, seg pendingSegmentInfo, cfg RollingMergeConfig) (*foldSegment, bool) {
	newInfo, err := ParseSegment(seg.filePath)
	if err != nil {
		rollingLogger.Warn("rolling merge: parse segment failed",
			"camera_id", seg.cameraID, "recording_id", seg.recordingID, "error", err)
		if r.metrics != nil {
			r.metrics.RecordMergeFailure("rolling_error")
		}
		return nil, false
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
		return nil, false
	} else if dropped > 0 {
		rollingLogger.Info("keyframe alignment dropped leading samples",
			"camera_id", seg.cameraID, "recording_id", seg.recordingID, "dropped", dropped)
	}

	// Compute SPS/PPS compatibility key. The SPS contributes its semantic
	// decode key, not raw bytes (#642): cameras alternating decode-equivalent
	// SPS encodings (VUI/timing-only differences) per GOP must not split/
	// re-spawn buckets at segment granularity — with the raw bytes, bucket
	// keys flip every segment whenever consecutive segments happened to be
	// created under different variants. PPS/VPS have no semantic parser and
	// stay raw (their variant flapping is rotation-rate-limited upstream).
	// Unparseable SPS falls back to raw bytes (conservative split).
	spsKeyBytes := newInfo.SPS
	if key, ok := nalutil.SPSSemanticKey(newInfo.SPS, newInfo.Codec == "h265"); ok && key != "" {
		spsKeyBytes = []byte(key)
	}
	h := sha256.New()
	h.Write(spsKeyBytes)
	h.Write(newInfo.PPS)
	h.Write(newInfo.VPS)

	windowStart, windowEnd := computeWindow(seg.startedAt, cfg.Window)
	return &foldSegment{
		seg:         seg,
		info:        newInfo,
		spsKey:      hex.EncodeToString(h.Sum(nil)),
		audioKey:    segmentAudioKey(newInfo),
		windowStart: windowStart,
		windowEnd:   windowEnd,
	}, true
}

// foldIntoBuckets groups classified segments into consecutive runs sharing
// (spsKey, audioKey, window) and folds each run into its bucket in ONE
// MergeMP4Segments pass — one bucket rewrite per key group regardless of how
// many fragments the dispatch carried (#852). mergeSegments has already
// sorted the input by startedAt, so consecutive grouping preserves time order
// inside each run.
func (r *RollingMergeCoordinator) foldIntoBuckets(ctx context.Context, cameraID string, segs []pendingSegmentInfo) {
	cfg := r.resolveRollingConfig(cameraID)
	classified := make([]*foldSegment, 0, len(segs))
	for _, seg := range segs {
		if ctx.Err() != nil {
			return
		}
		if fs, ok := r.classifyForFold(ctx, seg, cfg); ok {
			classified = append(classified, fs)
		}
	}

	sameRun := func(a, b *foldSegment) bool {
		return a.spsKey == b.spsKey && a.audioKey == b.audioKey && a.windowStart.Equal(b.windowStart)
	}
	for start := 0; start < len(classified); {
		end := start + 1
		for end < len(classified) && sameRun(classified[start], classified[end]) {
			end++
		}
		if err := r.mergeRunIntoBucket(ctx, classified[start:end]); err != nil {
			rollingLogger.Warn("rolling merge failed for run",
				"camera_id", cameraID, "segments", end-start, "error", err)
			if r.metrics != nil {
				r.metrics.RecordMergeFailure("rolling_error")
			}
		}
		start = end
	}
}

// mergeRunIntoBucket folds one same-key run (1..N segments) into the camera's
// current window bucket — the generalized mergeOneSegment (#852): a single
// segment is the classic low-latency append; a fragment batch folds as one
// pass. Caller holds the per-camera merge lock (mergeSegments).
func (r *RollingMergeCoordinator) mergeRunIntoBucket(ctx context.Context, run []*foldSegment) error {
	mergeStart := time.Now()
	first := run[0]
	cameraID := first.seg.cameraID

	// Fold-site re-check (#810 residual, M5 2026-09-16): the dispatch filter
	// holds pending-task segments, but production showed folds executing with
	// tasks pending for minutes — whatever let the segment past the filter
	// (query failure failing open, snapshot age, rail expiry mid-merge), the
	// deletion moment is the last line of defense. Inside the per-camera
	// merge lock, the task row is the authoritative state.
	if held, ok := r.queryTranscodeHold(ctx, cameraID); !ok {
		// Fail-closed: the hold set is unknowable — defer the whole run to the
		// backfill sweep rather than delete a queued task's input.
		return nil
	} else {
		kept := make([]*foldSegment, 0, len(run))
		for _, fs := range run {
			if held[fs.seg.filePath] {
				rollingLogger.Info("fold re-check: segment still held by a pending/running transcode task",
					"camera_id", cameraID, "recording_id", fs.seg.recordingID)
				continue
			}
			kept = append(kept, fs)
		}
		if len(kept) == 0 {
			return nil
		}
		run = kept
	}

	spsKey := first.spsKey
	audioKey := first.audioKey

	// Compute the window for this run (natural-hour or configured window).
	cfg := r.resolveRollingConfig(cameraID)

	// Select the bucket with the retention policy (#764): a retained bucket
	// with this exact key (params + audio + window) is resumed — flipping
	// back to a previous quality tier APPENDS instead of finalizing + re-
	// creating. Window rollover, SPS/PPS change, audio change and the size
	// limit are all selection mismatches now; the matched-at-size-limit case
	// size-rolls inside selectBucket.
	var runBytes int64
	for _, fs := range run {
		runBytes += fs.info.MdatSize
	}
	bucket := r.selectBucket(cameraID, spsKey, audioKey, first.windowStart, first.windowEnd, runBytes, cfg)

	bucket.mu.Lock()
	defer bucket.mu.Unlock()

	segs := make([]pendingSegmentInfo, len(run))
	infos := make([]*SegmentInfo, len(run))
	var totalMdat int64
	for i, fs := range run {
		segs[i] = fs.seg
		infos[i] = fs.info
		totalMdat += fs.info.MdatSize
	}

	// Perform the merge.
	var outputPath string
	var mergedRecID string
	var err error

	if bucket.mergedFilePath == "" {
		// First segments in this bucket — create the bucket file by merging
		// the run (this normalizes it into the bucket format and creates the
		// DB row that future appends will UPDATE).
		outputPath, mergedRecID, err = r.createBucket(ctx, segs, infos, bucket)
	} else {
		// Append the run to the existing bucket: one [bucket + run] merge.
		outputPath, mergedRecID, err = r.appendToBucket(ctx, segs, infos, bucket)
		if err != nil && strings.Contains(err.Error(), "bucket keyframe-less") {
			// Legacy corrupt bucket (written before keyframe alignment, no
			// keyframe-bearing sample left): drop the in-memory state and
			// rebuild the bucket from this run alone.
			rollingLogger.Warn("bucket has no keyframe-bearing samples, rebuilding bucket",
				"camera_id", cameraID, "old_bucket", bucket.mergedFilePath)
			bucket.mergedFilePath = ""
			bucket.mergedRecID = ""
			bucket.spsKey = ""
			bucket.audioKey = ""
			bucket.segmentCount = 0
			bucket.mergedFileSize = 0
			bucket.wallDurSec = 0
			bucket.fileDurSec = 0
			bucket.wallFile = nil
			outputPath, mergedRecID, err = r.createBucket(ctx, segs, infos, bucket)
		}
	}

	if err != nil {
		// A failed create left a never-created bucket in the retained set —
		// drop it so it doesn't waste a retention slot (it can never match a
		// real segment: its keys are empty).
		if bucket.mergedFilePath == "" && bucket.segmentCount == 0 {
			r.dropBucketIfEmpty(cameraID, bucket)
		}
		return err
	}

	// Update bucket state.
	bucket.mergedFilePath = outputPath
	bucket.mergedRecID = mergedRecID
	bucket.spsKey = spsKey
	bucket.audioKey = audioKey
	bucket.segmentCount += len(run)
	bucket.lastAppend = r.now() // retention LRU/TTL clock (#764)
	// Track merged file size for the bucketSizeLimit check on the next append.
	// One stat per merged run is cheap (the file was just written and its
	// inode is hot in cache), and avoids the need to thread size through every
	// create/append return path.
	if fi, statErr := os.Stat(outputPath); statErr == nil {
		bucket.mergedFileSize = fi.Size()
	} else {
		bucket.mergedFileSize = 0 // unknown — size check will be skipped next cycle
	}

	// Record metrics.
	if r.metrics != nil {
		r.metrics.RecordMergeSuccess(time.Since(mergeStart), totalMdat)
		// Rolling-specific metrics: latency from the run's LAST segment close
		// to merge complete (the batching delay a viewer experiences).
		last := segs[len(segs)-1]
		r.metrics.RecordRollingMergeLatency(cameraID, time.Since(last.endedAt))
		r.metrics.UpdateRollingMergeBucketSegments(cameraID, bucket.segmentCount)
	}

	rollingLogger.Debug("rolling merge complete",
		"camera_id", cameraID,
		"run_segments", len(run),
		"bucket_segments", bucket.segmentCount,
		"duration_ms", time.Since(mergeStart).Milliseconds())

	return nil
}

// mergeOneSegment folds a single segment into its bucket — the classic entry
// kept for direct callers (tests); the live dispatch path goes through
// foldIntoBuckets with run grouping (#852).
func (r *RollingMergeCoordinator) mergeOneSegment(ctx context.Context, seg pendingSegmentInfo) error {
	cfg := r.resolveRollingConfig(seg.cameraID)
	fs, ok := r.classifyForFold(ctx, seg, cfg)
	if !ok {
		return nil
	}
	return r.mergeRunIntoBucket(ctx, []*foldSegment{fs})
}

// createBucket creates the initial merged file for a new window bucket.
// It merges the single segment into a new output file and creates a DB row.
// The source segment's DB row is then deleted (replaced by the merged row).
