package hls

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bluenviron/gohlslib/v2"
	"github.com/bluenviron/gohlslib/v2/pkg/codecs"
	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph265"
	"github.com/pion/rtp"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
)

var hlsLogger = slog.Default().With("component", "hls-manager")

const (
	defaultIdleTimeout    = 60 * time.Second
	defaultMaxStreams     = 4
	defaultWriteBufSize   = 180              // buffered frames per stream (~9s at 20fps)
	defaultSegmentMaxSize = 10 * 1024 * 1024 // 10MB HLS segment max
	maxBackoff            = 16 * time.Second
	initialBackoff        = 1 * time.Second
	storageErrorBackoff   = 60 * time.Second // persistent storage failures (read-only, disk full, I/O)
)

// hlsFrame is an async write request for the HLS muxer.
type hlsFrame struct {
	pts int64
	au  [][]byte
}

// streamEntry holds a per-camera HLS muxer and its metadata.
type streamEntry struct {
	mu                sync.Mutex // protects lastUsed, lastFrameTime, and fpsCredit
	mux               *gohlslib.Muxer
	track             *gohlslib.Track
	dirPath           string
	lastUsed          time.Time
	cancel            context.CancelFunc
	frameCh           chan hlsFrame // async write buffer
	isH265            bool
	subStreamCancel   context.CancelFunc // cancels the sub-stream RTSP reader goroutine
	maxFPS            int
	lastFrameTime     time.Time
	fpsCredit         time.Duration // accumulated frame time credit for smooth FPS throttling
	idrReceived       bool          // true after first IDR frame is received
	consecutiveErrors int
	lastErrorTime     time.Time
	backoff           time.Duration
	observedSegments  map[string]bool
	// wg tracks the writeLoop + idleWatchdog goroutines so stopStreamLocked
	// can join them before returning. Without this, the goroutines briefly
	// outlive the entry (they touch entry.mux / entry.dirPath), leaking
	// during StopAll (#230). The wait runs AFTER cancel + map delete, so the
	// goroutines observe ctx.Done() and exit promptly.
	wg sync.WaitGroup
	// lastSegObserve throttles observeNewSegments: a full os.ReadDir of the segment
	// directory is only performed at most once per segmentObserveInterval. Without this
	// the scan ran after EVERY successful frame write (20-30 fps × N cameras = a
	// persistent directory-scan storm purely to collect segment-size metrics).
	lastSegObserve time.Time
}

// segmentObserveInterval is the minimum interval between segment-directory scans for
// metrics. Set well below a typical segment duration (2-10s) so sizes are still
// captured promptly, but high enough to eliminate the per-frame syscall storm.
const segmentObserveInterval = 2 * time.Second

// Manager manages on-demand HLS streams for cameras.
type Manager struct {
	mu              sync.RWMutex
	streams         map[string]*streamEntry // cameraID -> entry
	ctx             context.Context
	cancel          context.CancelFunc
	dataDir         string
	idleTimeout     time.Duration
	maxStreams      int
	writeBufSize    int
	segmentMaxSize  int
	segmentCount    int
	metrics         *metrics.Metrics
	lowLatency      bool          // enable Low-Latency HLS (MuxerVariantLowLatency)
	partMinDuration time.Duration // LL-HLS partial segment duration (default 200ms)
}

// newManager creates a new HLS Manager with default settings.
// Use NewManagerWithOpts for custom buffer/segment sizes.
func newManager(ctx context.Context, dataDir string) *Manager {
	ctx, cancel := context.WithCancel(ctx)
	return &Manager{
		ctx:            ctx,
		cancel:         cancel,
		streams:        make(map[string]*streamEntry),
		dataDir:        dataDir,
		idleTimeout:    defaultIdleTimeout,
		maxStreams:     defaultMaxStreams,
		writeBufSize:   defaultWriteBufSize,
		segmentMaxSize: defaultSegmentMaxSize,
		segmentCount:   3,
	}
}

// NewManagerWithOpts creates a new HLS Manager with custom buffer, segment sizes, and segment count.
// writeBufSize controls the async frame buffer per stream (default: 100).
// segmentMaxSize controls the maximum HLS segment file size in bytes (default: 10MB).
// segmentCount controls the number of HLS segments per stream (default: 7, range [3,10]).
func NewManagerWithOpts(ctx context.Context, dataDir string, writeBufSize, segmentMaxSize, segmentCount int, opts ...*metrics.Metrics) *Manager {
	if writeBufSize <= 0 {
		writeBufSize = defaultWriteBufSize
	}
	if segmentMaxSize <= 0 {
		segmentMaxSize = defaultSegmentMaxSize
	}
	if segmentCount <= 0 {
		segmentCount = 3
	}
	var m *metrics.Metrics
	if len(opts) > 0 {
		m = opts[0]
	}
	ctx, cancel := context.WithCancel(ctx)
	return &Manager{
		ctx:            ctx,
		cancel:         cancel,
		streams:        make(map[string]*streamEntry),
		dataDir:        dataDir,
		idleTimeout:    defaultIdleTimeout,
		maxStreams:     defaultMaxStreams,
		writeBufSize:   writeBufSize,
		segmentMaxSize: segmentMaxSize,
		segmentCount:   segmentCount,
		metrics:        m,
	}
}

