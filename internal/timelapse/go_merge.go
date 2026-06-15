package timelapse

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/abema/go-mp4"
)

// init registers MJPEG sample entry box types with go-mp4 library.
// Without this registration, mp4.Marshal for VisualSampleEntry with type 'mjpa'
// would fail with ErrBoxInfoNotFound.
func init() {
	mp4.AddAnyTypeBoxDef(&mp4.VisualSampleEntry{}, mp4.StrToBoxType("mjpa"))
}

// GoMerger implements TimelapseMerger using pure Go to create an MP4 file
// from JPEG frames. Each JPEG is stored as a sample in an MJPEG video track.
// When jpegQuality >= 0, frames are decoded and re-encoded at the given quality
// to reduce file size. When jpegQuality < 0, original JPEG data is used as-is (passthrough).
type GoMerger struct {
	jpegQuality int
}

// NewGoMerger creates a new GoMerger with passthrough mode (original JPEG quality preserved).
func NewGoMerger() *GoMerger {
	return &GoMerger{jpegQuality: -1}
}


// CanMerge always returns true since this is a pure Go implementation.
func (m *GoMerger) CanMerge() bool {
	return true
}

// Tier returns the merge tier identifier.
func (m *GoMerger) Tier() MergeTier {
	return TierGo
}

func (m *GoMerger) Merge(ctx context.Context, framesDir, outputPath string, fps int) (*MergeResult, error) {
	// List and sort frame files.
	frames, err := listFrameFiles(framesDir)
	if err != nil {
		return &MergeResult{
			Tier:  TierGo,
			Error: err.Error(),
		}, err
	}

	if len(frames) == 0 {
		err := fmt.Errorf("no JPEG frames found in %s", framesDir)
		return &MergeResult{
			Tier:  TierGo,
			Error: err.Error(),
		}, err
	}

	// Calculate sample duration in milliseconds.
	if fps <= 0 {
		fps = 1
	}
	sampleDuration := time.Duration(1000/fps) * time.Millisecond

	// Create the MP4 file.
	muxer, err := newMJPEGMuxer(outputPath)
	if err != nil {
		return &MergeResult{
			Tier:  TierGo,
			Error: err.Error(),
		}, err
	}

	// Read each JPEG and add as a sample.
	for _, framePath := range frames {
		select {
		case <-ctx.Done():
			muxer.close()
			os.Remove(outputPath)
			return &MergeResult{
				Tier:  TierGo,
				Error: ctx.Err().Error(),
			}, ctx.Err()
		default:
		}

		data, err := os.ReadFile(framePath)
		if err != nil {
			muxer.close()
			return &MergeResult{
				Tier:  TierGo,
				Error: err.Error(),
			}, err
		}

		// Re-encode JPEG at lower quality if configured.
		if m.jpegQuality >= 0 {
			data, err = reencodeJPEG(data, m.jpegQuality)
			if err != nil {
				muxer.close()
				return &MergeResult{
					Tier:  TierGo,
					Error: fmt.Sprintf("re-encode frame %s: %v", framePath, err),
				}, err
			}
		}

		if err := muxer.addSample(data, sampleDuration); err != nil {
			muxer.close()
			return &MergeResult{
				Tier:  TierGo,
				Error: err.Error(),
			}, err
		}
	}

	if err := muxer.close(); err != nil {
		return &MergeResult{
			Tier:  TierGo,
			Error: err.Error(),
		}, err
	}

	framesMerged := len(frames)
	return &MergeResult{
		Tier:         TierGo,
		OutputPath:   outputPath,
		FramesMerged: framesMerged,
		Duration:     float64(framesMerged) * sampleDuration.Seconds(),
	}, nil
}

// listFrameFiles returns a sorted list of JPEG frame files in the directory.
func listFrameFiles(dir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "frame_*.jpg"))
	if err != nil {
		return nil, err
	}
	// Also check for .jpeg extension.
	matches2, err := filepath.Glob(filepath.Join(dir, "frame_*.jpeg"))
	if err != nil {
		return nil, err
	}
	matches = append(matches, matches2...)

	sort.Strings(matches)
	return matches, nil
}

// --- MJPEG MP4 Muxer ---

