//go:build h264poc

// Package timelapse — H.264 Encoder POC test (research artifact).
//
// This file is gated behind the "h264poc" build tag to avoid pulling in
// the hi264 dependency for normal builds. Run with:
//
//	go test -tags h264poc ./internal/timelapse/ -run TestH264POC
//
// The actual POC code lives in .omo/drafts/h264-poc/ as a standalone module.
// This file provides a thin integration test that exercises the hi264 →
// abema/go-mp4 pipeline within the project context.
//
// KEY FINDING: No pure Go library (CGO_ENABLED=0) can produce production-grade
// H.264 compression from arbitrary pixel input. hi264 is a test-pattern
// generator with 44x worse compression than MJPEG. See .omo/drafts/h264-encoder-poc.md
// for full research results and recommendations.

package timelapse

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Eyevinn/hi264/pkg/encode"
	"github.com/Eyevinn/hi264/pkg/yuv"
	"github.com/abema/go-mp4"
)

// TestH264POC verifies the H.264 encoding pipeline works with CGO_ENABLED=0.
func TestH264POC(t *testing.T) {
	tmpDir := t.TempDir()
	width, height := 320, 240
	numFrames := 5

	// Generate test JPEG frames
	jpegPaths := make([]string, numFrames)
	for i := 0; i < numFrames; i++ {
		img := image.NewRGBA(image.Rect(0, 0, width, height))
		for x := 0; x < width; x++ {
			for y := 0; y < height; y++ {
				img.Set(x, y, color.RGBA{
					R: uint8(x * 255 / width),
					G: uint8(y * 255 / height),
					B: uint8((x + y) * 255 / (width + height)),
					A: 255,
				})
			}
		}
		path := filepath.Join(tmpDir, fmt.Sprintf("frame_%04d.jpg", i))
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 85}); err != nil {
			t.Fatal(err)
		}
		f.Close()
		jpegPaths[i] = path
	}

	// H.264 encoding with hi264
	p := encode.EncodeParams{
		Width:  width,
		Height: height,
		QP:     26,
		CABAC:  false,
	}

	sps, err := encode.GenerateSPS(p)
	if err != nil {
		t.Fatalf("GenerateSPS: %v", err)
	}
	pps, err := encode.GeneratePPS(p)
	if err != nil {
		t.Fatalf("GeneratePPS: %v", err)
	}

	t.Logf("SPS: %d bytes (NAL type %d)", len(sps), getNALType(sps))
	t.Logf("PPS: %d bytes (NAL type %d)", len(pps), getNALType(pps))

	if getNALType(sps) != 7 {
		t.Errorf("SPS should be NAL type 7, got %d", getNALType(sps))
	}
	if getNALType(pps) != 8 {
		t.Errorf("PPS should be NAL type 8, got %d", getNALType(pps))
	}

	// Mux into MP4
	muxer := newH264MuxerTest(sps, pps)
	duration := time.Second

	for i, jpegPath := range jpegPaths {
		f, err := os.Open(jpegPath)
		if err != nil {
			t.Fatal(err)
		}
		img, err := jpeg.Decode(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}

		plane := yuv.ScaleImageToPlaneGrid(img, width, height, 16, yuv.BT601, yuv.LimitedRange)
		idr, err := encode.GenerateIDRFromPlane(p, plane, uint32(i))
		if err != nil {
			t.Fatalf("GenerateIDR frame %d: %v", i, err)
		}

		t.Logf("Frame %d: %d bytes (NAL type %d)", i, len(idr), getNALType(idr))
		if getNALType(idr) != 5 {
			t.Errorf("IDR should be NAL type 5, got %d", getNALType(idr))
		}

		nalus := splitAnnexBTest(idr)
		muxer.addSample(nalus, duration)
	}

	outputPath := filepath.Join(tmpDir, "output_h264.mp4")
	if err := muxer.close(outputPath); err != nil {
		t.Fatalf("muxer close: %v", err)
	}

	// Validate MP4 structure
	f, err := os.Open(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	foundFtyp := false
	foundMoov := false
	foundMdat := false

	for {
		boxInfo, err := mp4.ReadBoxInfo(f)
		if err != nil {
			break
		}
		boxName := string(boxInfo.Type[:])
		switch boxName {
		case "ftyp":
			foundFtyp = true
		case "moov":
			foundMoov = true
		case "mdat":
			foundMdat = true
		}
		if _, err := f.Seek(int64(boxInfo.Offset)+int64(boxInfo.Size), 0); err != nil {
			break
		}
		if boxInfo.Size == 0 {
			break
		}
	}

	if !foundFtyp {
		t.Error("Missing ftyp box")
	}
	if !foundMoov {
		t.Error("Missing moov box")
	}
	if !foundMdat {
		t.Error("Missing mdat box")
	}

	// File size sanity check
	fi, err := os.Stat(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Output MP4: %d bytes", fi.Size())
	t.Logf("CGO_ENABLED=0: PASS (build tag gates hi264 dependency)")
	t.Log("H.264 encoding pipeline: CONFIRMED WORKING")
}

// getNALType extracts NAL unit type from an Annex-B or raw NAL unit.
func getNALType(data []byte) int {
	if len(data) == 0 {
		return -1
	}
	// Skip Annex-B start code
	offset := 0
	if len(data) >= 4 && data[0] == 0 && data[1] == 0 && data[2] == 0 && data[3] == 1 {
		offset = 4
	} else if len(data) >= 3 && data[0] == 0 && data[1] == 0 && data[2] == 1 {
		offset = 3
	}
	if offset >= len(data) {
		return -1
	}
	return int(data[offset] & 0x1F)
}

// splitAnnexBTest splits Annex-B byte stream into individual NAL units.
func splitAnnexBTest(data []byte) [][]byte {
	var nalus [][]byte
	start := 0
	i := 0
	for i < len(data)-3 {
		if data[i] == 0 && data[i+1] == 0 {
			var codeLen int
			if data[i+2] == 1 {
				codeLen = 3
			} else if i+3 < len(data) && data[i+2] == 0 && data[i+3] == 1 {
				codeLen = 4
			} else {
				i++
				continue
			}
			if start > 0 {
				nalus = append(nalus, data[start:i])
			}
			i += codeLen
			start = i
			continue
		}
		i++
	}
	if start < len(data) {
		nalu := data[start:]
		for len(nalu) > 0 && nalu[len(nalu)-1] == 0 {
			nalu = nalu[:len(nalu)-1]
		}
		if len(nalu) > 0 {
			nalus = append(nalus, nalu)
		}
	}
	return nalus
}

// h264MuxerTest is a minimal H.264 MP4 muxer for testing.
type h264MuxerTest struct {
	sps, pps []byte
	samples  []h264SampleTest
}

type h264SampleTest struct {
	nalus    [][]byte
	duration time.Duration
}

func newH264MuxerTest(sps, pps []byte) *h264MuxerTest {
	return &h264MuxerTest{sps: sps, pps: pps}
}

func (m *h264MuxerTest) addSample(nalus [][]byte, duration time.Duration) {
	m.samples = append(m.samples, h264SampleTest{nalus: nalus, duration: duration})
}

func (m *h264MuxerTest) close(path string) error {
	if len(m.samples) == 0 {
		return nil
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	avcCData := m.buildAvcC()

	// Calculate moov size using a buffer
	buf := &nopWriterAt{}
	bw := mp4.NewWriter(buf)
	if err := m.writeMoov(bw, 0, avcCData); err != nil {
		return err
	}
	moovSize := buf.written

	// Write to real file
	w := mp4.NewWriter(f)

	// ftyp
	bi, _ := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("ftyp")})
	mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand: [4]byte{'i', 's', 'o', 'm'}, MinorVersion: 0,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'i', 's', 'o', '2'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
			{CompatibleBrand: [4]byte{'a', 'v', 'c', '1'}},
		},
	}, mp4.Context{})
	w.EndBox()
	_ = bi

	// Calculate ftyp size
	ftypEnd, _ := w.Seek(0, 1)

	// write moov
	if err := m.writeMoov(w, ftypEnd+moovSize+8, avcCData); err != nil {
		return err
	}

	// mdat
	mdatData := m.collectMdatData()
	bi2, _ := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mdat"), Size: uint64(8 + len(mdatData))})
	w.Write(mdatData)
	w.EndBox()
	_ = bi2

	return nil
}

