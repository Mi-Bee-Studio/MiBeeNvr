package muxer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/abema/go-mp4"
)

// defaultTimescale is the timescale used for MP4 timestamps (ticks per second).
const defaultTimescale = 1000

// track holds per-track state for the MP4 muxer.
type track struct {
	id          int
	sps         []byte
	pps         []byte
	vps         []byte
	isH265      bool
	isAudio     bool
	audioCodec  string    // "aac" or "g711"
	audioConfig []byte    // AAC AudioSpecificConfig bytes
	g711MULaw   bool      // true=μ-law, false=A-law
	g711Rate    int       // sample rate (typically 8000)
	width       int
	height      int
	samples     []sample
}

// sample represents a single media sample (one NAL unit).
type sample struct {
	data     []byte
	pts      time.Duration
	duration time.Duration
}

// MP4Muxer writes H.264 video data into an MP4 file using abema/go-mp4.
//
// Usage:
//
//	m := NewMP4Muxer("output.mp4")
//	trackID, _ := m.AddH264Track(sps, pps)
//	m.WriteSample(trackID, nalData, pts, duration)
//	m.Close()
type MP4Muxer struct {
	filePath      string
	file          *os.File
	mu            sync.Mutex
	tracks        []*track
	nextTrackID   int
	totalDuration time.Duration
	closed        bool
}

// NewMP4Muxer creates a new MP4 muxer that will write to filePath.
func NewMP4Muxer(filePath string) *MP4Muxer {
	return &MP4Muxer{
		filePath:    filePath,
		nextTrackID: 1,
	}
}

// AddH264Track adds an H.264 video track with the given SPS and PPS codec config.
// Returns the track ID (1-based) or an error.
func (m *MP4Muxer) AddH264Track(sps, pps []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return 0, errors.New("muxer is closed")
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
		return 0, errors.New("muxer is closed")
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
		return 0, errors.New("muxer is closed")
	}

	if codec != "aac" && codec != "g711" {
		return 0, fmt.Errorf("unsupported audio codec: %s (only aac and g711 are supported)", codec)
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
	} else {
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
		return errors.New("muxer is closed")
	}

	var t *track
	for _, tr := range m.tracks {
		if tr.id == trackID {
			t = tr
			break
		}
	}
	if t == nil {
		return fmt.Errorf("track %d not found", trackID)
	}

	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)

	t.samples = append(t.samples, sample{
		data:     dataCopy,
		pts:      pts,
		duration: duration,
	})

	m.totalDuration += duration
	return nil
}

// WriteAudioSample writes a raw AAC frame as a sample to the specified audio track.
func (m *MP4Muxer) WriteAudioSample(trackID int, data []byte, pts time.Duration, duration time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return errors.New("muxer is closed")
	}

	var t *track
	for _, tr := range m.tracks {
		if tr.id == trackID {
			t = tr
			break
		}
	}
	if t == nil {
		return fmt.Errorf("track %d not found", trackID)
	}
	if !t.isAudio {
		return fmt.Errorf("track %d is not an audio track", trackID)
	}

	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)

	t.samples = append(t.samples, sample{
		data:     dataCopy,
		pts:      pts,
		duration: duration,
	})

	m.totalDuration += duration
	return nil
}

// Duration returns the total duration of all written samples.
func (m *MP4Muxer) Duration() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.totalDuration
}

