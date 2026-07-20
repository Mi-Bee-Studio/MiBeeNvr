// Package timelapse — RecordingFrameExtractor tests.
//
// These tests verify recording frame extraction for all three supported
// formats using synthetic test fixtures (no real recording files needed).
package timelapse

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/abema/go-mp4"
)

// --- Test AVI (JPEG) frame extraction ---

// buildTestJPEG creates a minimal valid JPEG frame (SOI + SOF0 + SOS + EOI)
// with the given frame number encoded in the scan data.
func buildTestJPEG(t *testing.T, frameNum byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	// SOI marker
	buf.Write([]byte{0xFF, 0xD8})
	// APP0/JFIF segment (optional but common)
	buf.Write([]byte{
		0xFF, 0xE0, 0x00, 0x10, // APP0, length=16
		'J', 'F', 'I', 'F', 0x00, // identifier
		0x01, 0x01, // version
		0x00, // units
		0x00, 0x01, // x density
		0x00, 0x01, // y density
		0x00, 0x00, // thumbnail
	})
	// SOF0 (baseline): 64x48
	buf.Write([]byte{
		0xFF, 0xC0, 0x00, 0x11, // SOF0, length=17
		0x08,       // precision (8 bits)
		0x00, 0x30, // height = 48
		0x00, 0x40, // width = 64
		0x03,       // number of components
		0x01, 0x11, 0x00, // component 1: Y, 1:1 sampling, quant table 0
		0x02, 0x11, 0x00, // component 2: Cb
		0x03, 0x11, 0x00, // component 3: Cr
	})
	// SOS (start of scan) — minimal
	buf.Write([]byte{
		0xFF, 0xDA, 0x00, 0x08, // SOS, length=8
		0x03,       // 3 components
		0x01, 0x00, // component 1: DC/AC table 0
		0x02, 0x11, // component 2: DC/AC table 1
		0x03, 0x11, // component 3: DC/AC table 1
		0x00, 0x3F, 0x00, // spectral selection + approx
	})
	// Minimal scan data (distinct per frame)
	buf.Write([]byte{frameNum, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	// EOI marker
	buf.Write([]byte{0xFF, 0xD9})
	return buf.Bytes()
}

// TestRecordingFrameExtractor_AVI verifies JPEG frame extraction from an AVI file.
func TestRecordingFrameExtractor_AVI(t *testing.T) {
	tmpDir := t.TempDir()
	aviPath := filepath.Join(tmpDir, "test.avi")
	outputDir := filepath.Join(tmpDir, "frames")

	// Create an AVI with 30 JPEG frames at ~30fps (33333 us per frame) = ~1 second.
	frameCount := 30
	{
		var buf bytes.Buffer
		m := avi.NewVideoOnlyMuxer(&buf, 64, 48)
		for i := range frameCount {
			frame := buildTestJPEG(t, byte(i))
			if err := m.WriteVideo(frame, int64(i)*33333); err != nil {
				t.Fatalf("WriteVideo %d: %v", i, err)
			}
		}
		if err := m.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if err := os.WriteFile(aviPath, buf.Bytes(), 0o644); err != nil {
			t.Fatalf("Write AVI: %v", err)
		}
	}

	extractor := NewRecordingFrameExtractor()
	n, err := extractor.ExtractFrames(aviPath, model.FormatAVI, 100*time.Millisecond, outputDir)
	if err != nil {
		t.Fatalf("ExtractFrames failed: %v", err)
	}

	// At 30fps with 100ms interval, expect ~10 frames.
	expected := 10
	if n != expected {
		t.Errorf("expected %d frames, got %d", expected, n)
	}

	// Verify output files exist and have correct naming.
	matches, err := filepath.Glob(filepath.Join(outputDir, "frame_*.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != expected {
		t.Errorf("expected %d frame files, got %d", expected, len(matches))
	}

	// Verify sort order.
	for i := 1; i < len(matches); i++ {
		if matches[i-1] > matches[i] {
			t.Errorf("files not sorted: %s > %s", matches[i-1], matches[i])
		}
	}

	// Verify each file starts with JPEG SOI marker.
	for i, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			t.Fatalf("read %s: %v", m, err)
		}
		if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
			t.Errorf("frame %d (%s) does not start with JPEG SOI", i, m)
		}
	}
}

// TestRecordingFrameExtractor_AVI_Empty tests error on AVI with no frames.
func TestRecordingFrameExtractor_AVI_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	aviPath := filepath.Join(tmpDir, "empty.avi")
	outputDir := filepath.Join(tmpDir, "frames")

	// Create an empty AVI (no video chunks).
	{
		var buf bytes.Buffer
		m := avi.NewVideoOnlyMuxer(&buf, 64, 48)
		if err := m.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if err := os.WriteFile(aviPath, buf.Bytes(), 0o644); err != nil {
			t.Fatalf("Write AVI: %v", err)
		}
	}

	extractor := NewRecordingFrameExtractor()
	_, err := extractor.ExtractFrames(aviPath, model.FormatAVI, time.Second, outputDir)
	if err == nil {
		t.Error("expected error for empty AVI, got nil")
	}
}

