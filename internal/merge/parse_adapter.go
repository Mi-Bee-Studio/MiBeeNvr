package merge

import "github.com/Mi-Bee-Studio/MiBeeNvr/internal/mediaprobe"

// Aliases keep merge-internal explicit type references compiling.
type SegmentInfo = mediaprobe.SegmentInfo

type SampleEntry = mediaprobe.SampleEntry

// The MP4 sample-table parser and SPS resolvers live in mediaprobe (the
// ffprobe-free metadata layer) — merge keeps these thin aliases so internal
// call sites and the exported surface stay untouched while mediaprobe stops
// depending on merge (#877 batch 2b).
func ParseSegment(path string) (*mediaprobe.SegmentInfo, error) {
	return mediaprobe.ParseSegment(path)
}

func ParseSegmentNoProbe(path string) (*mediaprobe.SegmentInfo, error) {
	return mediaprobe.ParseSegmentNoProbe(path)
}

func ParseSegmentDurationOnly(path string) (float64, error) {
	return mediaprobe.ParseSegmentDurationOnly(path)
}

func SPSResolution(codec string, sps []byte) (width, height int, err error) {
	return mediaprobe.SPSResolution(codec, sps)
}

func ParseSPSResolution(sps []byte) (int, int, error) {
	return mediaprobe.ParseSPSResolution(sps)
}

func ParseHEVCSPSResolution(sps []byte) (int, int, error) {
	return mediaprobe.ParseHEVCSPSResolution(sps)
}

// ValidateMergedMP4 按浏览器（Chromium 一族）严格口径校验合并产物结构。
// 历次合并回归（#485/#497/#853-tkhd）的共同漏洞是只用 ffprobe/解码器
// 验证产物——宽容解析器掩盖了浏览器拒播的结构缺陷。测试与 repair 共用。
func ValidateMergedMP4(path string) error {
	return mediaprobe.ValidateMergedMP4(path)
}

// ProbeVideoTrack 读取首个视频轨的浏览器相关字段（tkhd 尺寸/编解码配置/
// 采样表计数）。
func ProbeVideoTrack(path string) (*mediaprobe.VideoTrackInfo, error) {
	return mediaprobe.ProbeVideoTrack(path)
}
