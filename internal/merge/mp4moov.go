package merge

// moov box construction for merged output: the sampled-description chains
// (mvhd/trak/mdia/minf/stbl) plus the per-codec sample entries. Split out of
// mp4merge.go — these functions only write the trailer; the streaming pipeline
// that fills in the sample tables lives in mp4merge.go.

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mp4util"
	"github.com/abema/go-mp4"
)

// mergeTrack holds track info for building the merged moov box.
type mergeTrack struct {
	isH265       bool
	isAudio      bool
	sps          []byte
	pps          []byte
	vps          []byte
	audioConfig  []byte
	audioCodec   string // "g711" for G.711 audio, empty for AAC
	g711MULaw    bool   // true=μ-law, false=A-law
	timescale    uint32
	totalSamples int
	duration     uint32
	width        uint16
	height       uint16
	samples      []mergedSample
}

// writeMergeMoov writes a complete moov box for the merged output.
func writeMergeMoov(w *mp4.Writer, videoTrack *mergeTrack, audioTrack *mergeTrack, videoChunkOffset, audioChunkOffset int64) error {
	_, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("moov")})
	if err != nil {
		return err
	}
	if err := writeMergeMvhd(w, videoTrack, audioTrack != nil); err != nil {
		return err
	}
	if err := writeMergeTrak(w, videoTrack, videoChunkOffset); err != nil {
		return err
	}
	if audioTrack != nil {
		if err := writeMergeTrak(w, audioTrack, audioChunkOffset); err != nil {
			return err
		}
	}
	_, err = w.EndBox()
	return err
}

