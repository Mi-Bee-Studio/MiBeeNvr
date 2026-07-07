package transcoding

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
)

// BuildFFmpegCommand constructs the FFmpeg argument array for a transcode job.
// Returns the complete argument list ready for exec.Command.
func BuildFFmpegCommand(opts TranscodeOptions, caps HardwareCapabilities) ([]string, error) {
	if err := validateCodecCombination(opts.InputCodec, opts.OutputCodec); err != nil {
		return nil, err
	}

	// Normalize jpeg → mjpeg for FFmpeg command building
	inputCodec := opts.InputCodec
	if inputCodec == "jpeg" {
		inputCodec = "mjpeg"
	}

	var args []string

	// Input flags — MJPEG uses directory glob pattern with framerate
	if inputCodec == "mjpeg" {
		args = append(args, "-framerate", strconv.Itoa(opts.Framerate))
		args = append(args, "-i", filepath.Join(opts.InputPath, "%*.jpg"))
	} else {
		args = append(args, "-i", opts.InputPath)
	}

	// Video encoder selection
	videoArgs, err := buildVideoEncoderArgs(opts, caps)
	if err != nil {
		return nil, err
	}
	args = append(args, videoArgs...)

	// Audio: transcode to AAC (MP4-standard, universally compatible).
	// We cannot use -c:a copy because some source audio codecs are not
	// writable into MP4 by FFmpeg — notably G.711 (pcm_mulaw/alaw), which
	// NVR's own muxer writes as a non-standard "ulaw"/"alaw" sample entry
	// that FFmpeg rejects with "Could not find tag for codec pcm_mulaw in
	// stream #1, codec not currently supported in container". AAC transcoding
	// is cheap (8-48kHz mono) and avoids all container/codec-tag mismatches.
	// MJPEG input has no audio stream, so skip it.
	if inputCodec != "mjpeg" {
		args = append(args, "-c:a", "aac", "-b:a", "64k")
	}

	// Overwrite output without asking
	args = append(args, "-y", opts.OutputPath)

	return args, nil
}

// validateCodecCombination checks that the input→output codec pair is supported.
func validateCodecCombination(input, output string) error {
	validInput := map[string]bool{"h264": true, "h265": true, "mjpeg": true, "jpeg": true}
	validOutput := map[string]bool{"h264": true, "h265": true}

	if !validInput[input] {
		return fmt.Errorf("unsupported input codec: %s", input)
	}
	if !validOutput[output] {
		return fmt.Errorf("unsupported output codec: %s", output)
	}
	// All four combinations are supported:
	//   H264→H264, H264→H265, H265→H264, H265→H265, MJPEG→H264, MJPEG→H265.
	// MJPEG input forces software encoding (resolveEncoder) because v4l2m2m
	// hangs on MJPEG input — but libx264/libx265 software encoders handle
	// MJPEG→any-output fine (FFmpeg decodes JPEG internally, then encodes).
	return nil
}

// resolveEncoder returns the FFmpeg encoder name that will be used for the given options.
// Used for both command building and metric labeling.
func resolveEncoder(opts TranscodeOptions, caps HardwareCapabilities) string {
	useSoftware := opts.ForceSoftware || isMJPEGInput(opts.InputCodec)
	switch opts.OutputCodec {
	case "h264":
		if !useSoftware && caps.H264EncoderType != EncoderSoftware && caps.H264Encoder != "" {
			return caps.H264Encoder
		}
		return "libx264"
	case "h265":
		if !useSoftware && caps.H265EncoderType != EncoderSoftware && caps.H265Encoder != "" {
			return caps.H265Encoder
		}
		return "libx265"
	default:
		return "unknown"
	}
}

