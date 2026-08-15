package vod

import (
	"errors"
	"fmt"

	"github.com/abema/go-mp4"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mp4util"
)

var errShortParamSets = errors.New("SPS/PPS too short to build sample entry")

// BuildInitSegment produces the fMP4 initialization segment (ftyp + moov) for
// one recording: the track configurations with EMPTY sample tables. The
// sample tables live in each fragment's moof instead. Shared stsd-writing
// shape with internal/merge's moov builder (avc1/hvc1/mp4a/Opus entries).
func BuildInitSegment(info *merge.SegmentInfo, includeAudio bool) ([]byte, error) {
	if info.Timescale == 0 {
		return nil, fmt.Errorf("segment has no timescale")
	}

	width, height := 0, 0
	var err error
	switch info.Codec {
	case "h265":
		width, height, err = merge.SPSResolution("h265", info.SPS)
	case "h264":
		width, height, err = merge.SPSResolution("h264", info.SPS)
	default:
		return nil, fmt.Errorf("unsupported codec %q for init segment", info.Codec)
	}
	if err != nil {
		// Resolution is cosmetic (tkhd/stsd); tolerate a parse failure.
		width, height = 0, 0
	}

	includeAudio = includeAudio && info.HasAudio

	buf := &seekableBuffer{}
	w := mp4.NewWriter(buf)

	if err := writeInitFtyp(w, info.Codec); err != nil {
		return nil, err
	}

	// moov
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("moov")}); err != nil {
		return nil, err
	}
	nextTrack := uint32(2)
	if includeAudio {
		nextTrack = 3
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mvhd")}); err != nil {
		return nil, err
	}
	mvhd := &mp4.Mvhd{
		Timescale:   1000,
		DurationV0:  0,
		Rate:        0x00010000,
		Volume:      0x0100,
		NextTrackID: nextTrack,
		Matrix:      identityMatrix,
	}
	if _, err := mp4.Marshal(w, mvhd, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}

	if err := writeInitTrak(w, initTrack{
		trackID:   1,
		timescale: info.Timescale,
		width:     uint16(width),
		height:    uint16(height),
		video:     true,
		codec:     info.Codec,
		sps:       info.SPS,
		pps:       info.PPS,
		vps:       info.VPS,
	}); err != nil {
		return nil, err
	}

	if includeAudio {
		if err := writeInitTrak(w, initTrack{
			trackID:      2,
			timescale:    info.AudioTimescale,
			audio:        true,
			audioCodec:   info.AudioCodec,
			audioConfig:  info.AudioConfig,
			g711MULaw:    info.G711MULaw,
		}); err != nil {
			return nil, err
		}
	}

	// mvex > trex (per-track sample defaults; trun carries everything, these
	// are the spec-mandated fallbacks).
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mvex")}); err != nil {
		return nil, err
	}
	if err := writeTrex(w, 1); err != nil {
		return nil, err
	}
	if includeAudio {
		if err := writeTrex(w, 2); err != nil {
			return nil, err
		}
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}

	if _, err := w.EndBox(); err != nil { // moov
		return nil, err
	}
	return buf.Bytes(), nil
}

var identityMatrix = [9]int32{
	0x00010000, 0, 0,
	0, 0x00010000, 0,
	0, 0, 0x40000000,
}

type initTrack struct {
	trackID   uint32
	timescale uint32
	width     uint16
	height    uint16
	video     bool
	codec     string
	sps, pps  []byte
	vps       []byte
	audio       bool
	audioCodec  string
	audioConfig []byte
	g711MULaw   bool
}

