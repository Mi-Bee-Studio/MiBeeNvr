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

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mediaprobe"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mp4util"
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

func writeFtyp(w *mp4.Writer, tracks []*track) (int64, error) {
	start, _ := w.Seek(0, 1)
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("ftyp")})
	if err != nil {
		return 0, err
	}

	compatibleBrands := []mp4.CompatibleBrandElem{
		{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
		{CompatibleBrand: [4]byte{'i', 's', 'o', '2'}},
		{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
	}
	// Add codec-specific brands
	hasH264 := false
	hasH265 := false
	for _, tr := range tracks {
		if tr.isAudio {
			continue
		}
		if tr.isH265 {
			hasH265 = true
		} else {
			hasH264 = true
		}
	}
	if hasH264 {
		compatibleBrands = append(compatibleBrands, mp4.CompatibleBrandElem{CompatibleBrand: [4]byte{'a', 'v', 'c', '1'}})
	}
	if hasH265 {
		compatibleBrands = append(compatibleBrands, mp4.CompatibleBrandElem{CompatibleBrand: [4]byte{'h', 'e', 'v', '1'}})
	}

	ftyp := &mp4.Ftyp{
		MajorBrand:       [4]byte{'i', 's', 'o', 'm'},
		MinorVersion:     0,
		CompatibleBrands: compatibleBrands,
	}
	if _, err := mp4.Marshal(w, ftyp, mp4.Context{}); err != nil {
		return 0, err
	}
	if _, err := w.EndBox(); err != nil {
		return 0, err
	}
	_ = bi

	end, _ := w.Seek(0, 1)
	return end - start, nil
}

func writeMoov(w *mp4.Writer, tracks []*track) error {
	_, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("moov")})
	if err != nil {
		return err
	}
	if err := writeMvhd(w, tracks); err != nil {
		return err
	}

	// Per-sample chunk offsets are recorded on each track's samples at
	// WriteSample time (streaming layout: tracks interleave in arrival
	// order, so a track's samples are not contiguous — one chunk each).
	for _, tr := range tracks {
		if err := writeTrak(w, tr); err != nil {
			return err
		}
	}

	_, err = w.EndBox()
	return err
}

func writeMvhd(w *mp4.Writer, tracks []*track) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mvhd")})
	if err != nil {
		return err
	}

	nextID := uint32(1)
	maxDur := uint32(0)
	for _, tr := range tracks {
		if uint32(tr.id) >= nextID {
			nextID = uint32(tr.id) + 1
		}
		d := trackDurationMs(tr)
		if d > maxDur {
			maxDur = d
		}
	}

	mvhd := &mp4.Mvhd{
		Timescale:   defaultTimescale,
		DurationV0:  maxDur,
		Rate:        0x00010000,
		Volume:      0x0100,
		NextTrackID: nextID,
		Matrix: [9]int32{
			0x00010000, 0, 0,
			0, 0x00010000, 0,
			0, 0, 0x40000000,
		},
	}
	if _, err := mp4.Marshal(w, mvhd, mp4.Context{}); err != nil {
		return err
	}
	_, err = w.EndBox()
	_ = bi
	return err
}

func writeTrak(w *mp4.Writer, tr *track) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("trak")})
	if err != nil {
		return err
	}

	// tkhd
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("tkhd")})
	if err != nil {
		return err
	}
	tkhd := &mp4.Tkhd{
		TrackID:    uint32(tr.id),
		DurationV0: trackDurationMs(tr),
		Width:      uint32(tr.width) << 16,
		Height:     uint32(tr.height) << 16,
		Matrix: [9]int32{
			0x00010000, 0, 0,
			0, 0x00010000, 0,
			0, 0, 0x40000000,
		},
	}
	if _, err := mp4.Marshal(w, tkhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi2

	// mdia
	if err := writeMdia(w, tr); err != nil {
		return err
	}

	_, err = w.EndBox()
	_ = bi
	return err
}

