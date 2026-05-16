package plugin

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	gen "github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
)

var frLogger = slog.Default().With("component", "frame-receiver")

const defaultFrameReceiverSegDur = 10 * time.Minute

// FrameReceiver receives NAL frames from gRPC plugin processes and manages
// MP4Muxer + Segment lifecycle. It handles codec detection, IDR boundary
// detection, and segment creation/close/rename/DB-insert.
type FrameReceiver struct {
	store   SegmentStore
	db      RecordingDB
	metrics *metrics.Metrics
	segDur  time.Duration

	cameraID string

	mu sync.Mutex

	// current segment state
	muxer       *muxer.MP4Muxer
	trackID     int
	curTempPath string
	curFinalPath string
	segStart    time.Time
	frameCount  int
	lastFrameTime time.Time

	// codec state
	codecDetected bool
	codec         model.Format
	sps           []byte
	pps           []byte
	vps           []byte // H265 only
}

// NewFrameReceiver creates a new FrameReceiver for the given camera.
func NewFrameReceiver(store SegmentStore, db RecordingDB, m *metrics.Metrics, cameraID string, segDur time.Duration) *FrameReceiver {
	if segDur == 0 {
		segDur = defaultFrameReceiverSegDur
	}
	return &FrameReceiver{
		store:    store,
		db:       db,
		metrics:  m,
		cameraID: cameraID,
		segDur:   segDur,
	}
}

// HandleFrame processes a single frame received from the gRPC plugin stream.
// It handles codec detection, IDR-triggered segment splits, muxer lifecycle,
// and segment finalization.
func (r *FrameReceiver) HandleFrame(ctx context.Context, frame *gen.Frame) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Codec info frames carry SPS/PPS/VPS but are not written to the muxer.
	if frame.IsCodecInfo {
		r.handleCodecInfo(frame)
		return nil
	}

	// Discard frames until codec is detected.
	if !r.codecDetected {
		frLogger.Warn("discarding frame, codec not yet detected", "camera_id", r.cameraID)
		return nil
	}

	// IDR frame triggers segment split if we have an active segment.
	if frame.IsIdr && r.muxer != nil {
		r.closeCurrentSegment(ctx)
	}

	// Wait for IDR before starting a new segment.
	if r.muxer == nil && !frame.IsIdr {
		return nil
	}

	// No muxer yet — create new segment on IDR frame.
	if r.muxer == nil {
		if err := r.startNewSegment(); err != nil {
			return fmt.Errorf("start segment: %w", err)
		}
	}

	// Calculate PTS relative to segment start and frame duration.
	now := time.Now()
	pts := now.Sub(r.segStart)
	duration := now.Sub(r.lastFrameTime)
	if duration < time.Millisecond {
		duration = time.Millisecond
	}
	r.lastFrameTime = now

	if err := r.muxer.WriteSample(r.trackID, frame.Data, pts, duration); err != nil {
		frLogger.Error("failed to write sample", "camera_id", r.cameraID, "error", err)
		return nil // non-fatal: don't kill the stream
	}

	r.frameCount++

	// Check segment duration.
	if time.Since(r.segStart) >= r.segDur {
		r.closeCurrentSegment(ctx)
	}

	return nil
}

// Close finalizes any open segment and releases resources.
func (r *FrameReceiver) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeCurrentSegment(context.Background())
	return nil
}

// handleCodecInfo processes codec parameter frames (SPS/PPS/VPS).
func (r *FrameReceiver) handleCodecInfo(frame *gen.Frame) {
	// Detect codec type from the frame's Codec field.
	if !r.codecDetected && frame.Codec != gen.Codec_CODEC_UNSPECIFIED {
		switch frame.Codec {
		case gen.Codec_CODEC_H264:
			r.codec = model.FormatH264
		case gen.Codec_CODEC_H265:
			r.codec = model.FormatH265
		}
		r.codecDetected = true
		frLogger.Info("codec detected", "camera_id", r.cameraID, "codec", r.codec)
	}

	// Store parameter sets from extra map or from frame data.
	// Plugins may send SPS/PPS/VPS via Extra map keys or directly as frame data.
	if frame.Extra != nil {
		if spsHex, ok := frame.Extra["sps_hex"]; ok && spsHex != "" {
			r.sps = []byte(spsHex) // stored as raw bytes, not hex-decoded here
		}
		if ppsHex, ok := frame.Extra["pps_hex"]; ok && ppsHex != "" {
			r.pps = []byte(ppsHex)
		}
		if vpsHex, ok := frame.Extra["vps_hex"]; ok && vpsHex != "" {
			r.vps = []byte(vpsHex)
		}
	}

	// Also check if codec info is sent as NAL data — detect NAL type directly.
	if len(frame.Data) > 0 {
		switch r.codec {
		case model.FormatH264:
			r.handleH264CodecNAL(frame.Data)
		case model.FormatH265:
			r.handleH265CodecNAL(frame.Data)
		default:
			// Codec not yet known; try to probe from the frame's Codec enum.
			if frame.Codec == gen.Codec_CODEC_H264 {
				r.codec = model.FormatH264
				r.codecDetected = true
				r.handleH264CodecNAL(frame.Data)
			} else if frame.Codec == gen.Codec_CODEC_H265 {
				r.codec = model.FormatH265
				r.codecDetected = true
				r.handleH265CodecNAL(frame.Data)
			}
		}
	}
}

