package muxer

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/prealloc"
	"github.com/abema/go-mp4"
)

// defaultTimescale is the timescale used for MP4 timestamps (ticks per second).
const defaultTimescale = 1000

// ErrClosed is returned by every operation on a muxer whose Close has already
// run. Sentinel so callers (recorder teardown paths racing a rotation) can
// classify it with errors.Is instead of matching the message text.
var ErrClosed = errors.New("muxer is closed-REPLACED")

// track holds per-track state for the MP4 muxer.
type track struct {
	id          int
	sps         []byte
	pps         []byte
	vps         []byte
	isH265      bool
	isAudio     bool
	audioCodec  string // "aac", "g711", or "opus"
	audioConfig []byte // AAC AudioSpecificConfig bytes
	g711MULaw   bool   // true=μ-law, false=A-law
	g711Rate    int    // sample rate (typically 8000)
	opusChans   uint8  // Opus output channel count
	opusPreSkip uint16 // Opus pre-skip (priming samples)
	opusRate    uint32 // Opus input sample rate (typically 48000)
	width       int
	height      int
	samples     []sample
}

// sample is one media sample's FILE placement record. The media bytes
// themselves are streamed to the output file at WriteSample time (#521 root
// fix) — only the placement metadata stays in memory (~24 bytes/sample vs
// the old whole-frame copies, which turned Close into a tens-of-MB burst
// write and froze the recorder's write loop 15-50s on congested SD/eMMC).
type sample struct {
	offset   int64 // absolute file offset of the stored sample
	size     uint32
	duration time.Duration
}

// MP4Muxer writes H.264/H.265 video (+audio) into an MP4 file using
// abema/go-mp4.
//
// Layout is streamed (#521): ftyp | mdat | moov. Media bytes are appended to
// the file as samples arrive (buffered, so writes spread naturally across
// the segment's lifetime); Close() flushes, patches the mdat size field, and
// appends the moov with per-sample chunk offsets (tracks interleave in
// arrival order, so each sample is its own chunk).
//
// Usage:
//
//	m := NewMP4Muxer("output.mp4")
//	trackID, _ := m.AddH264Track(sps, pps)
//	m.WriteSample(trackID, nalData, pts, duration)
//	m.Close()
type MP4Muxer struct {
	filePath    string
	file        *os.File
	bw          *bufio.Writer
	mdatSizeOff int64 // file offset of mdat's 4-byte size field (patched at Close)
	payloadN    int64 // media bytes streamed so far (mdat payload size)
	mu          sync.Mutex
	tracks      []*track
	nextTrackID int
	// totalDuration accumulates every sample's duration (both Write methods).
	totalDuration time.Duration
	closed        bool
	// preallocBytes is the whole-segment size estimate fallocate'd at open
	// (#757): one contiguous extent instead of per-append growth metadata
	// transactions. Close truncates the file back to the real content end.
	// 0 = no preallocation. Hint, not a bound.
	preallocBytes int64
}

// NewMP4Muxer creates a new MP4 muxer that will write to filePath.
func NewMP4Muxer(filePath string) *MP4Muxer {
	return &MP4Muxer{
		filePath:    filePath,
		nextTrackID: 1,
	}
}

// SetPreallocateBytes advises the muxer of the expected final segment size.
// The file is fallocate'd to that size when opened (before the first sample
// arrives); Close() truncates to the actual content length. Must be called
// before the first WriteSample to take effect. Failures to preallocate are
// logged-and-ignored (append-write status quo).
func (m *MP4Muxer) SetPreallocateBytes(n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed && m.file == nil && n > 0 {
		m.preallocBytes = n
	}
}

// AddH264Track adds an H.264 video track with the given SPS and PPS codec config.
// Returns the track ID (1-based) or an error.
func (m *MP4Muxer) AddH264Track(sps, pps []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return 0, ErrClosed
	}

	spsCopy := make([]byte, len(sps))
	copy(spsCopy, sps)
	ppsCopy := make([]byte, len(pps))
	copy(ppsCopy, pps)

	t := &track{
		id:  m.nextTrackID,
		sps: spsCopy,
		pps: ppsCopy,
	}
	t.width, t.height = parseSPSResolution(spsCopy)

	m.tracks = append(m.tracks, t)
	m.nextTrackID++
	return t.id, nil
}

// AddH265Track adds an H.265/HEVC video track with the given VPS, SPS, and PPS codec config.
// Returns the track ID (1-based) or an error.
func (m *MP4Muxer) AddH265Track(vps, sps, pps []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return 0, ErrClosed
	}

	vpsCopy := make([]byte, len(vps))
	copy(vpsCopy, vps)
	spsCopy := make([]byte, len(sps))
	copy(spsCopy, sps)
	ppsCopy := make([]byte, len(pps))
	copy(ppsCopy, pps)

	t := &track{
		id:     m.nextTrackID,
		sps:    spsCopy,
		pps:    ppsCopy,
		vps:    vpsCopy,
		isH265: true,
	}
	t.width, t.height = parseHEVCSPSResolution(spsCopy)

	m.tracks = append(m.tracks, t)
	m.nextTrackID++

	return t.id, nil
}