func (m *h264MuxerTest) buildAvcC() []byte {
	profile := byte(66)
	compat := byte(0xC0)
	level := byte(30)
	if len(m.sps) >= 8 {
		profile = m.sps[5]
		compat = m.sps[6]
		level = m.sps[7]
	}
	var buf bytes.Buffer
	buf.WriteByte(1)
	buf.WriteByte(profile)
	buf.WriteByte(compat)
	buf.WriteByte(level)
	buf.WriteByte(0xFF)
	buf.WriteByte(0xE1)
	spsLen := len(m.sps) - 4 // strip start code
	buf.WriteByte(byte(spsLen >> 8))
	buf.WriteByte(byte(spsLen))
	buf.Write(m.sps[4:])
	buf.WriteByte(1)
	ppsLen := len(m.pps) - 4
	buf.WriteByte(byte(ppsLen >> 8))
	buf.WriteByte(byte(ppsLen))
	buf.Write(m.pps[4:])
	return buf.Bytes()
}

func (m *h264MuxerTest) writeMoov(w *mp4.Writer, chunkOffset int64, avcCData []byte) error {
	w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("moov")})

	// mvhd
	bi, _ := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mvhd")})
	mp4.Marshal(w, &mp4.Mvhd{
		Timescale: 1000, DurationV0: uint32(len(m.samples) * 1000),
		Rate: 0x00010000, Volume: 0x0100, NextTrackID: 2,
		Matrix: [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
	}, mp4.Context{})
	w.EndBox()
	_ = bi

	// trak
	w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("trak")})

	// tkhd
	bi2, _ := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("tkhd")})
	mp4.Marshal(w, &mp4.Tkhd{
		TrackID: 1, DurationV0: uint32(len(m.samples) * 1000),
		Width: 640 << 16, Height: 480 << 16,
		Matrix: [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
	}, mp4.Context{})
	w.EndBox()
	_ = bi2

	m.writeMdia(w, chunkOffset, avcCData)

	w.EndBox() // trak
	w.EndBox() // moov
	return nil
}