// TestRecordingFrameExtractor_AVI_IntervalTooLarge tests error when interval > duration.
func TestRecordingFrameExtractor_AVI_IntervalTooLarge(t *testing.T) {
	tmpDir := t.TempDir()
	aviPath := filepath.Join(tmpDir, "short.avi")
	outputDir := filepath.Join(tmpDir, "frames")

	{
		var buf bytes.Buffer
		m := avi.NewVideoOnlyMuxer(&buf, 64, 48)
		if err := m.WriteVideo(buildTestJPEG(t, 1), 0); err != nil {
			t.Fatalf("WriteVideo: %v", err)
		}
		if err := m.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if err := os.WriteFile(aviPath, buf.Bytes(), 0o644); err != nil {
			t.Fatalf("Write AVI: %v", err)
		}
	}

	extractor := NewRecordingFrameExtractor()
	_, err := extractor.ExtractFrames(aviPath, model.FormatAVI, 10*time.Second, outputDir)
	if err == nil {
		t.Error("expected error when interval > duration, got nil")
	}
}

// --- Test H264 MP4 frame extraction ---

// testSample describes a single MP4 sample for test MP4 creation.
type testSample struct {
	data       []byte // length-prefixed NALU data
	isKeyFrame bool
	duration   uint32 // in timescale units
}