// AddAudioTrack adds an audio track for the given codec.
// Supported codecs: "aac" (AudioSpecificConfig required), "g711" (audioConfig ignored).
// For G.711, muLaw=true selects μ-law (PT=0), muLaw=false selects A-law (PT=8).
// sampleRate is typically 8000 for G.711.
// Returns the track ID (1-based) or an error.
func (m *MP4Muxer) AddAudioTrack(codec string, audioConfig []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return 0, ErrClosed
	}

	if codec != "aac" && codec != "g711" && codec != "opus" {
		return 0, fmt.Errorf("unsupported audio codec: %s (only aac, g711, and opus are supported)", codec)
	}

	t := &track{
		id:         m.nextTrackID,
		isAudio:    true,
		audioCodec: codec,
	}

	if codec == "aac" {
		configCopy := make([]byte, len(audioConfig))
		copy(configCopy, audioConfig)
		t.audioConfig = configCopy
	} else if codec == "g711" {
		// G.711: parse muLaw flag from config. config format: 1 byte (0=A-law, 1=μ-law) + 4 bytes sample rate (big-endian uint32)
		if len(audioConfig) >= 1 {
			t.g711MULaw = audioConfig[0] != 0
		}
		if len(audioConfig) >= 5 {
			t.g711Rate = int(audioConfig[1])<<24 | int(audioConfig[2])<<16 | int(audioConfig[3])<<8 | int(audioConfig[4])
		}
		if t.g711Rate == 0 {
			t.g711Rate = 8000 // default
		}
	} else if codec == "opus" {
		// Opus: config format: 1 byte channel count + 2 bytes PreSkip (BE) + 4 bytes InputSampleRate (BE)
		t.opusChans = 1
		t.opusPreSkip = 0
		t.opusRate = 48000
		if len(audioConfig) >= 1 {
			t.opusChans = audioConfig[0]
		}
		if len(audioConfig) >= 3 {
			t.opusPreSkip = uint16(audioConfig[1])<<8 | uint16(audioConfig[2])
		}
		if len(audioConfig) >= 7 {
			t.opusRate = uint32(audioConfig[3])<<24 | uint32(audioConfig[4])<<16 | uint32(audioConfig[5])<<8 | uint32(audioConfig[6])
		}
	}

	m.tracks = append(m.tracks, t)
	m.nextTrackID++
	return t.id, nil
}

// WriteSample writes an H.264 NAL unit as a sample to the specified track.
func (m *MP4Muxer) WriteSample(trackID int, data []byte, pts time.Duration, duration time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return ErrClosed
	}

	t := m.trackByID(trackID)
	if t == nil {
		return fmt.Errorf("track %d not found", trackID)
	}

	_ = pts // arrival timestamp is not part of the MP4 timeline (durations are)
	return m.appendSample(t, data, duration)
}

// WriteAudioSample writes a raw AAC frame as a sample to the specified audio track.
func (m *MP4Muxer) WriteAudioSample(trackID int, data []byte, pts time.Duration, duration time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return ErrClosed
	}

	t := m.trackByID(trackID)
	if t == nil {
		return fmt.Errorf("track %d not found", trackID)
	}
	if !t.isAudio {
		return fmt.Errorf("track %d is not an audio track", trackID)
	}

	_ = pts
	return m.appendSample(t, data, duration)
}

// trackByID resolves the 1-based track ID. Callers hold m.mu.
func (m *MP4Muxer) trackByID(trackID int) *track {
	for _, tr := range m.tracks {
		if tr.id == trackID {
			return tr
		}
	}
	return nil
}

// mdatStcoLimit caps the mdat payload at the 32-bit stco offset space minus
// the box header — a single segment approaching 4 GiB is already far past
// the segment-duration policy, and silently wrapping offsets would corrupt
// every chunk pointer.
const mdatStcoLimit = int64(0xFFFFFFFF) - 8

// appendSample streams one sample's bytes to the output file and records its
// placement. Video samples get a 4-byte big-endian NAL length prefix; audio
// samples are written raw. Callers hold m.mu.
func (m *MP4Muxer) appendSample(t *track, data []byte, duration time.Duration) error {
	if err := m.ensureOpen(); err != nil {
		return err
	}

	size := uint32(len(data))
	if !t.isAudio {
		size += 4
	}
	if m.payloadN+int64(size) > mdatStcoLimit {
		return errors.New("segment mdat exceeds 4GiB stco offset limit — rotate earlier")
	}
	if !t.isAudio {
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
		if _, err := m.bw.Write(lenBuf[:]); err != nil {
			return fmt.Errorf("stream sample prefix: %w", err)
		}
	}
	if _, err := m.bw.Write(data); err != nil {
		return fmt.Errorf("stream sample: %w", err)
	}

	// Absolute offset of this sample inside the file: mdat box starts at
	// mdatSizeOff; its payload at +8 (past the size+type header).
	offset := m.mdatSizeOff + 8 + m.payloadN
	m.payloadN += int64(size)

	t.samples = append(t.samples, sample{
		offset:   offset,
		size:     size,
		duration: duration,
	})
	m.totalDuration += duration
	return nil
}