func writeMdia(w *mp4.Writer, tr *track) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mdia")})
	if err != nil {
		return err
	}

	// mdhd
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mdhd")})
	if err != nil {
		return err
	}
	mdhd := &mp4.Mdhd{
		Timescale:  defaultTimescale,
		DurationV0: trackDurationMs(tr),
		Language:   [3]byte{0x15, 0xC0, 0x00}, // 'und' packed
	}
	if _, err := mp4.Marshal(w, mdhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi2

	// hdlr
	bi3, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hdlr")})
	if err != nil {
		return err
	}
	hdlr := &mp4.Hdlr{
		HandlerType: [4]byte{'v', 'i', 'd', 'e'},
		Name:        "VideoHandler\x00",
	}
	if tr.isAudio {
		hdlr.HandlerType = [4]byte{'s', 'o', 'u', 'n'}
		hdlr.Name = "SoundHandler\x00"
	}
	if _, err := mp4.Marshal(w, hdlr, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi3

	// minf
	if err := writeMinf(w, tr); err != nil {
		return err
	}

	_, err = w.EndBox()
	_ = bi
	return err
}

func writeMinf(w *mp4.Writer, tr *track) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("minf")})
	if err != nil {
		return err
	}

	// vmhd (video) or smhd (audio)
	if tr.isAudio {
		bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("smhd")})
		if err != nil {
			return err
		}
		if _, err := mp4.Marshal(w, &mp4.Smhd{}, mp4.Context{}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
		_ = bi2
	} else {
		bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("vmhd")})
		if err != nil {
			return err
		}
		if _, err := mp4.Marshal(w, &mp4.Vmhd{Graphicsmode: 0}, mp4.Context{}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
		_ = bi2
	}

	// dinf > dref > url
	bi3, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("dinf")})
	if err != nil {
		return err
	}
	bi4, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("dref")})
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Dref{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	bi5, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("url ")})
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Url{Location: ""}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi5
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi4
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi3

	// stbl
	if err := writeStbl(w, tr); err != nil {
		return err
	}

	_, err = w.EndBox()
	_ = bi
	return err
}

