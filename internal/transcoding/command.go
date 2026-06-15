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
		args = append(args, "-framerate", fmt.Sprintf("%d", opts.Framerate))
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

	// Audio passthrough — always copy except MJPEG (no audio stream)
	if inputCodec != "mjpeg" {
		args = append(args, "-c:a", "copy")
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
	// MJPEG cannot be transcoded directly to H.265 — must go through H.264 first.
	if (input == "mjpeg" || input == "jpeg") && output == "h265" {
		return fmt.Errorf("unsupported codec combination: MJPEG to H.265 requires intermediate decode; use MJPEG→H.264 instead")
	}
	return nil
}

// buildVideoEncoderArgs selects the encoder and returns its FFmpeg flags.
func buildVideoEncoderArgs(opts TranscodeOptions, caps HardwareCapabilities) ([]string, error) {
	var args []string

	encoder := ""
	forceSoftware := opts.ForceSoftware

	// MJPEG input forces software encoder — v4l2m2m hangs on MJPEG input.
	useSoftware := forceSoftware || isMJPEGInput(opts.InputCodec)

	switch opts.OutputCodec {
	case "h264":
		if !useSoftware && caps.H264EncoderType != EncoderSoftware && caps.H264Encoder != "" {
			encoder = caps.H264Encoder
		} else {
			encoder = "libx264"
		}
	case "h265":
		if !useSoftware && caps.H265EncoderType != EncoderSoftware && caps.H265Encoder != "" {
			encoder = caps.H265Encoder
		} else {
			encoder = "libx265"
		}
	default:
		return nil, fmt.Errorf("unsupported output codec: %s", opts.OutputCodec)
	}

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
