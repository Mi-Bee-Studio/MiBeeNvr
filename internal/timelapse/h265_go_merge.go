// Package timelapse — Pure Go H.265/HEVC NAL → MP4 muxer.
//
// H265GoMerger converts raw H.265 keyframe files (Annex-B format with
// 0x00000001 start codes) captured by KeyframeExtractor into playable
// MP4 timelapse videos using only the abema/go-mp4 library and the
// Go standard library. No CGO, no external binaries.
package timelapse

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/abema/go-mp4"
)

func init() {
	// Register hvc1 VisualSampleEntry with go-mp4 so that mp4.Marshal works
	// for H.265/HEVC sample entries.
	mp4.AddAnyTypeBoxDef(&mp4.VisualSampleEntry{}, mp4.StrToBoxType("hvc1"))
}

// H265GoMerger implements TimelapseMerger using pure Go to create an MP4 file
// from raw H.265 IDR keyframe files. Each frame file contains one access unit
// with multiple NAL units (VPS, SPS, PPS, IDR slice) in Annex-B format using
// 0x00000001 start codes.
type H265GoMerger struct{}

// NewH265GoMerger creates a new H265GoMerger.
func NewH265GoMerger() *H265GoMerger {
	return &H265GoMerger{}
}

// CanMerge always returns true since this is a pure Go implementation.
func (m *H265GoMerger) CanMerge() bool {
	return true
}

// Tier returns the merge tier identifier.
func (m *H265GoMerger) Tier() MergeTier {
	return TierGo
}

