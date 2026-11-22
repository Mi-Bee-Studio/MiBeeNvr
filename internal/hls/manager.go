package hls

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"

	"github.com/bluenviron/gohlslib/v2"
	"github.com/bluenviron/gohlslib/v2/pkg/codecs"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/frametrace"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

var hlsLogger = slogx.Component("hls-manager")

// touchLastUsed stamps the entry's LRU clock. Race-free from any lock domain
// (see the lastUsed field comment).
func (e *streamEntry) touchLastUsed() {
	now := time.Now()
	e.lastUsed.Store(&now)
}

// lastUsedTime snapshots the entry's LRU clock; the zero Time means the entry
// was never stamped.
func (e *streamEntry) lastUsedTime() time.Time {
	if p := e.lastUsed.Load(); p != nil {
		return *p
	}
	return time.Time{}
}

// setLastUsed overwrites the LRU clock with a specific instant (tests build
// entries with deliberate orderings; runtime code uses touchLastUsed).
func (e *streamEntry) setLastUsed(t time.Time) {
	e.lastUsed.Store(&t)
}

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
	mu      sync.Mutex // protects lastFrameTime and fpsCredit
	mux     *gohlslib.Muxer
	track   *gohlslib.Track
	dirPath string
	// lastUsed feeds the LRU/idle decisions and is written from two lock
	// domains: startStream/evictLRULocked hold m.mu (which they must NOT
	// extend to entry.mu — the #230 lock-order audit keeps the order
	// m.mu → release → entry.mu), while writeFrame/Handle write it under
	// entry.mu. An immutable *time.Time behind atomic.Pointer is race-free
	// from both domains without nesting the locks (#1002).
	lastUsed          atomic.Pointer[time.Time]
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
	// hub is the StreamHub the entry's "hls" consumer is subscribed to.
	// Set by SubscribeToHub; compared by the sub-stream recycler (#513) to
	// decide whether this entry still belongs to a recycled pull generation.
	hub *streamhub.StreamHub
	// prevMuxers holds muxers retired by write-error recovery, still serving
	// their pre-rebuild names for legacyMuxerGrace (see legacy_muxers.go).
	// Guarded by entry.mu: written from writeLoop (handleWriteError / reap),
	// read from Handle on HTTP goroutines.
	prevMuxers []*legacyMuxer
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
	// Cached codec parameter sets: seeded at startStream from the recorder's
	// snapshot (possibly SDP sprop-seeded) and refreshed by writeLoop when
	// fresher in-band NALs appear. Owned by writeLoop after startStream
	// returns (seed happens-before goroutine spawn); read on the same
	// goroutine by withCachedParamSets and rebuildMuxer.
	sps, pps, vps []byte
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

// SetMaxStreams overrides the concurrent HLS stream budget — the LRU eviction
// threshold shared by all clients. Values < 1 fall back to the default.
// Must be called before any StartStream calls.
func (m *Manager) SetMaxStreams(n int) {
	if n < 1 {
		n = defaultMaxStreams
	}
	m.maxStreams = n
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
		entry.touchLastUsed()
		return nil
	}

	// At capacity — evict least recently used stream
	if len(m.streams) >= m.maxStreams {
		m.evictLRULocked(cameraID)
	}

	// Already active — just update lastUsed
	if entry, ok := m.streams[cameraID]; ok {
		entry.touchLastUsed()
		return nil
	}

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
		cancel:           cancel,
		frameCh:          make(chan hlsFrame, m.writeBufSize),
		isH265:           isH265,
		maxFPS:           maxFPS,
		observedSegments: make(map[string]bool),
		sps:              sps,
		pps:              pps,
		vps:              vps,
	}
	entry.touchLastUsed()
	m.streams[cameraID] = entry

	// Start async writer goroutine for this stream. Tracked by entry.wg so
	// stopStreamLocked can join them (prevents a brief leak during StopAll, #230).
	entry.wg.Add(2)
	go func() { defer entry.wg.Done(); m.writeLoop(ctx, cameraID, entry) }()

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
			au := frame.au
			isIDR := isFirstNalIDR(au, entry.isH265)
			traceID := "no-trace"
			if isIDR {
				traceID = fmt.Sprintf("%s-%d", cameraID, frame.pts)
			}
			if frametrace.Active(cameraID) {
				frametrace.Log(
					cameraID,
					"trace_id", traceID,
					"camera_id", cameraID,
					"stage", "hls_recv",
					"is_idr", isIDR,
				)
			}
			if waitForFirstIDR(au, entry.isH265, &entry.idrReceived) {
				continue
			}
			// Keep the entry's parameter-set cache fresh (in-band bytes are
			// newer than the startStream seed on a codec change), then make
			// IDR AUs self-contained: cameras that signal SPS/PPS only via
			// SDP sprop (out-of-band, RFC 6184) otherwise fail gohlslib's
			// DTS extraction on every keyframe (#772).
			refreshParamCache(entry, au)
			if isIDR {
				au = withCachedParamSets(au, entry)
			}
			// Muxer may have been nilled by handleWriteError after a transient write
			// failure (e.g. non-monotonic DTS from RTP packet loss). Rebuild it on
			// the next IDR frame so HLS recovers instead of staying broken forever.
			if entry.mux == nil {
				if !isIDR {
					continue
				}
				if !m.rebuildMuxer(cameraID, entry, au) {
					continue
				}
			}
			if err := writeFrameToMuxer(entry.isH265, entry.mux, entry.track, au, frame.pts, cameraID); err != nil {
				frametrace.LogDrop(
					cameraID,
					"trace_id", traceID,
					"camera_id", cameraID,
					"stage", "hls_error",
					"is_idr", isIDR,
					"error", err,
				)
				m.handleWriteError(ctx, cameraID, entry, err)
			} else {
				if frametrace.Active(cameraID) {
					frametrace.Log(
						cameraID,
						"trace_id", traceID,
						"camera_id", cameraID,
						"stage", "hls_write",
						"is_idr", isIDR,
					)
				}
				// Successful write — reset error tracking
				if entry.consecutiveErrors > 0 {
					entry.consecutiveErrors = 0
					entry.backoff = 0
				}
				// Retired-muxer grace stashes (write-error recovery) expire
				// here on the healthy path — a cheap no-op while none exist.
				reapRetiredMuxers(entry)
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

	// Retire the old muxer instead of closing it: closing deletes its segment
	// files from disk, which strands every client still requesting names from
	// the pre-error playlist (the rebuild changes the muxer's random filename
	// prefix). The stash keeps the old names resolvable during the write-error
	// backoff and for a grace window after the rebuild; the muxer is rebuilt
	// by rebuildMuxer when the next IDR frame arrives in writeLoop.
	entry.mu.Lock()
	if entry.mux != nil {
		stashRetiredMuxerLocked(entry, entry.mux)
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