// buildH264IDRSample builds a sample with a single H264 IDR NALU (type 5).
// The data is length-prefixed: [4-byte BE length][NALU bytes].
func buildH264IDRSample() []byte {
	// NALU header: forbidden=0, nal_ref_idc=3, nal_unit_type=5 (IDR)
	nalu := []byte{0x65, 0x88, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	sample := make([]byte, 4+len(nalu))
	binary.BigEndian.PutUint32(sample, uint32(len(nalu)))
	copy(sample[4:], nalu)
	return sample
}

// buildH264NonIDRSample builds a sample with a single H264 non-IDR NALU (type 1).
func buildH264NonIDRSample() []byte {
	nalu := []byte{0x41, 0x88, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	sample := make([]byte, 4+len(nalu))
	binary.BigEndian.PutUint32(sample, uint32(len(nalu)))
	copy(sample[4:], nalu)
	return sample
}

// buildH265IDRSample builds a sample with a single H265 IDR NALU (type 19).
func buildH265IDRSample() []byte {
	// H265 NALU header (2 bytes):
	// Byte 0: forbidden=0, nal_unit_type=19 (IDR_W_RADL), nuh_layer_id high 2 bits = 0
	// Byte 1: nuh_layer_id low 4 bits = 0, nuh_temporal_id_plus1 = 1
	nalu := []byte{0x26, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	sample := make([]byte, 4+len(nalu))
	binary.BigEndian.PutUint32(sample, uint32(len(nalu)))
	copy(sample[4:], nalu)
	return sample
}

// buildH265NonIDRSample builds a sample with a single H265 non-IDR NALU (type 1).
func buildH265NonIDRSample() []byte {
	// nal_unit_type = 1 (non-IDR)
	nalu := []byte{0x02, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	sample := make([]byte, 4+len(nalu))
	binary.BigEndian.PutUint32(sample, uint32(len(nalu)))
	copy(sample[4:], nalu)
	return sample
}

// buildTestH264SPS creates a minimal H264 SPS (used in avcC for MP4 creation).
func buildTestH264SPS() []byte {
	// SPS NALU header: type 7 (SPS)
	// Minimal valid-looking SPS: nal_header(1) + profile(1) + compat(1) + level(1)
	return []byte{0x67, 0x42, 0x80, 0x1E}
}

// buildTestH264PPS creates a minimal H264 PPS.
func buildTestH264PPS() []byte {
	return []byte{0x68, 0xCE, 0x38, 0x80}
}

// buildTestH265VPS creates a minimal H265 VPS.
func buildFrameTestH265VPS() []byte {
	// VPS NALU type 32: (32 << 1) = 0x40
	return []byte{0x40, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
}

// buildTestH265SPS creates a minimal H265 SPS.
func buildFrameTestH265SPS() []byte {
	// SPS NALU type 33: (33 << 1) = 0x42
	return []byte{0x42, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
}

// buildTestH265PPS creates a minimal H265 PPS.
func buildFrameTestH265PPS() []byte {
	// PPS NALU type 34: (34 << 1) = 0x44
	return []byte{0x44, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
}

// createTestMP4 builds a valid MP4 file with H264 or H265 video track
// containing the given samples. Returns the file path.
func createTestMP4(t *testing.T, tmpDir, name string, isH265 bool, samples []testSample, timescale uint32) string {
	t.Helper()

	sps, pps, vps := buildTestH264SPS(), buildTestH264PPS(), []byte(nil)
	codec := "h264"
	if isH265 {
		sps, pps, vps = buildFrameTestH265SPS(), buildFrameTestH265PPS(), buildFrameTestH265VPS()
		codec = "h265"
	}

	filePath := filepath.Join(tmpDir, name)

	// First pass: compute moov size by writing to a throwaway buffer.
	muxer := &testMP4Muxer{
		samples:   samples,
		timescale: timescale,
		codec:     codec,
		sps:       sps,
		pps:       pps,
		vps:       vps,
	}

	buf := &bytesWriter{}
	bw := mp4.NewWriter(buf)
	if err := muxer.writeMoov(bw, 0); err != nil {
		t.Fatalf("compute moov size: %v", err)
	}
	moovSize := buf.len()

	// Write the real file.
	f, err := os.Create(filePath)
	if err != nil {
		t.Fatalf("create MP4: %v", err)
	}
	defer f.Close()

	w := mp4.NewWriter(f)
	ftypSize, err := writeFtyp(w)
	if err != nil {
		t.Fatalf("write ftyp: %v", err)
	}

	mdatDataOffset := int64(ftypSize) + moovSize + 8 // +8 for mdat header
	muxer.chunkOffset = mdatDataOffset

	if err := muxer.writeMoov(w, mdatDataOffset); err != nil {
		t.Fatalf("write moov: %v", err)
	}

	// Write mdat.
	var mdatPayload []byte
	for _, s := range samples {
		mdatPayload = append(mdatPayload, s.data...)
	}
	mdatBoxSize := uint64(8 + len(mdatPayload))
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mdat"), Size: mdatBoxSize})
	if err != nil {
		t.Fatalf("start mdat: %v", err)
	}
	if _, err := w.Write(mdatPayload); err != nil {
		t.Fatalf("write mdat: %v", err)
	}
	if _, err := w.EndBox(); err != nil {
		t.Fatalf("end mdat: %v", err)
	}
	_ = bi

	return filePath
}

// testMP4Muxer writes the moov box with proper sample tables for test MP4s.
type testMP4Muxer struct {
	samples     []testSample
	timescale   uint32
	codec       string // "h264" or "h265"
	sps, pps    []byte
	vps         []byte
	chunkOffset int64
}

func (m *testMP4Muxer) writeMoov(w *mp4.Writer, chunkOffset int64) error {
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

func (m *testMP4Muxer) writeMvhd(w *mp4.Writer) error {
	_, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mvhd")})
	if err != nil {
		return err
	}
	var totalDur uint32
	for _, s := range m.samples {
		totalDur += s.duration
	}
	mvhd := &mp4.Mvhd{
		Timescale:   m.timescale,
		DurationV0:  totalDur,
		Rate:        0x00010000,
		Volume:      0x0100,
		NextTrackID: 2,
		Matrix:      [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
	}
	if _, err := mp4.Marshal(w, mvhd, mp4.Context{}); err != nil {
		return err
	}
	_, err = w.EndBox()
	return err
}

func (m *testMP4Muxer) writeTrak(w *mp4.Writer, chunkOffset int64) error {
	_, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("trak")})
	if err != nil {
		return err
	}
	// tkhd
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("tkhd")})
	if err != nil {
		return err
	}
	var totalDur uint32
	for _, s := range m.samples {
		totalDur += s.duration
	}
	tkhd := &mp4.Tkhd{
		TrackID:    1,
		DurationV0: totalDur,
		Width:      640 << 16,
		Height:     480 << 16,
		Matrix:     [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
	}
	if _, err := mp4.Marshal(w, tkhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi2

	if err := m.writeMdia(w, chunkOffset); err != nil {
		return err
	}
	_, err = w.EndBox()
	return err
}

func (m *testMP4Muxer) writeMdia(w *mp4.Writer, chunkOffset int64) error {
	_, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mdia")})
	if err != nil {
		return err
	}
	var totalDur uint32
	for _, s := range m.samples {
		totalDur += s.duration
	}

	// mdhd
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mdhd")})
	if err != nil {
		return err
	}
	mdhd := &mp4.Mdhd{
		Timescale:  m.timescale,
		DurationV0: totalDur,
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

	if err := m.writeMinf(w, chunkOffset); err != nil {
		return err
	}
	_, err = w.EndBox()
	return err
}

func (m *testMP4Muxer) writeMinf(w *mp4.Writer, chunkOffset int64) error {
	_, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("minf")})
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
	if err := writeDinf(w); err != nil {
		return err
	}

	if err := m.writeStbl(w, chunkOffset); err != nil {
		return err
	}
	_, err = w.EndBox()
	return err
}

func (m *testMP4Muxer) writeStbl(w *mp4.Writer, chunkOffset int64) error {
	_, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stbl")})
	if err != nil {
		return err
	}
	n := len(m.samples)

	// stsd with codec-specific sample entry.
	if err := m.writeStsd(w); err != nil {
		return err
	}

	// stts
	bi4, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stts")})
	if err != nil {
		return err
	}
	sttsEntries := make([]mp4.SttsEntry, n)
	for i, s := range m.samples {
		sttsEntries[i] = mp4.SttsEntry{
			SampleCount: 1,
			SampleDelta: s.duration,
		}
	}
	if _, err := mp4.Marshal(w, &mp4.Stts{
		EntryCount: uint32(n),
		Entries:    sttsEntries,
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi4

	// stss (sync sample table) — only written when at least one sample is NOT a keyframe.
	hasNonKeyframe := false
	for _, s := range m.samples {
		if !s.isKeyFrame {
			hasNonKeyframe = true
			break
		}
	}
	if hasNonKeyframe {
		bi5, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stss")})
		if err != nil {
			return err
		}
		var syncSampleNums []uint32
		for i, s := range m.samples {
			if s.isKeyFrame {
				syncSampleNums = append(syncSampleNums, uint32(i+1)) // 1-based
			}
		}
		if _, err := mp4.Marshal(w, &mp4.Stss{
			EntryCount: uint32(len(syncSampleNums)),
			SampleNumber: syncSampleNums,
		}, mp4.Context{}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
		_ = bi5
	}

	// stsc
	bi6, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsc")})
	if err != nil {
		return err
	}
	stscEntries := []mp4.StscEntry{
		{FirstChunk: 1, SamplesPerChunk: uint32(n), SampleDescriptionIndex: 1},
	}
	if _, err := mp4.Marshal(w, &mp4.Stsc{
		EntryCount: uint32(len(stscEntries)),
		Entries:    stscEntries,
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi6

	// stsz
	bi7, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsz")})
	if err != nil {
		return err
	}
	sizes := make([]uint32, n)
	for i, s := range m.samples {
		sizes[i] = uint32(len(s.data))
	}
	if _, err := mp4.Marshal(w, &mp4.Stsz{
		SampleSize:  0,
		SampleCount: uint32(n),
		EntrySize:   sizes,
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi7

	// stco
	bi8, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stco")})
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
	_ = bi8

	_, err = w.EndBox()
	return err
}

func (m *testMP4Muxer) writeStsd(w *mp4.Writer) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsd")})
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}

	if m.codec == "h265" {
		if err := m.writeHvc1SampleEntry(w); err != nil {
			return err
		}
	} else {
		if err := m.writeAvc1SampleEntry(w); err != nil {
			return err
		}
	}

	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi
	return nil
}

func (m *testMP4Muxer) writeAvc1SampleEntry(w *mp4.Writer) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("avc1")})
	if err != nil {
		return err
	}
	avc1 := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType("avc1")},
			DataReferenceIndex: 1,
		},
		Width:           640,
		Height:          480,
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
	}
	if _, err := mp4.Marshal(w, avc1, mp4.Context{}); err != nil {
		return err
	}

	// avcC box
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("avcC")})
	if err != nil {
		return err
	}
	avcC := buildAvcC(m.sps, m.pps)
	if _, err := w.Write(avcC); err != nil {
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

func (m *testMP4Muxer) writeHvc1SampleEntry(w *mp4.Writer) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hvc1")})
	if err != nil {
		return err
	}
	hvc1 := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType("hvc1")},
			DataReferenceIndex: 1,
		},
		Width:           640,
		Height:          480,
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
	}
	if _, err := mp4.Marshal(w, hvc1, mp4.Context{}); err != nil {
		return err
	}

	// hvcC box — use go-mp4 marshaling for parser compatibility.
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hvcC")})
	if err != nil {
		return err
	}
	hvcC := m.buildHvcCForMarshal()
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

// buildHvcCForMarshal builds an mp4.HvcC struct from the test parameter sets.
func (m *testMP4Muxer) buildHvcCForMarshal() *mp4.HvcC {
	var arrays []mp4.HEVCNaluArray

	// Helper to build a single NAL array.
	buildArray := func(naluType uint8, data []byte) mp4.HEVCNaluArray {
		if len(data) == 0 {
			return mp4.HEVCNaluArray{}
		}
		return mp4.HEVCNaluArray{
			Completeness: true,
			NaluType:     naluType,
			NumNalus:     1,
			Nalus: []mp4.HEVCNalu{
				{Length: uint16(len(data)), NALUnit: data},
			},
		}
	}

	// Only add arrays that are present.
	if len(m.vps) > 0 {
		vpsArray := buildArray(32, m.vps) // VPS NAL unit type = 32
		if vpsArray.Nalus != nil {
			arrays = append(arrays, vpsArray)
		}
	}
	if len(m.sps) > 0 {
		spsArray := buildArray(33, m.sps) // SPS NAL unit type = 33
		if spsArray.Nalus != nil {
			arrays = append(arrays, spsArray)
		}
	}
	if len(m.pps) > 0 {
		ppsArray := buildArray(34, m.pps) // PPS NAL unit type = 34
		if ppsArray.Nalus != nil {
			arrays = append(arrays, ppsArray)
		}
	}

	return &mp4.HvcC{
		ConfigurationVersion:        1,
		GeneralProfileSpace:         0,
		GeneralTierFlag:             false,
		GeneralProfileIdc:           1,
		GeneralLevelIdc:             51,  // Level 3.1
		MinSpatialSegmentationIdc:   0,
		ParallelismType:             0,
		ChromaFormatIdc:             1,  // 4:2:0
		BitDepthLumaMinus8:          0,  // 8-bit
		BitDepthChromaMinus8:        0,  // 8-bit
		AvgFrameRate:                0,
		ConstantFrameRate:           0,
		NumTemporalLayers:           0,
		TemporalIdNested:            0,
		LengthSizeMinusOne:          3,  // 4-byte NAL length prefix
		NumOfNaluArrays:             uint8(len(arrays)),
		NaluArrays:                  arrays,
	}
}


// writeDinf writes the data information box (dinf > dref > url).
func writeDinf(w *mp4.Writer) error {
	_, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("dinf")})
	if err != nil {
		return err
	}
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("dref")})
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Dref{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	bi3, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("url ")})
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Url{Location: ""}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi3
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi2
	_, err = w.EndBox()
	return err
}