// Merge reads H.265 keyframe files from framesDir, builds an MP4 file at
// outputPath with the given fps, and returns a MergeResult.
func (m *H265GoMerger) Merge(ctx context.Context, framesDir, outputPath string, fps int) (*MergeResult, error) {
	// List and sort frame files.
	frames, err := listH265FrameFiles(framesDir)
	if err != nil {
		return &MergeResult{Tier: TierGo, Error: err.Error()}, err
	}
	if len(frames) == 0 {
		err := fmt.Errorf("no H.265 frames found in %s", framesDir)
		return &MergeResult{Tier: TierGo, Error: err.Error()}, err
	}

	select {
	case <-ctx.Done():
		return &MergeResult{Tier: TierGo, Error: ctx.Err().Error()}, ctx.Err()
	default:
	}

	if fps <= 0 {
		fps = 1
	}
	sampleDuration := time.Duration(1000/fps) * time.Millisecond

	// Scan frames for VPS, SPS, and PPS — parameter sets may arrive in separate
	// AUs from IDR frames, so they might not be in the first frame file.
	// Scan up to 50 frames to find them.
	var vps, sps, pps []byte
	scanLimit := 50
	if len(frames) < scanLimit {
		scanLimit = len(frames)
	}
	for i := 0; i < scanLimit && (vps == nil || sps == nil || pps == nil); i++ {
		frameData, err := os.ReadFile(frames[i])
		if err != nil {
			continue
		}
		for _, nalu := range splitAnnexB(frameData) {
			if len(nalu) == 0 {
				continue
			}
			// H.265 NAL type: (first_byte >> 1) & 0x3F
			nalType := (nalu[0] >> 1) & 0x3F
			switch nalType {
			case 32: // VPS
				if vps == nil {
					vps = nalu
				}
			case 33: // SPS
				if sps == nil {
					sps = nalu
				}
			case 34: // PPS
				if pps == nil {
					pps = nalu
				}
			}
		}
	}

	if vps == nil {
		return &MergeResult{Tier: TierGo, Error: "frames missing VPS"},
			fmt.Errorf("frames missing VPS")
	}
	if sps == nil {
		return &MergeResult{Tier: TierGo, Error: "frames missing SPS"},
			fmt.Errorf("frames missing SPS")
	}
	if pps == nil {
		return &MergeResult{Tier: TierGo, Error: "frames missing PPS"},
			fmt.Errorf("frames missing PPS")
	}
	// Build hvcC decoder configuration from VPS/SPS/PPS.
	hvcCData := buildHvcC(vps, sps, pps)

	// Parse dimensions from SPS.
	width, height := parseH265Dimensions(sps)
	if width == 0 {
		width = 640
	}
	if height == 0 {
		height = 480
	}

	select {
	case <-ctx.Done():
		return &MergeResult{Tier: TierGo, Error: ctx.Err().Error()}, ctx.Err()
	default:
	}

	// Pre-scan all frames for sample sizes (needed by stsz).
	sampleSizes := make([]uint32, len(frames))
	for i, path := range frames {
		select {
		case <-ctx.Done():
			return &MergeResult{Tier: TierGo, Error: ctx.Err().Error()}, ctx.Err()
		default:
		}
		frameData, err := os.ReadFile(path)
		if err != nil {
			return &MergeResult{Tier: TierGo, Error: err.Error()},
				fmt.Errorf("read frame %s: %w", path, err)
		}
		nalus := splitAnnexB(frameData)
		for _, nalu := range nalus {
			// Skip parameter sets — they belong in hvcC only, not in sample data.
			if isH265ParamSet(nalu) {
				continue
			}
			sampleSizes[i] += 4 + uint32(len(nalu)) // 4-byte length prefix + NAL data
		}
	}

	select {
	case <-ctx.Done():
		return &MergeResult{Tier: TierGo, Error: ctx.Err().Error()}, ctx.Err()
	default:
	}

	// First pass: calculate moov size by writing to a buffer.
	muxer := &h265Muxer{
		frameCount:  len(frames),
		sampleSizes: sampleSizes,
		width:       width,
		height:      height,
		hvcCData:    hvcCData,
		sampleDur:   sampleDuration,
	}

	buf := &bytesWriter{}
	bw := mp4.NewWriter(buf)
	if err := muxer.writeMoov(bw, 0); err != nil {
		return &MergeResult{Tier: TierGo, Error: err.Error()},
			fmt.Errorf("calculate moov size: %w", err)
	}
	moovSize := buf.len()

	select {
	case <-ctx.Done():
		return &MergeResult{Tier: TierGo, Error: ctx.Err().Error()}, ctx.Err()
	default:
	}

	// Create the output file.
	f, err := os.Create(outputPath)
	if err != nil {
		return &MergeResult{Tier: TierGo, Error: err.Error()},
			fmt.Errorf("create output: %w", err)
	}
	defer f.Close()

	w := mp4.NewWriter(f)

	// Write ftyp.
	ftypSize, err := writeH265Ftyp(w)
	if err != nil {
		return &MergeResult{Tier: TierGo, Error: err.Error()},
			fmt.Errorf("write ftyp: %w", err)
	}

	// mdat data starts after ftyp + moov + 8-byte mdat header.
	mdatDataOffset := int64(ftypSize) + int64(moovSize) + 8

	// Write moov with correct stco chunk offset.
	muxer.chunkOffset = mdatDataOffset
	if err := muxer.writeMoov(w, mdatDataOffset); err != nil {
		return &MergeResult{Tier: TierGo, Error: err.Error()},
			fmt.Errorf("write moov: %w", err)
	}

	select {
	case <-ctx.Done():
		f.Close()
		os.Remove(outputPath)
		return &MergeResult{Tier: TierGo, Error: ctx.Err().Error()}, ctx.Err()
	default:
	}

	// Write mdat box (strips VPS/SPS/PPS from samples — they are in hvcC only).
	if err := writeH265Mdat(w, frames, ctx); err != nil {
		f.Close()
		os.Remove(outputPath)
		return &MergeResult{Tier: TierGo, Error: err.Error()}, err
	}

	framesMerged := len(frames)
	return &MergeResult{
		Tier:         TierGo,
		OutputPath:   outputPath,
		FramesMerged: framesMerged,
		Duration:     float64(framesMerged) * sampleDuration.Seconds(),
		Codec:        model.TimelapseMergeCodecH265,
	}, nil
}

