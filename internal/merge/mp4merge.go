package merge

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"
)

const (
	mergeBufferSize = 1 << 20 // 1MB buffer for sample data copying
)

// mergedSample holds a sample's info relative to the merged output file.
type mergedSample struct {
	offset     int64
	size       uint32
	duration   uint32
	isKeyFrame bool
}

// MergeMP4Segments performs a streaming merge of multiple MP4 segments into a single output file.
// All segments must share the same codec and SPS/PPS (for H.264) or VPS/SPS/PPS (for H.265).
// The output file is written to outputPath directly (caller handles temp→final rename).
func MergeMP4Segments(segments []*SegmentInfo, outputPath string) error {
	if len(segments) == 0 {
		return fmt.Errorf("no segments to merge")
	}

	first := segments[0]
	codec := first.Codec

	// Validate all segments share the same codec and SPS/PPS.
	for i, seg := range segments {
		if seg.Codec != codec {
			return fmt.Errorf("segment %d: codec mismatch (%s vs %s)", i, seg.Codec, codec)
		}
		if i > 0 {
			if !bytes.Equal(seg.SPS, first.SPS) || !bytes.Equal(seg.PPS, first.PPS) {
				return fmt.Errorf("segment %d: SPS/PPS mismatch", i)
			}
			if codec == "h265" && !bytes.Equal(seg.VPS, first.VPS) {
				return fmt.Errorf("segment %d: VPS mismatch", i)
			}
		}
	}

	out, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer out.Close()

	// Step 1: Write ftyp box.
	ftypSize, err := writeMergeFtyp(out, codec)
	if err != nil {
		return fmt.Errorf("write ftyp: %w", err)
	}

	// Step 2: Calculate moov size by writing to a buffer with placeholder offsets.
	// Count total samples across all segments.
	var totalSamples int
	for _, seg := range segments {
		totalSamples += seg.SampleCount
	}

	tmpTrack := &mergeTrack{
		isH265:       codec == "h265",
		sps:          first.SPS,
		pps:          first.PPS,
		vps:          first.VPS,
		timescale:    first.Timescale,
		totalSamples: totalSamples,
	}
	// Populate placeholder samples so the size calculation includes per-sample tables.
	tmpTrack.samples = make([]mergedSample, totalSamples)
	for i := range tmpTrack.samples {
		tmpTrack.samples[i].duration = 33 // placeholder
	}

	// Write moov to a buffer to get its exact size.
	moovBuf := &bytesWriter{}
	moovW := mp4.NewWriter(moovBuf)
	if err := writeMergeMoov(moovW, tmpTrack, 0); err != nil {
		return fmt.Errorf("calculate moov size: %w", err)
	}
	moovSize := moovBuf.len()

	// Clear placeholder samples; real ones will be set after streaming mdat.
	tmpTrack.samples = nil

	// Step 3: Write placeholder moov at the correct position.
	moovOffset := ftypSize
	moovPlaceholder := make([]byte, moovSize)
	if _, err := out.Write(moovPlaceholder); err != nil {
		return fmt.Errorf("write moov placeholder: %w", err)
	}

	// Step 4: Write mdat box header (size placeholder + "mdat").
	mdatHeaderOffset := moovOffset + moovSize
	var mdatHeader [8]byte
	copy(mdatHeader[4:8], "mdat")
	if _, err := out.Write(mdatHeader[:]); err != nil {
		return fmt.Errorf("write mdat header: %w", err)
	}
	mdatDataStart := mdatHeaderOffset + 8

	// Step 5: Stream sample data from each segment into the output.
	buf := make([]byte, mergeBufferSize)
	var currentOffset int64
	var allSamples []mergedSample

	for _, seg := range segments {
		if seg.FilePath == "" {
			return fmt.Errorf("segment has empty FilePath")
		}

		src, err := os.Open(seg.FilePath)
		if err != nil {
			return fmt.Errorf("open segment %s: %w", seg.FilePath, err)
		}

		for _, s := range seg.Samples {
			sampleAbsOffset := currentOffset + mdatDataStart

			_, copyErr := copySampleData(src, out, s.Offset, int64(s.Size), buf)
			if copyErr != nil {
				src.Close()
				return fmt.Errorf("copy sample from %s at offset %d: %w", seg.FilePath, s.Offset, copyErr)
			}

			allSamples = append(allSamples, mergedSample{
				offset:     sampleAbsOffset,
				size:       s.Size,
				duration:   s.Duration,
				isKeyFrame: s.IsKeyFrame,
			})
			currentOffset += int64(s.Size)
		}

		src.Close()
	}

	// Step 6: Patch mdat box size.
	mdatBoxSize := uint32(8 + currentOffset)
	if _, err := out.Seek(mdatHeaderOffset, io.SeekStart); err != nil {
		return fmt.Errorf("seek to mdat header: %w", err)
	}
	var sizeBuf [4]byte
	binary.BigEndian.PutUint32(sizeBuf[:], mdatBoxSize)
	if _, err := out.Write(sizeBuf[:]); err != nil {
		return fmt.Errorf("write mdat size: %w", err)
	}

	// Step 7: Go back and write the real moov box at the placeholder position.
	if _, err := out.Seek(moovOffset, io.SeekStart); err != nil {
		return fmt.Errorf("seek to moov: %w", err)
	}

	// Calculate total duration in timescale units.
	var totalDuration uint32
	for _, s := range allSamples {
		totalDuration += s.duration
	}
	tmpTrack.duration = totalDuration
	tmpTrack.samples = allSamples

	// Use a limited writer to prevent overflow into mdat.
	moovOut := &limitedWriter{w: out, remaining: moovSize, pos: moovOffset}
	moovWriter := mp4.NewWriter(moovOut)
	chunkOffset := mdatDataStart
	if err := writeMergeMoov(moovWriter, tmpTrack, chunkOffset); err != nil {
		return fmt.Errorf("write moov: %w", err)
	}

	if moovOut.remaining < 0 {
		return fmt.Errorf("moov box overflow: calculated %d, actual %d", moovSize, moovSize-moovOut.remaining)
	}

	// Sync and close.
	if err := out.Sync(); err != nil {
		return fmt.Errorf("sync output: %w", err)
	}

	return nil
}