// TestRecordingFrameExtractor_H264 verifies H264 keyframe extraction from MP4.
func TestRecordingFrameExtractor_H264(t *testing.T) {
	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "frames")

	// Create 30 samples: keyframes at positions 0, 5, 10, 15, 20, 25 (every 5th).
	// All at 30fps: timescale=30, each sample duration=1.
	var samples []testSample
	for i := range 30 {
		var samp testSample
		if i%5 == 0 {
			samp = testSample{data: buildH264IDRSample(), isKeyFrame: true, duration: 1}
		} else {
			samp = testSample{data: buildH264NonIDRSample(), isKeyFrame: false, duration: 1}
		}
		samples = append(samples, samp)
	}

	mp4Path := createTestMP4(t, tmpDir, "test.h264.mp4", false, samples, 30)
	// Total duration = 30 samples * (1/30 sec) = 1 second.

	extractor := NewRecordingFrameExtractor()
	n, err := extractor.ExtractFrames(mp4Path, model.FormatH264, 200*time.Millisecond, outputDir)
	if err != nil {
		t.Fatalf("ExtractFrames failed: %v", err)
	}

	// With 6 keyframes and 200ms interval over 1 sec, expect 5 frames.
	expected := 5
	if n != expected {
		t.Errorf("expected %d frames, got %d", expected, n)
	}

	// Verify output files exist and are named correctly.
	matches, err := filepath.Glob(filepath.Join(outputDir, "frame_*.h264"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != expected {
		t.Errorf("expected %d .h264 files, got %d", expected, len(matches))
	}

	// Verify each file starts with Annex-B start code (0x00000001).
	for i, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			t.Fatalf("read %s: %v", m, err)
		}
		if len(data) < 4 {
			t.Errorf("frame %d (%s) too short: %d bytes", i, m, len(data))
			continue
		}
		if data[0] != 0x00 || data[1] != 0x00 || data[2] != 0x00 || data[3] != 0x01 {
			t.Errorf("frame %d (%s) does not start with Annex-B start code", i, m)
		}
		// Verify it contains SPS+PPS+IDR by checking for NAL type bytes.
		foundSPS := bytes.Contains(data, []byte{0x00, 0x00, 0x00, 0x01, 0x67})
		foundPPS := bytes.Contains(data, []byte{0x00, 0x00, 0x00, 0x01, 0x68})
		foundIDR := bytes.Contains(data, []byte{0x00, 0x00, 0x00, 0x01, 0x65})
		if !foundSPS || !foundPPS || !foundIDR {
			t.Errorf("frame %d (%s) missing required NALUs: SPS=%v PPS=%v IDR=%v",
				i, m, foundSPS, foundPPS, foundIDR)
		}
	}
}