// SetLowLatency enables Low-Latency HLS mode with the given partial segment duration.
// When enabled, the muxer uses MuxerVariantLowLatency (fMP4) for both H.264 and H.265,
// producing partial segments for sub-second live latency.
// partMinDuration controls the partial segment duration (default: 200ms).
// Must be called before any StartStream calls.
func (m *Manager) SetLowLatency(enabled bool, partMinDuration time.Duration) {
	m.lowLatency = enabled
	if partMinDuration > 0 {
		m.partMinDuration = partMinDuration
	}
}

// StartStream creates and starts an HLS muxer for the given camera.
// The caller must provide the H264 SPS and PPS NAL units (without start bytes).
func (m *Manager) StartStream(cameraID string, sps, pps []byte, maxFPS int) error {
	return m.startStream(cameraID, false, sps, pps, nil, maxFPS)
}

// StartStreamH265 creates and starts an HLS muxer for an H265 camera.
func (m *Manager) StartStreamH265(cameraID string, vps, sps, pps []byte, maxFPS int) error {
	return m.startStream(cameraID, true, sps, pps, vps, maxFPS)
}

func (m *Manager) startStream(cameraID string, isH265 bool, sps, pps, vps []byte, maxFPS int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Already active — just update lastUsed (check before eviction to avoid unnecessary evict)
	if entry, ok := m.streams[cameraID]; ok {
		entry.lastUsed = time.Now()
		return nil
	}

	// At capacity — evict least recently used stream
	if len(m.streams) >= m.maxStreams {
		m.evictLRULocked(cameraID)
	}

	// Already active — just update lastUsed
	if entry, ok := m.streams[cameraID]; ok {
		entry.lastUsed = time.Now()
		return nil
	}

	// Create per-camera directory
	dirPath := filepath.Join(m.dataDir, cameraID)
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		return err
	}

	mux, track, err := m.createMuxAndTrack(isH265, sps, pps, vps, dirPath)
	if err != nil {
		os.RemoveAll(dirPath)
		return err
	}

	ctx, cancel := context.WithCancel(m.ctx)
	entry := &streamEntry{
		mux:              mux,
		track:            track,
		dirPath:          dirPath,
		lastUsed:         time.Now(),
		cancel:           cancel,
		frameCh:          make(chan hlsFrame, m.writeBufSize),
		isH265:           isH265,
		maxFPS:           maxFPS,
		observedSegments: make(map[string]bool),
	}
	m.streams[cameraID] = entry

	// Start async writer goroutine for this stream. Tracked by entry.wg so
	// stopStreamLocked can join them (prevents a brief leak during StopAll, #230).
	entry.wg.Add(2)
	go func() { defer entry.wg.Done(); m.writeLoop(ctx, cameraID, entry) }()

	// Start idle watchdog
	go func() { defer entry.wg.Done(); m.idleWatchdog(ctx, cameraID) }()

	codecStr := "H264"
	if isH265 {
		codecStr = "H265"
	}
	mode := "standard"
	if m.lowLatency {
		mode = "low-latency"
	}
	hlsLogger.Info("HLS stream started", "camera_id", cameraID, "codec", codecStr, "mode", mode)
	if m.metrics != nil {
		m.metrics.HLSActiveStreams.WithLabelValues(cameraID).Set(1)
	}
	return nil
}

// createMuxAndTrack builds a gohlslib muxer + track for the given codec
// parameters. Shared between startStream (initial creation) and rebuildMuxer
// (recovery after a transient write error). The caller owns dirPath cleanup
// on error; this function only starts the muxer.
func (m *Manager) createMuxAndTrack(isH265 bool, sps, pps, vps []byte, dirPath string) (*gohlslib.Muxer, *gohlslib.Track, error) {
	var track *gohlslib.Track
	var mux *gohlslib.Muxer

	if m.lowLatency {
		// LL-HLS: fMP4 for both codecs, 1s segments + partials for sub-second latency.
		if isH265 {
			track = &gohlslib.Track{
				Codec:     &codecs.H265{VPS: vps, SPS: sps, PPS: pps},
				ClockRate: 90000,
			}
		} else {
			track = &gohlslib.Track{
				Codec:     &codecs.H264{SPS: sps, PPS: pps},
				ClockRate: 90000,
			}
		}
		mux = &gohlslib.Muxer{
			Tracks:             []*gohlslib.Track{track},
			Variant:            gohlslib.MuxerVariantLowLatency,
			SegmentCount:       m.segmentCount,
			SegmentMinDuration: 1 * time.Second,
			PartMinDuration:    m.partMinDuration,
			SegmentMaxSize:     uint64(m.segmentMaxSize),
			Directory:          dirPath,
		}
	} else if isH265 {
		track = &gohlslib.Track{
			Codec:     &codecs.H265{VPS: vps, SPS: sps, PPS: pps},
			ClockRate: 90000,
		}
		mux = &gohlslib.Muxer{
			Tracks:             []*gohlslib.Track{track},
			Variant:            gohlslib.MuxerVariantFMP4,
			SegmentCount:       m.segmentCount,
			SegmentMinDuration: 2 * time.Second,
			SegmentMaxSize:     uint64(m.segmentMaxSize),
			Directory:          dirPath,
		}
	} else {
		track = &gohlslib.Track{
			Codec:     &codecs.H264{SPS: sps, PPS: pps},
			ClockRate: 90000,
		}
		mux = &gohlslib.Muxer{
			Tracks:             []*gohlslib.Track{track},
			Variant:            gohlslib.MuxerVariantMPEGTS,
			SegmentCount:       m.segmentCount,
			SegmentMinDuration: 2 * time.Second,
			SegmentMaxSize:     uint64(m.segmentMaxSize),
			Directory:          dirPath,
		}
	}

	if err := mux.Start(); err != nil {
		return nil, nil, fmt.Errorf("muxer start: %w", err)
	}
	return mux, track, nil
}

