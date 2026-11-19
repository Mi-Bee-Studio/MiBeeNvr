package muxer

// MP4 box writers for the recorder muxer: ftyp/moov and the per-track
// trak/mdia/minf/stbl chain plus the codec sample entries. Split out of
// mp4mux.go.

import (
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mediaprobe"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mp4util"
	"github.com/abema/go-mp4"
)

func writeFtyp(w *mp4.Writer, tracks []*track) (int64, error) {
	start, _ := w.Seek(0, 1)
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("ftyp")})
	if err != nil {
		return 0, err
	}

	compatibleBrands := []mp4.CompatibleBrandElem{
		{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
		{CompatibleBrand: [4]byte{'i', 's', 'o', '2'}},
		{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
	}
	// Add codec-specific brands
	hasH264 := false
	hasH265 := false
	for _, tr := range tracks {
		if tr.isAudio {
			continue
		}
		if tr.isH265 {
			hasH265 = true
		} else {
			hasH264 = true
		}
	}
	if hasH264 {
		compatibleBrands = append(compatibleBrands, mp4.CompatibleBrandElem{CompatibleBrand: [4]byte{'a', 'v', 'c', '1'}})
	}
	if hasH265 {
		compatibleBrands = append(compatibleBrands, mp4.CompatibleBrandElem{CompatibleBrand: [4]byte{'h', 'e', 'v', '1'}})
	}

	ftyp := &mp4.Ftyp{
		MajorBrand:       [4]byte{'i', 's', 'o', 'm'},
		MinorVersion:     0,
		CompatibleBrands: compatibleBrands,
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

func writeMoov(w *mp4.Writer, tracks []*track) error {
	_, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("moov")})
	if err != nil {
		return err
	}
	if err := writeMvhd(w, tracks); err != nil {
		return err
	}

	// Per-sample chunk offsets are recorded on each track's samples at
	// WriteSample time (streaming layout: tracks interleave in arrival
	// order, so a track's samples are not contiguous — one chunk each).
	for _, tr := range tracks {
		if err := writeTrak(w, tr); err != nil {
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

func writeTrak(w *mp4.Writer, tr *track) error {
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
	if err := writeMdia(w, tr); err != nil {
		return err
	}

	_, err = w.EndBox()
	_ = bi
	return err
}

func writeMdia(w *mp4.Writer, tr *track) error {
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
	if tr.isAudio {
		hdlr.HandlerType = [4]byte{'s', 'o', 'u', 'n'}
		hdlr.Name = "SoundHandler\x00"
	}
	if _, err := mp4.Marshal(w, hdlr, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi3

	// minf
	if err := writeMinf(w, tr); err != nil {
		return err
	}

	_, err = w.EndBox()
	_ = bi
	return err
}

func writeMinf(w *mp4.Writer, tr *track) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("minf")})
	if err != nil {
		return err
	}

	// vmhd (video) or smhd (audio)
	if tr.isAudio {
		bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("smhd")})
		if err != nil {
			return err
		}
		if _, err := mp4.Marshal(w, &mp4.Smhd{}, mp4.Context{}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
		_ = bi2
	} else {
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
	}

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
	if err := writeStbl(w, tr); err != nil {
		return err
	}

	_, err = w.EndBox()
	_ = bi
	return err
}

func writeStbl(w *mp4.Writer, tr *track) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stbl")})
	if err != nil {
		return err
	}

	// stsd > hvc1 > hvcC (HEVC) or avc1 > avcC (H.264)
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsd")})
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	if tr.isAudio {
		switch tr.audioCodec {
		case "g711":
			if err := writeG711SampleEntry(w, tr); err != nil {
				return err
			}
		case "opus":
			if err := writeOpusSampleEntry(w, tr); err != nil {
				return err
			}
		default:
			if err := writeAACSampleEntry(w, tr); err != nil {
				return err
			}
		}
	} else if tr.isH265 {
		if err := writeH265SampleEntry(w, tr); err != nil {
			return err
		}
	} else {
		if err := writeH264SampleEntry(w, tr); err != nil {
			return err
		}
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi2

	// stts — run-length encoded: consecutive equal deltas compress to one
	// entry, exactly like the merge-side writer (internal/merge mp4merge.go).
	// The expanded per-sample table is identical — every consumer either
	// expands runs (mediaprobe parser) or is a standard player, for which RLE
	// is the norm. Real-time camera tracks are near-uniform (one delta per
	// cadence), so this collapses one-entry-per-sample to one-entry-per-run:
	// ~96KB less moov for a 12k-frame segment.
	bi6, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stts")})
	if err != nil {
		return err
	}
	var sttsEntries []mp4.SttsEntry
	if len(tr.samples) > 0 {
		sttsEntries = make([]mp4.SttsEntry, 0, len(tr.samples))
		prevDelta := uint32(tr.samples[0].duration.Milliseconds())
		runCount := uint32(1)
		for i := 1; i < len(tr.samples); i++ {
			delta := uint32(tr.samples[i].duration.Milliseconds())
			if delta == prevDelta {
				runCount++
				continue
			}
			sttsEntries = append(sttsEntries, mp4.SttsEntry{
				SampleCount: runCount,
				SampleDelta: prevDelta,
			})
			prevDelta = delta
			runCount = 1
		}
		// Flush final run.
		sttsEntries = append(sttsEntries, mp4.SttsEntry{
			SampleCount: runCount,
			SampleDelta: prevDelta,
		})
	}
	if _, err := mp4.Marshal(w, &mp4.Stts{EntryCount: uint32(len(sttsEntries)), Entries: sttsEntries}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi6

	// stsc (streaming layout: every sample is its own chunk — tracks
	// interleave in arrival order, so per-track contiguity is impossible)
	bi7, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsc")})
	if err != nil {
		return err
	}
	stscEntries := []mp4.StscEntry{
		{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1},
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
		sizes[i] = s.size
	}
	if _, err := mp4.Marshal(w, &mp4.Stsz{SampleSize: 0, SampleCount: uint32(len(sizes)), EntrySize: sizes}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi8

	// stco (one offset per sample — offsets are absolute, recorded at
	// WriteSample time when the bytes hit the file)
	bi9, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stco")})
	if err != nil {
		return err
	}
	stco := &mp4.Stco{EntryCount: 0, ChunkOffset: nil}
	if len(tr.samples) > 0 {
		stco.ChunkOffset = make([]uint32, len(tr.samples))
		for i, s := range tr.samples {
			stco.ChunkOffset[i] = uint32(s.offset)
		}
		stco.EntryCount = uint32(len(tr.samples))
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

// writeH264SampleEntry writes avc1 + avcC boxes for H.264 tracks.
func writeH264SampleEntry(w *mp4.Writer, tr *track) error {
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
	avcC := mp4util.BuildAvcC(tr.sps, tr.pps)
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
	return nil
}

// writeH265SampleEntry writes hvc1 + hvcC boxes for H.265/HEVC tracks.
func writeH265SampleEntry(w *mp4.Writer, tr *track) error {
	bi3, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hvc1")})
	if err != nil {
		return err
	}
	hvc1 := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType("hvc1")},
			DataReferenceIndex: 1,
		},
		Width:           uint16(tr.width),
		Height:          uint16(tr.height),
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
	}
	if _, err := mp4.Marshal(w, hvc1, mp4.Context{}); err != nil {
		return err
	}
	// hvcC
	bi4, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hvcC")})
	if err != nil {
		return err
	}
	hvcC := mp4util.BuildHvcC(tr.vps, tr.sps, tr.pps)
	if _, err := mp4.Marshal(w, hvcC, mp4.Context{}); err != nil {
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
	return nil
}

// writeAACSampleEntry writes mp4a + esds boxes for AAC audio tracks.
// The esds box contains the AudioSpecificConfig from audioConfig.
func writeAACSampleEntry(w *mp4.Writer, tr *track) error {
	bi3, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mp4a")})
	if err != nil {
		return err
	}

	// Parse AudioSpecificConfig to extract channel count and sample rate.
	// For AAC-LC: 2 bytes. Format: audioObjectType(5) + samplingFreqIndex(4) + channelConfig(4) + ...
	channelCount := uint16(2) // default stereo
	sampleRate := uint32(44100)
	if len(tr.audioConfig) >= 2 {
		sampleRateIndex := (tr.audioConfig[0] >> 3) & 0x0F
		if sampleRateIndex == 0xF && len(tr.audioConfig) >= 3 {
			sampleRate = uint32(tr.audioConfig[1]&0x07)<<8 | uint32(tr.audioConfig[2])
		} else if sampleRateIndex < 15 {
			sampleRates := [...]uint32{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}
			if int(sampleRateIndex) < len(sampleRates) {
				sampleRate = sampleRates[sampleRateIndex]
			}
		}
		channelConfig := ((tr.audioConfig[0] & 0x01) << 2) | ((tr.audioConfig[1] >> 6) & 0x03)
		if channelConfig > 0 {
			channelCount = uint16(channelConfig)
		}
	}

	mp4a := &mp4.AudioSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType("mp4a")},
			DataReferenceIndex: 1,
		},
		EntryVersion: 0,
		ChannelCount: channelCount,
		SampleSize:   16,
		SampleRate:   sampleRate << 16, // fixed-point 16.16
	}
	if _, err := mp4.Marshal(w, mp4a, mp4.Context{}); err != nil {
		return err
	}

	// esds box
	bi4, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("esds")})
	if err != nil {
		return err
	}
	esds := mediaprobe.BuildEsds(tr.audioConfig)
	if _, err := mp4.Marshal(w, esds, mp4.Context{}); err != nil {
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
	return nil
}

