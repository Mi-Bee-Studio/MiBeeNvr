package merge

import (
	"encoding/binary"
	"fmt"
	"os"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mediaprobe"
)

// RepairZeroDimensions 就地修补视频轨尺寸元数据写 0 的合并产物，覆盖
// 两处：stsd 视频样本条目的 u16 宽高（Chromium 构建流配置的真正依据）
// 与 tkhd 的 16.16 定点宽高（显示层元数据）。
//
// 背景（2026-09-24 现场回归，两轮定位）：#853 追加桶模板把两处宽高都
// 写 0——Chromium 一族报 no supported streams 整文件拒播，而 ffprobe/
// VLC 解码时从 SPS 重推尺寸照常可播（下载可播掩盖网页不可播）。第一轮
// 修复只补 tkhd，被同一掩盖骗过；样本条目才是浏览器读的字段。尺寸从
// 文件自身 avcC/hvcC 携带的 SPS 无损还原，各改 4/8 字节元数据，不动
// 任何采样数据，幂等（非零字段不再改写）。
//
// 返回 tkhdPatched/sePatched 表示对应位置被改写；width/height 为补入尺寸。
func RepairZeroDimensions(path string) (tkhdPatched, sePatched bool, width, height uint16, err error) {
	vt, perr := mediaprobe.ProbeVideoTrack(path)
	if perr != nil {
		return false, false, 0, 0, fmt.Errorf("probe: %w", perr)
	}
	tkhdZero := vt.Width == 0 || vt.Height == 0
	seZero := vt.SampleEntryWidth == 0 || vt.SampleEntryHeight == 0
	if !tkhdZero && !seZero {
		return false, false, vt.Width, vt.Height, nil
	}

	var w, h int
	switch vt.Codec {
	case "h264":
		w, h, err = mediaprobe.ParseSPSResolution(vt.SPS)
	case "h265":
		w, h, err = mediaprobe.ParseHEVCSPSResolution(vt.SPS)
	default:
		return false, false, 0, 0, fmt.Errorf("no codec config (avcC/hvcC) to recover dimensions from")
	}
	if err != nil {
		return false, false, 0, 0, fmt.Errorf("parse SPS resolution: %w", err)
	}
	if w <= 0 || w > 65535 || h <= 0 || h > 65535 {
		return false, false, 0, 0, fmt.Errorf("unreasonable SPS resolution %d×%d", w, h)
	}

	f, oerr := os.OpenFile(path, os.O_RDWR, 0o644)
	if oerr != nil {
		return false, false, 0, 0, fmt.Errorf("open for write: %w", oerr)
	}
	defer f.Close()

	if seZero {
		if vt.SampleEntryDimsOffset <= 0 {
			return false, false, 0, 0, fmt.Errorf("stsd sample entry not located")
		}
		var se [4]byte
		binary.BigEndian.PutUint16(se[0:2], uint16(w))
		binary.BigEndian.PutUint16(se[2:4], uint16(h))
		if _, werr := f.WriteAt(se[:], vt.SampleEntryDimsOffset); werr != nil {
			return false, false, 0, 0, fmt.Errorf("write sample entry dims: %w", werr)
		}
		sePatched = true
	}
	if tkhdZero {
		if vt.TkhdDimsOffset <= 0 {
			return false, false, 0, 0, fmt.Errorf("tkhd box not located")
		}
		var dims [8]byte
		binary.BigEndian.PutUint32(dims[0:4], uint32(w)<<16)
		binary.BigEndian.PutUint32(dims[4:8], uint32(h)<<16)
		if _, werr := f.WriteAt(dims[:], vt.TkhdDimsOffset); werr != nil {
			return false, false, 0, 0, fmt.Errorf("write tkhd dims: %w", werr)
		}
		tkhdPatched = true
	}
	if cerr := f.Close(); cerr != nil {
		return false, false, 0, 0, fmt.Errorf("close: %w", cerr)
	}
	return tkhdPatched, sePatched, uint16(w), uint16(h), nil
}