// extractParamSets scans a H264/H265 access unit for its parameter set NALs
// (SPS/PPS for H264, VPS/SPS/PPS for H265). Returns ok=false if any required
// parameter is missing. Most cameras emit these immediately before each IDR.
func extractParamSets(au [][]byte, isH265 bool) (vps, sps, pps []byte, ok bool) {
	for _, nal := range au {
		if len(nal) == 0 {
			continue
		}
		if isH265 {
			// HEVC NAL type is the low 6 bits of (first byte >> 1).
			nalType := (nal[0] >> 1) & 0x3F
			switch nalType {
			case 32:
				vps = nal // VPS_NUT
			case 33:
				sps = nal // SPS_NUT
			case 34:
				pps = nal // PPS_NUT
			}
		} else {
			// AVC NAL type is the low 5 bits of the first byte.
			nalType := nal[0] & 0x1F
			switch nalType {
			case 7:
				sps = nal // SPS
			case 8:
				pps = nal // PPS
			}
		}
	}
	if isH265 {
		return vps, sps, pps, vps != nil && sps != nil && pps != nil
	}
	return nil, sps, pps, sps != nil && pps != nil
}

// rebuildMuxer recreates the HLS muxer for a stream whose previous muxer was
// destroyed by handleWriteError after a transient write failure (e.g. DTS
// non-monotonic from RTP packet loss). Must be called from writeLoop only
// (which owns entry.mux writes). The current frame must be an IDR carrying
// fresh parameter sets. Returns true on success.
func (m *Manager) rebuildMuxer(cameraID string, entry *streamEntry, au [][]byte) bool {
	vps, sps, pps, ok := extractParamSets(au, entry.isH265)
	if !ok {
		hlsLogger.Warn("HLS rebuild: IDR frame missing parameter sets, waiting for next IDR",
			"camera_id", cameraID, "consecutive_errors", entry.consecutiveErrors)
		return false
	}
	mux, track, err := m.createMuxAndTrack(entry.isH265, sps, pps, vps, entry.dirPath)
	if err != nil {
		hlsLogger.Warn("HLS rebuild: failed to create muxer",
			"camera_id", cameraID, "error", err, "consecutive_errors", entry.consecutiveErrors)
		return false
	}
	entry.mu.Lock()
	entry.mux = mux
	entry.track = track
	entry.mu.Unlock()
	// NOTE: consecutiveErrors/backoff are NOT reset here — muxer creation can
	// succeed on a read-only filesystem (the file write is deferred). They are
	// only cleared on a successful frame write in writeLoop, proving the path
	// is truly writable.
	if m.metrics != nil {
		m.metrics.HLSMuxerRestarts.WithLabelValues(cameraID).Inc()
	}
	hlsLogger.Info("HLS muxer rebuilt after transient failure", "camera_id", cameraID)
	return true
}

// StartSubStreamReader starts a separate RTSP connection to a sub-stream URL for HLS.
// It connects to subStreamURL, extracts codec parameters (SPS/PPS for H264, VPS/SPS/PPS for H265),
// and feeds frames to the HLS muxer for the given camera.
// If the sub-stream connection fails, it logs a warning and returns — the caller should fall back to main stream.
func (m *Manager) StartSubStreamReader(cameraID, subStreamURL string, isH265 bool, fallbackFn func()) error {
	m.mu.RLock()
	entry, ok := m.streams[cameraID]
	m.mu.RUnlock()

	if !ok {
		return ErrStreamNotActive
	}
	if entry.subStreamCancel != nil {
		return nil // already running
	}

	ctx, cancel := context.WithCancel(m.ctx)
	entry.subStreamCancel = cancel

	go m.readSubStream(ctx, cameraID, subStreamURL, isH265, entry, fallbackFn)

	hlsLogger.Info("HLS sub-stream reader started", "camera_id", cameraID, "sub_stream_url", subStreamURL)
	return nil
}

