package merge

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/abema/go-mp4"
)

// SegmentInfo contains parsed metadata and sample table from an MP4 segment.
type SegmentInfo struct {
	Codec         string        // "h264" or "h265"
	SPS           []byte        // H.264 only
	PPS           []byte        // H.264 only
	VPS           []byte        // H.265 only
	Timescale     uint32
	SampleCount   int
	TotalDuration time.Duration
	MdatOffset    int64 // file offset of mdat box header
	MdatSize      int64 // total mdat box size including header
	Samples       []SampleEntry
	FilePath      string        // source file path for data reading
}

// SampleEntry describes a single media sample within mdat.
type SampleEntry struct {
	Offset     int64  // absolute file offset of sample data (after 4-byte NAL length prefix)
	Size       uint32 // size of sample data (including 4-byte NAL length prefix)
	Duration   uint32 // in timescale units
	IsKeyFrame bool
}

// ParseSegment reads an MP4 file and extracts codec config, sample tables,
// and mdat location. Uses file seeking — does not load the entire file.
func ParseSegment(filePath string) (*SegmentInfo, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	// Get file size for validation against corrupted box headers.
	fileInfo, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}
	fileSize := fileInfo.Size()

	// Accumulators for box data collected during the walk.
	var (
		timescale   uint32
		mdatOffset  int64
		mdatSize    int64
		codec       string
		sps, pps    []byte
		vps         []byte
		sttsEntries []mp4.SttsEntry
		stszSizes   []uint32 // per-sample sizes (used when SampleSize == 0)
		stszUniform uint32   // uniform size (when SampleSize != 0)
		sampleCount uint32
		stscEntries []mp4.StscEntry
		stcoOffsets []uint32
		co64Offsets []uint64
	)

	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		boxType := h.BoxInfo.Type.String()

		// Skip mdat at any nesting level — it can be very large and must
		// never be loaded into memory. Record its offset/size for later seeking.
		if boxType == "mdat" {
			if len(h.Path) == 1 { // top-level mdat
				mdatOffset = int64(h.BoxInfo.Offset)
				mdatSize = int64(h.BoxInfo.Size)
			}
			return nil, nil
		}

		// Validate box size against file size to catch corrupted headers.
		if h.BoxInfo.Size > uint64(fileSize) {
			return nil, fmt.Errorf("box %q claims size %d but file is only %d bytes",
				boxType, h.BoxInfo.Size, fileSize)
		}

		if !h.BoxInfo.IsSupportedType() {
			return nil, nil
		}

		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, err
		}

		switch b := box.(type) {
		case *mp4.Mdhd:
			timescale = b.Timescale

		case *mp4.Stts:
			sttsEntries = b.Entries

		case *mp4.Stsz:
			sampleCount = b.SampleCount
			if b.SampleSize != 0 {
				stszUniform = b.SampleSize
			} else {
				stszSizes = b.EntrySize
			}

		case *mp4.Stsc:
			stscEntries = b.Entries

		case *mp4.Stco:
			stcoOffsets = b.ChunkOffset

		case *mp4.Co64:
			co64Offsets = b.ChunkOffset

		case *mp4.AVCDecoderConfiguration:
			codec = "h264"
			if len(b.SequenceParameterSets) > 0 {
				sps = make([]byte, len(b.SequenceParameterSets[0].NALUnit))
				copy(sps, b.SequenceParameterSets[0].NALUnit)
			}
			if len(b.PictureParameterSets) > 0 {
				pps = make([]byte, len(b.PictureParameterSets[0].NALUnit))
				copy(pps, b.PictureParameterSets[0].NALUnit)
			}

		case *mp4.HvcC:
			codec = "h265"
			for _, arr := range b.NaluArrays {
				if len(arr.Nalus) == 0 {
					continue
				}
				nal := arr.Nalus[0].NALUnit
				switch arr.NaluType {
				case 32: // VPS
					vps = make([]byte, len(nal))
					copy(vps, nal)
				case 33: // SPS
					sps = make([]byte, len(nal))
					copy(sps, nal)
				case 34: // PPS
					pps = make([]byte, len(nal))
					copy(pps, nal)
				}
			}
		}

		return h.Expand()
	})
	if err != nil {
		return nil, fmt.Errorf("parse MP4: %w", err)
	}

	if timescale == 0 {
		return nil, fmt.Errorf("no mdhd box found")
	}
	if sampleCount == 0 {
		return nil, fmt.Errorf("no samples in segment")
	}

	// Build per-sample size array.
	if stszUniform != 0 {
		stszSizes = make([]uint32, sampleCount)
		for i := range stszSizes {
			stszSizes[i] = stszUniform
		}
	}

	// Merge chunk offsets: prefer stco, fallback to co64.
	chunkOffsets := make([]int64, 0, len(stcoOffsets)+len(co64Offsets))
	for _, off := range stcoOffsets {
		chunkOffsets = append(chunkOffsets, int64(off))
	}
	if len(co64Offsets) > 0 {
		chunkOffsets = chunkOffsets[:0]
		for _, off := range co64Offsets {
			chunkOffsets = append(chunkOffsets, int64(off))
		}
	}

	// Build sample entries from the sample tables.
	samples, err := buildSampleEntries(stszSizes, stscEntries, chunkOffsets, sttsEntries)
	if err != nil {
		return nil, fmt.Errorf("build sample entries: %w", err)
	}

	// Calculate total duration from stts.
	totalDur := time.Duration(0)
	for _, e := range sttsEntries {
		totalDur += time.Duration(e.SampleCount) * time.Duration(e.SampleDelta) * time.Second / time.Duration(timescale)
	}

	// Detect keyframes by reading NAL headers from file.
	if err := detectKeyframes(f, samples, codec); err != nil {
		return nil, fmt.Errorf("detect keyframes: %w", err)
	}

	return &SegmentInfo{
		Codec:         codec,
		SPS:           sps,
		PPS:           pps,
		VPS:           vps,
		Timescale:     timescale,
		SampleCount:   len(samples),
		TotalDuration: totalDur,
		MdatOffset:    mdatOffset,
		MdatSize:      mdatSize,
		Samples:       samples,
		FilePath:      filePath,
	}, nil
}

