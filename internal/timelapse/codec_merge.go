// Shared MP4 box-skeleton + frame I/O for the H.264 and H.265 Go mergers.
//
// h264_go_merge.go and h265_go_merge.go each previously carried their own
// byte-identical copy of the moov/mvhd/trak/mdia/minf/stbl box chain (the only
// real divergence was the sample-entry box: avc1+avcC vs hvc1+hvcC). This file
// consolidates that skeleton into one codecMuxer type plus the shared frame
// helpers (splitAnnexB, bitReader, removeEmulationPrevention) that were already
// de-facto shared across files. See #236.
package timelapse

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/abema/go-mp4"
)

// codecMuxer builds the moov box structure for a single-codec video track.
// It is the shared base of the former h264Muxer / h265Muxer; the only fields
// that differed between them were the decoder-config payload (avcC vs hvcC
// bytes) and the sample-entry / config box type names, now parameterized below.
type codecMuxer struct {
	frameCount  int
	sampleSizes []uint32
	width       int
	height      int
	// decoderConfigData is the marshaled avcC / hvcC payload written inside the
	// config box (avcC / hvcC) child of the visual sample entry.
	decoderConfigData []byte
	sampleDur         time.Duration
	chunkOffset       int64
	// sampleEntryType is the 4-byte visual sample entry box type ("avc1"/"hvc1").
	sampleEntryType [4]byte
	// configBoxType is the 4-byte decoder config box type ("avcC"/"hvcC").
	configBoxType [4]byte
}