func (m *Manager) readSubStream(ctx context.Context, cameraID, rtspURL string, isH265 bool, entry *streamEntry, fallbackFn func()) {
	var err error
	defer func() {
		m.mu.Lock()
		if e, ok := m.streams[cameraID]; ok {
			e.subStreamCancel = nil
		}
		m.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			hlsLogger.Warn("HLS sub-stream reader exited, falling back to main stream", "camera_id", cameraID, "error", err)
			if fallbackFn != nil {
				fallbackFn()
			}
		}
	}()

	u, parseErr := base.ParseURL(rtspURL)
	if parseErr != nil {
		err = fmt.Errorf("invalid sub-stream RTSP URL: %w", parseErr)
		return
	}

	tcp := gortsplib.ProtocolTCP
	client := &gortsplib.Client{
		Scheme:   u.Scheme,
		Host:     u.Host,
		Protocol: &tcp,
	}

	if dialErr := client.Start(); dialErr != nil {
		err = fmt.Errorf("sub-stream client start: %w", dialErr)
		return
	}
	defer client.Close()

	desc, _, descErr := client.Describe(u)
	if descErr != nil {
		err = fmt.Errorf("sub-stream DESCRIBE: %w", descErr)
		return
	}

	if isH265 {
		err = m.readSubStreamH265(ctx, client, desc, cameraID, entry)
	} else {
		err = m.readSubStreamH264(ctx, client, desc, cameraID, entry)
	}
}

func (m *Manager) readSubStreamH264(ctx context.Context, client *gortsplib.Client, desc *description.Session, cameraID string, entry *streamEntry) error {
	var forma *format.H264
	medi := desc.FindFormat(&forma)
	if medi == nil {
		return fmt.Errorf("H264 media not found in sub-stream")
	}

	rtpDec, err := forma.CreateDecoder()
	if err != nil {
		return fmt.Errorf("sub-stream create RTP decoder: %w", err)
	}

	if _, err := client.Setup(desc.BaseURL, medi, 0, 0); err != nil {
		return fmt.Errorf("sub-stream SETUP: %w", err)
	}

	errCh := make(chan error, 1)

	client.OnPacketRTP(medi, forma, func(pkt *rtp.Packet) {
		select {
		case <-ctx.Done():
			return
		default:
		}

		aus, decErr := rtpDec.Decode(pkt)
		if decErr != nil {
			if !errors.Is(decErr, rtph264.ErrNonStartingPacketAndNoPrevious) && !errors.Is(decErr, rtph264.ErrMorePacketsNeeded) {
				hlsLogger.Warn("sub-stream RTP decode error", "camera_id", cameraID, "error", decErr)
			}
			return
		}
		_ = m.WriteH264(cameraID, int64(pkt.Timestamp), aus)
	})

	if _, playErr := client.Play(nil); playErr != nil {
		return fmt.Errorf("sub-stream PLAY: %w", playErr)
	}

	go func() { errCh <- client.Wait() }()

	select {
	case <-ctx.Done():
		client.Close()
		return nil
	case err = <-errCh:
		return err
	}
}

func (m *Manager) readSubStreamH265(ctx context.Context, client *gortsplib.Client, desc *description.Session, cameraID string, entry *streamEntry) error {
	var forma *format.H265
	medi := desc.FindFormat(&forma)
	if medi == nil {
		return fmt.Errorf("H265 media not found in sub-stream")
	}

	rtpDec, err := forma.CreateDecoder()
	if err != nil {
		return fmt.Errorf("sub-stream create RTP decoder: %w", err)
	}

	if _, err := client.Setup(desc.BaseURL, medi, 0, 0); err != nil {
		return fmt.Errorf("sub-stream SETUP: %w", err)
	}

	errCh := make(chan error, 1)

	client.OnPacketRTP(medi, forma, func(pkt *rtp.Packet) {
		select {
		case <-ctx.Done():
			return
		default:
		}

		aus, decErr := rtpDec.Decode(pkt)
		if decErr != nil {
			if !errors.Is(decErr, rtph265.ErrNonStartingPacketAndNoPrevious) && !errors.Is(decErr, rtph265.ErrMorePacketsNeeded) {
				hlsLogger.Warn("sub-stream RTP decode error", "camera_id", cameraID, "error", decErr)
			}
			return
		}
		_ = m.WriteH265(cameraID, int64(pkt.Timestamp), aus)
	})

	if _, playErr := client.Play(nil); playErr != nil {
		return fmt.Errorf("sub-stream PLAY: %w", playErr)
	}

	go func() { errCh <- client.Wait() }()

	select {
	case <-ctx.Done():
		client.Close()
		return nil
	case err = <-errCh:
		return err
	}
}

// writeLoop drains frames from the async buffer and writes them to the muxer.
// This ensures RTP receive path is never blocked by HLS disk I/O.
// On write error: increments error counter, destroys muxer, resets IDR flag,
// and applies exponential backoff before allowing re-creation.
func (m *Manager) writeLoop(ctx context.Context, cameraID string, entry *streamEntry) {
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-entry.frameCh:
			isIDR := isFirstNalIDR(frame.au, entry.isH265)
			traceID := "no-trace"
			if isIDR {
				traceID = fmt.Sprintf("%s-%d", cameraID, frame.pts)
			}
			slog.Debug(
				"frame_trace",
				"trace_id", traceID,
				"camera_id", cameraID,
				"stage", "hls_recv",
				"is_idr", isIDR,
			)
			if waitForFirstIDR(frame.au, entry.isH265, &entry.idrReceived) {
				continue
			}
			// Muxer may have been nilled by handleWriteError after a transient write
			// failure (e.g. non-monotonic DTS from RTP packet loss). Rebuild it on
			// the next IDR frame so HLS recovers instead of staying broken forever.
			if entry.mux == nil {
				if !isIDR {
					continue
				}
				if !m.rebuildMuxer(cameraID, entry, frame.au) {
					continue
				}
			}
			if err := writeFrameToMuxer(entry.isH265, entry.mux, entry.track, frame.au, frame.pts, cameraID); err != nil {
				slog.Warn(
					"frame_trace",
					"trace_id", traceID,
					"camera_id", cameraID,
					"stage", "hls_error",
					"is_idr", isIDR,
					"error", err,
				)
				m.handleWriteError(ctx, cameraID, entry, err)
			} else {
				slog.Debug(
					"frame_trace",
					"trace_id", traceID,
					"camera_id", cameraID,
					"stage", "hls_write",
					"is_idr", isIDR,
				)
				// Successful write — reset error tracking
				if entry.consecutiveErrors > 0 {
					entry.consecutiveErrors = 0
					entry.backoff = 0
				}
				// Observe new segment file sizes for metrics
				m.observeNewSegments(cameraID, entry)
			}
		}
	}
}