// writeG711SampleEntry writes ulaw (μ-law) or alaw (A-law) sample entry for
// G.711 audio via the shared mediaprobe builder (issue #1012 block 2).
func writeG711SampleEntry(w *mp4.Writer, tr *track) error {
	return mediaprobe.WriteG711SampleEntry(w, tr.g711MULaw, uint32(tr.g711Rate))
}

// writeOpusSampleEntry writes Opus sample entry via the shared mediaprobe
// builder (issue #1012 block 2).
func writeOpusSampleEntry(w *mp4.Writer, tr *track) error {
	return mediaprobe.WriteOpusSampleEntry(w, uint16(tr.opusChans), tr.opusPreSkip, tr.opusRate)
}

// --- Helpers ---

func trackDurationMs(tr *track) uint32 {
	d := uint32(0)
	for _, s := range tr.samples {
		d += uint32(s.duration.Milliseconds())
	}
	return d
}

// --- SPS Resolution Parser ---
//
// The bitstream parsers live in internal/mediaprobe (sps.go) — the single
// canonical implementation (issue #1012 block 2). These thin wrappers keep
// muxer's (width, height) no-error call shape: a malformed SPS yields (0, 0),
// which callers treat as "unknown resolution". Note the error-returning
// mediaprobe parsers reject truncated SPS outright, while the old local
// parser silently zero-filled past end-of-data — for such garbage-in cases
// (0, 0) is written instead of a synthesized small resolution.

// parseSPSResolution extracts width and height from an H.264 SPS NAL unit.
// Returns (0, 0) if parsing fails.
func parseSPSResolution(sps []byte) (width, height int) {
	w, h, err := mediaprobe.ParseSPSResolution(sps)
	if err != nil {
		return 0, 0
	}
	return w, h
}

// parseHEVCSPSResolution extracts width and height from an HEVC SPS NAL unit.
// HEVC SPS has a 2-byte NAL header, then: sps_video_parameter_set_id(4),
// sps_max_sub_layers_minus1(3), sps_temporal_id_nesting_flag(1),
// profile_tier_level(88+ bits for max_sub_layers=1),
// sps_seq_parameter_set_id(UE), chroma_format_idc(UE),
// pic_width_in_luma_samples(UE), pic_height_in_luma_samples(UE).
// Returns (0, 0) if parsing fails.
func parseHEVCSPSResolution(sps []byte) (width, height int) {
	w, h, err := mediaprobe.ParseHEVCSPSResolution(sps)
	if err != nil {
		return 0, 0
	}
	return w, h
}
