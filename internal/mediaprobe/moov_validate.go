package mediaprobe

import (
	"fmt"
	"os"

	"github.com/abema/go-mp4"
)

// Chromium-grade structural validation for merged MP4 outputs.
//
// 背景（2026-09-24 现场回归，#853 追加桶，两轮定位）：moov 模板把 stsd
// 视频样本条目与 tkhd 的宽高都写 0。Chromium 一族（FFmpegDemuxer）构建
// 视频流配置读的是 stsd 样本条目尺寸 → "no supported streams" 整文件
// 拒播；ffprobe/VLC 解码时从 SPS 重推尺寸照常可播，掩盖了问题（第一轮
// 修复只补 tkhd 即被该掩盖骗过）。历次合并层回归（#485/#497 采样表、
// #853 尺寸元数据）的共同教训：产物校验不能只靠 ffprobe/解码器兜底——
// 宽容解析器会掩盖浏览器不可播的结构缺陷。本校验器按浏览器严格口径核查
// 合并产物的结构不变量，供合并测试（硬门）与 repair 使用。

// VideoTrackInfo is the browser-relevant summary of the first video track.
type VideoTrackInfo struct {
	Width, Height uint16 // tkhd dims (16.16 >> 16)
	Codec         string // "h264" / "h265" ("" = no codec config found)
	SPS, PPS, VPS []byte
	SampleCount   uint32
	ChunkCount    int
	// TkhdDimsOffset is the absolute file offset of the tkhd width field
	// (tkhd box end - 8). In-place repair rewrites the 8 width/height bytes
	// there. -1 = tkhd not found.
	TkhdDimsOffset int64
	// SampleEntryWidth/Height are the stsd visual sample entry dims — the
	// field Chromium's demuxer actually builds the stream config from.
	SampleEntryWidth, SampleEntryHeight uint16
	// SampleEntryDimsOffset is the absolute file offset of the sample entry
	// width u16 (box start + 8 hdr + 24 fixed fields). Repair rewrites the
	// 4 width/height bytes there. -1 = sample entry not found.
	SampleEntryDimsOffset int64
}

type validateAccum struct {
	handlerType [4]byte
	width       uint16
	height      uint16
	tkhdOff     int64
	seWidth     uint16
	seHeight    uint16
	seOff       int64
	codec       string
	sps, pps    []byte
	vps         []byte
	sttsTotal   uint32
	sampleCount uint32
	stscEntries []mp4.StscEntry
	chunkCount  uint32
	offsets     []uint64 // stco (zero-extended) + co64
	stssMax     uint32
	hasStss     bool
}

// ProbeVideoTrack walks the first video trak and returns its browser-relevant
// fields. Errors on structural unreadability, not on policy violations —
// use ValidateMergedMP4 for the verdict.
func ProbeVideoTrack(path string) (*VideoTrackInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	acc, _, err := walkForValidation(f, false)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return nil, fmt.Errorf("no video track (hdlr vide) found")
	}
	return &VideoTrackInfo{
		Width:                 acc.width,
		Height:                acc.height,
		Codec:                 acc.codec,
		SPS:                   acc.sps,
		PPS:                   acc.pps,
		VPS:                   acc.vps,
		SampleCount:           acc.sampleCount,
		ChunkCount:            int(acc.chunkCount),
		TkhdDimsOffset:        acc.tkhdOff,
		SampleEntryWidth:      acc.seWidth,
		SampleEntryHeight:     acc.seHeight,
		SampleEntryDimsOffset: acc.seOff,
	}, nil
}

// ValidateMergedMP4 returns nil when the file satisfies the invariants a
// Chromium-grade demuxer needs; otherwise an error naming the violation.
func ValidateMergedMP4(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	acc, mdat, err := walkForValidation(f, true)
	if err != nil {
		return err
	}
	if acc == nil {
		return fmt.Errorf("no video track (hdlr vide) found")
	}
	if acc.codec != "h264" && acc.codec != "h265" {
		return fmt.Errorf("video track has no codec config (stsd avcC/hvcC missing or empty)")
	}
	if len(acc.sps) == 0 {
		return fmt.Errorf("codec config carries no SPS")
	}
	if acc.width == 0 || acc.height == 0 {
		return fmt.Errorf("tkhd dimensions are 0×0 (display metadata; got %d×%d)", acc.width, acc.height)
	}
	if acc.seWidth == 0 || acc.seHeight == 0 {
		return fmt.Errorf("stsd sample entry dimensions are 0×0 — Chromium builds the stream config from the sample entry (no supported streams; got %d×%d)", acc.seWidth, acc.seHeight)
	}
	if acc.sampleCount == 0 {
		return fmt.Errorf("stsz sample count is 0")
	}
	if acc.sttsTotal != acc.sampleCount {
		return fmt.Errorf("stts total sample count %d != stsz count %d", acc.sttsTotal, acc.sampleCount)
	}
	if acc.chunkCount == 0 || len(acc.offsets) != int(acc.chunkCount) {
		return fmt.Errorf("chunk table mismatch: stco/co64 entries %d, derived chunk count %d", len(acc.offsets), acc.chunkCount)
	}
	if len(acc.stscEntries) == 0 {
		return fmt.Errorf("stsc is empty")
	}
	// stsc runs must not reference chunks beyond stco's table.
	lastRun := acc.stscEntries[len(acc.stscEntries)-1]
	if lastRun.FirstChunk > acc.chunkCount {
		return fmt.Errorf("stsc references chunk %d but only %d chunks exist", lastRun.FirstChunk, acc.chunkCount)
	}
	if mdat != nil {
		dataStart := mdat.offset + 8
		dataEnd := mdat.offset + mdat.size
		for _, o := range acc.offsets {
			if o < uint64(dataStart) || o >= uint64(dataEnd) {
				return fmt.Errorf("chunk offset %d outside mdat [%d,%d)", o, dataStart, dataEnd)
			}
		}
	}
	if acc.hasStss && acc.stssMax > acc.sampleCount {
		return fmt.Errorf("stss references sample %d > sample count %d", acc.stssMax, acc.sampleCount)
	}
	return nil
}