// isFirstNalIDR checks if any NAL unit in an access unit is an IDR frame.
// Checks all NALUs (not just the first) because some recorders prepend
// parameter sets (VPS/SPS/PPS) before the IDR slice, making au[0] a
// non-IDR NALU. This is the standard format from Xiaomi and ONVIF cameras.
// For H.264, NAL unit type 5 = IDR.
// For H.265, NAL unit types 19 (IDR_W_RADL) and 20 (IDR_N_LP) = IDR.
func isFirstNalIDR(au [][]byte, isH265 bool) bool {
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		if isH265 {
			// HEVC: forbidden_zero_bit(1) | nal_unit_type(6) | nuh_layer_id(6) | nuh_temporal_id_plus1(3)
			naluType := (nalu[0] >> 1) & 0x3F
			if naluType == 19 || naluType == 20 {
				return true
			}
		} else {
			// H.264: forbidden_zero_bit(1) | nal_ref_idc(2) | nal_unit_type(5)
			naluType := nalu[0] & 0x1F
			if naluType == 5 {
				return true
			}
		}
	}
	return false
}

// shouldThrottle implements credit-based FPS throttling.
// Returns true if the frame should be dropped (insufficient credit).
// Modifies fpsCredit and lastFrameTime in place.
// When maxFPS <= 0 (disabled), always returns false (never throttle).
func shouldThrottle(maxFPS int, fpsCredit *time.Duration, lastFrameTime *time.Time, now time.Time, isIDR bool) bool {
	if maxFPS <= 0 {
		return false
	}
	minInterval := time.Second / time.Duration(maxFPS)
	if lastFrameTime.IsZero() {
		*lastFrameTime = now
		*fpsCredit = 0
		return false // first frame always passes
	}
	elapsed := now.Sub(*lastFrameTime)
	*lastFrameTime = now
	*fpsCredit += elapsed
	if *fpsCredit < minInterval {
		if isIDR {
			return false // IDR always passes even with insufficient credit
		}
		return true // insufficient credit — drop
	}
	// Consume one interval of credit; cap surplus to prevent burst.
	*fpsCredit -= minInterval
	if *fpsCredit > minInterval*2 {
		*fpsCredit = minInterval * 2
	}
	return false
}

// waitForFirstIDR checks if a frame should be skipped while waiting for the first IDR.
// Returns true if the frame should be skipped (first IDR not yet received).
// Sets *idrReceived to true when the first IDR frame is detected.
func waitForFirstIDR(au [][]byte, isH265 bool, idrReceived *bool) bool {
	if *idrReceived {
		return false // already received IDR, don't skip
	}
	if !isFirstNalIDR(au, isH265) {
		return true // not an IDR frame, skip
	}
	*idrReceived = true
	return false // first IDR detected, don't skip
}

// writeFrameToMuxer writes a frame to the HLS muxer, dispatching to WriteH264 or WriteH265.
// Returns error from muxer write; caller is responsible for logging.
func writeFrameToMuxer(isH265 bool, mux *gohlslib.Muxer, track *gohlslib.Track, au [][]byte, pts int64, cameraID string) error {
	if mux == nil || track == nil {
		return fmt.Errorf("hls muxer not initialized for camera %s", cameraID)
	}
	if isH265 {
		return mux.WriteH265(track, time.Now(), pts, au)
	}
	return mux.WriteH264(track, time.Now(), pts, au)
}

// calculateBackoff computes exponential backoff: min(maxBackoff, initialBackoff << errors).
func calculateBackoff(consecutiveErrors int) time.Duration {
	// Cap shift to avoid undefined behavior for large consecutiveErrors
	shift := consecutiveErrors
	if shift > 4 {
		return maxBackoff // 1s << 5+ exceeds 16s cap
	}
	backoff := initialBackoff << shift
	if backoff > maxBackoff {
		return maxBackoff
	}
	return backoff
}