// mjpegMuxer creates an MP4 file with an MJPEG video track.
type mjpegMuxer struct {
	filePath string
	samples  []mjpegSample
}

type mjpegSample struct {
	data     []byte
	duration time.Duration
}

// newMJPEGMuxer creates a new muxer for the given output path.
func newMJPEGMuxer(filePath string) (*mjpegMuxer, error) {
	return &mjpegMuxer{filePath: filePath}, nil
}

// addSample adds a JPEG frame as a sample.
func (m *mjpegMuxer) addSample(data []byte, duration time.Duration) error {
	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)
	m.samples = append(m.samples, mjpegSample{
		data:     dataCopy,
		duration: duration,
	})
	return nil
}

// close finalizes the MP4 file.
func (m *mjpegMuxer) close() error {
	if len(m.samples) == 0 {
		return nil
	}

	f, err := os.Create(m.filePath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	// Step 1: Calculate moov size by writing to a buffer.
	buf := &bytesWriter{}
	bw := mp4.NewWriter(buf)
	if err := m.writeMoov(bw, 0); err != nil {
		return fmt.Errorf("calculate moov size: %w", err)
	}
	moovSize := buf.len()

	// Step 2: Write ftyp to the real file.
	w := mp4.NewWriter(f)
	ftypSize, err := m.writeFtyp(w)
	if err != nil {
		return fmt.Errorf("write ftyp: %w", err)
	}

	// Step 3: mdat data starts at ftypSize + moovSize + 8 (mdat header).
	mdatDataOffset := int64(ftypSize) + int64(moovSize) + 8

	// Step 4: Write moov with correct stco offset.
	if err := m.writeMoov(w, mdatDataOffset); err != nil {
		return fmt.Errorf("write moov: %w", err)
	}

	// Step 5: Write mdat box.
	mdatData := m.collectMdatData()
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

func (m *mjpegMuxer) writeFtyp(w *mp4.Writer) (int64, error) {
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
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
			{CompatibleBrand: [4]byte{'m', 'j', 'p', '2'}},
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

func (m *mjpegMuxer) writeMoov(w *mp4.Writer, chunkOffset int64) error {
	_, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("moov")})
	if err != nil {
		return err
	}
	if err := m.writeMvhd(w); err != nil {
		return err
	}
	if err := m.writeTrak(w, chunkOffset); err != nil {
		return err
	}
	_, err = w.EndBox()
	return err
}

func (m *mjpegMuxer) writeMvhd(w *mp4.Writer) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mvhd")})
	if err != nil {
		return err
	}

	duration := uint32(0)
	for _, s := range m.samples {
		duration += uint32(s.duration.Milliseconds())
	}

	mvhd := &mp4.Mvhd{
		Timescale:   1000,
		DurationV0:  duration,
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

func (m *mjpegMuxer) writeTrak(w *mp4.Writer, chunkOffset int64) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("trak")})
	if err != nil {
		return err
	}

	// tkhd
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("tkhd")})
	if err != nil {
		return err
	}

	duration := uint32(0)
	for _, s := range m.samples {
		duration += uint32(s.duration.Milliseconds())
	}

	// Try to extract dimensions from the first JPEG.
	width, height := 0, 0
	if len(m.samples) > 0 {
		width, height = parseJPEGDimensions(m.samples[0].data)
	}
	if width == 0 {
		width = 640
	}
	if height == 0 {
		height = 480
	}

	tkhd := &mp4.Tkhd{
		TrackID:    1,
		DurationV0: duration,
		Width:      uint32(width) << 16,
		Height:     uint32(height) << 16,
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
	if err := m.writeMdia(w, chunkOffset); err != nil {
		return err
	}

	_, err = w.EndBox()
	_ = bi
	return err
}

func (m *mjpegMuxer) writeMdia(w *mp4.Writer, chunkOffset int64) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mdia")})
	if err != nil {
		return err
	}

	duration := uint32(0)
	for _, s := range m.samples {
		duration += uint32(s.duration.Milliseconds())
	}

	// mdhd
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mdhd")})
	if err != nil {
		return err
	}
	mdhd := &mp4.Mdhd{
		Timescale:  1000,
		DurationV0: duration,
		Language:   [3]byte{0x15, 0xC0, 0x00},
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
	if err := m.writeMinf(w, chunkOffset); err != nil {
		return err
	}

	_, err = w.EndBox()
	_ = bi
	return err
}