// copySampleData copies size bytes from src at offset to dst using the provided buffer.
func copySampleData(src *os.File, dst io.Writer, offset, size int64, buf []byte) (int64, error) {
	if _, err := src.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	remaining := size
	var written int64
	for remaining > 0 {
		toRead := int64(len(buf))
		if toRead > remaining {
			toRead = remaining
		}
		n, err := src.Read(buf[:toRead])
		if err != nil && err != io.EOF {
			return written, err
		}
		if n == 0 {
			break
		}
		nw, err := dst.Write(buf[:n])
		if err != nil {
			return written, err
		}
		written += int64(nw)
		remaining -= int64(n)
	}
	return written, nil
}

// mergeTrack holds track info for building the merged moov box.
type mergeTrack struct {
	isH265       bool
	sps          []byte
	pps          []byte
	vps          []byte
	timescale    uint32
	totalSamples int
	duration     uint32
	samples      []mergedSample
}

// writeMergeMoov writes a complete moov box for the merged output.
func writeMergeMoov(w *mp4.Writer, tr *mergeTrack, chunkOffset int64) error {
	_, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("moov")})
	if err != nil {
		return err
	}
	if err := writeMergeMvhd(w, tr); err != nil {
		return err
	}
	if err := writeMergeTrak(w, tr, chunkOffset); err != nil {
		return err
	}
	_, err = w.EndBox()
	return err
}

func writeMergeMvhd(w *mp4.Writer, tr *mergeTrack) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mvhd")})
	if err != nil {
		return err
	}
	mvhd := &mp4.Mvhd{
		Timescale:   tr.timescale,
		DurationV0:  tr.duration,
		Rate:        0x00010000,
		Volume:      0x0100,
		NextTrackID: 2,
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

func writeMergeTrak(w *mp4.Writer, tr *mergeTrack, chunkOffset int64) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("trak")})
	if err != nil {
		return err
	}
	// tkhd — width/height unknown from merge, use 0 (players infer from stream).
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("tkhd")})
	if err != nil {
		return err
	}
	tkhd := &mp4.Tkhd{
		TrackID:    1,
		DurationV0: tr.duration,
		Width:      0,
		Height:     0,
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
	if err := writeMergeMdia(w, tr, chunkOffset); err != nil {
		return err
	}
	_, err = w.EndBox()
	_ = bi
	return err
}

func writeMergeMdia(w *mp4.Writer, tr *mergeTrack, chunkOffset int64) error {
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
		Timescale:  tr.timescale,
		DurationV0: tr.duration,
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
	// minf > stbl
	if err := writeMergeMinf(w, tr, chunkOffset); err != nil {
		return err
	}
	_, err = w.EndBox()
	_ = bi
	return err
}

func writeMergeMinf(w *mp4.Writer, tr *mergeTrack, chunkOffset int64) error {
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
	if err := writeMergeStbl(w, tr, chunkOffset); err != nil {
		return err
	}
	_, err = w.EndBox()
	_ = bi
	return err
}

