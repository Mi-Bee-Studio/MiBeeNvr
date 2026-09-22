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
