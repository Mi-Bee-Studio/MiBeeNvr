// Package timelapse provides keyframe extraction from active recorder StreamHubs.
package timelapse

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

var kfeLogger = slog.Default().With("component", "keyframe-extractor")

// SegmentStore defines the segment lifecycle interface needed by KeyframeExtractor.
// This is a subset of the recorder.SegmentStore interface — defined locally to
// avoid circular imports (recorder imports timelapse).
type SegmentStore interface {
	CreateSegment(cameraID string, format string) (tempPath string, finalPath string, err error)
	CloseSegment(tempPath, finalPath string) error
}

// RecordingDB defines the database operations needed for recording metadata.
type RecordingDB interface {
	InsertRecording(ctx context.Context, r *model.Recording) error
	InsertRecordingWithRetry(ctx context.Context, r *model.Recording, maxRetries int, backoff time.Duration) error
}

// KeyframeExtractorConfig holds configuration for the KeyframeExtractor.
type KeyframeExtractorConfig struct {
	CameraID   string
	Interval   time.Duration // how often to capture a frame (default: 5s)
	SegmentDur time.Duration // duration of each segment (default: 10min)
	IsH265     bool          // true if the source stream is H.265

	Store    SegmentStore         // required
	DB       RecordingDB          // optional — enables DB recording entries
	MergeMgr *RollingMergeManager // optional — enables rolling merge on segment close

	// CodecParamsProvider returns the recorder's codec parameter sets (SPS/PPS for
	// H.264, VPS/SPS/PPS for H.265). Used as a FALLBACK when parameter sets are not
	// found inline in the frame AU — this happens when the camera sends them
	// out-of-band (in the RTSP DESCRIBE SDP or MP4 moov box) rather than inline
	// with each IDR. Without this fallback, every captured frame file lacks SPS/PPS,
	// and the H264GoMerger/H265GoMerger permanently fails with "frames missing SPS".
	// Optional — when nil, only inline parameter sets are used.
	CodecParamsProvider func() (sps, pps, vps []byte)

	// RecordEnabled gates whether captured frames are written to disk, mirroring
	// the segment recorder's RecordEnabled (internal/recorder/base.go).
	// nil or true = write timelapse frames (default). false = "preview-only":
	// the capture loop keeps ticking (so it self-heals if the flag flips back),
	// but performs no segment/frame I/O — useful when a camera is set to
	// recording_enabled=false and the user expects zero disk writes.
	RecordEnabled *bool
}

// KeyframeExtractor subscribes to a recorder's StreamHub and captures
// keyframes at configurable intervals, storing them as raw frame files
// in timelapse segment directories.
//
// It is NOT a recorder — it is a frame consumer that works alongside
// a regular (non-timelapse) recorder for the same camera. It reuses
// the recorder's StreamHub for frame delivery and does NOT create
// its own RTSP connection.
//
// The extractor filters for IDR frames (NAL type 5 for H.264,
// NAL type 19/20 for H.265) and falls back to P-frames if no IDR
// arrives within the capture interval.
type KeyframeExtractor struct {
	cameraID   string
	interval   time.Duration
	segmentDur time.Duration
	isH265     bool

	store      SegmentStore
	db         RecordingDB
	mergeMgr   *RollingMergeManager
	mu         sync.Mutex
	hub        *streamhub.StreamHub
	consumerID string

	// Latest IDR frame access unit (deep-copied in callback).
	latestFrame   frameData
	latestFrameMu sync.Mutex

	// Latest non-IDR frame (P-frame fallback when no IDR available).
	latestPFrame   frameData
	latestPFrameMu sync.Mutex

	// Cached parameter sets (SPS/PPS for H.264, VPS/SPS/PPS for H.265).
	// Extracted from every AU and prepended to saved frame files so the
	// H264GoMerger always has parameter sets available.
	cachedParamSets   [][]byte
	cachedParamSetsMu sync.Mutex

	// codecParamsProvider fetches SPS/PPS/VPS from the recorder when inline
	// parameter sets are missing from frame AUs (out-of-band codec config).
	codecParamsProvider func() (sps, pps, vps []byte)

	// recordEnabled gates disk writes (nil/true = write, false = preview-only).
	// Mirrors the recorder gate; see KeyframeExtractorConfig.RecordEnabled.
	recordEnabled *bool

	// Segment state.
	curTempPath  string
	curFinalPath string
	segStart     time.Time
	frameCount   int

	cancel  context.CancelFunc
	done    chan struct{}
	running atomic.Bool
}