// --- H.265 MP4 Muxer ---

// h265Muxer builds the moov box structure for an H.265 video track.
type h265Muxer struct {
	frameCount  int
	sampleSizes []uint32
	width       int
	height      int
	hvcCData    []byte
	sampleDur   time.Duration
	chunkOffset int64
}

func (m *h265Muxer) writeMoov(w *mp4.Writer, chunkOffset int64) error {
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

func (m *h265Muxer) writeMvhd(w *mp4.Writer) error {
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

func (m *h265Muxer) writeTrak(w *mp4.Writer, chunkOffset int64) error {
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

func (m *h265Muxer) writeMdia(w *mp4.Writer, chunkOffset int64) error {
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

func (m *h265Muxer) writeMinf(w *mp4.Writer, chunkOffset int64) error {
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

func (m *h265Muxer) writeStbl(w *mp4.Writer, chunkOffset int64) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stbl")})
	if err != nil {
		return err
	}

	n := m.frameCount

	// stsd > hvc1 + hvcC
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsd")})
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	if err := m.writeHvc1SampleEntry(w); err != nil {
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

// writeHvc1SampleEntry writes the hvc1 sample entry with the hvcC box inside stsd.
func (m *h265Muxer) writeHvc1SampleEntry(w *mp4.Writer) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hvc1")})
	if err != nil {
		return err
	}

	hvc1 := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType("hvc1")},
			DataReferenceIndex: 1,
		},
		Width:           uint16(m.width),
		Height:          uint16(m.height),
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
	}
	if _, err := mp4.Marshal(w, hvc1, mp4.Context{}); err != nil {
		return err
	}

	// hvcC box (HEVC decoder configuration data)
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hvcC")})
	if err != nil {
		return err
	}
	if _, err := w.Write(m.hvcCData); err != nil {
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

// --- Helpers ---

// writeH265Ftyp writes the ftyp box for an HEVC MP4 file and returns its total size.
func writeH265Ftyp(w *mp4.Writer) (int64, error) {
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
			{CompatibleBrand: [4]byte{'h', 'v', 'c', '1'}},
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

// buildHvcC builds the HEVCDecoderConfigurationRecord bytes from raw VPS, SPS,
// and PPS NAL units (without start codes). Format per ISO 14496-15.
func buildHvcC(vps, sps, pps []byte) []byte {
	var buf bytes.Buffer

	// configurationVersion
	buf.WriteByte(1)

	// Parse SPS for profile/level/chroma/bit-depth info.
	// Defaults (Main Profile, 8-bit 4:2:0, Level 4.0).
	profileSpace := 0
	tierFlag := 0
	profileIDC := byte(1)
	compatFlags := uint32(0)
	constraintFlags := [6]byte{} // 48 bits
	levelIDC := byte(120)        // Level 4.0 (40 * 3)
	chromaFormat := byte(1)      // 4:2:0
	bitDepthLuma := byte(0)      // 8-bit
	bitDepthChroma := byte(0)    // 8-bit

	if len(sps) >= 15 {
		parsed := parseH265ProfileLevel(sps)
		if parsed != nil {
			profileSpace = parsed.profileSpace
			tierFlag = parsed.tierFlag
			profileIDC = parsed.profileIDC
			compatFlags = parsed.compatFlags
			constraintFlags = parsed.constraintFlags
			levelIDC = parsed.levelIDC
			chromaFormat = parsed.chromaFormat
			bitDepthLuma = parsed.bitDepthLuma
			bitDepthChroma = parsed.bitDepthChroma
		}
	}

	// general_profile_space (2) + general_tier_flag (1) + general_profile_idc (5)
	buf.WriteByte(byte(profileSpace<<6) | byte(tierFlag<<5) | profileIDC)

	// general_profile_compatibility_flags (32 bits)
	compatBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(compatBytes, compatFlags)
	buf.Write(compatBytes)

	// general_constraint_indicator_flags (48 bits)
	buf.Write(constraintFlags[:])

	// general_level_idc
	buf.WriteByte(levelIDC)

	// min_spatial_segmentation_idc (4 reserved + 12 value) = 0
	buf.WriteByte(0xF0)
	buf.WriteByte(0x00)

	// parallelismType (6 reserved + 2 value) = 0
	buf.WriteByte(0xFC)

	// chromaFormat (6 reserved + 2 value)
	buf.WriteByte(0xFC | (chromaFormat & 0x03))

	// bitDepthLumaMinus8 (5 reserved + 3 value)
	buf.WriteByte(0xF8 | (bitDepthLuma & 0x07))

	// bitDepthChromaMinus8 (5 reserved + 3 value)
	buf.WriteByte(0xF8 | (bitDepthChroma & 0x07))

	// avgFrameRate (16 bits)
	buf.Write([]byte{0x00, 0x00})

	// constantFrameRate(2) + numTemporalLayers(3) + temporalIdNested(1) + lengthSizeMinusOne(2)
	// numTemporalLayers=1, lengthSizeMinusOne=3 (4-byte lengths)
	buf.WriteByte(0x0B)

	// numOfArrays = 3 (VPS, SPS, PPS)
	buf.WriteByte(3)

	// Write arrays in standard order: VPS, SPS, PPS.
	writeHvcCArray(&buf, 32, vps) // VPS type 32
	writeHvcCArray(&buf, 33, sps) // SPS type 33
	writeHvcCArray(&buf, 34, pps) // PPS type 34

	return buf.Bytes()
}

// writeHvcCArray writes one NAL array to the hvcC buffer per ISO 14496-15
// section 8.3.3.1.2 (HEVC NAL Unit Array).
//
// Layout per array:
//
//	array_completeness(1) | reserved(0) | NAL_unit_type(6)   — 1 byte
//	numNalus                                              — 2 bytes  (count of NALUs)
//	for each NALU:
//	  nalUnitLength                                       — 2 bytes
//	  nalUnit                                             — nalUnitLength bytes
//
// We always write exactly one NALU per array (numNalus = 1), since each call
// receives a single parameter set (VPS / SPS / PPS).
func writeHvcCArray(buf *bytes.Buffer, nalType byte, nalu []byte) {
	// array_completeness(1) | reserved(0) | NAL_unit_type(6)
	buf.WriteByte(0x80 | (nalType & 0x3F))
	// numNalus (16 bits) — one NALU in this array.
	buf.WriteByte(0x00)
	buf.WriteByte(0x01)
	// nalUnitLength (16 bits) + nalUnit data
	naluLen := uint16(len(nalu))
	buf.WriteByte(byte(naluLen >> 8))
	buf.WriteByte(byte(naluLen))
	buf.Write(nalu)
}

// isH265ParamSet returns true if the NAL unit is a parameter set (VPS, SPS, or PPS).
// These must only appear in the hvcC box, not in sample data.
func isH265ParamSet(nalu []byte) bool {
	if len(nalu) == 0 {
		return false
	}
	nalType := (nalu[0] >> 1) & 0x3F
	return nalType == 32 || nalType == 33 || nalType == 34 // VPS, SPS, PPS
}

// writeH265Mdat writes the mdat box with H.265 frame data, stripping VPS/SPS/PPS
// from each frame. Parameter sets belong in hvcC only — including them in sample
// data causes MEDIA_ERR_SRC_NOT_SUPPORTED in browsers.
func writeH265Mdat(w *mp4.Writer, framePaths []string, ctx context.Context) error {
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
			if isH265ParamSet(nalu) {
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
			if isH265ParamSet(nalu) {
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

// --- SPS parsing for H.265 ---

// h265SPSProfile holds parsed profile/level/chroma/bit-depth info from an H.265 SPS.
type h265SPSProfile struct {
	profileSpace    int
	tierFlag        int
	profileIDC      byte
	compatFlags     uint32
	constraintFlags [6]byte
	levelIDC        byte
	chromaFormat    byte
	bitDepthLuma    byte
	bitDepthChroma  byte
}

// parseH265ProfileLevel extracts profile/level/chroma/bit-depth from an H.265 SPS.
// The SPS data must include the 2-byte NAL header. Returns nil on parse failure.
func parseH265ProfileLevel(sps []byte) *h265SPSProfile {
	data := removeEmulationPrevention(sps)
	if len(data) < 15 {
		return nil
	}

	r := &bitReader{data: data}
	// Skip NAL header (2 bytes).
	for range 16 {
		r.readBit()
	}

	// sps_video_parameter_set_id (4 bits)
	r.readBits(4)
	// sps_max_sub_layers_minus1 (3 bits)
	maxSubLayers := int(r.readBits(3))
	// sps_temporal_id_nesting_flag (1 bit)
	r.readBit()

	if r.overran {
		return nil
	}

	// profile_tier_level( maxSubLayers )
	profileSpace := int(r.readBits(2))
	tierFlag := int(r.readBit())
	profileIDC := byte(r.readBits(5))

	// general_profile_compatibility_flags (32 bits)
	compatFlags := uint32(0)
	for range 32 {
		compatFlags = (compatFlags << 1) | uint32(r.readBit())
	}

	// general_constraint_indicator_flags (48 bits)
	var constraintFlags [6]byte
	for i := range 6 {
		for range 8 {
			constraintFlags[i] = (constraintFlags[i] << 1) | r.readBit()
		}
	}

	// general_level_idc (8 bits)
	levelIDC := byte(r.readBits(8))

	if r.overran {
		return nil
	}

	// Skip sub-layer profile/level if maxSubLayers > 0.
	if maxSubLayers > 0 {
		subLayerFlags := make([]struct {
			profilePresent bool
			levelPresent   bool
		}, maxSubLayers)
		for i := range maxSubLayers - 1 {
			subLayerFlags[i].profilePresent = r.readBit() == 1
			subLayerFlags[i].levelPresent = r.readBit() == 1
		}
		for i := range maxSubLayers - 1 {
			if subLayerFlags[i].profilePresent {
				// Recursive profile_tier_level(0) - skip 96 bits.
				for range 96 {
					r.readBit()
				}
			}
			if subLayerFlags[i].levelPresent {
				r.readBits(8)
			}
		}
	}

	if r.overran {
		return nil
	}

	// sps_seq_parameter_set_id (ue(v))
	r.readUE()

	// chroma_format_idc (ue(v))
	chromaFormat := byte(r.readUE())

	if chromaFormat == 3 {
		r.readBit() // separate_colour_plane_flag
	}

	// Make a deeper pass to find bit depth. These come after dimensions in the SPS,
	// but we need a simpler approach. We'll parse them by continuing from here.

	// pic_width_in_luma_samples (ue(v))
	r.readUE()
	// pic_height_in_luma_samples (ue(v))
	r.readUE()

	// conformance_window_flag
	if r.readBit() != 0 {
		r.readUE() // conf_win_left_offset
		r.readUE() // conf_win_right_offset
		r.readUE() // conf_win_top_offset
		r.readUE() // conf_win_bottom_offset
	}

	if r.overran {
		return nil
	}

	// bit_depth_luma_minus8 (ue(v))
	bitDepthLuma := byte(r.readUE())
	// bit_depth_chroma_minus8 (ue(v))
	bitDepthChroma := byte(r.readUE())

	if r.overran {
		// Bit depth parsing failed; use defaults but return the rest we have.
		bitDepthLuma = 0
		bitDepthChroma = 0
	}

	return &h265SPSProfile{
		profileSpace:    profileSpace,
		tierFlag:        tierFlag,
		profileIDC:      profileIDC,
		compatFlags:     compatFlags,
		constraintFlags: constraintFlags,
		levelIDC:        levelIDC,
		chromaFormat:    chromaFormat,
		bitDepthLuma:    bitDepthLuma,
		bitDepthChroma:  bitDepthChroma,
	}
}

// parseH265Dimensions extracts width and height from a raw H.265 SPS NAL unit.
// Returns 0, 0 if parsing fails.
func parseH265Dimensions(sps []byte) (width, height int) {
	if len(sps) < 15 {
		return 0, 0
	}

	// Remove emulation prevention bytes for accurate parsing.
	data := removeEmulationPrevention(sps)

	r := &bitReader{data: data}

	// Skip NAL header (2 bytes).
	for range 16 {
		r.readBit()
	}

	// sps_video_parameter_set_id (4 bits)
	r.readBits(4)
	// sps_max_sub_layers_minus1 (3 bits)
	maxSubLayers := int(r.readBits(3))
	// sps_temporal_id_nesting_flag (1 bit)
	r.readBit()

	if r.overran {
		return 0, 0
	}

	// profile_tier_level( maxSubLayers )
	// Skip: profile_space(2) + tier_flag(1) + profile_idc(5) = 8 bits
	// + compat_flags(32) + constraint_flags(48) + level_idc(8) = 88 bits
	// Total fixed portion: 96 bits = 12 bytes
	for range 96 {
		r.readBit()
	}

	if r.overran {
		return 0, 0
	}

	// Skip sub-layer profile/level if maxSubLayers > 0.
	if maxSubLayers > 0 {
		subLayerFlags := make([]struct {
			profilePresent bool
			levelPresent   bool
		}, maxSubLayers)
		for i := range maxSubLayers - 1 {
			subLayerFlags[i].profilePresent = r.readBit() == 1
			subLayerFlags[i].levelPresent = r.readBit() == 1
		}
		for i := range maxSubLayers - 1 {
			if subLayerFlags[i].profilePresent {
				// profile_tier_level(0) = 96 bits
				for range 96 {
					r.readBit()
				}
			}
			if subLayerFlags[i].levelPresent {
				r.readBits(8)
			}
		}
	}

	if r.overran {
		return 0, 0
	}

	// sps_seq_parameter_set_id (ue(v))
	r.readUE()

	// chroma_format_idc (ue(v))
	chromaFormatIDC := int(r.readUE())

	if r.overran {
		return 0, 0
	}

	if chromaFormatIDC == 3 {
		r.readBit() // separate_colour_plane_flag
	}

	// pic_width_in_luma_samples (ue(v))
	width = int(r.readUE())
	// pic_height_in_luma_samples (ue(v))
	height = int(r.readUE())

	if r.overran {
		return 0, 0
	}

	// conformance_window_flag
	cropFlag := r.readBit()
	if cropFlag != 0 {
		cropLeft := int(r.readUE())
		cropRight := int(r.readUE())
		cropTop := int(r.readUE())
		cropBottom := int(r.readUE())

		// SubWidthC and SubHeightC based on chroma format.
		subWidthC := 1
		subHeightC := 1
		switch chromaFormatIDC {
		case 1: // 4:2:0
			subWidthC = 2
			subHeightC = 2
		case 2: // 4:2:2
			subWidthC = 2
			subHeightC = 1
		case 3: // 4:4:4
			subWidthC = 1
			subHeightC = 1
		}

		width -= (cropLeft + cropRight) * subWidthC
		height -= (cropTop + cropBottom) * subHeightC
	}

	if r.overran {
		return 0, 0
	}

	return width, height
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

// --- Frame listing ---

// listH265FrameFiles returns a sorted list of .h265 frame files in the directory.
func listH265FrameFiles(dir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "frame_*.h265"))
	if err != nil {
		return nil, err
	}
	// Sort to ensure correct frame order.
	sort.Strings(matches)
	return matches, nil
}

// Ensure H265GoMerger satisfies the TimelapseMerger interface.
var _ TimelapseMerger = (*H265GoMerger)(nil)