// TestRecordingFrameExtractor_H265 verifies H265 keyframe extraction from MP4.
func TestRecordingFrameExtractor_H265(t *testing.T) {
	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "frames")

	// Create 30 samples: keyframes at positions 0, 5, 10, 15, 20, 25.
	var samples []testSample
	for i := range 30 {
		var samp testSample
		if i%5 == 0 {
			samp = testSample{data: buildH265IDRSample(), isKeyFrame: true, duration: 1}
		} else {
			samp = testSample{data: buildH265NonIDRSample(), isKeyFrame: false, duration: 1}
		}
		samples = append(samples, samp)
	}

	mp4Path := createTestMP4(t, tmpDir, "test.h265.mp4", true, samples, 30)

	extractor := NewRecordingFrameExtractor()
	n, err := extractor.ExtractFrames(mp4Path, model.FormatH265, 200*time.Millisecond, outputDir)
	if err != nil {
		t.Fatalf("ExtractFrames failed: %v", err)
	}

	expected := 5
	if n != expected {
		t.Errorf("expected %d frames, got %d", expected, n)
	}

	matches, err := filepath.Glob(filepath.Join(outputDir, "frame_*.h265"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != expected {
		t.Errorf("expected %d .h265 files, got %d", expected, len(matches))
	}

	// Verify each file starts with Annex-B start code and contains VPS+SPS+PPS+IDR.
	for i, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			t.Fatalf("read %s: %v", m, err)
		}
		if len(data) < 4 || data[0] != 0x00 || data[1] != 0x00 || data[2] != 0x00 || data[3] != 0x01 {
			t.Errorf("frame %d (%s) does not start with Annex-B start code", i, m)
		}
		// H265 VPS header byte = 0x40 (type 32), SPS = 0x42 (type 33), PPS = 0x44 (type 34)
		foundVPS := bytes.Contains(data, []byte{0x00, 0x00, 0x00, 0x01, 0x40})
		foundSPS := bytes.Contains(data, []byte{0x00, 0x00, 0x00, 0x01, 0x42})
		foundPPS := bytes.Contains(data, []byte{0x00, 0x00, 0x00, 0x01, 0x44})
		// H265 IDR type 19 header byte = 0x26
		foundIDR := bytes.Contains(data, []byte{0x00, 0x00, 0x00, 0x01, 0x26})
		if !foundVPS || !foundSPS || !foundPPS || !foundIDR {
			t.Errorf("frame %d (%s) missing NALUs: VPS=%v SPS=%v PPS=%v IDR=%v",
				i, m, foundVPS, foundSPS, foundPPS, foundIDR)
		}
	}
}

// TestRecordingFrameExtractor_H264_AllKeyframes tests with all samples as keyframes.
func TestRecordingFrameExtractor_H264_AllKeyframes(t *testing.T) {
	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "frames")

	var samples []testSample
	for range 30 {
		samples = append(samples, testSample{data: buildH264IDRSample(), isKeyFrame: true, duration: 1})
	}
	mp4Path := createTestMP4(t, tmpDir, "allkf.h264.mp4", false, samples, 30)

	extractor := NewRecordingFrameExtractor()
	n, err := extractor.ExtractFrames(mp4Path, model.FormatH264, 200*time.Millisecond, outputDir)
	if err != nil {
		t.Fatalf("ExtractFrames failed: %v", err)
	}

	// With 30 keyframes in 1 sec and 200ms interval, expect 5 frames.
	expected := 5
	if n != expected {
		t.Errorf("expected %d frames, got %d", expected, n)
	}
}

// TestRecordingFrameExtractor_H264_EmptyMP4 tests error on MP4 with no keyframes.
func TestRecordingFrameExtractor_H264_EmptyMP4(t *testing.T) {
	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "frames")

	// Create MP4 with no keyframes (all type 1 NALUs, no IDR).
	var samples []testSample
	for range 10 {
		samples = append(samples, testSample{data: buildH264NonIDRSample(), isKeyFrame: false, duration: 1})
	}
	mp4Path := createTestMP4(t, tmpDir, "nokeyframes.h264.mp4", false, samples, 30)

	extractor := NewRecordingFrameExtractor()
	_, err := extractor.ExtractFrames(mp4Path, model.FormatH264, time.Second, outputDir)
	if err == nil {
		t.Error("expected error for MP4 with no keyframes, got nil")
	}
}