// handleH264CodecNAL processes H.264 SPS/PPS NALUs from codec info frames.
func (r *FrameReceiver) handleH264CodecNAL(data []byte) {
	if len(data) < 2 {
		return
	}
	// Skip Annex B start code if present (00 00 00 01 or 00 00 01).
	nalu := skipStartCode(data)
	if len(nalu) == 0 {
		return
	}
	naluType := nalu[0] & 0x1F
	switch naluType {
	case 7: // SPS
		r.sps = append([]byte(nil), nalu...)
	case 8: // PPS
		r.pps = append([]byte(nil), nalu...)
	}
}

// handleH265CodecNAL processes H.265 VPS/SPS/PPS NALUs from codec info frames.
func (r *FrameReceiver) handleH265CodecNAL(data []byte) {
	if len(data) < 3 {
		return
	}
	nalu := skipStartCode(data)
	if len(nalu) == 0 {
		return
	}
	naluType := (nalu[0] >> 1) & 0x3F
	switch naluType {
	case 32: // VPS
		r.vps = append([]byte(nil), nalu...)
	case 33: // SPS
		r.sps = append([]byte(nil), nalu...)
	case 34: // PPS
		r.pps = append([]byte(nil), nalu...)
	}
}

// startNewSegment creates a new segment file and MP4Muxer.
func (r *FrameReceiver) startNewSegment() error {
	// Validate codec params before creating segment.
	switch r.codec {
	case model.FormatH264:
		if r.sps == nil || r.pps == nil {
			return fmt.Errorf("missing H264 codec params (SPS=%v PPS=%v)", r.sps != nil, r.pps != nil)
		}
	case model.FormatH265:
		if r.vps == nil || r.sps == nil || r.pps == nil {
			return fmt.Errorf("missing H265 codec params (VPS=%v SPS=%v PPS=%v)", r.vps != nil, r.sps != nil, r.pps != nil)
		}
	default:
		return fmt.Errorf("unsupported codec: %s", r.codec)
	}

	tempPath, finalPath, err := r.store.CreateSegment(r.cameraID, string(r.codec))
	if err != nil {
		return fmt.Errorf("create segment: %w", err)
	}

	m := muxer.NewMP4Muxer(tempPath)

	var trackID int
	switch r.codec {
	case model.FormatH264:
		trackID, err = m.AddH264Track(r.sps, r.pps)
	case model.FormatH265:
		trackID, err = m.AddH265Track(r.vps, r.sps, r.pps)
	}
	if err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("add %s track: %w", r.codec, err)
	}

	r.muxer = m
	r.trackID = trackID
	r.curTempPath = tempPath
	r.curFinalPath = finalPath
	r.segStart = time.Now()
	r.lastFrameTime = r.segStart
	r.frameCount = 0

	return nil
}

// closeCurrentSegment finalizes the current segment: close muxer, atomic rename, DB insert, metrics.
func (r *FrameReceiver) closeCurrentSegment(ctx context.Context) {
	if r.muxer == nil {
		return
	}

	if err := r.muxer.Close(); err != nil {
		frLogger.Error("failed to close muxer", "camera_id", r.cameraID, "error", err)
		if r.curTempPath != "" {
			os.Remove(r.curTempPath)
		}
		r.resetSegment()
		return
	}

	// Atomic rename: temp → final.
	if r.curTempPath != "" && r.curFinalPath != "" {
		if err := r.store.CloseSegment(r.curTempPath, r.curFinalPath); err != nil {
			frLogger.Error("failed to close segment", "camera_id", r.cameraID, "error", err)
		}
	}

	// Insert recording into database.
	var fileSize int64
	if r.db != nil && r.curFinalPath != "" {
		now := time.Now()
		duration := now.Sub(r.segStart).Seconds()
		rec := &model.Recording{
			ID:         fmt.Sprintf("%d", now.UnixNano()),
			CameraID:   r.cameraID,
			FilePath:   r.curFinalPath,
			Format:     r.codec,
			StartedAt:  r.segStart,
			EndedAt:    now,
			Duration:   duration,
			FrameCount: r.frameCount,
		}
		if info, err := os.Stat(r.curFinalPath); err == nil {
			fileSize = info.Size()
			rec.FileSize = fileSize
		}
		if err := r.db.InsertRecordingWithRetry(ctx, rec, 3, 500*time.Millisecond); err != nil {
			frLogger.Error("failed to insert recording", "camera_id", r.cameraID, "error", err)
		}
	}

	// Update metrics.
	if r.frameCount > 0 && r.curFinalPath != "" {
		r.recordSegmentCreated()
		if fileSize > 0 {
			r.recordBytes(fileSize)
		}
	}

	r.resetSegment()
}

// resetSegment clears current segment state.
func (r *FrameReceiver) resetSegment() {
	r.muxer = nil
	r.curTempPath = ""
	r.curFinalPath = ""
	r.frameCount = 0
}

// recordSegmentCreated increments the segments created counter.
func (r *FrameReceiver) recordSegmentCreated() {
	if r.metrics != nil {
		r.metrics.SegmentsCreated.WithLabelValues(r.cameraID, string(r.codec)).Inc()
	}
}

// recordBytes adds to the recording bytes counter.
func (r *FrameReceiver) recordBytes(bytes int64) {
	if r.metrics != nil {
		r.metrics.RecordingBytesTotal.WithLabelValues(r.cameraID, string(r.codec)).Add(float64(bytes))
	}
}

// skipStartCode skips Annex B start code prefix (00 00 00 01 or 00 00 01)
// and returns the NAL unit data without the start code.
func skipStartCode(data []byte) []byte {
	if len(data) >= 4 && data[0] == 0 && data[1] == 0 && data[2] == 0 && data[3] == 1 {
		return data[4:]
	}
	if len(data) >= 3 && data[0] == 0 && data[1] == 0 && data[2] == 1 {
		return data[3:]
	}
	return data
}
