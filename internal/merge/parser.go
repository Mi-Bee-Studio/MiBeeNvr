package merge

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/abema/go-mp4"
)

// SegmentInfo contains parsed metadata and sample table from an MP4 segment.
type SegmentInfo struct {
	Codec         string // "h264" or "h265"
	SPS           []byte // H.264 only
	PPS           []byte // H.264 only
	VPS           []byte // H.265 only
	Timescale     uint32
	SampleCount   int
	TotalDuration time.Duration
	MdatOffset    int64 // file offset of mdat box header
	MdatSize      int64 // total mdat box size including header
	Samples       []SampleEntry
	FilePath      string // source file path for data reading

	// Audio track fields (populated when segment contains audio).
	HasAudio         bool
	AudioConfig      []byte // AAC AudioSpecificConfig bytes
	AudioTimescale   uint32
	AudioSampleCount int
	AudioSamples     []SampleEntry
}

// SampleEntry describes a single media sample within mdat.
type SampleEntry struct {
	Offset     int64  // absolute file offset of sample data
	Size       uint32 // size of sample data
	Duration   uint32 // in timescale units
	IsKeyFrame bool
}

// trackAccum collects per-track data during MP4 box structure walking.
type trackAccum struct {
	handlerType [4]byte // 'vide' or 'soun'
	timescale   uint32

	// Video codec fields
	codec    string
	sps, pps []byte
	vps      []byte

	// Audio codec fields
	audioConfig []byte

	// Sample table fields
	sttsEntries []mp4.SttsEntry
	stszSizes   []uint32 // per-sample sizes (used when SampleSize == 0)
	stszUniform uint32   // uniform size (when SampleSize != 0)
	sampleCount uint32
	stscEntries []mp4.StscEntry
	stcoOffsets []uint32
	co64Offsets []uint64
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

	// Per-track accumulators.
	var (
		mdatOffset int64
		mdatSize   int64
		tracks     []*trackAccum
		current    *trackAccum
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

		// Track new trak boxes — creates a new accumulator per trak.
		// DFS order ensures children of trak N are processed before trak N+1.
		if boxType == "trak" {
			current = &trackAccum{}
			tracks = append(tracks, current)
			return h.Expand()
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

		// Only accumulate data when inside a trak.
		if current == nil {
			return h.Expand()
		}

		switch b := box.(type) {
		case *mp4.Hdlr:
			current.handlerType = b.HandlerType

		case *mp4.Mdhd:
			current.timescale = b.Timescale

		case *mp4.Stts:
			current.sttsEntries = b.Entries

		case *mp4.Stsz:
			current.sampleCount = b.SampleCount
			if b.SampleSize != 0 {
				current.stszUniform = b.SampleSize
			} else {
				current.stszSizes = b.EntrySize
			}

		case *mp4.Stsc:
			current.stscEntries = b.Entries

		case *mp4.Stco:
			current.stcoOffsets = b.ChunkOffset

		case *mp4.Co64:
			current.co64Offsets = b.ChunkOffset

		case *mp4.AVCDecoderConfiguration:
			current.codec = "h264"
			if len(b.SequenceParameterSets) > 0 {
				current.sps = make([]byte, len(b.SequenceParameterSets[0].NALUnit))
				copy(current.sps, b.SequenceParameterSets[0].NALUnit)
			}
			if len(b.PictureParameterSets) > 0 {
				current.pps = make([]byte, len(b.PictureParameterSets[0].NALUnit))
				copy(current.pps, b.PictureParameterSets[0].NALUnit)
			}

		case *mp4.HvcC:
			current.codec = "h265"
			for _, arr := range b.NaluArrays {
				if len(arr.Nalus) == 0 {
					continue
				}
				nal := arr.Nalus[0].NALUnit
				switch arr.NaluType {
				case 32: // VPS
					current.vps = make([]byte, len(nal))
					copy(current.vps, nal)
				case 33: // SPS
					current.sps = make([]byte, len(nal))
					copy(current.sps, nal)
				case 34: // PPS
					current.pps = make([]byte, len(nal))
					copy(current.pps, nal)
				}
			}

		case *mp4.Esds:
			for _, d := range b.Descriptors {
				if d.Tag == mp4.DecSpecificInfoTag && len(d.Data) > 0 {
					current.audioConfig = make([]byte, len(d.Data))
					copy(current.audioConfig, d.Data)
				}
			}
		}

		return h.Expand()
	})
	if err != nil {
		return nil, fmt.Errorf("parse MP4: %w", err)
	}

	// Identify video and audio tracks.
	var videoTrack, audioTrack *trackAccum
	for _, tr := range tracks {
		if bytes.Equal(tr.handlerType[:], []byte("soun")) {
			audioTrack = tr
		} else {
			// Default to video for 'vide' or any unrecognized handler.
			videoTrack = tr
		}
	}

	if videoTrack == nil {
		return nil, fmt.Errorf("no video track found")
	}
	if videoTrack.timescale == 0 {
		return nil, fmt.Errorf("no mdhd box found")
	}
	if videoTrack.sampleCount == 0 {
		return nil, fmt.Errorf("no samples in segment")
	}

	// Build video sample entries.
	videoSamples, err := buildTrackSamples(videoTrack)
	if err != nil {
		return nil, fmt.Errorf("build video samples: %w", err)
	}

	// Detect keyframes in video samples.
	if err := detectKeyframes(f, videoSamples, videoTrack.codec); err != nil {
		return nil, fmt.Errorf("detect keyframes: %w", err)
	}

	// Calculate total video duration from stts.
	totalDur := time.Duration(0)
	for _, e := range videoTrack.sttsEntries {
		totalDur += time.Duration(e.SampleCount) * time.Duration(e.SampleDelta) * time.Second / time.Duration(videoTrack.timescale)
	}

	info := &SegmentInfo{
		Codec:         videoTrack.codec,
		SPS:           videoTrack.sps,
		PPS:           videoTrack.pps,
		VPS:           videoTrack.vps,
		Timescale:     videoTrack.timescale,
		SampleCount:   len(videoSamples),
		TotalDuration: totalDur,
		MdatOffset:    mdatOffset,
		MdatSize:      mdatSize,
		Samples:       videoSamples,
		FilePath:      filePath,
	}

	// Build audio sample entries if audio track present.
	if audioTrack != nil && audioTrack.sampleCount > 0 && len(audioTrack.audioConfig) > 0 {
		audioSamples, err := buildTrackSamples(audioTrack)
		if err != nil {
			return nil, fmt.Errorf("build audio samples: %w", err)
		}

		info.HasAudio = true
		info.AudioConfig = audioTrack.audioConfig
		info.AudioTimescale = audioTrack.timescale
		info.AudioSampleCount = len(audioSamples)
		info.AudioSamples = audioSamples
	}

	return info, nil
}

// buildTrackSamples builds per-sample file offsets, sizes, and durations
// from a track accumulator's sample tables.
func buildTrackSamples(tr *trackAccum) ([]SampleEntry, error) {
	// Build per-sample size array.
	stszSizes := tr.stszSizes
	if tr.stszUniform != 0 {
		stszSizes = make([]uint32, tr.sampleCount)
		for i := range stszSizes {
			stszSizes[i] = tr.stszUniform
		}
	}

	// Merge chunk offsets: prefer stco, fallback to co64.
	chunkOffsets := make([]int64, 0, len(tr.stcoOffsets)+len(tr.co64Offsets))
	for _, off := range tr.stcoOffsets {
		chunkOffsets = append(chunkOffsets, int64(off))
	}
	if len(tr.co64Offsets) > 0 {
		chunkOffsets = chunkOffsets[:0]
		for _, off := range tr.co64Offsets {
			chunkOffsets = append(chunkOffsets, int64(off))
		}
	}

	return buildSampleEntries(stszSizes, tr.stscEntries, chunkOffsets, tr.sttsEntries)
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