// resolveEffectiveCRF returns the effective CRF value (after defaults) or -1 for hardware encoders.
func resolveEffectiveCRF(opts TranscodeOptions, caps HardwareCapabilities) int {
	encoder := resolveEncoder(opts, caps)
	if !isSoftwareEncoder(encoder) {
		return -1 // hardware encoders don't use CRF
	}
	switch opts.OutputCodec {
	case "h264":
		if opts.CRF > 0 && opts.CRF <= 51 {
			return opts.CRF
		}
		return 23
	case "h265":
		if opts.CRF > 0 && opts.CRF <= 51 {
			return opts.CRF
		}
		return 28
	default:
		return 23
	}
}

// buildVideoEncoderArgs selects the encoder and returns its FFmpeg flags.
func buildVideoEncoderArgs(opts TranscodeOptions, caps HardwareCapabilities) ([]string, error) {
	var args []string

	encoder := resolveEncoder(opts, caps)
	forceSoftware := opts.ForceSoftware

	// MJPEG input forces software encoder — v4l2m2m hangs on MJPEG input.
	useSoftware := forceSoftware || isMJPEGInput(opts.InputCodec)
	_ = useSoftware // already used in resolveEncoder

	// Log a warning when using software encoding on ARM — it's slow but may be
	// the only option when v4l2m2m is listed but the device lacks encoding capability
	// (e.g. Amlogic S905X3 meson-video-decoder only does decode, not encode).
	if !forceSoftware && isARMArch(caps.Arch) && isSoftwareEncoder(encoder) && !isMJPEGInput(opts.InputCodec) {
		slog.Warn("using software encoder on ARM — transcoding will be slow; no working hardware encoder found", "encoder", encoder, "arch", caps.Arch)
	}

	args = append(args, "-c:v", encoder)

	// Encoder-specific flags
	switch {
	case strings.Contains(encoder, "v4l2m2m"):
		// V4L2 M2M requires explicit GOP and no B-frames
		args = append(args, "-g", "50", "-bf", "0")

		// V4L2 M2M requires yuv420p pixel format (MJPEG produces yuvj422p)
		if opts.InputCodec == "mjpeg" || opts.InputCodec == "jpeg" {
			args = append(args, "-vf", "format=yuv420p")
		}
	case strings.Contains(encoder, "vaapi"):
		// VAAPI needs hwaccel init flags
		args = append(args, "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi")
	case encoder == "libx264":
		preset := opts.Preset
		if preset == "" {
			preset = "faster"
		}
		// CRF 0 = use encoder default (23); otherwise honor the configured value (0-51).
		crf := 23
		if opts.CRF > 0 && opts.CRF <= 51 {
			crf = opts.CRF
		}
		args = append(args, "-preset", preset, "-crf", strconv.Itoa(crf))
	case encoder == "libx265":
		preset := opts.Preset
		if preset == "" {
			preset = "faster"
		}
		// CRF 0 = use encoder default (28); otherwise honor the configured value (0-51).
		crf := 28
		if opts.CRF > 0 && opts.CRF <= 51 {
			crf = opts.CRF
		}
		args = append(args, "-preset", preset, "-crf", strconv.Itoa(crf))
	}

	// Bitrate override
	if opts.Bitrate != "" {
		args = append(args, "-b:v", opts.Bitrate)
	}

	// Resolution override
	if opts.Width > 0 && opts.Height > 0 {
		args = append(args, "-vf", fmt.Sprintf("scale=%d:%d", opts.Width, opts.Height))
	}

	return args, nil
}

// isARMArch returns true if the architecture is ARM (32-bit or 64-bit).
func isARMArch(arch string) bool {
	return arch == "arm64" || arch == "arm"
}

// isSoftwareEncoder returns true if the encoder name is a software encoder.
func isSoftwareEncoder(encoder string) bool {
	return encoder == "libx264" || encoder == "libx265"
}

// isMJPEGInput returns true if the input codec is MJPEG or JPEG.
// These formats are always low-resolution and software encode is fast enough;
// v4l2m2m may hang on MJPEG input, so software encoding is the safe fallback.
func isMJPEGInput(codec string) bool {
	return codec == "mjpeg" || codec == "jpeg"
}
