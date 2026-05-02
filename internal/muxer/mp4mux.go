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
	id      int
	sps     []byte
	pps     []byte
	width   int
	height  int
	samples []sample
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
	filePath     string
	file         *os.File
	mu           sync.Mutex
	tracks       []*track
	nextTrackID  int
	totalDuration time.Duration
	closed       bool
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
		id:     m.nextTrackID,
		sps:    spsCopy,
		pps:   ppsCopy,
	}
	t.width, t.height = parseSPSResolution(spsCopy)

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
	ftypSize, err := writeFtyp(w)
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

func writeFtyp(w *mp4.Writer) (int64, error) {
	start, _ := w.Seek(0, 1)
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("ftyp")})
	if err != nil {
		return 0, err
	}

	ftyp := &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 0,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'i', 's', 'o', '2'}},
			{CompatibleBrand: [4]byte{'a', 'v', 'c', '1'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
		},
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
	for _, tr := range tracks {
		if err := writeTrak(w, tr, chunkOffset); err != nil {
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

	// vmhd
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

	// stsd > avc1 > avcC
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsd")})
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	// avc1
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
		sizes[i] = uint32(len(s.data)) + 4
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

// --- Helpers ---

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
			binary.BigEndian.PutUint32(lenBuf[:], uint32(len(s.data)))
			buf = append(buf, lenBuf[:]...)
			buf = append(buf, s.data...)
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
		r.readUE() // bit_depth_luma_minus8
		r.readUE() // bit_depth_chroma_minus8
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
		r.readSE() // offset_for_non_ref_pic
		r.readSE() // offset_for_top_to_bottom_field
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