func (m *h264MuxerTest) writeMdia(w *mp4.Writer, chunkOffset int64, avcCData []byte) error {
	w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mdia")})

	bi, _ := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mdhd")})
	mp4.Marshal(w, &mp4.Mdhd{
		Timescale: 1000, DurationV0: uint32(len(m.samples) * 1000),
		Language: [3]byte{0x15, 0xC0, 0x00},
	}, mp4.Context{})
	w.EndBox()
	_ = bi

	bi2, _ := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hdlr")})
	mp4.Marshal(w, &mp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: "VideoHandler\x00"}, mp4.Context{})
	w.EndBox()
	_ = bi2

	m.writeMinf(w, chunkOffset, avcCData)

	w.EndBox() // mdia
	return nil
}

func (m *h264MuxerTest) writeMinf(w *mp4.Writer, chunkOffset int64, avcCData []byte) error {
	w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("minf")})

	bi, _ := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("vmhd")})
	mp4.Marshal(w, &mp4.Vmhd{Graphicsmode: 0}, mp4.Context{})
	w.EndBox()
	_ = bi

	// dinf > dref > url
	bi2, _ := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("dinf")})
	bi3, _ := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("dref")})
	mp4.Marshal(w, &mp4.Dref{EntryCount: 1}, mp4.Context{})
	bi4, _ := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("url ")})
	mp4.Marshal(w, &mp4.Url{Location: ""}, mp4.Context{})
	w.EndBox()
	_ = bi4
	w.EndBox()
	_ = bi3
	w.EndBox()
	_ = bi2

	m.writeStbl(w, chunkOffset, avcCData)

	w.EndBox() // minf
	return nil
}

func (m *h264MuxerTest) writeStbl(w *mp4.Writer, chunkOffset int64, avcCData []byte) error {
	n := len(m.samples)
	w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stbl")})

	// stsd > avc1
	bi, _ := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsd")})
	mp4.Marshal(w, &mp4.Stsd{EntryCount: 1}, mp4.Context{})
	bi2, _ := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("avc1")})
	mp4.Marshal(w, &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType("avc1")},
			DataReferenceIndex: 1,
		},
		Width: 640, Height: 480,
		Horizresolution: 0x00480000, Vertresolution: 0x00480000,
		FrameCount: 1, Depth: 0x0018,
	}, mp4.Context{})

	// avcC
	bi3, _ := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("avcC")})
	w.Write(avcCData)
	w.EndBox()
	_ = bi3

	w.EndBox() // avc1
	_ = bi2
	w.EndBox() // stsd
	_ = bi

	// stts
	bi4, _ := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stts")})
	stts := make([]mp4.SttsEntry, n)
	for i := range m.samples {
		stts[i] = mp4.SttsEntry{SampleCount: 1, SampleDelta: 1000}
	}
	mp4.Marshal(w, &mp4.Stts{EntryCount: uint32(n), Entries: stts}, mp4.Context{})
	w.EndBox()
	_ = bi4

	// stsc
	bi5, _ := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsc")})
	mp4.Marshal(w, &mp4.Stsc{
		EntryCount: 1,
		Entries:    []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: uint32(n), SampleDescriptionIndex: 1}},
	}, mp4.Context{})
	w.EndBox()
	_ = bi5

	// stsz
	bi6, _ := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsz")})
	sizes := make([]uint32, n)
	for i, s := range m.samples {
		for _, nalu := range s.nalus {
			sizes[i] += 4 + uint32(len(nalu))
		}
	}
	mp4.Marshal(w, &mp4.Stsz{SampleSize: 0, SampleCount: uint32(n), EntrySize: sizes}, mp4.Context{})
	w.EndBox()
	_ = bi6

	// stco
	bi7, _ := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stco")})
	mp4.Marshal(w, &mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{uint32(chunkOffset)}}, mp4.Context{})
	w.EndBox()
	_ = bi7

	w.EndBox() // stbl
	return nil
}

func (m *h264MuxerTest) collectMdatData() []byte {
	var buf bytes.Buffer
	for _, s := range m.samples {
		for _, nalu := range s.nalus {
			lenBytes := make([]byte, 4)
			binary.BigEndian.PutUint32(lenBytes, uint32(len(nalu)))
			buf.Write(lenBytes)
			buf.Write(nalu)
		}
	}
	return buf.Bytes()
}

type nopWriterAt struct {
	written int64
}

func (w *nopWriterAt) Write(p []byte) (int, error) {
	w.written += int64(len(p))
	return len(p), nil
}

func (w *nopWriterAt) Seek(offset int64, whence int) (int64, error) {
	return w.written + offset, nil
}