// handleWriteError handles a muxer write error by incrementing metrics,
// destroying the muxer, resetting the IDR flag, and sleeping with backoff.
func (m *Manager) handleWriteError(ctx context.Context, cameraID string, entry *streamEntry, err error) {
	entry.consecutiveErrors++
	storageFailed := isPersistentStorageError(err)
	if storageFailed {
		entry.backoff = storageErrorBackoff
	} else {
		entry.backoff = calculateBackoff(entry.consecutiveErrors)
	}
	entry.lastErrorTime = time.Now()

	hlsLogger.Error(
		"HLS write error",
		"camera_id", cameraID,
		"error", err,
		"consecutive_errors", entry.consecutiveErrors,
		"backoff", entry.backoff,
		"storage_failed", storageFailed,
	)

	if m.metrics != nil {
		m.metrics.HLSWriteErrors.WithLabelValues(cameraID).Inc()
		m.metrics.HLSMuxerRestarts.WithLabelValues(cameraID).Inc()
	}

	// Destroy old muxer so it will be recreated on next write
	entry.mu.Lock()
	// Destroy old muxer so it will be recreated on next write (rebuilt by
	// rebuildMuxer when the next IDR frame arrives in writeLoop).
	if entry.mux != nil {
		entry.mux.Close()
		entry.mux = nil
	}
	entry.track = nil
	entry.mu.Unlock()
	entry.idrReceived = false // force wait for next IDR

	// Sleep with backoff (interruptible by context cancellation)
	select {
	case <-ctx.Done():
		return
	case <-time.After(entry.backoff):
	}
}

// isPersistentStorageError reports whether err indicates a storage-layer
// failure that won't be resolved by rebuilding the muxer (e.g. filesystem
// remounted read-only, disk full, I/O error). For these we use a long fixed
// backoff instead of the short exponential one, since retrying every few
// seconds only produces log spam without resolving the underlying issue.
func isPersistentStorageError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EROFS) || errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EIO) {
		return true
	}
	// gohlslib wraps file errors without syscall unwrapping, so fall back to
	// substring matching for the common kernel error messages.
	s := err.Error()
	return strings.Contains(s, "read-only file system") ||
		strings.Contains(s, "no space left") ||
		strings.Contains(s, "input/output error")
}

// StopStream stops the HLS muxer for the given camera and cleans up temp files.
func (m *Manager) StopStream(cameraID string) {
	m.mu.Lock()
	wg := m.stopStreamLocked(cameraID)
	m.mu.Unlock()
	// Join writeLoop + idleWatchdog OUTSIDE m.mu. Joining under the lock would
	// deadlock if idleWatchdog is concurrently in its own m.StopStream path
	// (waiting on m.mu). See stopStreamLocked audit (#230).
	if wg != nil {
		wg.Wait()
	}
}

// EvictStream stops and removes an active HLS stream, freeing a slot.
// Returns ErrStreamNotActive if the stream is not running.
func (m *Manager) EvictStream(cameraID string) error {
	m.mu.Lock()
	if _, ok := m.streams[cameraID]; !ok {
		m.mu.Unlock()
		return ErrStreamNotActive
	}
	wg := m.stopStreamLocked(cameraID)
	m.mu.Unlock()
	if wg != nil {
		wg.Wait() // join outside m.mu — see StopStream (#230)
	}
	return nil
}

// GetActiveStreamCount returns the number of currently active HLS streams.
func (m *Manager) GetActiveStreamCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.streams)
}

// evictLRULocked finds and evicts the stream with the oldest lastUsed timestamp.
// Caller must hold m.mu write lock. The newStreamID is excluded from eviction.
func (m *Manager) evictLRULocked(newStreamID string) {
	var oldestID string
	var oldestTime time.Time
	for id, entry := range m.streams {
		if id == newStreamID {
			continue
		}
		if oldestID == "" || entry.lastUsed.Before(oldestTime) {
			oldestID = id
			oldestTime = entry.lastUsed
		}
	}
	if oldestID == "" {
		return
	}
	hlsLogger.Warn("HLS max streams reached, evicting LRU stream", "camera_id", oldestID)
	if m.metrics != nil {
		m.metrics.HLSIdleEvictions.WithLabelValues(oldestID).Inc()
	}
	// Discard the returned WaitGroup: this runs under m.mu (EnsureStream holds
	// the write lock), so joining here would deadlock with idleWatchdog's own
	// m.StopStream path. The evicted goroutines exit within ≤ idleTimeout/2 of
	// the cancel and only read their (already-closed) mux, so the brief leak is
	// benign. StopStream/StopAll/EvictStream — the user-facing stop paths — do
	// join. See stopStreamLocked audit (#230).
	_ = m.stopStreamLocked(oldestID)
}