// writeMoov writes the moov box containing mvhd + a single video trak.
func (m *codecMuxer) writeMoov(w *mp4.Writer, chunkOffset int64) error {
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

func (m *codecMuxer) writeMvhd(w *mp4.Writer) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mvhd")})
	if err != nil {
		return err
	}

	duration := uint32(m.frameCount) * uint32(m.sampleDur.Milliseconds())

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

func (m *codecMuxer) writeTrak(w *mp4.Writer, chunkOffset int64) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("trak")})
	if err != nil {
		return err
	}

	// tkhd
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("tkhd")})
	if err != nil {
		return err
	}
	duration := uint32(m.frameCount) * uint32(m.sampleDur.Milliseconds())
	tkhd := &mp4.Tkhd{
		TrackID:    1,
		DurationV0: duration,
		Width:      uint32(m.width) << 16,
		Height:     uint32(m.height) << 16,
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

func (m *codecMuxer) writeMdia(w *mp4.Writer, chunkOffset int64) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mdia")})
	if err != nil {
		return err
	}

	duration := uint32(m.frameCount) * uint32(m.sampleDur.Milliseconds())

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

func (m *codecMuxer) writeMinf(w *mp4.Writer, chunkOffset int64) error {
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

func (m *codecMuxer) writeStbl(w *mp4.Writer, chunkOffset int64) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stbl")})
	if err != nil {
		return err
	}

	n := m.frameCount

	// stsd > <avc1|hvc1> + <avcC|hvcC>
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsd")})
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	if err := m.writeSampleEntry(w); err != nil {
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
	sampleDelta := uint32(m.sampleDur.Milliseconds())
	sttsEntries := make([]mp4.SttsEntry, n)
	for i := range sttsEntries {
		sttsEntries[i] = mp4.SttsEntry{
			SampleCount: 1,
			SampleDelta: sampleDelta,
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
	if _, err := mp4.Marshal(w, &mp4.Stsz{SampleSize: 0, SampleCount: uint32(n), EntrySize: m.sampleSizes}, mp4.Context{}); err != nil {
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

// writeSampleEntry writes the visual sample entry (avc1/hvc1) with the
// codec-specific decoder config box (avcC/hvcC) as its child. The box type
// names and the config payload come from the codecMuxer fields, so the same
// method serves both codecs.
func (m *codecMuxer) writeSampleEntry(w *mp4.Writer) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType(string(m.sampleEntryType[:]))})
	if err != nil {
		return err
	}

	vse := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType(string(m.sampleEntryType[:]))},
			DataReferenceIndex: 1,
		},
		Width:           uint16(m.width),
		Height:          uint16(m.height),
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
	}
	if _, err := mp4.Marshal(w, vse, mp4.Context{}); err != nil {
		return err
	}

	// decoder config box (avcC/hvcC) — raw payload
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType(string(m.configBoxType[:]))})
	if err != nil {
		return err
	}
	if _, err := w.Write(m.decoderConfigData); err != nil {
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

// writeCodecFtyp writes the ftyp box for a codec-specific MP4 and returns its
// total size. compatibleBrand is the codec brand appended to the common
// isom/iso2/mp41 set ("avc1" for H.264, "hvc1" for H.265).
func writeCodecFtyp(w *mp4.Writer, compatibleBrand [4]byte) (int64, error) {
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
			{CompatibleBrand: compatibleBrand},
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

// writeCodecMdat writes the mdat box with length-prefixed NALU sample data,
// stripping parameter sets (which belong only in avcC/hvcC). isParamSet is the
// codec-specific predicate (isH264ParamSet / isH265ParamSet). Including param
// sets in sample data causes MEDIA_ERR_SRC_NOT_SUPPORTED in browsers.
func writeCodecMdat(w *mp4.Writer, framePaths []string, ctx context.Context, isParamSet func([]byte) bool) error {
	// First pass: compute total mdat payload size (excluding param sets).
	var totalPayloadSize uint32
	for _, path := range framePaths {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read frame %s: %w", path, err)
		}
		for _, nalu := range splitAnnexB(data) {
			if isParamSet(nalu) {
				continue
			}
			totalPayloadSize += 4 + uint32(len(nalu))
		}
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	mdatBoxSize := uint64(8 + totalPayloadSize)
	_, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mdat"), Size: mdatBoxSize})
	if err != nil {
		return fmt.Errorf("start mdat: %w", err)
	}

	// Write each frame as length-prefixed NALUs, skipping param sets.
	for _, path := range framePaths {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read frame for mdat %s: %w", path, err)
		}

		for _, nalu := range splitAnnexB(data) {
			if isParamSet(nalu) {
				continue
			}
			lenBytes := make([]byte, 4)
			binary.BigEndian.PutUint32(lenBytes, uint32(len(nalu)))
			if _, err := w.Write(lenBytes); err != nil {
				return fmt.Errorf("write NALU length: %w", err)
			}
			if _, err := w.Write(nalu); err != nil {
				return fmt.Errorf("write NALU data: %w", err)
			}
		}
	}

	if _, err := w.EndBox(); err != nil {
		return fmt.Errorf("end mdat: %w", err)
	}
	return nil
}

// listCodecFrameFiles globs and sorts frame_NNNNNN.<ext> files in dir.
func listCodecFrameFiles(dir, ext string) ([]string, error) {
	pattern := filepath.Join(dir, "frame_*."+ext)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}

// --- Shared Annex-B / SPS-parsing helpers ---
//
// These were previously scattered: splitAnnexB + bitReader lived in
// h264_go_merge.go but were cross-used by h265_go_merge.go (fragile — renaming
// or moving either would break the other). They are codec-agnostic Annex-B /
// RBSP helpers and belong here. removeEmulationPrevention was defined in the
// h265 file; it is also codec-agnostic (works for both H.264 and H.265 RBSP).

// splitAnnexB splits an Annex-B byte stream into individual raw NAL units,
// stripping the 0x00000001 (or 0x000001) start codes.
func splitAnnexB(data []byte) [][]byte {
	var nalus [][]byte
	start := 0
	found := false

	for i := 0; i < len(data)-3; i++ {
		if data[i] == 0 && data[i+1] == 0 {
			var codeLen int
			if data[i+2] == 1 {
				codeLen = 3
			} else if i+3 < len(data) && data[i+2] == 0 && data[i+3] == 1 {
				codeLen = 4
			} else {
				continue
			}

			if found {
				// Extract NALU from start to current position.
				nalu := data[start:i]
				// Strip trailing zeros (emulation prevention or padding).
				for len(nalu) > 0 && nalu[len(nalu)-1] == 0 {
					nalu = nalu[:len(nalu)-1]
				}
				if len(nalu) > 0 {
					nalus = append(nalus, nalu)
				}
			}
			i += codeLen - 1
			start = i + 1
			found = true
		}
	}

	// Handle the last NALU.
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

// removeEmulationPrevention removes H.264/H.265 emulation prevention bytes
// (0x00 0x00 0x03 → 0x00 0x00) from RBSP data.
func removeEmulationPrevention(data []byte) []byte {
	result := make([]byte, 0, len(data))
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

// bitReader is a bit-level reader for parsing exp-Golomb-coded SPS fields.
// Used by both the H.264 (parseH264Dimensions) and H.265 (parseH265Dimensions)
// SPS dimension parsers.
type bitReader struct {
	data    []byte
	pos     int  // bit position (0-7 in current byte)
	offset  int  // byte offset
	overran bool // true if we read past the end of data
}

func (r *bitReader) readBit() uint8 {
	if r.offset >= len(r.data) {
		r.overran = true
		return 0
	}
	bit := (r.data[r.offset] >> (7 - r.pos)) & 1
	r.pos++
	if r.pos >= 8 {
		r.pos = 0
		r.offset++
	}
	return bit
}

// readBits reads n bits as a uint32 (MSB first).
func (r *bitReader) readBits(n int) uint32 {
	var val uint32
	for range n {
		val = (val << 1) | uint32(r.readBit())
	}
	return val
}

// readUE reads an unsigned exp-golomb coded value.
// Returns 0 and sets r.overran on EOF.
func (r *bitReader) readUE() uint32 {
	leadingZeros := 0
	for r.readBit() == 0 {
		if r.overran {
			return 0
		}
		leadingZeros++
		if leadingZeros > 31 {
			r.overran = true
			return 0
		}
	}
	if leadingZeros == 0 {
		return 0
	}
	// Read leadingZeros more bits to form the value.
	suffix := r.readBits(leadingZeros)
	return (1 << leadingZeros) - 1 + suffix
}

// readSE reads a signed exp-golomb coded value.
func (r *bitReader) readSE() int32 {
	ue := r.readUE()
	if ue == 0 {
		return 0
	}
	if ue&1 == 0 {
		return -int32(ue / 2)
	}
	return int32((ue + 1) / 2)
}

// skipScalingLists skips the scaling-list fields in an H.264 SPS (High profile).
// Called only by parseH264Dimensions.
func skipScalingLists(r *bitReader) {
	for i := range 8 {
		if r.readBit() != 0 {
			size := 16
			if i >= 6 {
				size = 64
			}
			lastScale := int32(8)
			nextScale := int32(8)
			for range size {
				if nextScale != 0 {
					delta := r.readSE()
					nextScale = (lastScale + delta + 256) % 256
				}
				lastScale = nextScale
			}
		}
	}
}