// TestRecordingFrameExtractor_IntervalTooLarge tests error when interval exceeds duration.
func TestRecordingFrameExtractor_IntervalTooLarge(t *testing.T) {
	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "frames")

	samples := []testSample{
		{data: buildH264IDRSample(), isKeyFrame: true, duration: 1},
	}
	mp4Path := createTestMP4(t, tmpDir, "short.h264.mp4", false, samples, 30)

	extractor := NewRecordingFrameExtractor()
	_, err := extractor.ExtractFrames(mp4Path, model.FormatH264, 10*time.Second, outputDir)
	if err == nil {
		t.Error("expected error when interval > duration, got nil")
	}
}

// TestRecordingFrameExtractor_InvalidFormat tests error on unsupported format.
func TestRecordingFrameExtractor_InvalidFormat(t *testing.T) {
	tmpDir := t.TempDir()
	extractor := NewRecordingFrameExtractor()
	_, err := extractor.ExtractFrames("dummy", model.FormatMJPEG, time.Second, tmpDir)
	if err == nil {
		t.Error("expected error for unsupported format, got nil")
	}
}

// TestRecordingFrameExtractor_ZeroInterval tests error on zero interval.
func TestRecordingFrameExtractor_ZeroInterval(t *testing.T) {
	extractor := NewRecordingFrameExtractor()
	_, err := extractor.ExtractFrames("dummy", model.FormatH264, 0, t.TempDir())
	if err == nil {
		t.Error("expected error for zero interval, got nil")
	}
}

// TestRecordingFrameExtractor_MissingFile tests error on non-existent file.
func TestRecordingFrameExtractor_MissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	extractor := NewRecordingFrameExtractor()
	_, err := extractor.ExtractFrames("/nonexistent/file.mp4", model.FormatH264, time.Second, tmpDir)
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

// TestBuildAnnexBFrame verifies the helper builds correct Annex-B data.
func TestBuildAnnexBFrame(t *testing.T) {
	tests := []struct {
		name      string
		isH265    bool
		sample    []byte
		paramSets [][]byte
		wantNalus int // expected NALU count in output
	}{
		{
			name:      "H264 single IDR with SPS/PPS",
			isH265:    false,
			sample:    buildH264IDRSample(),
			paramSets: [][]byte{buildTestH264SPS(), buildTestH264PPS()},
			wantNalus: 3, // SPS + PPS + IDR
		},
		{
			name:      "H265 single IDR with VPS/SPS/PPS",
			isH265:    true,
			sample:    buildH265IDRSample(),
			paramSets: [][]byte{buildFrameTestH265VPS(), buildFrameTestH265SPS(), buildFrameTestH265PPS()},
			wantNalus: 4, // VPS + SPS + PPS + IDR
		},
		{
			name:      "No param sets",
			isH265:    false,
			sample:    buildH264IDRSample(),
			paramSets: nil,
			wantNalus: 1, // just IDR
		},
		{
			name:      "H264 param sets in sample data are stripped",
			isH265:    false,
			sample:    func() []byte {
				// Build sample with SPS+PPS+IDR (all length-prefixed)
				sps := buildTestH264SPS()
				pps := buildTestH264PPS()
				idr := []byte{0x65, 0x88, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
				var buf bytes.Buffer
				writeNal := func(b *bytes.Buffer, nalu []byte) {
					var hdr [4]byte
					binary.BigEndian.PutUint32(hdr[:], uint32(len(nalu)))
					b.Write(hdr[:])
					b.Write(nalu)
				}
				writeNal(&buf, sps)
				writeNal(&buf, pps)
				writeNal(&buf, idr)
				return buf.Bytes()
			}(),
			paramSets: [][]byte{buildTestH264SPS(), buildTestH264PPS()},
			wantNalus: 3, // SPS + PPS from paramSets, IDR from sample (SPS/PPS in sample stripped)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame := buildAnnexBFrame(tt.sample, tt.paramSets, tt.isH265)
			nalus := splitAnnexB(frame)
			if len(nalus) != tt.wantNalus {
				t.Errorf("expected %d NALUs, got %d", tt.wantNalus, len(nalus))
			}
		})
	}
}