// frameData holds a single captured frame's metadata and NALU data.
type frameData struct {
	pts int64
	au  [][]byte
	ts  time.Time
}

// NewKeyframeExtractor creates a new KeyframeExtractor with the given config.
func NewKeyframeExtractor(cfg KeyframeExtractorConfig) *KeyframeExtractor {
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Second
	}
	if cfg.SegmentDur <= 0 {
		cfg.SegmentDur = 10 * time.Minute
	}
	return &KeyframeExtractor{
		cameraID:            cfg.CameraID,
		interval:            cfg.Interval,
		segmentDur:          cfg.SegmentDur,
		isH265:              cfg.IsH265,
		store:               cfg.Store,
		db:                  cfg.DB,
		mergeMgr:            cfg.MergeMgr,
		codecParamsProvider: cfg.CodecParamsProvider,
		recordEnabled:       cfg.RecordEnabled,
		consumerID:          "keyframe-extractor-" + cfg.CameraID,
	}
}

// Start subscribes to the given StreamHub and begins the capture loop.
// The hub must belong to an active recorder for the same camera.
// Returns an error if the extractor is already running or if subscription fails.
func (k *KeyframeExtractor) Start(ctx context.Context, hub *streamhub.StreamHub) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.running.Load() {
		return fmt.Errorf("keyframe extractor for %q already running", k.cameraID)
	}

	k.hub = hub
	if err := hub.Subscribe(k.consumerID, k.onFrame); err != nil {
		k.hub = nil
		return fmt.Errorf("subscribe to stream hub: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	k.cancel = cancel
	k.done = make(chan struct{})
	k.running.Store(true)

	go k.captureLoop(ctx)

	kfeLogger.Info(
		"keyframe extractor started",
		"camera_id", k.cameraID,
		"interval", k.interval,
		"segment_dur", k.segmentDur,
		"is_h265", k.isH265,
	)
	return nil
}

// Stop unsubscribes from the StreamHub and stops the capture loop.
// Closes the current segment if one is active.
func (k *KeyframeExtractor) Stop() error {
	k.mu.Lock()
	cancel := k.cancel
	k.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	if k.done != nil {
		<-k.done
	}

	k.closeCurrentSegment()

	k.mu.Lock()
	if k.hub != nil {
		k.hub.Unsubscribe(k.consumerID)
		k.hub = nil
	}
	k.mu.Unlock()

	k.running.Store(false)

	kfeLogger.Info("keyframe extractor stopped", "camera_id", k.cameraID)
	return nil
}

// IsRunning returns whether the extractor is currently active.
func (k *KeyframeExtractor) IsRunning() bool {
	return k.running.Load()
}

// onFrame is the StreamHub FrameCallback — MUST be non-blocking.
// It deep-copies the access unit and stores it as either the latest
// IDR frame (if it contains an IDR NALU) or the latest P-frame fallback.
func (k *KeyframeExtractor) onFrame(pts int64, au [][]byte) {
	// Cache parameter sets from every AU — they may arrive separately from IDR frames.
	k.cacheParamSets(au)

	if nalutil.IsIDR(au, k.isH265) {
		k.latestFrameMu.Lock()
		k.latestFrame = frameData{
			pts: pts,
			au:  copyAU(au),
			ts:  time.Now(),
		}
		k.latestFrameMu.Unlock()
	} else {
		// Store as P-frame fallback for when no IDR is available.
		k.latestPFrameMu.Lock()
		k.latestPFrame = frameData{
			pts: pts,
			au:  copyAU(au),
			ts:  time.Now(),
		}
		k.latestPFrameMu.Unlock()
	}
}

// cacheParamSets extracts and caches parameter set NALUs (SPS/PPS for H.264,
// VPS/SPS/PPS for H.265) from the given access unit. Parameter sets often
// arrive in separate AUs from IDR frames, so we must cache them and prepend
// to every saved frame file.
func (k *KeyframeExtractor) cacheParamSets(au [][]byte) {
	var psNALUs [][]byte
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		if k.isH265 {
			// H.265: VPS=32, SPS=33, PPS=34
			nalType := (nalu[0] >> 1) & 0x3F
			if nalType == 32 || nalType == 33 || nalType == 34 {
				cp := make([]byte, len(nalu))
				copy(cp, nalu)
				psNALUs = append(psNALUs, cp)
			}
		} else {
			// H.264: SPS=7, PPS=8
			nalType := nalu[0] & 0x1F
			if nalType == 7 || nalType == 8 {
				cp := make([]byte, len(nalu))
				copy(cp, nalu)
				psNALUs = append(psNALUs, cp)
			}
		}
	}
	if len(psNALUs) > 0 {
		k.cachedParamSetsMu.Lock()
		k.cachedParamSets = psNALUs
		k.cachedParamSetsMu.Unlock()
	}
}