func writeTrex(w *mp4.Writer, trackID uint32) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("trex")}); err != nil {
		return err
	}
	trex := &mp4.Trex{TrackID: trackID, DefaultSampleDescriptionIndex: 1}
	if _, err := mp4.Marshal(w, trex, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeInitTrak(w *mp4.Writer, tr initTrack) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("trak")}); err != nil {
		return err
	}
	// tkhd
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("tkhd")}); err != nil {
		return err
	}
	tkhd := &mp4.Tkhd{TrackID: tr.trackID, DurationV0: 0, Matrix: identityMatrix}
	if tr.video {
		tkhd.Width = uint32(tr.width) << 16
		tkhd.Height = uint32(tr.height) << 16
	}
	if _, err := mp4.Marshal(w, tkhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	// mdia
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mdia")}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mdhd")}); err != nil {
		return err
	}
	mdhd := &mp4.Mdhd{Timescale: tr.timescale, Language: [3]byte{0x15, 0xC0, 0x00}} // 'und'
	if _, err := mp4.Marshal(w, mdhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hdlr")}); err != nil {
		return err
	}
	hdlr := &mp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: "VideoHandler\x00"}
	if tr.audio {
		hdlr = &mp4.Hdlr{HandlerType: [4]byte{'s', 'o', 'u', 'n'}, Name: "SoundHandler\x00"}
	}
	if _, err := mp4.Marshal(w, hdlr, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	// minf
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("minf")}); err != nil {
		return err
	}
	mediaHeader := "vmhd"
	if tr.audio {
		mediaHeader = "smhd"
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType(mediaHeader)}); err != nil {
		return err
	}
	if tr.audio {
		if _, err := mp4.Marshal(w, &mp4.Smhd{}, mp4.Context{}); err != nil {
			return err
		}
	} else {
		if _, err := mp4.Marshal(w, &mp4.Vmhd{Graphicsmode: 0}, mp4.Context{}); err != nil {
			return err
		}
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if err := writeDinf(w); err != nil {
		return err
	}
	if err := writeEmptyStbl(w, tr); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // minf
		return err
	}
	if _, err := w.EndBox(); err != nil { // mdia
		return err
	}
	_, err := w.EndBox() // trak
	return err
}

func writeDinf(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("dinf")}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("dref")}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Dref{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("url ")}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Url{Location: ""}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

// writeEmptyStbl writes stsd (the track's codec configuration) followed by
// EMPTY stts/stsc/stsz/stco tables — sample timing/offsets live in the moof
// fragments instead.
func writeEmptyStbl(w *mp4.Writer, tr initTrack) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stbl")}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsd")}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	var err error
	if tr.audio {
		err = writeAudioSampleEntry(w, tr)
	} else if tr.codec == "h265" {
		err = writeH265SampleEntry(w, tr)
	} else {
		err = writeH264SampleEntry(w, tr)
	}
	if err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // stsd
		return err
	}
	// Empty tables.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stts")}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stts{EntryCount: 0}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsc")}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsc{EntryCount: 0}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsz")}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsz{SampleSize: 0, SampleCount: 0}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stco")}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stco{EntryCount: 0}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_, err = w.EndBox() // stbl
	return err
}

func writeH264SampleEntry(w *mp4.Writer, tr initTrack) error {
	if len(tr.sps) < 4 || len(tr.pps) < 1 {
		return errShortParamSets
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("avc1")}); err != nil {
		return err
	}
	avc1 := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType("avc1")},
			DataReferenceIndex: 1,
		},
		Width:           tr.width,
		Height:          tr.height,
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
	}
	if _, err := mp4.Marshal(w, avc1, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("avcC")}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, mp4util.BuildAvcC(tr.sps, tr.pps), mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeH265SampleEntry(w *mp4.Writer, tr initTrack) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hvc1")}); err != nil {
		return err
	}
	hvc1 := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType("hvc1")},
			DataReferenceIndex: 1,
		},
		Width:           tr.width,
		Height:          tr.height,
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
	}
	if _, err := mp4.Marshal(w, hvc1, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hvcC")}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, mp4util.BuildHvcC(tr.vps, tr.sps, tr.pps), mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeAudioSampleEntry(w *mp4.Writer, tr initTrack) error {
	switch tr.audioCodec {
	case "g711":
		return writeG711SampleEntry(w, tr.g711MULaw)
	case "opus":
		return writeOpusSampleEntry(w)
	default: // "" = AAC (legacy default)
		return writeAACSampleEntry(w, tr.audioConfig, tr.timescale)
	}
}