// TestIsParamSetNALU verifies parameter set detection.
func TestIsParamSetNALU(t *testing.T) {
	tests := []struct {
		name   string
		nalu   []byte
		isH265 bool
		want   bool
	}{
		{"H264 SPS (type 7)", []byte{0x67, 0x42}, false, true},
		{"H264 PPS (type 8)", []byte{0x68, 0xCE}, false, true},
		{"H264 IDR (type 5)", []byte{0x65, 0x88}, false, false},
		{"H264 non-IDR (type 1)", []byte{0x41, 0x88}, false, false},
		{"H265 VPS (type 32)", []byte{0x40, 0x01}, true, true},   // 32 << 1 = 0x40
		{"H265 SPS (type 33)", []byte{0x42, 0x01}, true, true},    // 33 << 1 = 0x42
		{"H265 PPS (type 34)", []byte{0x44, 0x01}, true, true},    // 34 << 1 = 0x44
		{"H265 IDR (type 19)", []byte{0x26, 0x01}, true, false},   // 19 << 1 = 0x26
		{"H265 non-IDR (type 1)", []byte{0x02, 0x01}, true, false},
		{"empty", []byte{}, false, false},
		{"nil", nil, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isParamSetNALU(tt.nalu, tt.isH265)
			if got != tt.want {
				t.Errorf("isParamSetNALU = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCollectParamSets verifies parameter set collection from SegmentInfo.
func TestCollectParamSets(t *testing.T) {
	t.Run("H264 with SPS and PPS", func(t *testing.T) {
		seg := &merge.SegmentInfo{
			SPS: []byte{0x67},
			PPS: []byte{0x68},
		}
		ps := collectParamSets(seg, false)
		if len(ps) != 2 {
			t.Fatalf("expected 2 param sets, got %d", len(ps))
		}
		if ps[0][0] != 0x67 || ps[1][0] != 0x68 {
			t.Error("unexpected param set data")
		}
	})

	t.Run("H265 with VPS, SPS, PPS", func(t *testing.T) {
		seg := &merge.SegmentInfo{
			VPS: []byte{0x40},
			SPS: []byte{0x42},
			PPS: []byte{0x44},
		}
		ps := collectParamSets(seg, true)
		if len(ps) != 3 {
			t.Fatalf("expected 3 param sets, got %d", len(ps))
		}
	})

	t.Run("empty sets", func(t *testing.T) {
		seg := &merge.SegmentInfo{}
		ps := collectParamSets(seg, false)
		if len(ps) != 0 {
			t.Errorf("expected 0 param sets, got %d", len(ps))
		}
	})
}

// TestRecordingFrameExtractor_H264_ExactCount verifies exact frame count
// matches expected duration/interval.
func TestRecordingFrameExtractor_H264_ExactCount(t *testing.T) {
	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "frames")

	// 60 keyframes at 30fps (timescale=30, each sample duration=1) = 2 seconds.
	var samples []testSample
	for range 60 {
		samples = append(samples, testSample{data: buildH264IDRSample(), isKeyFrame: true, duration: 1})
	}
	mp4Path := createTestMP4(t, tmpDir, "exact.h264.mp4", false, samples, 30)

	extractor := NewRecordingFrameExtractor()
	n, err := extractor.ExtractFrames(mp4Path, model.FormatH264, 500*time.Millisecond, outputDir)
	if err != nil {
		t.Fatalf("ExtractFrames failed: %v", err)
	}

	// 2 seconds at 500ms interval = 4 frames.
	expected := 4
	if n != expected {
		t.Errorf("expected %d frames, got %d", expected, n)
	}
}

// TestRecordingFrameExtractor_SingleFrameMP4 tests extraction with a single keyframe.
func TestRecordingFrameExtractor_SingleFrameMP4(t *testing.T) {
	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "frames")

	samples := []testSample{
		{data: buildH264IDRSample(), isKeyFrame: true, duration: 30}, // 1 second at timescale=30
	}
	mp4Path := createTestMP4(t, tmpDir, "single.h264.mp4", false, samples, 30)

	extractor := NewRecordingFrameExtractor()
	n, err := extractor.ExtractFrames(mp4Path, model.FormatH264, time.Second, outputDir)
	if err != nil {
		t.Fatalf("ExtractFrames failed: %v", err)
	}

	// 1 second at 1s interval = 1 frame.
	if n != 1 {
		t.Errorf("expected 1 frame, got %d", n)
	}

	// Verify the frame file exists.
	files, err := filepath.Glob(filepath.Join(outputDir, "frame_*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Errorf("expected 1 file, got %d", len(files))
	}
}

// TestFrameExtractorBuildHvcC verifies the HEVC config record is built correctly.
func TestFrameExtractorBuildHvcC(t *testing.T) {
	vps := buildFrameTestH265VPS()
	sps := buildFrameTestH265SPS()
	pps := buildFrameTestH265PPS()

	hvcC := buildHvcC(vps, sps, pps)

	// Verify structure: version(1) + profile(1) + compat(4) + constraint(6) + level(1) + ...
	if len(hvcC) < 23 {
		t.Fatalf("hvcC too short: %d bytes, need at least 23", len(hvcC))
	}
	if hvcC[0] != 1 {
		t.Errorf("configurationVersion = %d, want 1", hvcC[0])
	}
	// numOfArrays at offset 22
	if hvcC[22] != 3 {
		t.Errorf("numOfArrays = %d, want 3 (VPS+SPS+PPS)", hvcC[22])
	}
}

// TestCreateTestMP4_H264 verifies the test MP4 is valid (parseable by merge.ParseSegment).
func TestCreateTestMP4_H264(t *testing.T) {
	tmpDir := t.TempDir()
	var samples []testSample
	for range 10 {
		samples = append(samples, testSample{data: buildH264IDRSample(), isKeyFrame: true, duration: 1})
	}
	mp4Path := createTestMP4(t, tmpDir, "valid.h264.mp4", false, samples, 30)

	seg, err := merge.ParseSegment(mp4Path)
	if err != nil {
		t.Fatalf("ParseSegment failed: %v", err)
	}

	if seg.Codec != "h264" {
		t.Errorf("expected codec h264, got %q", seg.Codec)
	}
	if seg.SampleCount != 10 {
		t.Errorf("expected 10 samples, got %d", seg.SampleCount)
	}
	if len(seg.Samples) != 10 {
		t.Errorf("expected 10 SampleEntries, got %d", len(seg.Samples))
	}
	if len(seg.SPS) == 0 {
		t.Error("expected SPS to be parsed")
	}
	if len(seg.PPS) == 0 {
		t.Error("expected PPS to be parsed")
	}
}

// TestCreateTestMP4_H265 verifies the H265 test MP4 is valid (parseable by merge.ParseSegment).
func TestCreateTestMP4_H265(t *testing.T) {
	tmpDir := t.TempDir()
	var samples []testSample
	for range 10 {
		samples = append(samples, testSample{data: buildH265IDRSample(), isKeyFrame: true, duration: 1})
	}
	mp4Path := createTestMP4(t, tmpDir, "valid.h265.mp4", true, samples, 30)

	seg, err := merge.ParseSegment(mp4Path)
	if err != nil {
		t.Fatalf("ParseSegment failed: %v", err)
	}

	if seg.Codec != "h265" {
		t.Errorf("expected codec h265, got %q", seg.Codec)
	}
	if seg.SampleCount != 10 {
		t.Errorf("expected 10 samples, got %d", seg.SampleCount)
	}
	if len(seg.VPS) == 0 {
		t.Error("expected VPS to be parsed")
	}
	if len(seg.SPS) == 0 {
		t.Error("expected SPS to be parsed")
	}
}

// TestRecordingFrameExtractor_OutputNamingConvention verifies output file names.
func TestRecordingFrameExtractor_OutputNamingConvention(t *testing.T) {
	tests := []struct {
		format model.Format
		ext    string
	}{
		{model.FormatAVI, ".jpg"},
		{model.FormatH264, ".h264"},
		{model.FormatH265, ".h265"},
	}
	for _, tt := range tests {
		t.Run(string(tt.format), func(t *testing.T) {
			tmpDir := t.TempDir()
			outputDir := filepath.Join(tmpDir, "frames")
			extractor := NewRecordingFrameExtractor()

			var filePath string
			if tt.format == model.FormatAVI {
				var buf bytes.Buffer
				m := avi.NewVideoOnlyMuxer(&buf, 64, 48)
			for i := range 30 {
					m.WriteVideo(buildTestJPEG(t, byte(i)), int64(i)*33333)
				}
				m.Close()
				filePath = filepath.Join(tmpDir, "test.avi")
				os.WriteFile(filePath, buf.Bytes(), 0o644)
			} else {
				isH265 := tt.format == model.FormatH265
				var samples []testSample
			for range 30 {
					if isH265 {
						samples = append(samples, testSample{data: buildH265IDRSample(), isKeyFrame: true, duration: 1})
					} else {
						samples = append(samples, testSample{data: buildH264IDRSample(), isKeyFrame: true, duration: 1})
					}
				}
				filePath = createTestMP4(t, tmpDir, "test.mp4", isH265, samples, 30)
			}

			_, err := extractor.ExtractFrames(filePath, tt.format, 100*time.Millisecond, outputDir)
			if err != nil {
				t.Fatalf("ExtractFrames failed: %v", err)
			}

			// Check naming: frame_000001.ext, frame_000002.ext, etc.
			for i := 1; i <= 10; i++ {
				expected := fmt.Sprintf("frame_%06d%s", i, tt.ext)
				framePath := filepath.Join(outputDir, expected)
				if _, err := os.Stat(framePath); err != nil {
					t.Errorf("expected frame file %s: %v", expected, err)
				}
			}
		})
	}
}

// TestRecordingFrameExtractor_CorruptMP4 tests handling of corrupt MP4 files.
func TestRecordingFrameExtractor_CorruptMP4(t *testing.T) {
	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "frames")

	// Write a small corrupt MP4 (no moov box).
	corruptPath := filepath.Join(tmpDir, "corrupt.mp4")
	if err := os.WriteFile(corruptPath, []byte{0x00, 0x00, 0x00, 0x08, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, 0o644); err != nil {
		t.Fatal(err)
	}

	extractor := NewRecordingFrameExtractor()
	_, err := extractor.ExtractFrames(corruptPath, model.FormatH264, time.Second, outputDir)
	if err == nil {
		t.Error("expected error for corrupt MP4, got nil")
	}
}

// TestRecordingFrameExtractor_AVI_FrameCountPrecision verifies
// that frame count is close to recording_duration / interval.
func TestRecordingFrameExtractor_AVI_FrameCountPrecision(t *testing.T) {
	tmpDir := t.TempDir()
	aviPath := filepath.Join(tmpDir, "precision.avi")
	outputDir := filepath.Join(tmpDir, "frames")

	// 10 minutes of 30fps video = 18000 frames.
	frameCount := 18000
	{
		var buf bytes.Buffer
		m := avi.NewVideoOnlyMuxer(&buf, 64, 48)
		for i := range frameCount {
			if err := m.WriteVideo(buildTestJPEG(t, byte(i%256)), int64(i)*33333); err != nil {
				t.Fatalf("WriteVideo %d: %v", i, err)
			}
		}
		if err := m.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if err := os.WriteFile(aviPath, buf.Bytes(), 0o644); err != nil {
			t.Fatalf("Write AVI: %v", err)
		}
	}

	extractor := NewRecordingFrameExtractor()
	n, err := extractor.ExtractFrames(aviPath, model.FormatAVI, 30*time.Second, outputDir)
	if err != nil {
		t.Fatalf("ExtractFrames failed: %v", err)
	}

	// 600 seconds / 30s = 20 frames (±1 tolerance for boundary conditions).
	expected := 20
	if n < expected-1 || n > expected+1 {
		t.Errorf("expected ~%d frames (tolerance ±1), got %d", expected, n)
	}
}
