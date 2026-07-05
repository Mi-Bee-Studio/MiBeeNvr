// Package timelapse — Pure Go H.264 NAL → MP4 muxer.
//
// H264GoMerger converts raw H.264 keyframe files (Annex-B format with
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

	"github.com/abema/go-mp4"
)

// H264GoMerger implements TimelapseMerger using pure Go to create an MP4 file
// from raw H.264 IDR keyframe files. Each frame file contains one access unit
// with multiple NAL units (SPS, PPS, IDR slice) in Annex-B format using
// 0x00000001 start codes.
type H264GoMerger struct{}

// NewH264GoMerger creates a new H264GoMerger.
func NewH264GoMerger() *H264GoMerger {
	return &H264GoMerger{}
}

// CanMerge always returns true since this is a pure Go implementation.
func (m *H264GoMerger) CanMerge() bool {
	return true
}

// Tier returns the merge tier identifier.
func (m *H264GoMerger) Tier() MergeTier {
	return TierGo
}

// Merge reads H.264 keyframe files from framesDir, builds an MP4 file at
// outputPath with the given fps, and returns a MergeResult.
func (m *H264GoMerger) Merge(ctx context.Context, framesDir, outputPath string, fps int) (*MergeResult, error) {
	// List and sort frame files.
	frames, err := listH264FrameFiles(framesDir)
	if err != nil {
		return &MergeResult{Tier: TierGo, Error: err.Error()}, err
	}
	if len(frames) == 0 {
		err := fmt.Errorf("no H.264 frames found in %s", framesDir)
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

	// Scan frames for SPS and PPS — parameter sets may arrive in separate
	// AUs from IDR frames, so they might not be in the first frame file.
	// Scan up to 50 frames to find them.
	var sps, pps []byte
	scanLimit := 50
	if len(frames) < scanLimit {
		scanLimit = len(frames)
	}
	for i := 0; i < scanLimit && (sps == nil || pps == nil); i++ {
		frameData, err := os.ReadFile(frames[i])
		if err != nil {
			continue
		}
		for _, nalu := range splitAnnexB(frameData) {
			if len(nalu) == 0 {
				continue
			}
			nalType := nalu[0] & 0x1F
			switch nalType {
			case 7: // SPS
				if sps == nil {
					sps = nalu
				}
			case 8: // PPS
				if pps == nil {
					pps = nalu
				}
			}
		}
	}

	if sps == nil {
		return &MergeResult{Tier: TierGo, Error: "frames missing SPS"},
			fmt.Errorf("frames missing SPS")
	}
	if pps == nil {
		return &MergeResult{Tier: TierGo, Error: "frames missing PPS"},
			fmt.Errorf("frames missing PPS")
	}

	// Build avcC decoder configuration from SPS/PPS.
	avcCData := buildAvcC(sps, pps)

	// Parse dimensions from SPS.
	width, height := parseH264Dimensions(sps)
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
			// Skip parameter sets — they belong in avcC only, not in sample data.
			if isH264ParamSet(nalu) {
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
	muxer := &h264Muxer{
		frameCount:  len(frames),
		sampleSizes: sampleSizes,
		width:       width,
		height:      height,
		avcCData:    avcCData,
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
	ftypSize, err := writeFtyp(w)
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

	// Write mdat box (strips SPS/PPS from samples — they are in avcC only).
	if err := writeH264Mdat(w, frames, ctx); err != nil {
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
	}, nil
}

// --- H.264 MP4 Muxer ---

// h264Muxer builds the moov box structure for an H.264 video track.
type h264Muxer struct {
	frameCount  int
	sampleSizes []uint32
	width       int
	height      int
	avcCData    []byte
	sampleDur   time.Duration
	chunkOffset int64
}

func (m *h264Muxer) writeMoov(w *mp4.Writer, chunkOffset int64) error {
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

func (m *h264Muxer) writeMvhd(w *mp4.Writer) error {
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

func (m *h264Muxer) writeTrak(w *mp4.Writer, chunkOffset int64) error {
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

func (m *h264Muxer) writeMdia(w *mp4.Writer, chunkOffset int64) error {
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

func (m *h264Muxer) writeMinf(w *mp4.Writer, chunkOffset int64) error {
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

func (m *h264Muxer) writeStbl(w *mp4.Writer, chunkOffset int64) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stbl")})
	if err != nil {
		return err
	}

	n := m.frameCount

	// stsd > avc1 + avcC
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsd")})
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	if err := m.writeAVC1SampleEntry(w); err != nil {
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

// writeAVC1SampleEntry writes the avc1 sample entry with the avcC box inside stsd.
func (m *h264Muxer) writeAVC1SampleEntry(w *mp4.Writer) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("avc1")})
	if err != nil {
		return err
	}

	avc1 := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType("avc1")},
			DataReferenceIndex: 1,
		},
		Width:           uint16(m.width),
		Height:          uint16(m.height),
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
	}
	if _, err := mp4.Marshal(w, avc1, mp4.Context{}); err != nil {
		return err
	}

	// avcC box (raw decoder configuration data)
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("avcC")})
	if err != nil {
		return err
	}
	if _, err := w.Write(m.avcCData); err != nil {
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

// writeFtyp writes the ftyp box and returns its total size.
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
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
			{CompatibleBrand: [4]byte{'a', 'v', 'c', '1'}},
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

// isH264ParamSet returns true if the NAL unit is a parameter set (SPS or PPS).
// These must only appear in the avcC box, not in sample data.
func isH264ParamSet(nalu []byte) bool {
	if len(nalu) == 0 {
		return false
	}
	nalType := nalu[0] & 0x1F
	return nalType == 7 || nalType == 8 // SPS, PPS
}

// writeH264Mdat writes the mdat box with H.264 frame data, stripping SPS/PPS
// from each frame. Parameter sets belong in avcC only — including them in sample
// data causes MEDIA_ERR_SRC_NOT_SUPPORTED in browsers.
func writeH264Mdat(w *mp4.Writer, framePaths []string, ctx context.Context) error {
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
			if isH264ParamSet(nalu) {
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
			if isH264ParamSet(nalu) {
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

// buildAvcC builds the AVCDecoderConfiguration record bytes from raw SPS and PPS
// NAL units (without start codes). Format per ISO 14496-15.
func buildAvcC(sps, pps []byte) []byte {
	// Extract profile/compatibility/level from SPS.
	// Raw SPS NALU layout: [NAL header][profile_idc][constraint_flags][level_idc]...
	profile := byte(66)  // default: Baseline
	compat := byte(0xC0) // default
	level := byte(30)    // default: Level 3.0
	if len(sps) >= 4 {
		profile = sps[1]
		compat = sps[2]
		level = sps[3]
	}

	var buf bytes.Buffer
	// configurationVersion
	buf.WriteByte(1)
	// AVCProfileIndication
	buf.WriteByte(profile)
	// profile_compatibility
	buf.WriteByte(compat)
	// AVCLevelIndication
	buf.WriteByte(level)
	// Reserved (6 bits) + lengthSizeMinusOne (2 bits) = 0xFF (length size = 4 bytes)
	buf.WriteByte(0xFF)
	// Reserved (3 bits) + numOfSequenceParameterSets (5 bits) = 0xE1 (1 SPS)
	buf.WriteByte(0xE1)

	// SPS
	spsLen := len(sps)
	buf.WriteByte(byte(spsLen >> 8))
	buf.WriteByte(byte(spsLen))
	buf.Write(sps)

	// PPS
	buf.WriteByte(1) // numOfPictureParameterSets
	ppsLen := len(pps)
	buf.WriteByte(byte(ppsLen >> 8))
	buf.WriteByte(byte(ppsLen))
	buf.Write(pps)

	return buf.Bytes()
}

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

// parseH264Dimensions extracts width and height from a raw H.264 SPS NAL unit.
// Returns 0, 0 if parsing fails.
func parseH264Dimensions(sps []byte) (width, height int) {
	if len(sps) < 8 {
		return 0, 0
	}

	// Skip NAL header byte.
	data := sps[1:]

	// profile_idc (1 byte), constraint_set_flags (1 byte), level_idc (1 byte).
	if len(data) < 3 {
		return 0, 0
	}
	profileIDC := data[0]
	data = data[3:]

	r := &bitReader{data: data}

	// seq_parameter_set_id (ue(v))
	r.readUE()

	// High-profile-specific fields.
	if profileIDC == 100 || profileIDC == 110 || profileIDC == 122 || profileIDC == 244 ||
		profileIDC == 44 || profileIDC == 83 || profileIDC == 86 || profileIDC == 118 ||
		profileIDC == 128 || profileIDC == 138 || profileIDC == 139 || profileIDC == 134 ||
		profileIDC == 135 {
		chromaFormatIDC := r.readUE()
		if chromaFormatIDC == 3 {
			r.readBit() // separate_colour_plane_flag
		}
		r.readUE()            // bit_depth_luma_minus8
		r.readUE()            // bit_depth_chroma_minus8
		r.readBit()           // qpprime_y_zero_transform_bypass_flag
		if r.readBit() != 0 { // seq_scaling_matrix_present_flag
			skipScalingLists(r)
		}
	}

	// log2_max_frame_num_minus4 (ue(v))
	r.readUE()

	// pic_order_cnt_type (ue(v))
	pocType := r.readUE()
	if pocType == 0 {
		r.readUE() // log2_max_pic_order_cnt_lsb_minus4
	} else if pocType == 1 {
		r.readBit() // delta_pic_order_always_zero_flag
		r.readSE()  // offset_for_non_ref_pic
		numRefFramesInPOC := r.readUE()
		for i := uint32(0); i < numRefFramesInPOC; i++ {
			r.readSE() // offset_for_ref_frame
		}
	}
	// pocType == 2: no additional fields

	// max_num_ref_frames (ue(v))
	r.readUE()

	// gaps_in_frame_num_value_allowed_flag (u(1))
	r.readBit()

	// pic_width_in_mbs_minus1 (ue(v))
	picWidthInMBs := r.readUE() + 1
	if r.overran {
		return 0, 0
	}
	width = int(picWidthInMBs) * 16
	// pic_height_in_map_units_minus1 (ue(v))
	picHeightInMapUnits := r.readUE() + 1
	if r.overran {
		return width, 0
	}

	// frame_mbs_only_flag (u(1))
	frameMBsOnlyFlag := r.readBit()
	heightInMBs := picHeightInMapUnits
	if frameMBsOnlyFlag == 0 {
		// Interlaced: each map unit is a field macroblock pair.
		heightInMBs = picHeightInMapUnits * 2
	}
	height = int(heightInMBs) * 16

	// mb_adaptive_frame_field_flag (only when frame_mbs_only_flag == 0)
	if frameMBsOnlyFlag == 0 {
		r.readBit() // mb_adaptive_frame_field_flag
	}

	// direct_8x8_inference_flag (u(1))
	r.readBit()

	// frame_cropping_flag
	croppingFlag := r.readBit()
	if croppingFlag != 0 {
		// frame_crop_left_offset (ue(v))
		cropLeft := r.readUE()
		// frame_crop_right_offset (ue(v))
		cropRight := r.readUE()
		// frame_crop_top_offset (ue(v))
		cropTop := r.readUE()
		// frame_crop_bottom_offset (ue(v))
		cropBottom := r.readUE()

		// For 4:2:0 chroma (assumed), crop units are 2 for horizontal and 2 for vertical.
		width -= int(cropLeft+cropRight) * 2
		height -= int(cropTop+cropBottom) * 2
	}

	return width, height
}

// --- Bit-level reader for H.264 SPS parsing ---

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
	for i := 0; i < n; i++ {
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

// skipScalingLists skips scaling list fields in an SPS.
func skipScalingLists(r *bitReader) {
	for i := 0; i < 8; i++ {
		if r.readBit() != 0 {
			size := 16
			if i >= 6 {
				size = 64
			}
			lastScale := int32(8)
			nextScale := int32(8)
			for j := 0; j < size; j++ {
				if nextScale != 0 {
					delta := r.readSE()
					nextScale = (lastScale + delta + 256) % 256
				}
				lastScale = nextScale
			}
		}
	}
}

// --- Frame listing ---

// listH264FrameFiles returns a sorted list of .h264 frame files in the directory.
func listH264FrameFiles(dir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "frame_*.h264"))
	if err != nil {
		return nil, err
	}
	// Sort to ensure correct frame order.
	sort.Strings(matches)
	return matches, nil
}

// Ensure H264GoMerger satisfies the TimelapseMerger interface.
var _ TimelapseMerger = (*H264GoMerger)(nil)