func (m *mjpegMuxer) writeMinf(w *mp4.Writer, chunkOffset int64) error {
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
	if err := m.writeStbl(w, chunkOffset); err != nil {
		return err
	}

	_, err = w.EndBox()
	_ = bi
	return err
}

func (m *mjpegMuxer) writeStbl(w *mp4.Writer, chunkOffset int64) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stbl")})
	if err != nil {
		return err
	}

	n := len(m.samples)

	// stsd > mjpa (MJPEG sample entry)
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsd")})
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	if err := m.writeMJPEGSampleEntry(w); err != nil {
		return err
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
	sttsEntries := make([]mp4.SttsEntry, n)
	for i, s := range m.samples {
		sttsEntries[i] = mp4.SttsEntry{
			SampleCount: 1,
			SampleDelta: uint32(s.duration.Milliseconds()),
		}
	}
	if _, err := mp4.Marshal(w, &mp4.Stts{EntryCount: uint32(n), Entries: sttsEntries}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi6

	// stsc
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
	for i, s := range m.samples {
		sizes[i] = uint32(len(s.data))
	}
	if _, err := mp4.Marshal(w, &mp4.Stsz{SampleSize: 0, SampleCount: uint32(n), EntrySize: sizes}, mp4.Context{}); err != nil {
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

// writeMJPEGSampleEntry writes the MJPEG sample entry (mjpa) in the stsd box.
// MJPEG in MP4 uses a VisualSampleEntry with type 'mjpa' (or 'mjpg').
func (m *mjpegMuxer) writeMJPEGSampleEntry(w *mp4.Writer) error {
	// Use 'mjpa' as the sample entry type for MJPEG.
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mjpa")})
	if err != nil {
		return err
	}

	// Extract dimensions from first JPEG.
	width, height := 0, 0
	if len(m.samples) > 0 {
		width, height = parseJPEGDimensions(m.samples[0].data)
	}
	if width == 0 {
		width = 640
	}
	if height == 0 {
		height = 480
	}

	mjpa := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType("mjpa")},
			DataReferenceIndex: 1,
		},
		Width:           uint16(width),
		Height:          uint16(height),
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
	}
	if _, err := mp4.Marshal(w, mjpa, mp4.Context{}); err != nil {
		return err
	}

	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi
	return nil
}

func (m *mjpegMuxer) collectMdatData() []byte {
	var buf []byte
	for _, s := range m.samples {
		buf = append(buf, s.data...)
	}
	return buf
}

// --- Helpers ---

// bytesWriter implements io.WriteSeeker backed by a byte buffer.
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

// parseJPEGDimensions extracts width and height from JPEG SOF0 marker.
func parseJPEGDimensions(data []byte) (width, height int) {
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		return 0, 0
	}

	i := 2
	for i < len(data) {
		if data[i] != 0xFF {
			i++
			continue
		}
		if i+1 >= len(data) {
			break
		}
		marker := data[i+1]
		// Skip padding.
		if marker == 0xFF {
			i++
			continue
		}
		// SOI or markers without length.
		if marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD9) {
			i += 2
			continue
		}
		// SOF0, SOF1, SOF2 (baseline, extended, progressive)
		if marker == 0xC0 || marker == 0xC1 || marker == 0xC2 {
			if i+9 < len(data) {
				height = int(data[i+3])<<8 | int(data[i+4])
				width = int(data[i+5])<<8 | int(data[i+6])
				return width, height
			}
		}
		// Read segment length.
		if i+3 >= len(data) {
			break
		}
		length := int(data[i+2])<<8 | int(data[i+3])
		if length < 2 {
			break
		}
		i += 2 + length
	}
	return 0, 0
}


// reencodeJPEG decodes a JPEG image and re-encodes it at the given quality level.
// This provides file size reduction at the cost of visual quality.
// quality: 1-100 (1 = smallest, 100 = best).
func reencodeJPEG(data []byte, quality int) ([]byte, error) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode jpeg: %w", err)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("encode jpeg at quality %d: %w", quality, err)
	}
	return buf.Bytes(), nil
}