func writeStbl(w *mp4.Writer, tr *track) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stbl")})
	if err != nil {
		return err
	}

	// stsd > hvc1 > hvcC (HEVC) or avc1 > avcC (H.264)
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsd")})
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	if tr.isAudio {
		switch tr.audioCodec {
		case "g711":
			if err := writeG711SampleEntry(w, tr); err != nil {
				return err
			}
		case "opus":
			if err := writeOpusSampleEntry(w, tr); err != nil {
				return err
			}
		default:
			if err := writeAACSampleEntry(w, tr); err != nil {
				return err
			}
		}
	} else if tr.isH265 {
		if err := writeH265SampleEntry(w, tr); err != nil {
			return err
		}
	} else {
		if err := writeH264SampleEntry(w, tr); err != nil {
			return err
		}
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi2

	// stts — run-length encoded: consecutive equal deltas compress to one
	// entry, exactly like the merge-side writer (internal/merge mp4merge.go).
	// The expanded per-sample table is identical — every consumer either
	// expands runs (mediaprobe parser) or is a standard player, for which RLE
	// is the norm. Real-time camera tracks are near-uniform (one delta per
	// cadence), so this collapses one-entry-per-sample to one-entry-per-run:
	// ~96KB less moov for a 12k-frame segment.
	bi6, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stts")})
	if err != nil {
		return err
	}
	var sttsEntries []mp4.SttsEntry
	if len(tr.samples) > 0 {
		sttsEntries = make([]mp4.SttsEntry, 0, len(tr.samples))
		prevDelta := uint32(tr.samples[0].duration.Milliseconds())
		runCount := uint32(1)
		for i := 1; i < len(tr.samples); i++ {
			delta := uint32(tr.samples[i].duration.Milliseconds())
			if delta == prevDelta {
				runCount++
				continue
			}
			sttsEntries = append(sttsEntries, mp4.SttsEntry{
				SampleCount: runCount,
				SampleDelta: prevDelta,
			})
			prevDelta = delta
			runCount = 1
		}
		// Flush final run.
		sttsEntries = append(sttsEntries, mp4.SttsEntry{
			SampleCount: runCount,
			SampleDelta: prevDelta,
		})
	}
	if _, err := mp4.Marshal(w, &mp4.Stts{EntryCount: uint32(len(sttsEntries)), Entries: sttsEntries}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi6

	// stsc (streaming layout: every sample is its own chunk — tracks
	// interleave in arrival order, so per-track contiguity is impossible)
	bi7, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsc")})
	if err != nil {
		return err
	}
	stscEntries := []mp4.StscEntry{
		{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1},
	}
	if len(tr.samples) == 0 {
		stscEntries = nil
	}
	if _, err := mp4.Marshal(w, &mp4.Stsc{EntryCount: uint32(len(stscEntries)), Entries: stscEntries}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi7

	// stsz
	bi8, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsz")})
	if err != nil {
		return err
	}
	sizes := make([]uint32, len(tr.samples))
	for i, s := range tr.samples {
		sizes[i] = s.size
	}
	if _, err := mp4.Marshal(w, &mp4.Stsz{SampleSize: 0, SampleCount: uint32(len(sizes)), EntrySize: sizes}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi8

	// stco (one offset per sample — offsets are absolute, recorded at
	// WriteSample time when the bytes hit the file)
	bi9, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stco")})
	if err != nil {
		return err
	}
	stco := &mp4.Stco{EntryCount: 0, ChunkOffset: nil}
	if len(tr.samples) > 0 {
		stco.ChunkOffset = make([]uint32, len(tr.samples))
		for i, s := range tr.samples {
			stco.ChunkOffset[i] = uint32(s.offset)
		}
		stco.EntryCount = uint32(len(tr.samples))
	}
	if _, err := mp4.Marshal(w, stco, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi9

	_, err = w.EndBox()
	_ = bi
	return err
}

// writeH264SampleEntry writes avc1 + avcC boxes for H.264 tracks.
func writeH264SampleEntry(w *mp4.Writer, tr *track) error {
	bi3, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("avc1")})
	if err != nil {
		return err
	}
	avc1 := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType("avc1")},
			DataReferenceIndex: 1,
		},
		Width:           uint16(tr.width),
		Height:          uint16(tr.height),
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
	}
	if _, err := mp4.Marshal(w, avc1, mp4.Context{}); err != nil {
		return err
	}
	// avcC
	bi4, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("avcC")})
	if err != nil {
		return err
	}
	avcC := mp4util.BuildAvcC(tr.sps, tr.pps)
	if _, err := mp4.Marshal(w, avcC, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi4
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi3
	return nil
}

// writeH265SampleEntry writes hvc1 + hvcC boxes for H.265/HEVC tracks.
func writeH265SampleEntry(w *mp4.Writer, tr *track) error {
	bi3, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hvc1")})
	if err != nil {
		return err
	}
	hvc1 := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType("hvc1")},
			DataReferenceIndex: 1,
		},
		Width:           uint16(tr.width),
		Height:          uint16(tr.height),
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
	}
	if _, err := mp4.Marshal(w, hvc1, mp4.Context{}); err != nil {
		return err
	}
	// hvcC
	bi4, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hvcC")})
	if err != nil {
		return err
	}
	hvcC := mp4util.BuildHvcC(tr.vps, tr.sps, tr.pps)
	if _, err := mp4.Marshal(w, hvcC, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi4
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi3
	return nil
}