func writeMergeMvhd(w *mp4.Writer, tr *mergeTrack, hasAudio bool) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mvhd")})
	if err != nil {
		return err
	}
	nextTrackID := uint32(2)
	if hasAudio {
		nextTrackID = 3
	}
	mvhd := &mp4.Mvhd{
		Timescale:   tr.timescale,
		DurationV0:  tr.duration,
		Rate:        0x00010000,
		Volume:      0x0100,
		NextTrackID: nextTrackID,
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
	trackID := uint32(1)
	if tr.isAudio {
		trackID = 2
	}
	tkhd := &mp4.Tkhd{
		TrackID:    trackID,
		DurationV0: tr.duration,
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
	var hdlr *mp4.Hdlr
	if tr.isAudio {
		hdlr = &mp4.Hdlr{
			HandlerType: [4]byte{'s', 'o', 'u', 'n'},
			Name:        "SoundHandler\x00",
		}
	} else {
		hdlr = &mp4.Hdlr{
			HandlerType: [4]byte{'v', 'i', 'd', 'e'},
			Name:        "VideoHandler\x00",
		}
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
	// vmhd or smhd
	if tr.isAudio {
		// smhd
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
	if tr.isAudio {
		if err := writeMergeAudioSampleEntry(w, tr); err != nil {
			return err
		}
	} else if tr.isH265 {
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

	// stts — run-length encoded (compressed).
	bi6, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stts")})
	if err != nil {
		return err
	}
	var sttsEntries []mp4.SttsEntry
	if n > 0 {
		sttsEntries = make([]mp4.SttsEntry, 0, n)
		prevDur := samples[0].duration
		runCount := uint32(1)
		for i := 1; i < n; i++ {
			if samples[i].duration == prevDur {
				runCount++
				continue
			}
			sttsEntries = append(sttsEntries, mp4.SttsEntry{
				SampleCount: runCount,
				SampleDelta: prevDur,
			})
			prevDur = samples[i].duration
			runCount = 1
		}
		// Flush final run.
		sttsEntries = append(sttsEntries, mp4.SttsEntry{
			SampleCount: runCount,
			SampleDelta: prevDur,
		})
	}
	if _, err := mp4.Marshal(w, &mp4.Stts{EntryCount: uint32(len(sttsEntries)), Entries: sttsEntries}, mp4.Context{}); err != nil {
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

	// stss — sync sample table (1-indexed). Lets parsers (VOD fragmenter, other
	// players) find keyframes without probing media bytes. Written whenever any
	// keyframe exists — including the all-sync case — so the SIZE-estimation
	// pass (which marks every placeholder sample as a keyframe = worst case)
	// never under-reserves the moov.
	var syncSamples []uint32
	for i, s := range samples {
		if s.isKeyFrame {
			syncSamples = append(syncSamples, uint32(i+1))
		}
	}
	if len(syncSamples) > 0 {
		biSS, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stss")})
		if err != nil {
			return err
		}
		stss := &mp4.Stss{EntryCount: uint32(len(syncSamples)), SampleNumber: syncSamples}
		if _, err := mp4.Marshal(w, stss, mp4.Context{}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
		_ = biSS
	}

	// stco or co64 — single chunk at chunkOffset.
	if uint64(chunkOffset) > math.MaxUint32 {
		bi9, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("co64")})
		if err != nil {
			return err
		}
		co64 := &mp4.Co64{
			EntryCount:  0,
			ChunkOffset: nil,
		}
		if n > 0 {
			co64.EntryCount = 1
			co64.ChunkOffset = []uint64{uint64(chunkOffset)}
		}
		if _, err := mp4.Marshal(w, co64, mp4.Context{}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
		_ = bi9
	} else {
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
	}

	_, err = w.EndBox()
	_ = bi
	return err
}

// writeMergeAudioSampleEntry writes mp4a+esds (AAC) or ulaw/alaw (G.711) sample entry.
func writeMergeAudioSampleEntry(w *mp4.Writer, tr *mergeTrack) error {
	switch tr.audioCodec {
	case "g711":
		return writeMergeG711SampleEntry(w, tr)
	case "opus":
		return writeMergeOpusSampleEntry(w, tr)
	default:
		return writeMergeAACSampleEntry(w, tr)
	}
}

// writeMergeAACSampleEntry writes mp4a + esds boxes for AAC audio.
func writeMergeAACSampleEntry(w *mp4.Writer, tr *mergeTrack) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mp4a")})
	if err != nil {
		return err
	}

	// Parse AudioSpecificConfig for channel count and sample rate.
	channelCount, sampleRate := parseAudioConfig(tr.audioConfig)

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
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("esds")})
	if err != nil {
		return err
	}
	esds := buildMergeEsds(tr.audioConfig)
	if _, err := mp4.Marshal(w, esds, mp4.Context{}); err != nil {
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

// writeMergeG711SampleEntry writes ulaw (μ-law) or alaw (A-law) sample entry for G.711 audio.
// G.711 is raw PCM — no esds or codec config boxes needed.
// Written as raw bytes since go-mp4 only registers AudioSampleEntry for mp4a/enca.
func writeMergeG711SampleEntry(w *mp4.Writer, tr *mergeTrack) error {
	boxType := mp4.StrToBoxType("ulaw")
	if !tr.g711MULaw {
		boxType = mp4.StrToBoxType("alaw")
	}

	bi, err := w.StartBox(&mp4.BoxInfo{Type: boxType})
	if err != nil {
		return err
	}

	// G.711: mono, 8-bit samples, 8000 Hz.
	sampleRate := uint32(8000)

	// Write AudioSampleEntry fields manually (same layout as mp4a without esds):
	// reserved[6] + data_reference_index[2] + entry_version[2] + reserved[6] +
	// channel_count[2] + sample_size[2] + pre_defined[2] + reserved[2] + sample_rate[4]
	buf := make([]byte, 28)
	buf[7] = 0x01  // data_reference_index = 1
	buf[17] = 0x01 // channel_count = 1 (mono)
	buf[19] = 0x08 // sample_size = 8
	rateFixed := sampleRate << 16
	buf[24] = byte(rateFixed >> 24)
	buf[25] = byte(rateFixed >> 16)
	buf[26] = byte(rateFixed >> 8)
	buf[27] = byte(rateFixed)

	if _, err := w.Write(buf); err != nil {
		return err
	}

	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi
	return nil
}

// writeMergeOpusSampleEntry writes Opus sample entry + dOps box for Opus audio.
func writeMergeOpusSampleEntry(w *mp4.Writer, tr *mergeTrack) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeOpus()})
	if err != nil {
		return err
	}

	chans := uint16(1) // mono default for camera audio

	opus := &mp4.AudioSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeOpus()},
			DataReferenceIndex: 1,
		},
		EntryVersion: 0,
		ChannelCount: chans,
		SampleSize:   16,
		SampleRate:   48000 << 16,
	}
	if _, err := mp4.Marshal(w, opus, mp4.Context{}); err != nil {
		return err
	}

	// dOps box
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDOps()})
	if err != nil {
		return err
	}
	dOps := &mp4.DOps{
		Version:              0,
		OutputChannelCount:   uint8(chans),
		PreSkip:              0,
		InputSampleRate:      48000,
		OutputGain:           0,
		ChannelMappingFamily: 0,
	}
	if _, err := mp4.Marshal(w, dOps, mp4.Context{}); err != nil {
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

// parseAudioConfig extracts channel count and sample rate from an AAC AudioSpecificConfig.
func parseAudioConfig(config []byte) (uint16, uint32) {
	channelCount := uint16(2)
	sampleRate := uint32(44100)

	if len(config) >= 2 {
		sampleRateIndex := (config[0] >> 3) & 0x0F
		if sampleRateIndex == 0x0F && len(config) >= 4 {
			sampleRate = uint32(config[1])<<16 | uint32(config[2])<<8 | uint32(config[3]&0xFC)<<4>>4
		} else if sampleRateIndex < 15 {
			sampleRates := [...]uint32{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}
			if int(sampleRateIndex) < len(sampleRates) {
				sampleRate = sampleRates[sampleRateIndex]
			}
		}
		channelConfig := ((config[0] & 0x01) << 2) | ((config[1] >> 6) & 0x03)
		if channelConfig > 0 {
			channelCount = uint16(channelConfig)
		}
	}

	return channelCount, sampleRate
}

// buildMergeEsds constructs an esds (Elementary Stream Descriptor) box for AAC.
// Same structure as internal/muxer buildEsds.
func buildMergeEsds(audioConfig []byte) *mp4.Esds {
	return &mp4.Esds{
		FullBox: mp4.FullBox{Version: 0},
		Descriptors: []mp4.Descriptor{
			{
				Tag:  mp4.ESDescrTag,
				Size: uint32(25 + len(audioConfig)),
				ESDescriptor: &mp4.ESDescriptor{
					ESID:           1,
					StreamPriority: 0,
				},
				DecoderConfigDescriptor: nil,
			},
			{
				Tag:  mp4.DecoderConfigDescrTag,
				Size: uint32(13 + len(audioConfig)),
				DecoderConfigDescriptor: &mp4.DecoderConfigDescriptor{
					ObjectTypeIndication: 0x40, // Audio ISO/IEC 14496-3 (AAC)
					StreamType:           0x05, // AudioStream
					UpStream:             false,
					Reserved:             true,
					BufferSizeDB:         0,
					MaxBitrate:           128000,
					AvgBitrate:           128000,
				},
			},
			{
				Tag:  mp4.DecSpecificInfoTag,
				Size: uint32(len(audioConfig)),
				Data: audioConfig,
			},
			{
				Tag:  mp4.SLConfigDescrTag,
				Size: 1,
				Data: []byte{0x02}, // predefined: use timestamps
			},
		},
	}
}

// writeMergeH264SampleEntry writes avc1 + avcC boxes for H.264.
func writeMergeH264SampleEntry(w *mp4.Writer, tr *mergeTrack) error {
	// Guard against empty/truncated SPS/PPS — these can occur in corrupted
	// segments (camera disconnect mid-IDR, incomplete writes). Without this
	// guard, accessing sps[1] panics and kills the entire process.
	if len(tr.sps) < 4 {
		return fmt.Errorf("h264 sample entry: SPS too short (%d bytes, need >=4)", len(tr.sps))
	}
	if len(tr.pps) < 1 {
		return fmt.Errorf("h264 sample entry: PPS too short (%d bytes, need >=1)", len(tr.pps))
	}

	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("avc1")})
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
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("avcC")})
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
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hvcC")})
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
	_ = bi2
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi
	return nil
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