// stopStreamLocked stops a stream. Caller must hold m.mu write lock.
// Returns the stopped entry's WaitGroup (non-nil if a stream was stopped) so
// the caller can join writeLoop + idleWatchdog AFTER releasing m.mu. Joining
// under m.mu would deadlock: idleWatchdog's idle-timeout path calls
// m.StopStream (m.mu.Lock), so a wait while holding m.mu can stall forever.
//
// # Lock-order audit (#230): no m.mu ↔ entry.mu cycle exists.
//
// Every site that takes entry.mu first releases m.mu:
//   - writeFrame:   m.mu.RLock → read entry → m.mu.RUnlock → entry.mu.Lock …
//   - idleWatchdog: m.mu.RLock → read entry → m.mu.RUnlock → entry.mu.Lock …
//
// And this method (under m.mu) NEVER takes entry.mu — it only calls
// cancel/Close/RemoveAll. So the lock order is uniformly
// m.mu → (release) → entry.mu, with no reverse acquisition. The idleWatchdog
// does call m.StopStream (m.mu.Lock) on idle timeout, but by then it has
// already released entry.mu, so there is no nested hold.
func (m *Manager) stopStreamLocked(cameraID string) *sync.WaitGroup {
	entry, ok := m.streams[cameraID]
	if !ok {
		return nil
	}

	entry.cancel()
	if entry.subStreamCancel != nil {
		entry.subStreamCancel()
		entry.subStreamCancel = nil
	}
	if entry.mux != nil {
		entry.mux.Close()
	}

	// Clean up segment directory
	os.RemoveAll(entry.dirPath)

	delete(m.streams, cameraID)
	if m.metrics != nil {
		m.metrics.HLSActiveStreams.WithLabelValues(cameraID).Set(0)
	}
	hlsLogger.Info("HLS stream stopped", "camera_id", cameraID)
	return &entry.wg
}

// WriteH264 queues an H264 access unit for async writing to the HLS stream.
// This is non-blocking — it acquires a read lock only briefly and never blocks on disk I/O.
// If the write buffer is full, the frame is silently dropped to protect the recording pipeline.
func (m *Manager) WriteH264(cameraID string, pts int64, au [][]byte) error {
	return m.writeFrame(cameraID, pts, au)
}

// WriteH265 queues an H265 access unit for async writing to the HLS stream.
// Same non-blocking semantics as WriteH264.
func (m *Manager) WriteH265(cameraID string, pts int64, au [][]byte) error {
	return m.writeFrame(cameraID, pts, au)
}

func (m *Manager) writeFrame(cameraID string, pts int64, au [][]byte) error {
	m.mu.RLock()
	entry, ok := m.streams[cameraID]
	m.mu.RUnlock()

	if !ok {
		return nil // stream not active, silently ignore
	}

	entry.mu.Lock()
	entry.lastUsed = time.Now()

	// Credit-based FPS throttling: accumulate elapsed time between frames,
	// send only when enough credit has accumulated for one interval.
	// This produces consistent frame intervals instead of jittery drops.
	if shouldThrottle(entry.maxFPS, &entry.fpsCredit, &entry.lastFrameTime, time.Now(), nalutil.IsIDR(au, entry.isH265)) {
		isIDR := nalutil.IsIDR(au, entry.isH265)
		traceID := "no-trace"
		if isIDR {
			traceID = fmt.Sprintf("%s-%d", cameraID, pts)
		}
		slog.Debug(
			"frame_trace",
			"trace_id", traceID,
			"camera_id", cameraID,
			"stage", "hls_drop",
			"is_idr", isIDR,
			"reason", "fps_throttle",
		)
		if m.metrics != nil {
			m.metrics.HLSFramesDropped.WithLabelValues(cameraID).Inc()
		}
		entry.mu.Unlock()
		return nil
	}

	entry.mu.Unlock()

	// Non-blocking send — drop frame if buffer full to protect recording pipeline
	select {
	case entry.frameCh <- hlsFrame{pts: pts, au: au}:
	default:
		// Buffer full, drop frame. Live view tolerates dropped frames.
		isIDR := nalutil.IsIDR(au, entry.isH265)
		traceID := "no-trace"
		if isIDR {
			traceID = fmt.Sprintf("%s-%d", cameraID, pts)
		}
		slog.Debug(
			"frame_trace",
			"trace_id", traceID,
			"camera_id", cameraID,
			"stage", "hls_drop",
			"is_idr", isIDR,
			"reason", "buffer_full",
			"queue_depth", len(entry.frameCh),
		)
		if m.metrics != nil {
			m.metrics.HLSFramesDropped.WithLabelValues(cameraID).Inc()
		}

	}

	return nil
}

// IsActive returns true if an HLS stream is active for the given camera.
func (m *Manager) IsActive(cameraID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.streams[cameraID]
	return ok
}

// CodecFor returns the video codec frozen into the HLS muxer for the given
// camera at stream-start time. Unlike the recorder's CodecParams(), this value
// persists across recorder reconnects (the muxer track is not torn down on a
// P2P blip), so callers that need the codec while a recorder is mid-reconnect
// (e.g. /protocols routing, which otherwise falls back to a stale DB value)
// get the correct answer. ok is false when no HLS stream entry exists for the
// camera (never started, idle-evicted, or server just restarted with no viewer).
func (m *Manager) CodecFor(cameraID string) (codec model.Format, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, found := m.streams[cameraID]
	if !found {
		return "", false
	}
	if entry.isH265 {
		return model.FormatH265, true
	}
	return model.FormatH264, true
}

// GetStreamStatus returns whether a stream is active for the given camera.
// Returns (active, nil) — use IsActive() for simple boolean check.
// This method is designed for API responses that include stream metadata.
func (m *Manager) GetStreamStatus(cameraID string) (active bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.streams[cameraID]
	return ok
}