type mdatRange struct {
	offset int64
	size   int64
}

// full=true 读取完整采样表（ValidateMergedMP4 用）；
// full=false 只读 hdlr/tkhd/编解码配置（repair 检测路径——大表
// （stts 可达 1MB+）跳过，万级文件扫描不为 USB HDD 制造压力）。
func walkForValidation(f *os.File, full bool) (*validateAccum, *mdatRange, error) {
	var (
		tracks   []*validateAccum
		current  *validateAccum
		videoAcc *validateAccum
		mdat     *mdatRange
	)
	_, err := mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		boxType := h.BoxInfo.Type.String()
		if boxType == "mdat" {
			if len(h.Path) == 1 {
				mdat = &mdatRange{offset: int64(h.BoxInfo.Offset), size: int64(h.BoxInfo.Size)}
			}
			return nil, nil
		}
		if boxType == "trak" {
			current = &validateAccum{tkhdOff: -1, seOff: -1}
			tracks = append(tracks, current)
			return h.Expand()
		}
		if current == nil {
			if h.BoxInfo.IsSupportedType() {
				return h.Expand()
			}
			return nil, nil
		}
		if !h.BoxInfo.IsSupportedType() {
			return nil, nil
		}
		if !full {
			switch h.BoxInfo.Type.String() {
			case "stts", "stsc", "stss", "stco", "co64", "stsz":
				return nil, nil // 大表：轻量模式不读
			}
		}
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, err
		}
		switch b := box.(type) {
		case *mp4.Hdlr:
			current.handlerType = b.HandlerType
		case *mp4.Tkhd:
			current.width = uint16(b.Width >> 16)
			current.height = uint16(b.Height >> 16)
			current.tkhdOff = int64(h.BoxInfo.Offset) + int64(h.BoxInfo.Size) - 8
		case *mp4.VisualSampleEntry:
			current.seWidth = b.Width
			current.seHeight = b.Height
			// 宽度 u16 位于盒起始 + 8(头) + 24(6 保留 + 2 DRI + 2 pre + 2 res + 12 pre3)。
			current.seOff = int64(h.BoxInfo.Offset) + 8 + 24
		case *mp4.Stts:
			for _, e := range b.Entries {
				current.sttsTotal += e.SampleCount
			}
		case *mp4.Stsz:
			current.sampleCount = b.SampleCount
		case *mp4.Stsc:
			current.stscEntries = b.Entries
		case *mp4.Stco:
			current.chunkCount = uint32(len(b.ChunkOffset))
			for _, o := range b.ChunkOffset {
				current.offsets = append(current.offsets, uint64(o))
			}
		case *mp4.Co64:
			current.chunkCount = uint32(len(b.ChunkOffset))
			current.offsets = append(current.offsets, b.ChunkOffset...)
		case *mp4.Stss:
			current.hasStss = true
			for _, s := range b.SampleNumber {
				if s > current.stssMax {
					current.stssMax = s
				}
			}
		case *mp4.AVCDecoderConfiguration:
			current.codec = "h264"
			if len(b.SequenceParameterSets) > 0 {
				current.sps = b.SequenceParameterSets[0].NALUnit
			}
			if len(b.PictureParameterSets) > 0 {
				current.pps = b.PictureParameterSets[0].NALUnit
			}
		case *mp4.HvcC:
			current.codec = "h265"
			for _, arr := range b.NaluArrays {
				if len(arr.Nalus) == 0 {
					continue
				}
				switch arr.NaluType {
				case 32:
					current.vps = arr.Nalus[0].NALUnit
				case 33:
					current.sps = arr.Nalus[0].NALUnit
				case 34:
					current.pps = arr.Nalus[0].NALUnit
				}
			}
		}
		// 与 parser.go 同规则：叶子采样表/编解码配置盒不 Expand——
		// #853 追加桶在这些盒里预留容量槽位，零填充会被当幻影盒
		// （size 0 = 到 EOF）中断整个遍历。
		switch box.(type) {
		case *mp4.Stts, *mp4.Stsc, *mp4.Stss, *mp4.Stco, *mp4.Co64,
			*mp4.Stsz, *mp4.AVCDecoderConfiguration, *mp4.HvcC, *mp4.Esds:
			return nil, nil
		}
		return h.Expand()
	})
	if err != nil {
		return nil, nil, fmt.Errorf("walk boxes: %w", err)
	}
	for _, t := range tracks {
		if t.handlerType == [4]byte{'v', 'i', 'd', 'e'} {
			videoAcc = t
			break
		}
	}
	return videoAcc, mdat, nil
}