// ensureOpen lazily creates the output file and writes the ftyp box plus the
// mdat header with a placeholder size (patched at Close). Tracks must all be
// registered before the first sample — writeFtyp derives brands from them;
// every caller adds tracks immediately after construction. Callers hold m.mu.
func (m *MP4Muxer) ensureOpen() error {
	if m.file != nil {
		return nil
	}
	f, err := os.Create(m.filePath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}

	// ftyp first (small, written through directly).
	ftypSize, err := writeFtyp(mp4.NewWriter(f), m.tracks)
	if err != nil {
		f.Close()
		return fmt.Errorf("write ftyp: %w", err)
	}

	// mdat header with a zero size placeholder — Close() seeks back and
	// patches it once the payload length is known.
	var hdr [8]byte
	bt := mp4.StrToBoxType("mdat")
	copy(hdr[4:8], bt[:])
	if _, err := f.Write(hdr[:]); err != nil {
		f.Close()
		return fmt.Errorf("write mdat header: %w", err)
	}

	m.file = f
	m.bw = bufio.NewWriterSize(f, 512*1024)
	m.mdatSizeOff = ftypSize // offset of mdat's SIZE field (box starts here)

	// Whole-segment preallocation (#757): reserve the estimated extent up
	// front so the append path stops paying per-growth inode metadata
	// transactions and the segment lands contiguous. Degrades silently on
	// filesystems without fallocate.
	if m.preallocBytes > 0 {
		if err := prealloc.Preallocate(f, m.preallocBytes); err != nil {
			slog.Debug("segment preallocation unavailable, continuing append-only",
				"path", m.filePath, "estimate", m.preallocBytes, "error", err)
		}
	}
	return nil
}

// Duration returns the total duration of all written samples.
func (m *MP4Muxer) Duration() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.totalDuration
}

// Close finalizes the streamed MP4 (#521): flushes the buffered media,
// patches the mdat size placeholder, and appends the moov. The only burst I/O
// left is the (small) moov — media bytes went out incrementally during the
// segment, so Close no longer hands the kernel tens of MB of dirty pages at
// once (the 15-50s writeFrames stalls behind the old accumulate-then-dump
// layout).
func (m *MP4Muxer) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil
	}
	m.closed = true

	if len(m.tracks) == 0 {
		return nil
	}

	// Zero-sample edge (tracks registered, no media arrived): still produce
	// the artifact so callers' cleanup/DB paths see a well-formed file.
	if m.file == nil {
		if err := m.ensureOpen(); err != nil {
			return err
		}
	}

	if err := m.bw.Flush(); err != nil {
		m.file.Close()
		return fmt.Errorf("flush media: %w", err)
	}

	// Patch the mdat size field (box header 8 + payload).
	var sizeBuf [4]byte
	binary.BigEndian.PutUint32(sizeBuf[:], uint32(8+m.payloadN))
	if _, err := m.file.WriteAt(sizeBuf[:], m.mdatSizeOff); err != nil {
		m.file.Close()
		return fmt.Errorf("patch mdat size: %w", err)
	}

	// Append moov after the mdat payload. Position is COMPUTED, not
	// SeekEnd — with preallocation (#757) the file size already includes
	// the reserved extent, so EOF is not the content end. Per-sample
	// offsets are already final (moov trails mdat).
	moovStart := m.mdatSizeOff + 8 + m.payloadN
	if _, err := m.file.Seek(moovStart, io.SeekStart); err != nil {
		m.file.Close()
		return fmt.Errorf("seek to moov: %w", err)
	}
	if err := writeMoov(mp4.NewWriter(m.file), m.tracks); err != nil {
		m.file.Close()
		return fmt.Errorf("write moov: %w", err)
	}

	// THE zero-tail nail (#757): preallocation extended the file to the
	// estimate — truncate back to the actual content end, or every
	// crash-recovered segment carries a zero tail that inflates the probed
	// duration. No-op without preallocation (offset == size).
	if end, err := m.file.Seek(0, io.SeekCurrent); err == nil && end < m.preallocExpected() {
		if err := m.file.Truncate(end); err != nil {
			m.file.Close()
			return fmt.Errorf("truncate preallocated tail: %w", err)
		}
	}
	return m.file.Close()
}

// preallocExpected reports the file size the preallocation reserved (0 when
// none). Close only needs to truncate when the content end is BELOW the
// reserved size; seeking past it (overshoot) means the file already ends at
// the content.
func (m *MP4Muxer) preallocExpected() int64 {
	return m.preallocBytes
}

// --- Box writing functions ---
