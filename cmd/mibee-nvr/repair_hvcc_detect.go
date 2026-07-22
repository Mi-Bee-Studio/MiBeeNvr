package main

// repair_hvcc_detect.go — detectBuggyHvcC helper for `repair remerge-h265`.
//
// Reads an MP4's hvcC box (HEVCDecoderConfigurationRecord, ISO 14496-15) and
// reports whether it carries the production bug fixed in PR #92:
// GeneralTierFlag=true paired with GeneralProfileIdc=1 (Main profile). Main
// profile cannot legally use High tier, and Windows Edge's HEVC Video
// Extension rejects such files with
// PipelineStatus::DEMUXER_ERROR_NO_SUPPORTED_STREAMS.
//
// Uses abema/go-mp4's ReadBoxStructure — no ffprobe, no shell-out.

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"
)

// detectBuggyHvcC walks the MP4 box tree at mp4Path and inspects the first
// hvcC box found.
//
// Returns:
//   - isBuggy: true iff the file is H.265 with GeneralTierFlag=true AND
//     GeneralProfileIdc=1 (the self-inconsistent combo Edge rejects).
//   - codec: "h265" if an hvcC box was found, "h264" if an avcC box was
//     found instead, "" otherwise (no codec box at all).
//   - err: I/O or parse error.
//
// For non-HEVC files (H.264, MJPEG-in-MP4, plain text, etc.) isBuggy is
// always false and codec reflects what was found.
func detectBuggyHvcC(mp4Path string) (isBuggy bool, codec string, err error) {
	f, err := os.Open(mp4Path)
	if err != nil {
		return false, "", fmt.Errorf("open %s: %w", mp4Path, err)
	}
	defer f.Close()

	// ReadBoxStructure walks every box in the file, calling the callback for
	// each. h.Expand() recurses into container boxes (moov, trak, mdia, minf,
	// stbl, stsd, hvc1). We stop at the first hvcC OR avcC — we don't need
	// anything else.
	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		box, _, derr := h.ReadPayload()
		if derr != nil {
			// Some boxes (e.g. mdat) can't be read as structured payloads.
			// Skip them and keep walking.
			return h.Expand()
		}
		switch b := box.(type) {
		case *mp4.HvcC:
			codec = "h265"
			// PR #92 bug signature: tier=1 (High) paired with profile_idc=1
			// (Main). Main profile is only valid in Main tier; a High-tier
			// Main profile is non-conformant and Edge's HEVC extension
			// rejects it. Also treat a non-zero compat flag with profile_idc=1
			// as buggy — cameras that get tier wrong also tend to set stray
			// compat bits (observed: 0x40000000). Both are fixed by PR #92's
			// conservative hvcC defaults.
			if b.GeneralTierFlag && b.GeneralProfileIdc == 1 {
				isBuggy = true
			}
			return nil, io.EOF // stop walking
		case *mp4.AVCDecoderConfiguration:
			codec = "h264"
			return nil, io.EOF // stop walking
		}
		return h.Expand()
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return false, "", fmt.Errorf("read boxes from %s: %w", mp4Path, err)
	}
	return isBuggy, codec, nil
}