// writeAACSampleEntry writes mp4a + esds boxes for AAC audio tracks.
// The esds box contains the AudioSpecificConfig from audioConfig.
func writeAACSampleEntry(w *mp4.Writer, tr *track) error {
	bi3, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mp4a")})
	if err != nil {
		return err
	}

	// Parse AudioSpecificConfig to extract channel count and sample rate.
	// For AAC-LC: 2 bytes. Format: audioObjectType(5) + samplingFreqIndex(4) + channelConfig(4) + ...
	channelCount := uint16(2) // default stereo
	sampleRate := uint32(44100)
	if len(tr.audioConfig) >= 2 {
		sampleRateIndex := (tr.audioConfig[0] >> 3) & 0x0F
		if sampleRateIndex == 0xF && len(tr.audioConfig) >= 3 {
			sampleRate = uint32(tr.audioConfig[1]&0x07)<<8 | uint32(tr.audioConfig[2])
		} else if sampleRateIndex < 15 {
			sampleRates := [...]uint32{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}
			if int(sampleRateIndex) < len(sampleRates) {
				sampleRate = sampleRates[sampleRateIndex]
			}
		}
		channelConfig := ((tr.audioConfig[0] & 0x01) << 2) | ((tr.audioConfig[1] >> 6) & 0x03)
		if channelConfig > 0 {
			channelCount = uint16(channelConfig)
		}
	}

	mp4a := &mp4.AudioSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType("mp4a")},
			DataReferenceIndex: 1,
		},
		EntryVersion: 0,
		ChannelCount: channelCount,
		SampleSize:   16,
		SampleRate:   sampleRate << 16, // fixed-point 16.16
	}
	if _, err := mp4.Marshal(w, mp4a, mp4.Context{}); err != nil {
		return err
	}

	// esds box
	bi4, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("esds")})
	if err != nil {
		return err
	}
	esds := mediaprobe.BuildEsds(tr.audioConfig)
	if _, err := mp4.Marshal(w, esds, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi4

	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi3
	return nil
}

// writeG711SampleEntry writes ulaw (μ-law) or alaw (A-law) sample entry for
// G.711 audio via the shared mediaprobe builder (issue #1012 block 2).
func writeG711SampleEntry(w *mp4.Writer, tr *track) error {
	return mediaprobe.WriteG711SampleEntry(w, tr.g711MULaw, uint32(tr.g711Rate))
}

// writeOpusSampleEntry writes Opus sample entry via the shared mediaprobe
// builder (issue #1012 block 2).
func writeOpusSampleEntry(w *mp4.Writer, tr *track) error {
	return mediaprobe.WriteOpusSampleEntry(w, uint16(tr.opusChans), tr.opusPreSkip, tr.opusRate)
}

// --- Helpers ---

func trackDurationMs(tr *track) uint32 {
	d := uint32(0)
	for _, s := range tr.samples {
		d += uint32(s.duration.Milliseconds())
	}
	return d
}

// --- SPS Resolution Parser ---
//
// The bitstream parsers live in internal/mediaprobe (sps.go) — the single
// canonical implementation (issue #1012 block 2). These thin wrappers keep
// muxer's (width, height) no-error call shape: a malformed SPS yields (0, 0),
// which callers treat as "unknown resolution". Note the error-returning
// mediaprobe parsers reject truncated SPS outright, while the old local
// parser silently zero-filled past end-of-data — for such garbage-in cases
// (0, 0) is written instead of a synthesized small resolution.

// parseSPSResolution extracts width and height from an H.264 SPS NAL unit.
// Returns (0, 0) if parsing fails.
func parseSPSResolution(sps []byte) (width, height int) {
	w, h, err := mediaprobe.ParseSPSResolution(sps)
	if err != nil {
		return 0, 0
	}
	return w, h
}

// parseHEVCSPSResolution extracts width and height from an HEVC SPS NAL unit.
// HEVC SPS has a 2-byte NAL header, then: sps_video_parameter_set_id(4),
// sps_max_sub_layers_minus1(3), sps_temporal_id_nesting_flag(1),
// profile_tier_level(88+ bits for max_sub_layers=1),
// sps_seq_parameter_set_id(UE), chroma_format_idc(UE),
// pic_width_in_luma_samples(UE), pic_height_in_luma_samples(UE).
// Returns (0, 0) if parsing fails.
func parseHEVCSPSResolution(sps []byte) (width, height int) {
	w, h, err := mediaprobe.ParseHEVCSPSResolution(sps)
	if err != nil {
		return 0, 0
	}
	return w, h
}