// captureLoop runs on a ticker, capturing frames at the configured interval.
func (k *KeyframeExtractor) captureLoop(ctx context.Context) {
	defer close(k.done)
	defer k.closeCurrentSegment()

	ticker := time.NewTicker(k.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			k.captureFrame()
		}
	}
}

// captureFrame captures the latest available frame and writes it to the
// current segment. Prefers IDR frames but falls back to P-frames if no
// IDR has been received since the last capture.
func (k *KeyframeExtractor) captureFrame() {
	// Preview-only: keep the loop alive (so it recovers if the flag flips back)
	// but skip ALL segment/frame I/O. Mirrors the recorder gate in base.go.
	if k.recordEnabled != nil && !*k.recordEnabled {
		return
	}

	// Try to get the latest IDR frame first.
	k.latestFrameMu.Lock()
	frame := k.latestFrame
	k.latestFrameMu.Unlock()

	// Fall back to P-frame if no IDR is available.
	if frame.au == nil {
		k.latestPFrameMu.Lock()
		frame = k.latestPFrame
		k.latestPFrameMu.Unlock()
	}

	if frame.au == nil {
		kfeLogger.Debug("no frame available for capture", "camera_id", k.cameraID)
		return
	}

	// Ensure segment exists.
	if err := k.ensureSegment(); err != nil {
		kfeLogger.Error("failed to create segment", "camera_id", k.cameraID, "error", err)
		return
	}

	// Write frame as raw Annex B bitstream file in the segment directory.
	k.mu.Lock()
	tempPath := k.curTempPath
	frameCount := k.frameCount + 1
	k.frameCount = frameCount
	k.mu.Unlock()

	ext := ".h264"
	if k.isH265 {
		ext = ".h265"
	}
	frameName := fmt.Sprintf("frame_%06d%s", frameCount, ext)
	framePath := filepath.Join(tempPath, frameName)

	// Prepend cached parameter sets (SPS/PPS) so every frame file is self-contained.
	// Parameter sets often arrive in separate AUs from IDR frames; without them
	// the H264GoMerger cannot build a valid MP4.
	k.cachedParamSetsMu.Lock()
	paramSets := k.cachedParamSets
	k.cachedParamSetsMu.Unlock()

	// Fallback: if no inline parameter sets were cached (camera sends them
	// out-of-band), fetch from the recorder's codec params. This prevents
	// "frames missing SPS" merge failures on cameras like Xiaomi H.265 that
	// only send VPS/SPS/PPS in the SDP/moov, not inline with frames.
	if len(paramSets) == 0 && k.codecParamsProvider != nil {
		sps, pps, vps := k.codecParamsProvider()
		if k.isH265 {
			if vps != nil {
				paramSets = append(paramSets, vps)
			}
			if sps != nil {
				paramSets = append(paramSets, sps)
			}
			if pps != nil {
				paramSets = append(paramSets, pps)
			}
		} else {
			if sps != nil {
				paramSets = append(paramSets, sps)
			}
			if pps != nil {
				paramSets = append(paramSets, pps)
			}
		}
		// Persist into the cache so subsequent frames don't re-query the provider.
		if len(paramSets) > 0 {
			k.cachedParamSetsMu.Lock()
			k.cachedParamSets = paramSets
			k.cachedParamSetsMu.Unlock()
		}
	}

	// Skip the capture if no parameter sets are available. A frame file without
	// SPS/PPS cannot be merged into a valid MP4 and would poison the segment with
	// a permanent "frames missing SPS" failure (issue #90). Waiting for the next
	// tick is preferable to writing an unusable frame.
	if len(paramSets) == 0 {
		kfeLogger.Warn(
			"skipping keyframe capture: no codec parameter sets available yet",
			"camera_id", k.cameraID,
			"frame", frameCount,
		)
		k.mu.Lock()
		k.frameCount--
		k.mu.Unlock()
		return
	}

	// Concatenate parameter sets + frame NALUs with Annex B start codes.
	var data []byte
	for _, nalu := range paramSets {
		data = append(data, []byte{0x00, 0x00, 0x00, 0x01}...)
		data = append(data, nalu...)
	}
	for _, nalu := range frame.au {
		data = append(data, []byte{0x00, 0x00, 0x00, 0x01}...)
		data = append(data, nalu...)
	}

	if err := os.WriteFile(framePath, data, 0o644); err != nil {
		kfeLogger.Error(
			"failed to write keyframe file",
			"camera_id", k.cameraID,
			"frame", frameCount,
			"error", err,
		)
		k.mu.Lock()
		k.frameCount--
		k.mu.Unlock()
		return
	}

	kfeLogger.Debug(
		"keyframe captured",
		"camera_id", k.cameraID,
		"frame", frameCount,
		"pts", frame.pts,
		"size", len(data),
	)

	// Check if segment duration has elapsed.
	k.mu.Lock()
	segStart := k.segStart
	k.mu.Unlock()

	if time.Since(segStart) >= k.segmentDur {
		k.closeCurrentSegment()
	}
}