// Handle proxies an HTTP request to the HLS muxer for the given camera.
// Returns false if the stream is not active.
// Includes a 30s timeout to prevent indefinite blocking when the muxer
// has no segments (e.g. stale Hub consumer after idle eviction).
func (m *Manager) Handle(cameraID string, w http.ResponseWriter, r *http.Request) bool {
	m.mu.RLock()
	entry, ok := m.streams[cameraID]
	m.mu.RUnlock()

	if !ok {
		return false
	}

	entry.mu.Lock()
	entry.lastUsed = time.Now()
	// Snapshot muxer under the lock. handleWriteError sets entry.mux = nil
	// on write failures; an HTTP request arriving in that window would
	// dereference nil and panic the whole process (SIGSEGV addr=0xd0).
	mux := entry.mux
	entry.mu.Unlock()

	if mux == nil {
		// Muxer destroyed by write-error recovery and not yet recreated.
		// Returning false lets the caller (HTTP handler) respond 404 so the
		// frontend retries; the stream re-initialises on next start.
		hlsLogger.Warn("HLS Handle: muxer not initialized", "camera_id", cameraID)
		return false
	}

	// Guard against muxer blocking forever when no frames arrive.
	// The muxer blocks on m3u8 requests until the first segment is ready.
	// If no frames reach the muxer (e.g. Hub subscription failed), this
	// timeout ensures the HTTP request eventually returns.
	const handleTimeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(r.Context(), handleTimeout)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Recover from panics that occur when the HTTP handler context is
		// cancelled (e.g. client disconnect or timeout) but gohlslib's
		// mux.Handle goroutine still tries to write to the response writer.
		// Without this recover, the panic crashes the entire process.
		defer func() {
			if rv := recover(); rv != nil {
				hlsLogger.Warn("HLS Handle recovered from panic",
					"camera_id", cameraID, "panic", rv)
			}
		}()
		mux.Handle(w, r)
	}()

	select {
	case <-done:
		return true
	case <-ctx.Done():
		hlsLogger.Warn("HLS Handle timed out or cancelled", "camera_id", cameraID, "timeout", handleTimeout)
		return true
	}
}

// StopAll stops all active HLS streams and cancels the manager context.
func (m *Manager) StopAll() {
	m.mu.Lock()
	var wgs []*sync.WaitGroup
	for id := range m.streams {
		if wg := m.stopStreamLocked(id); wg != nil {
			wgs = append(wgs, wg)
		}
	}
	m.cancel()
	m.mu.Unlock()
	// Join all goroutines outside m.mu — see StopStream (#230).
	for _, wg := range wgs {
		wg.Wait()
	}
}

// SubscribeToHub subscribes the HLS manager to a StreamHub for the given camera.
// It sets the HLS consumer callback ("hls") and registers an additional drop
// callback to increment the hls_frames_dropped_total Prometheus counter.
// #469: uses AddOnDrop (callback list) — the old direct assignment silently
// destroyed the camera manager's hub-level Prometheus wiring as soon as any
// HLS subscriber attached.
func (m *Manager) SubscribeToHub(cameraID string, hub *model.StreamHub, isH265 bool) error {
	if m.metrics != nil {
		hub.AddOnDrop(func(id string, isIDR bool) {
			if id == "hls" {
				m.metrics.HLSFramesDropped.WithLabelValues(cameraID).Inc()
			}
		})
	}
	if isH265 {
		return hub.Subscribe("hls", func(pts int64, au [][]byte) {
			_ = m.WriteH265(cameraID, pts, au)
		})
	}
	return hub.Subscribe("hls", func(pts int64, au [][]byte) {
		_ = m.WriteH264(cameraID, pts, au)
	})
}

func (m *Manager) idleWatchdog(ctx context.Context, cameraID string) {
	ticker := time.NewTicker(m.idleTimeout / 2)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.mu.RLock()
			entry, ok := m.streams[cameraID]
			m.mu.RUnlock()

			if !ok {
				return
			}
			entry.mu.Lock()
			lastUsed := entry.lastUsed
			entry.mu.Unlock()
			if time.Since(lastUsed) > m.idleTimeout {
				hlsLogger.Info("HLS stream idle timeout, stopping", "camera_id", cameraID)
				if m.metrics != nil {
					m.metrics.HLSIdleEvictions.WithLabelValues(cameraID).Inc()
				}
				m.StopStream(cameraID)
				return
			}
		}
	}
}

// observeNewSegments scans the segment directory for new files and reports sizes.
// Throttled to at most one os.ReadDir per segmentObserveInterval per stream to avoid
// a per-frame directory-scan storm (was called after every successful frame write).
func (m *Manager) observeNewSegments(cameraID string, entry *streamEntry) {
	if m.metrics == nil {
		return
	}
	now := time.Now()
	if now.Sub(entry.lastSegObserve) < segmentObserveInterval {
		return
	}
	entry.lastSegObserve = now
	entries, err := os.ReadDir(entry.dirPath)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".m3u8") || strings.HasSuffix(name, ".tmp") {
			continue
		}
		if entry.observedSegments[name] {
			continue
		}
		entry.observedSegments[name] = true
		info, err := e.Info()
		if err != nil {
			continue
		}
		m.metrics.HLSSegmentSizeBytes.WithLabelValues(cameraID).Observe(float64(info.Size()))
	}
}