func writeMergeStbl(w *mp4.Writer, tr *mergeTrack, chunkOffset int64) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stbl")})
	if err != nil {
		return err
	}
	samples := tr.samples
	n := len(samples)

	// stsd
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsd")})
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	if tr.isH265 {
		if err := writeMergeH265SampleEntry(w, tr); err != nil {
			return err
		}
	} else {
		if err := writeMergeH264SampleEntry(w, tr); err != nil {
			return err
		}
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi2

	// stts — one entry per sample.
	bi6, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stts")})
	if err != nil {
		return err
	}
	sttsEntries := make([]mp4.SttsEntry, n)
	for i, s := range samples {
		sttsEntries[i] = mp4.SttsEntry{
			SampleCount: 1,
			SampleDelta: s.duration,
		}
	}
	if _, err := mp4.Marshal(w, &mp4.Stts{EntryCount: uint32(n), Entries: sttsEntries}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi6

	// stsc — all samples in one chunk, SampleDescriptionIndex MUST be 1.
	bi7, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsc")})
	if err != nil {
		return err
	}
	stscEntries := []mp4.StscEntry{
		{FirstChunk: 1, SamplesPerChunk: uint32(n), SampleDescriptionIndex: 1},
	}
	if n == 0 {
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
	sizes := make([]uint32, n)
	for i, s := range samples {
		sizes[i] = s.size
	}
	if _, err := mp4.Marshal(w, &mp4.Stsz{SampleSize: 0, SampleCount: uint32(n), EntrySize: sizes}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi8

	// stco — single chunk at chunkOffset.
	bi9, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stco")})
	if err != nil {
		return err
	}
	stco := &mp4.Stco{EntryCount: 0, ChunkOffset: nil}
	if n > 0 {
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

// writeMergeH264SampleEntry writes avc1 + avcC boxes for H.264.
func writeMergeH264SampleEntry(w *mp4.Writer, tr *mergeTrack) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("avc1")})
	if err != nil {
		return err
	}
	avc1 := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType("avc1")},
			DataReferenceIndex: 1,
		},
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
	}
	if _, err := mp4.Marshal(w, avc1, mp4.Context{}); err != nil {
		return err
	}
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("avcC")})
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
	_ = bi2
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi
	return nil
}

// writeMergeH265SampleEntry writes hvc1 + hvcC boxes for H.265.
func writeMergeH265SampleEntry(w *mp4.Writer, tr *mergeTrack) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hvc1")})
	if err != nil {
		return err
	}
	hvc1 := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType("hvc1")},
			DataReferenceIndex: 1,
		},
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
	}
	if _, err := mp4.Marshal(w, hvc1, mp4.Context{}); err != nil {
		return err
	}
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hvcC")})
	if err != nil {
		return err
	}
	hvcC := buildMergeHvcC(tr.vps, tr.sps, tr.pps)
	if _, err := mp4.Marshal(w, hvcC, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi2
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi
	return nil
}

// buildMergeHvcC constructs an HvcC from VPS, SPS, PPS.
func buildMergeHvcC(vps, sps, pps []byte) *mp4.HvcC {
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
		GeneralProfileCompatibility: [32]bool{},
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

// writeMergeFtyp writes a minimal ftyp box to the output file.
func writeMergeFtyp(w io.Writer, codec string) (int64, error) {
	brands := [][4]byte{
		{'i', 's', 'o', 'm'},
		{'i', 's', 'o', '2'},
		{'m', 'p', '4', '1'},
	}
	if codec == "h264" {
		brands = append(brands, [4]byte{'a', 'v', 'c', '1'})
	} else if codec == "h265" {
		brands = append(brands, [4]byte{'h', 'e', 'v', '1'})
	}

	boxSize := uint32(8 + 4 + 4 + 4*len(brands))
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], boxSize)
	if _, err := w.Write(buf[:]); err != nil {
		return 0, err
	}
	if _, err := w.Write([]byte("ftyp")); err != nil {
		return 0, err
	}
	if _, err := w.Write([]byte("isom")); err != nil {
		return 0, err
	}
	binary.BigEndian.PutUint32(buf[:], 0)
	if _, err := w.Write(buf[:]); err != nil {
		return 0, err
	}
	for _, b := range brands {
		if _, err := w.Write(b[:]); err != nil {
			return 0, err
		}
	}
	return int64(boxSize), nil
}

// --- Helper types ---

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
	case 0:
		b.pos = offset
	case 1:
		b.pos += offset
	case 2:
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

// limitedWriter wraps an io.WriteSeeker and limits the total bytes written.
// Used to write moov box in-place without overflowing into mdat.
// It tracks the actual file position so seeks are properly accounted for.
type limitedWriter struct {
	w         io.WriteSeeker
	remaining int64
	pos       int64 // tracks actual file position
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.w.Write(p)
	l.remaining -= int64(n)
	l.pos += int64(n)
	return n, err
}

// Seek delegates to the underlying writer.
// Adjusts remaining based on position changes to prevent overflow.
func (l *limitedWriter) Seek(offset int64, whence int) (int64, error) {
	newPos, err := l.w.Seek(offset, whence)
	if err != nil {
		return newPos, err
	}
	// Adjust remaining: forward seek reduces, backward seek increases.
	delta := newPos - l.pos
	l.remaining -= delta
	l.pos = newPos
	return newPos, nil
}