func writeAACSampleEntry(w *mp4.Writer, audioConfig []byte, timescale uint32) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mp4a")}); err != nil {
		return err
	}
	channelCount, sampleRate := parseAACAudioConfig(audioConfig)
	if sampleRate == 0 {
		sampleRate = timescale
	}
	mp4a := &mp4.AudioSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType("mp4a")},
			DataReferenceIndex: 1,
		},
		ChannelCount: channelCount,
		SampleSize:   16,
		SampleRate:   sampleRate << 16,
	}
	if _, err := mp4.Marshal(w, mp4a, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("esds")}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, buildEsds(audioConfig), mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeG711SampleEntry(w *mp4.Writer, mulaw bool) error {
	boxType := mp4.StrToBoxType("alaw")
	if mulaw {
		boxType = mp4.StrToBoxType("ulaw")
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: boxType}); err != nil {
		return err
	}
	buf := make([]byte, 28)
	buf[7] = 0x01  // data_reference_index
	buf[17] = 0x01 // channel_count = 1
	buf[19] = 0x08 // sample_size = 8
	rate := uint32(8000) << 16
	buf[24], buf[25], buf[26], buf[27] = byte(rate>>24), byte(rate>>16), byte(rate>>8), byte(rate)
	if _, err := w.Write(buf); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeOpusSampleEntry(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeOpus()}); err != nil {
		return err
	}
	opus := &mp4.AudioSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeOpus()},
			DataReferenceIndex: 1,
		},
		ChannelCount: 1,
		SampleSize:   16,
		SampleRate:   48000 << 16,
	}
	if _, err := mp4.Marshal(w, opus, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDOps()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.DOps{OutputChannelCount: 1, InputSampleRate: 48000}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeInitFtyp(w *mp4.Writer, codec string) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("ftyp")}); err != nil {
		return err
	}
	ftyp := &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 0,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'i', 's', 'o', '2'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
		},
	}
	if codec == "h265" {
		ftyp.CompatibleBrands = append(ftyp.CompatibleBrands, mp4.CompatibleBrandElem{CompatibleBrand: [4]byte{'h', 'e', 'v', '1'}})
	}
	if _, err := mp4.Marshal(w, ftyp, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

// parseAACAudioConfig extracts channel count + sample rate from an AAC
// AudioSpecificConfig (mirrors internal/merge's parseAudioConfig).
func parseAACAudioConfig(config []byte) (uint16, uint32) {
	channelCount := uint16(2)
	sampleRate := uint32(44100)
	if len(config) >= 2 {
		sampleRateIndex := (config[0] >> 3) & 0x0F
		if sampleRateIndex < 15 {
			rates := [...]uint32{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}
			if int(sampleRateIndex) < len(rates) {
				sampleRate = rates[sampleRateIndex]
			}
		}
		channelConfig := ((config[0] & 0x01) << 2) | ((config[1] >> 6) & 0x03)
		if channelConfig > 0 {
			channelCount = uint16(channelConfig)
		}
	}
	return channelCount, sampleRate
}

// buildEsds mirrors internal/merge's buildMergeEsds (AAC esds structure).
func buildEsds(audioConfig []byte) *mp4.Esds {
	return &mp4.Esds{
		FullBox: mp4.FullBox{Version: 0},
		Descriptors: []mp4.Descriptor{
			{Tag: mp4.ESDescrTag, Size: uint32(25 + len(audioConfig)), ESDescriptor: &mp4.ESDescriptor{ESID: 1}},
			{Tag: mp4.DecoderConfigDescrTag, Size: uint32(13 + len(audioConfig)), DecoderConfigDescriptor: &mp4.DecoderConfigDescriptor{
				ObjectTypeIndication: 0x40, StreamType: 0x05, Reserved: true,
				MaxBitrate: 128000, AvgBitrate: 128000,
			}},
			{Tag: mp4.DecSpecificInfoTag, Size: uint32(len(audioConfig)), Data: audioConfig},
			{Tag: mp4.SLConfigDescrTag, Size: 1, Data: []byte{0x02}},
		},
	}
}