// Close finalizes the MP4 file by writing ftyp + moov + mdat atoms.
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

	f, err := os.Create(m.filePath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	m.file = f
	defer f.Close()

	// Step 1: Calculate moov size by writing to a buffer (with placeholder stco=0).
	buf := &bytesWriter{}
	bw := mp4.NewWriter(buf)
	if err := writeMoov(bw, m.tracks, 0); err != nil {
		return fmt.Errorf("calculate moov size: %w", err)
	}
	moovSize := buf.len()

	// Step 2: Write ftyp to the real file.
	w := mp4.NewWriter(f)
	ftypSize, err := writeFtyp(w, m.tracks)
	if err != nil {
		return fmt.Errorf("write ftyp: %w", err)
	}

	// Step 3: mdat data starts at ftypSize + moovSize + 8 (mdat header).
	mdatDataOffset := int64(ftypSize) + int64(moovSize) + 8

	// Step 4: Write moov with correct stco offset.
	if err := writeMoov(w, m.tracks, mdatDataOffset); err != nil {
		return fmt.Errorf("write moov: %w", err)
	}

	// Step 5: Write mdat box.
	mdatData := collectMdatData(m.tracks)
	mdatBoxSize := uint64(8 + len(mdatData))
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mdat"), Size: mdatBoxSize})
	if err != nil {
		return fmt.Errorf("start mdat: %w", err)
	}
	if _, err := w.Write(mdatData); err != nil {
		return fmt.Errorf("write mdat data: %w", err)
	}
	if _, err := w.EndBox(); err != nil {
		return fmt.Errorf("end mdat: %w", err)
	}
	_ = bi

	return nil
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

func writeMoov(w *mp4.Writer, tracks []*track, chunkOffset int64) error {
	_, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("moov")})
	if err != nil {
		return err
	}
	if err := writeMvhd(w, tracks); err != nil {
		return err
	}

	// Calculate per-track chunk offsets within mdat.
	// Samples are written in track order, so each track's
	// data follows the previous track's data.
	off := chunkOffset
	for _, tr := range tracks {
		if err := writeTrak(w, tr, off); err != nil {
			return err
		}
		off += int64(trackMdatSize(tr))
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

func writeTrak(w *mp4.Writer, tr *track, chunkOffset int64) error {
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
	if err := writeMdia(w, tr, chunkOffset); err != nil {
		return err
	}

	_, err = w.EndBox()
	_ = bi
	return err
}

func writeMdia(w *mp4.Writer, tr *track, chunkOffset int64) error {
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
	if err := writeMinf(w, tr, chunkOffset); err != nil {
		return err
	}

	_, err = w.EndBox()
	_ = bi
	return err
}

func writeMinf(w *mp4.Writer, tr *track, chunkOffset int64) error {
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
	if err := writeStbl(w, tr, chunkOffset); err != nil {
		return err
	}

	_, err = w.EndBox()
	_ = bi
	return err
}

func writeStbl(w *mp4.Writer, tr *track, chunkOffset int64) error {
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
		if tr.audioCodec == "g711" {
			if err := writeG711SampleEntry(w, tr); err != nil {
				return err
			}
		} else {
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

	// stts
	bi6, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stts")})
	if err != nil {
		return err
	}
	sttsEntries := make([]mp4.SttsEntry, len(tr.samples))
	for i, s := range tr.samples {
		sttsEntries[i] = mp4.SttsEntry{
			SampleCount: 1,
			SampleDelta: uint32(s.duration.Milliseconds()),
		}
	}
	if _, err := mp4.Marshal(w, &mp4.Stts{EntryCount: uint32(len(sttsEntries)), Entries: sttsEntries}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi6

	// stsc (all samples in one chunk)
	bi7, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsc")})
	if err != nil {
		return err
	}
	stscEntries := []mp4.StscEntry{
		{FirstChunk: 1, SamplesPerChunk: uint32(len(tr.samples)), SampleDescriptionIndex: 1},
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
		sz := uint32(len(s.data))
		if !tr.isAudio {
			sz += 4 // 4-byte NAL length prefix for video
		}
		sizes[i] = sz
	}
	if _, err := mp4.Marshal(w, &mp4.Stsz{SampleSize: 0, SampleCount: uint32(len(sizes)), EntrySize: sizes}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi8

	// stco
	bi9, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stco")})
	if err != nil {
		return err
	}
	stco := &mp4.Stco{EntryCount: 0, ChunkOffset: nil}
	if len(tr.samples) > 0 {
		stco.EntryCount = 1
		stco.ChunkOffset = []uint32{uint32(chunkOffset)}
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
	avcC := &mp4.AVCDecoderConfiguration{
		AnyTypeBox:                 mp4.AnyTypeBox{Type: mp4.StrToBoxType("avcC")},
		ConfigurationVersion:       1,
		Profile:                    tr.sps[1],
		ProfileCompatibility:       tr.sps[2],
		Level:                      tr.sps[3],
		LengthSizeMinusOne:         3,
		NumOfSequenceParameterSets: 1,
		SequenceParameterSets: []mp4.AVCParameterSet{
			{Length: uint16(len(tr.sps)), NALUnit: tr.sps},
		},
		NumOfPictureParameterSets: 1,
		PictureParameterSets: []mp4.AVCParameterSet{
			{Length: uint16(len(tr.pps)), NALUnit: tr.pps},
		},
	}
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
	hvcC := buildHvcC(tr.vps, tr.sps, tr.pps)
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
	esds := buildEsds(tr.audioConfig)
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

// writeG711SampleEntry writes ulaw (μ-law) or alaw (A-law) sample entry for G.711 audio.
// G.711 is raw PCM — no esds or codec config boxes needed.
// Written as raw bytes since go-mp4 only registers AudioSampleEntry for mp4a/enca.
func writeG711SampleEntry(w *mp4.Writer, tr *track) error {
	boxType := mp4.StrToBoxType("ulaw")
	if !tr.g711MULaw {
		boxType = mp4.StrToBoxType("alaw")
	}

	bi, err := w.StartBox(&mp4.BoxInfo{Type: boxType})
	if err != nil {
		return err
	}

	sampleRate := uint32(tr.g711Rate)
	if sampleRate == 0 {
		sampleRate = 8000
	}

	// Write AudioSampleEntry fields manually (same layout as mp4a without esds):
	// reserved[6] + data_reference_index[2] + entry_version[2] + reserved[6] +
	// channel_count[2] + sample_size[2] + pre_defined[2] + reserved[2] + sample_rate[4]
	buf := make([]byte, 28)
	// bytes 6-7: data_reference_index = 1
	buf[7] = 0x01
	// bytes 16-17: channel_count = 1
	buf[17] = 0x01
	// bytes 18-19: sample_size = 8
	buf[19] = 0x08
	// bytes 24-27: sample_rate = fixed-point 16.16
	rateFixed := sampleRate << 16
	buf[24] = byte(rateFixed >> 24)
	buf[25] = byte(rateFixed >> 16)
	buf[26] = byte(rateFixed >> 8)
	buf[27] = byte(rateFixed)

	if _, err := w.Write(buf); err != nil {
		return err
	}

	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi
	return nil
}

// buildEsds constructs an esds (Elementary Stream Descriptor) box for AAC.
// Structure: ES_Descriptor > DecoderConfigDescriptor > DecSpecificInfoTag(AudioSpecificConfig) + SLConfigDescriptor
func buildEsds(audioConfig []byte) *mp4.Esds {
	return &mp4.Esds{
		FullBox: mp4.FullBox{Version: 0},
		Descriptors: []mp4.Descriptor{
			{
				Tag:  mp4.ESDescrTag,
				Size: uint32(25 + len(audioConfig)),
				ESDescriptor: &mp4.ESDescriptor{
					ESID:           1,
					StreamPriority: 0,
				},
				DecoderConfigDescriptor: nil,
			},
			{
				Tag:  mp4.DecoderConfigDescrTag,
				Size: uint32(13 + len(audioConfig)),
				DecoderConfigDescriptor: &mp4.DecoderConfigDescriptor{
					ObjectTypeIndication: 0x40, // Audio ISO/IEC 14496-3 (AAC)
					StreamType:           0x05, // AudioStream
					UpStream:             false,
					Reserved:             true,
					BufferSizeDB:         0,
					MaxBitrate:           128000,
					AvgBitrate:           128000,
				},
			},
			{
				Tag:  mp4.DecSpecificInfoTag,
				Size: uint32(len(audioConfig)),
				Data: audioConfig,
			},
			{
				Tag:  mp4.SLConfigDescrTag,
				Size: 1,
				Data: []byte{0x02}, // predefined: use timestamps
			},
		},
	}
}

// buildHvcC constructs an HvcC (HEVCDecoderConfigurationRecord) from VPS, SPS, PPS.
func buildHvcC(vps, sps, pps []byte) *mp4.HvcC {
	profile := uint8(0)
	if len(sps) > 1 {
		profile = sps[1]
	}
	level := uint8(0)
	if len(sps) > 12 {
		level = sps[12]
	}
	return &mp4.HvcC{
		ConfigurationVersion:        1,
		GeneralProfileSpace:         0,
		GeneralTierFlag:             false,
		GeneralProfileIdc:           profile,
		GeneralProfileCompatibility: [32]bool{}, // zeroed
		GeneralConstraintIndicator:  [6]uint8{},
		GeneralLevelIdc:             level,
		Reserved1:                   15,
		MinSpatialSegmentationIdc:   0,
		Reserved2:                   63,
		ParallelismType:             0,
		Reserved3:                   63,
		ChromaFormatIdc:             1,
		Reserved4:                   31,
		BitDepthLumaMinus8:          0,
		Reserved5:                   31,
		BitDepthChromaMinus8:        0,
		AvgFrameRate:                0,
		ConstantFrameRate:           0,
		NumTemporalLayers:           1,
		TemporalIdNested:            1,
		LengthSizeMinusOne:          3,
		NumOfNaluArrays:             3,
		NaluArrays: []mp4.HEVCNaluArray{
			{Completeness: true, NaluType: 32, NumNalus: 1, Nalus: []mp4.HEVCNalu{{Length: uint16(len(vps)), NALUnit: vps}}},
			{Completeness: true, NaluType: 33, NumNalus: 1, Nalus: []mp4.HEVCNalu{{Length: uint16(len(sps)), NALUnit: sps}}},
			{Completeness: true, NaluType: 34, NumNalus: 1, Nalus: []mp4.HEVCNalu{{Length: uint16(len(pps)), NALUnit: pps}}},
		},
	}
}

// --- Helpers ---

// trackMdatSize returns the total bytes this track's samples occupy in the mdat box.
// Video samples include a 4-byte NAL length prefix per sample.
func trackMdatSize(tr *track) int {
	total := 0
	for _, s := range tr.samples {
		if tr.isAudio {
			total += len(s.data)
		} else {
			total += 4 + len(s.data) // 4-byte NAL length prefix
		}
	}
	return total
}

func trackDurationMs(tr *track) uint32 {
	d := uint32(0)
	for _, s := range tr.samples {
		d += uint32(s.duration.Milliseconds())
	}
	return d
}

func collectMdatData(tracks []*track) []byte {
	var buf []byte
	var lenBuf [4]byte
	for _, tr := range tracks {
		for _, s := range tr.samples {
			if tr.isAudio {
				// Audio samples are written raw (no length prefix)
				buf = append(buf, s.data...)
			} else {
				// Video samples get a 4-byte big-endian NAL length prefix
				binary.BigEndian.PutUint32(lenBuf[:], uint32(len(s.data)))
				buf = append(buf, lenBuf[:]...)
				buf = append(buf, s.data...)
			}
		}
	}
	return buf
}

// bytesWriter implements io.WriteSeeker backed by a byte buffer.
// Used to pre-calculate moov box size.
type bytesWriter struct {
	data []byte
	pos  int64
}

func (b *bytesWriter) Write(p []byte) (int, error) {
	if b.pos+int64(len(p)) > int64(len(b.data)) {
		grow := b.pos + int64(len(p)) - int64(len(b.data))
		b.data = append(b.data, make([]byte, grow)...)
	}
	copy(b.data[b.pos:], p)
	b.pos += int64(len(p))
	return len(p), nil
}

func (b *bytesWriter) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case 0: // SeekStart
		b.pos = offset
	case 1: // SeekCurrent
		b.pos += offset
	case 2: // SeekEnd
		b.pos = int64(len(b.data)) + offset
	}
	if b.pos < 0 {
		b.pos = 0
	}
	return b.pos, nil
}

func (b *bytesWriter) len() int64 {
	return int64(len(b.data))
}

// --- SPS Resolution Parser ---

// bitReader reads bits from a byte slice, MSB first.
type bitReader struct {
	data   []byte
	offset int
}

func (r *bitReader) readBit() int {
	if r.offset >= len(r.data)*8 {
		return 0
	}
	byteIdx := r.offset / 8
	bitIdx := 7 - (r.offset % 8)
	r.offset++
	return int((r.data[byteIdx] >> bitIdx) & 1)
}

func (r *bitReader) readBits(n int) int {
	var val int
	for i := 0; i < n; i++ {
		val = (val << 1) | r.readBit()
	}
	return val
}

// readUE reads an unsigned Exp-Golomb coded value.
func (r *bitReader) readUE() int {
	leadingZeros := 0
	for r.readBit() == 0 {
		leadingZeros++
		if leadingZeros > 32 {
			return 0
		}
	}
	if leadingZeros == 0 {
		return 0
	}
	return (1 << leadingZeros) - 1 + r.readBits(leadingZeros)
}

// readSE reads a signed Exp-Golomb coded value.
func (r *bitReader) readSE() int {
	val := r.readUE()
	if val%2 == 0 {
		return -(val / 2)
	}
	return (val + 1) / 2
}

// removeEmulationPrevention removes H.264 emulation prevention bytes (0x00 0x00 0x03).
func removeEmulationPrevention(data []byte) []byte {
	var result []byte
	i := 0
	for i < len(data) {
		if i+2 < len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 3 {
			result = append(result, 0, 0)
			i += 3
		} else {
			result = append(result, data[i])
			i++
		}
	}
	return result
}

// parseSPSResolution extracts width and height from an H.264 SPS NAL unit.
// Returns (0, 0) if parsing fails.
func parseSPSResolution(sps []byte) (width, height int) {
	if len(sps) < 8 {
		return 0, 0
	}

	// Remove emulation prevention bytes, skip NAL header byte.
	rbsp := removeEmulationPrevention(sps[1:])
	if len(rbsp) < 4 {
		return 0, 0
	}

	r := &bitReader{data: rbsp}

	// profile_idc (8 bits)
	profileIDC := r.readBits(8)
	// constraint_set_flags (8 bits)
	r.readBits(8)
	// level_idc (8 bits)
	r.readBits(8)

	// seq_parameter_set_id
	r.readUE()

	// High profile and extensions require additional fields.
	highProfile := profileIDC == 100 || profileIDC == 110 || profileIDC == 122 ||
		profileIDC == 244 || profileIDC == 44 || profileIDC == 83 ||
		profileIDC == 86 || profileIDC == 118 || profileIDC == 128 ||
		profileIDC == 138 || profileIDC == 139 || profileIDC == 134

	chromaFormatIDC := 1
	if highProfile {
		chromaFormatIDC = r.readUE()
		if chromaFormatIDC == 3 {
			r.readBit() // separate_colour_plane_flag
		}
		r.readUE()  // bit_depth_luma_minus8
		r.readUE()  // bit_depth_chroma_minus8
		r.readBit() // qpprime_y_zero_transform_bypass_flag
		scalingPresent := r.readBit()
		if scalingPresent == 1 {
			count := 8
			if chromaFormatIDC == 3 {
				count = 12
			}
			for i := 0; i < count; i++ {
				present := r.readBit()
				if present == 1 {
					size := 16
					if i >= 6 {
						size = 64
					}
					lastScale := 8
					for j := 0; j < size; j++ {
						delta := r.readSE()
						nextScale := (lastScale + delta + 256) % 256
						if nextScale == 0 {
							nextScale = 256
						}
						lastScale = nextScale
					}
				}
			}
		}
	}

	// log2_max_frame_num_minus4
	r.readUE()

	// pic_order_cnt_type
	picOrderCntType := r.readUE()
	if picOrderCntType == 0 {
		r.readUE() // log2_max_pic_order_cnt_lsb_minus4
	} else if picOrderCntType == 1 {
		r.readBit() // delta_pic_order_always_zero_flag
		r.readSE()  // offset_for_non_ref_pic
		r.readSE()  // offset_for_top_to_bottom_field
		numRefFrames := r.readUE()
		for i := 0; i < numRefFrames; i++ {
			r.readSE()
		}
	}

	// max_num_ref_frames
	r.readUE()
	// gaps_in_frame_num_value_allowed_flag
	r.readBit()

	// pic_width_in_mbs_minus1
	picWidthInMbs := r.readUE() + 1
	// pic_height_in_map_units_minus1
	picHeightInMapUnits := r.readUE() + 1
	// frame_mbs_only_flag
	frameMbsOnly := r.readBit()
	if frameMbsOnly == 0 {
		r.readBit() // mb_adaptive_frame_field_flag
	}
	// direct_8x8_inference_flag
	r.readBit()
	// frame_cropping_flag
	frameCropping := r.readBit()

	var cropLeft, cropRight, cropTop, cropBottom int
	if frameCropping == 1 {
		cropLeftMinus1 := r.readUE()
		cropRightMinus1 := r.readUE()
		cropTopMinus1 := r.readUE()
		cropBottomMinus1 := r.readUE()

		var cropUnitX, cropUnitY int
		if chromaFormatIDC == 0 {
			cropUnitX, cropUnitY = 1, 1
		} else if chromaFormatIDC == 1 {
			cropUnitX, cropUnitY = 2, 2
		} else if chromaFormatIDC == 2 {
			cropUnitX, cropUnitY = 2, 1
		} else {
			cropUnitX, cropUnitY = 1, 1
		}
		cropLeft = cropUnitX * cropLeftMinus1
		cropRight = cropUnitX * cropRightMinus1
		cropTop = cropUnitY * cropTopMinus1
		cropBottom = cropUnitY * cropBottomMinus1
	}

	width = picWidthInMbs*16 - cropLeft - cropRight
	height = (2-frameMbsOnly)*picHeightInMapUnits*16 - cropTop - cropBottom

	if width <= 0 || height <= 0 || width > 8192 || height > 8192 {
		return 0, 0
	}
	return width, height
}

// parseHEVCSPSResolution extracts width and height from an HEVC SPS NAL unit.
// HEVC SPS has a 2-byte NAL header, then: sps_video_parameter_set_id(4),
// sps_max_sub_layers_minus1(3), sps_temporal_id_nesting_flag(1),
// profile_tier_level(88+ bits for max_sub_layers=1),
// sps_seq_parameter_set_id(UE), chroma_format_idc(UE),
// pic_width_in_luma_samples(UE), pic_height_in_luma_samples(UE).
// Returns (0, 0) if parsing fails.
func parseHEVCSPSResolution(sps []byte) (width, height int) {
	if len(sps) < 8 {
		return 0, 0
	}
	rbsp := removeEmulationPrevention(sps[2:]) // skip 2-byte NAL header
	if len(rbsp) < 13 {
		return 0, 0
	}
	r := &bitReader{data: rbsp}
	// sps_video_parameter_set_id (4 bits)
	r.readBits(4)
	// sps_max_sub_layers_minus1 (3 bits)
	maxSubLayersMinus1 := r.readBits(3)
	// sps_temporal_id_nesting_flag (1 bit)
	r.readBit()
	// profile_tier_level: skip general_profile_space(2) + general_tier_flag(1) + general_profile_idc(5)
	r.readBits(8)
	// general_profile_compatibility_flag[32]: 32 bits
	r.readBits(32)
	// general constraint indicator flags: 48 bits
	r.readBits(48)
	// general_level_idc: 8 bits
	r.readBits(8)
	// sub-layer profile_present/level_present flags: 2 bits per sub-layer (skip)
	for i := 0; i < maxSubLayersMinus1; i++ {
		r.readBits(2)
	}
	if maxSubLayersMinus1 > 0 {
		// sub_layer_level_present_flag: 1 bit per sub-layer
		for i := 0; i < maxSubLayersMinus1; i++ {
			r.readBit()
		}
	}
	// sps_seq_parameter_set_id (UE)
	r.readUE()
	// chroma_format_idc (UE)
	chromaFormatIDC := r.readUE()
	if chromaFormatIDC == 3 {
		r.readBit() // separate_colour_plane_flag
	}
	// pic_width_in_luma_samples (UE)
	width = r.readUE()
	// pic_height_in_luma_samples (UE)
	height = r.readUE()
	if width <= 0 || height <= 0 || width > 8192 || height > 8192 {
		return 0, 0
	}
	return width, height
}