// ensureSegment creates a new timelapse segment if none is active.
func (k *KeyframeExtractor) ensureSegment() error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.curTempPath != "" {
		return nil
	}

	tempPath, finalPath, err := k.store.CreateSegment(k.cameraID, "timelapse")
	if err != nil {
		return fmt.Errorf("create segment: %w", err)
	}

	k.curTempPath = tempPath
	k.curFinalPath = finalPath
	k.segStart = time.Now()
	k.frameCount = 0

	kfeLogger.Info(
		"new keyframe segment created",
		"camera_id", k.cameraID,
		"temp_path", tempPath,
		"final_path", finalPath,
	)
	return nil
}

// closeCurrentSegment finalizes the current segment: closes the storage
// segment and optionally records the segment in the database.
func (k *KeyframeExtractor) closeCurrentSegment() {
	k.mu.Lock()
	tempPath := k.curTempPath
	finalPath := k.curFinalPath
	frameCount := k.frameCount
	segStart := k.segStart

	k.curTempPath = ""
	k.curFinalPath = ""
	k.frameCount = 0
	k.mu.Unlock()

	if tempPath == "" {
		return
	}

	// Close segment via storage (atomic rename).
	if err := k.store.CloseSegment(tempPath, finalPath); err != nil {
		kfeLogger.Error(
			"failed to close keyframe segment",
			"camera_id", k.cameraID,
			"temp_path", tempPath,
			"error", err,
		)
	}

	// Optionally insert recording entry in database.
	if k.db != nil && finalPath != "" && frameCount > 0 {
		now := time.Now()
		duration := now.Sub(segStart).Seconds()

		// Calculate directory size for file_size metadata.
		var totalSize int64
		filepath.Walk(finalPath, func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				totalSize += info.Size()
			}
			return nil
		})

		rec := &model.Recording{
			ID:          strconv.FormatInt(now.UnixNano(), 10),
			CameraID:    k.cameraID,
			FilePath:    finalPath,
			Format:      model.FormatTimelapse,
			StartedAt:   segStart,
			EndedAt:     now,
			Duration:    duration,
			FrameCount:  frameCount,
			FileSize:    totalSize,
			MergeStatus: model.MergeStatusPending,
		}

		if err := k.db.InsertRecordingWithRetry(context.Background(), rec, 3, 500*time.Millisecond); err != nil {
			kfeLogger.Error(
				"failed to insert recording entry",
				"camera_id", k.cameraID,
				"error", err,
			)
		}

		// Trigger async rolling merge if merge manager is configured.
		if k.mergeMgr != nil {
			k.mergeMgr.StartSegmentMerge(context.Background(), k.cameraID, finalPath, finalPath+".mp4", rec.ID)
		}
	}

	if frameCount > 0 {
		kfeLogger.Info(
			"keyframe segment closed",
			"camera_id", k.cameraID,
			"frames", frameCount,
			"final_path", finalPath,
		)
	}
}

// copyAU deep-copies an access unit (slice of NALUs) so the original
// can be reused by the StreamHub without data races.
func copyAU(au [][]byte) [][]byte {
	result := make([][]byte, len(au))
	for i, nalu := range au {
		result[i] = append([]byte(nil), nalu...)
	}
	return result
}
