package mediaprobe

import (
	"github.com/abema/go-mp4"
)

// MP4 audio sample-entry builders shared by internal/muxer and internal/merge
// (issue #1012 block 2).
//
// Both packages write the same audio sample entries — mp4a+esds (AAC),
// ulaw/alaw (G.711), Opus+dOps — and used to carry byte-identical copies
// ("Same structure as internal/muxer buildEsds", mp4merge.go). They cannot
// import each other: muxer's in-package tests import merge, so a
// merge→muxer edge would be a test-time import cycle. mediaprobe is the
// shared home — merge already imports it and muxer can too.
//
// The AAC mp4a shell is NOT shared: the two AudioSpecificConfig sample-rate
// parsers genuinely diverge (muxer reads 11 explicit-rate bits, merge reads a
// 22-bit variant) — unifying them would change one side's bytes. Each package
// keeps its own; only the esds child (byte-identical in both) lives here.

// BuildEsds constructs an esds (Elementary Stream Descriptor) box for AAC.
// Structure: ES_Descriptor > DecoderConfigDescriptor >
// DecspecificInfoTag(AudioSpecificConfig) + SLConfigDescriptor.
func BuildEsds(audioConfig []byte) *mp4.Esds {
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

// WriteG711SampleEntry writes ulaw (μ-law) or alaw (A-law) sample entry for
// G.711 audio. G.711 is raw PCM — no esds or codec config boxes needed.
// Written as raw bytes since go-mp4 only registers AudioSampleEntry for
// mp4a/enca. sampleRate 0 falls back to 8000 Hz.
func WriteG711SampleEntry(w *mp4.Writer, muLaw bool, sampleRate uint32) error {
	boxType := mp4.StrToBoxType("ulaw")
	if !muLaw {
		boxType = mp4.StrToBoxType("alaw")
	}

	bi, err := w.StartBox(&mp4.BoxInfo{Type: boxType})
	if err != nil {
		return err
	}

	if sampleRate == 0 {
		sampleRate = 8000
	}

	// Write AudioSampleEntry fields manually (same layout as mp4a without esds):
	// reserved[6] + data_reference_index[2] + entry_version[2] + reserved[6] +
	// channel_count[2] + sample_size[2] + pre_defined[2] + reserved[2] + sample_rate[4]
	buf := make([]byte, 28)
	// bytes 6-7: data_reference_index = 1
	buf[7] = 0x01
	// bytes 16-17: channel_count = 1
	buf[17] = 0x01
	// bytes 18-19: sample_size = 8
	buf[19] = 0x08
	// bytes 24-27: sample_rate = fixed-point 16.16
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

// WriteOpusSampleEntry writes Opus sample entry ("Opus" box + "dOps" child)
// for Opus audio. Uses go-mp4 native Opus box types (available since go-mp4
// v0.12.0). chans 0 falls back to mono; the timescale is always 48kHz per
// spec, preSkip/inputRate carry the stream's priming and input rate.
func WriteOpusSampleEntry(w *mp4.Writer, chans uint16, preSkip uint16, inputRate uint32) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeOpus()})
	if err != nil {
		return err
	}

	if chans == 0 {
		chans = 1 // mono default
	}

	opus := &mp4.AudioSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeOpus()},
			DataReferenceIndex: 1,
		},
		EntryVersion: 0,
		ChannelCount: chans,
		SampleSize:   16,
		SampleRate:   48000 << 16, // Opus always uses 48kHz timescale per spec
	}
	if _, err := mp4.Marshal(w, opus, mp4.Context{}); err != nil {
		return err
	}

	// dOps (OpusSpecificBox) — mandatory child
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDOps()})
	if err != nil {
		return err
	}
	dOps := &mp4.DOps{
		Version:              0,
		OutputChannelCount:   uint8(chans),
		PreSkip:              preSkip,
		InputSampleRate:      inputRate,
		OutputGain:           0,
		ChannelMappingFamily: 0, // simple mono/stereo
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