// buildSampleEntries computes per-sample file offsets, sizes, and durations
// from the stsz, stsc, stco/co64, and stts tables.
func buildSampleEntries(
	sizes []uint32,
	stsc []mp4.StscEntry,
	chunkOffsets []int64,
	stts []mp4.SttsEntry,
) ([]SampleEntry, error) {
	n := len(sizes)
	if n == 0 {
		return nil, nil
	}
	if len(stsc) == 0 {
		return nil, fmt.Errorf("no stsc entries")
	}
	if len(chunkOffsets) == 0 {
		return nil, fmt.Errorf("no chunk offsets")
	}

	samples := make([]SampleEntry, n)

	// --- Durations from stts (run-length encoded). ---
	if len(stts) > 0 {
		durIdx := 0
		durRemaining := stts[0].SampleCount
		for i := 0; i < n; i++ {
			for durRemaining == 0 && durIdx+1 < len(stts) {
				durIdx++
				durRemaining = stts[durIdx].SampleCount
			}
			if durRemaining > 0 {
				samples[i].Duration = stts[durIdx].SampleDelta
				durRemaining--
			}
		}
	}

	// --- File offsets from stsc + stco + stsz. ---
	// stsc entries are sorted by FirstChunk (1-indexed).
	sampleIdx := 0
	for i, entry := range stsc {
		firstChunk := int(entry.FirstChunk)
		samplesPerChunk := int(entry.SamplesPerChunk)

		var lastChunk int
		if i+1 < len(stsc) {
			lastChunk = int(stsc[i+1].FirstChunk) - 1
		} else {
			lastChunk = len(chunkOffsets)
		}

		for chunkNum := firstChunk; chunkNum <= lastChunk; chunkNum++ {
			if chunkNum < 1 || chunkNum-1 >= len(chunkOffsets) || sampleIdx >= n {
				break
			}
			chunkOff := chunkOffsets[chunkNum-1]
			offsetInChunk := int64(0)

			for s := 0; s < samplesPerChunk && sampleIdx < n; s++ {
				samples[sampleIdx].Offset = chunkOff + offsetInChunk
				samples[sampleIdx].Size = sizes[sampleIdx]
				offsetInChunk += int64(sizes[sampleIdx])
				sampleIdx++
			}
		}
	}

	if sampleIdx != n {
		return nil, fmt.Errorf("sample count mismatch: got %d from stsc, expected %d from stsz", sampleIdx, n)
	}

	return samples, nil
}

// detectKeyframes reads the first few bytes of each sample's NAL data
// to determine if it's a keyframe (IDR for H.264, IRAP for H.265).
func detectKeyframes(f *os.File, samples []SampleEntry, codec string) error {
	if len(samples) == 0 || codec == "" {
		return nil
	}

	// 4-byte NAL length prefix + up to 2 bytes NAL header (H.265 has 2-byte header).
	buf := make([]byte, 6)

	for i := range samples {
		if samples[i].Size < 5 {
			continue
		}

		n, err := f.ReadAt(buf, samples[i].Offset)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return fmt.Errorf("read sample %d at offset %d: %w", i, samples[i].Offset, err)
		}
		if n < 5 {
			continue
		}

		// buf[0:4] = NAL length prefix (big-endian).
		// buf[4] = first byte of NAL unit (for both H.264 and H.265).
		switch codec {
		case "h264":
			nalType := buf[4] & 0x1F
			samples[i].IsKeyFrame = (nalType == 5) // IDR slice
		case "h265":
			// H.265 NAL header: forbidden_zero_bit(1) + nal_unit_type(6) + nuh_layer_id(6) + nuh_temporal_id_plus1(3).
			// Type is in bits 1-6 of the first NAL header byte.
			nalType := (uint16(buf[4]) >> 1) & 0x3F
			samples[i].IsKeyFrame = (nalType >= 16 && nalType <= 21) // IRAP: BLA/IDR/CRA
		}
	}

	return nil
}
