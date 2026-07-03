package transcoding

import (
	"path/filepath"
	"strings"
	"testing"
)

// Helper: check args slice contains a consecutive sequence.
func argsContain(t *testing.T, args []string, want ...string) {
	t.Helper()
outer:
	for i := 0; i <= len(args)-len(want); i++ {
		for j, w := range want {
			if args[i+j] != w {
				continue outer
			}
		}
		return
	}
	t.Errorf("args %v do not contain %v", args, want)
}

// Helper: check args slice does NOT contain a value.
func argsNotContain(t *testing.T, args []string, want string) {
	t.Helper()
	for _, a := range args {
		if a == want {
			t.Errorf("args %v should not contain %q", args, want)
			return
		}
	}
}

// Helper: software-only capabilities (no hardware encoders).
func softwareCaps() HardwareCapabilities {
	return HardwareCapabilities{
		Arch:             "amd64",
		H265Encoder:      "libx265",
		H264EncoderType:  EncoderSoftware,
		H265EncoderType:  EncoderSoftware,
		FFmpegAvailable:  true,
		FFmpegPath:       "/usr/bin/ffmpeg",
	}
}

// Helper: V4L2M2M capabilities.
func v4l2m2mCaps() HardwareCapabilities {
	return HardwareCapabilities{
		Arch:             "arm64",
		H264Encoder:      "h264_v4l2m2m",
		H265Encoder:      "hevc_v4l2m2m",
		H264EncoderType:  EncoderV4L2M2M,
		H265EncoderType:  EncoderV4L2M2M,
		FFmpegAvailable:  true,
		FFmpegPath:       "/usr/bin/ffmpeg",
	}
}

// Helper: VAAPI capabilities.
func vaapiCaps() HardwareCapabilities {
	return HardwareCapabilities{
		Arch:             "amd64",
		H264Encoder:      "h264_vaapi",
		H265Encoder:      "hevc_vaapi",
		H264EncoderType:  EncoderVAAPI,
		H265EncoderType:  EncoderVAAPI,
		FFmpegAvailable:  true,
		FFmpegPath:       "/usr/bin/ffmpeg",
	}
}

// Helper: default H.264→H.265 options.
func h264ToH265Opts() TranscodeOptions {
	return TranscodeOptions{
		InputPath:   "/tmp/input.mp4",
		OutputPath:  "/tmp/output.mp4",
		InputCodec:  "h264",
		OutputCodec: "h265",
		Framerate:   30,
	}
}

func TestBuildCommand_H264ToH265_Software(t *testing.T) {
	args, err := BuildFFmpegCommand(h264ToH265Opts(), softwareCaps())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	argsContain(t, args, "-c:v", "libx265")
	argsContain(t, args, "-preset", "faster")
	argsContain(t, args, "-c:a", "aac")
	argsContain(t, args, "-b:a", "64k")
	argsContain(t, args, "-y", "/tmp/output.mp4")
}

func TestBuildCommand_H264ToH265_V4L2M2M(t *testing.T) {
	args, err := BuildFFmpegCommand(h264ToH265Opts(), v4l2m2mCaps())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	argsContain(t, args, "-c:v", "hevc_v4l2m2m")
	argsContain(t, args, "-g", "50")
	argsContain(t, args, "-bf", "0")
	argsContain(t, args, "-c:a", "aac")
	argsContain(t, args, "-b:a", "64k")
}

func TestBuildCommand_H264ToH265_VAAPI(t *testing.T) {
	args, err := BuildFFmpegCommand(h264ToH265Opts(), vaapiCaps())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	argsContain(t, args, "-c:v", "hevc_vaapi")
	argsContain(t, args, "-hwaccel", "vaapi")
	argsContain(t, args, "-hwaccel_output_format", "vaapi")
}

func TestBuildCommand_H265ToH264_Software(t *testing.T) {
	opts := TranscodeOptions{
		InputPath:   "/tmp/h265input.mp4",
		OutputPath:  "/tmp/h264output.mp4",
		InputCodec:  "h265",
		OutputCodec: "h264",
		Framerate:   30,
	}
	args, err := BuildFFmpegCommand(opts, softwareCaps())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	argsContain(t, args, "-c:v", "libx264")
	argsContain(t, args, "-preset", "faster")
	argsContain(t, args, "-crf", "23")
	argsContain(t, args, "-c:a", "aac")
	argsContain(t, args, "-b:a", "64k")
}

func TestBuildCommand_CRFOverride(t *testing.T) {
	// CRF set in options should override the libx264 default (23).
	t.Run("libx264 custom CRF", func(t *testing.T) {
		opts := TranscodeOptions{
			InputPath:   "/tmp/in.mp4",
			OutputPath:  "/tmp/out.mp4",
			InputCodec:  "h265",
			OutputCodec: "h264",
			Framerate:   30,
			CRF:         18,
		}
		args, err := BuildFFmpegCommand(opts, softwareCaps())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		argsContain(t, args, "-c:v", "libx264")
		argsContain(t, args, "-crf", "18")
	})

	// CRF set for libx265 output should override the default (28).
	t.Run("libx265 custom CRF", func(t *testing.T) {
		opts := TranscodeOptions{
			InputPath:   "/tmp/in.mp4",
			OutputPath:  "/tmp/out.mp4",
			InputCodec:  "h264",
			OutputCodec: "h265",
			Framerate:   30,
			CRF:         24,
		}
		args, err := BuildFFmpegCommand(opts, softwareCaps())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		argsContain(t, args, "-c:v", "libx265")
		argsContain(t, args, "-crf", "24")
	})

	// CRF=0 (zero value) should fall back to the encoder default, not emit "-crf 0".
	t.Run("CRF zero uses default", func(t *testing.T) {
		opts := TranscodeOptions{
			InputPath:   "/tmp/in.mp4",
			OutputPath:  "/tmp/out.mp4",
			InputCodec:  "h265",
			OutputCodec: "h264",
			Framerate:   30,
		}
		args, err := BuildFFmpegCommand(opts, softwareCaps())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		argsContain(t, args, "-crf", "23") // libx264 default
	})

	// Out-of-range CRF (>51) should clamp to default, not emit an invalid value.
	t.Run("CRF out of range clamps to default", func(t *testing.T) {
		opts := TranscodeOptions{
			InputPath:   "/tmp/in.mp4",
			OutputPath:  "/tmp/out.mp4",
			InputCodec:  "h264",
			OutputCodec: "h265",
			Framerate:   30,
			CRF:         99,
		}
		args, err := BuildFFmpegCommand(opts, softwareCaps())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		argsContain(t, args, "-crf", "28") // libx265 default
	})
}

func TestBuildCommand_H265ToH264_V4L2M2M(t *testing.T) {
	opts := TranscodeOptions{
		InputPath:   "/tmp/h265input.mp4",
		OutputPath:  "/tmp/h264output.mp4",
		InputCodec:  "h265",
		OutputCodec: "h264",
		Framerate:   30,
	}
	args, err := BuildFFmpegCommand(opts, v4l2m2mCaps())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	argsContain(t, args, "-c:v", "h264_v4l2m2m")
	argsContain(t, args, "-g", "50")
	argsContain(t, args, "-bf", "0")
	argsContain(t, args, "-c:a", "aac")
	argsContain(t, args, "-b:a", "64k")
}

func TestBuildCommand_MJPEGToH264(t *testing.T) {
	opts := TranscodeOptions{
		InputPath:   "/tmp/frames",
		OutputPath:  "/tmp/mjpeg_out.mp4",
		InputCodec:  "mjpeg",
		OutputCodec: "h264",
		Framerate:   15,
	}
	args, err := BuildFFmpegCommand(opts, softwareCaps())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	argsContain(t, args, "-framerate", "15")
	argsContain(t, args, "-i", filepath.Join("/tmp/frames", "%*.jpg"))
	argsContain(t, args, "-c:v", "libx264")
	// MJPEG has no audio, so no -c:a aac
	argsNotContain(t, args, "aac")
}

func TestBuildCommand_MJPEGToH265_Supported(t *testing.T) {
	// MJPEG→H.265 is supported via software libx265 encoder (FFmpeg decodes
	// JPEG internally then encodes). v4l2m2m is bypassed for MJPEG input.
	opts := TranscodeOptions{
		InputPath:   "/tmp/frames",
		OutputPath:  "/tmp/out.mp4",
		InputCodec:  "mjpeg",
		OutputCodec: "h265",
		Framerate:   15,
	}
	args, err := BuildFFmpegCommand(opts, softwareCaps())
	if err != nil {
		t.Fatalf("MJPEG→H.265 should be supported, got error: %v", err)
	}
	// MJPEG input forces software encoder (libx265, not v4l2m2m).
	argsContain(t, args, "-c:v", "libx265")
	// MJPEG input pattern.
	argsContain(t, args, "-i", filepath.Join("/tmp/frames", "%*.jpg"))
}

func TestBuildCommand_AudioPassthrough(t *testing.T) {
	// All non-MJPEG valid commands must include -c:a aac (transcoded, not copy)
	cases := []struct {
		name string
		opts TranscodeOptions
		caps HardwareCapabilities
	}{
		{"h264_to_h265_sw", h264ToH265Opts(), softwareCaps()},
		{"h265_to_h264_sw", TranscodeOptions{
			InputPath: "/tmp/in.mp4", OutputPath: "/tmp/out.mp4",
			InputCodec: "h265", OutputCodec: "h264", Framerate: 30,
		}, softwareCaps()},
		{"h264_to_h265_v4l2", h264ToH265Opts(), v4l2m2mCaps()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, err := BuildFFmpegCommand(tc.opts, tc.caps)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			argsContain(t, args, "-c:a", "aac")
	argsContain(t, args, "-b:a", "64k")
		})
	}
}

func TestBuildCommand_CustomBitrate(t *testing.T) {
	opts := h264ToH265Opts()
	opts.Bitrate = "2M"
	args, err := BuildFFmpegCommand(opts, softwareCaps())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	argsContain(t, args, "-b:v", "2M")
}

func TestBuildCommand_ForceSoftware(t *testing.T) {
	opts := h264ToH265Opts()
	opts.ForceSoftware = true
	// V4L2M2M caps should be ignored
	args, err := BuildFFmpegCommand(opts, v4l2m2mCaps())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	argsContain(t, args, "-c:v", "libx265")
	argsNotContain(t, args, "hevc_v4l2m2m")
}

func TestBuildCommand_CustomPreset(t *testing.T) {
	opts := h264ToH265Opts()
	opts.Preset = "ultrafast"
	args, err := BuildFFmpegCommand(opts, softwareCaps())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	argsContain(t, args, "-preset", "ultrafast")
	argsNotContain(t, args, "faster")
}

func TestBuildCommand_InvalidInputCodec(t *testing.T) {
	opts := TranscodeOptions{
		InputPath:   "/tmp/in.mp4",
		OutputPath:  "/tmp/out.mp4",
		InputCodec:  "vp9",
		OutputCodec: "h264",
		Framerate:   30,
	}
	_, err := BuildFFmpegCommand(opts, softwareCaps())
	if err == nil {
		t.Fatal("expected error for invalid input codec, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported input codec") {
		t.Errorf("error should mention 'unsupported input codec', got: %v", err)
	}
}

func TestBuildCommand_InvalidOutputCodec(t *testing.T) {
	opts := TranscodeOptions{
		InputPath:   "/tmp/in.mp4",
		OutputPath:  "/tmp/out.mp4",
		InputCodec:  "h264",
		OutputCodec: "vp9",
		Framerate:   30,
	}
	_, err := BuildFFmpegCommand(opts, softwareCaps())
	if err == nil {
		t.Fatal("expected error for invalid output codec, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported output codec") {
		t.Errorf("error should mention 'unsupported output codec', got: %v", err)
	}
}

func TestBuildCommand_ResolutionOverride(t *testing.T) {
	opts := h264ToH265Opts()
	opts.Width = 1280
	opts.Height = 720
	args, err := BuildFFmpegCommand(opts, softwareCaps())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	argsContain(t, args, "-vf", "scale=1280:720")
}

func TestBuildCommand_NVENC(t *testing.T) {
	caps := HardwareCapabilities{
		Arch:            "amd64",
		H264Encoder:     "h264_nvenc",
		H265Encoder:     "hevc_nvenc",
		H264EncoderType: EncoderNVENC,
		H265EncoderType: EncoderNVENC,
		FFmpegAvailable: true,
	}
	opts := h264ToH265Opts()
	args, err := BuildFFmpegCommand(opts, caps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	argsContain(t, args, "-c:v", "hevc_nvenc")
	argsContain(t, args, "-c:a", "aac")
	argsContain(t, args, "-b:a", "64k")
}

func TestBuildCommand_JPEGToH264(t *testing.T) {
	opts := TranscodeOptions{
		InputPath:   "/tmp/frames",
		OutputPath:  "/tmp/jpeg_out.mp4",
		InputCodec:  "jpeg",
		OutputCodec: "h264",
		Framerate:   15,
	}
	args, err := BuildFFmpegCommand(opts, softwareCaps())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	argsContain(t, args, "-framerate", "15")
	argsContain(t, args, "-i", filepath.Join("/tmp/frames", "%*.jpg"))
	argsContain(t, args, "-c:v", "libx264")
	// JPEG has no audio, so no -c:a aac
	argsNotContain(t, args, "aac")
}

func TestBuildCommand_JPEGToH265_Supported(t *testing.T) {
	// JPEG→H.265 is supported via software libx265 encoder.
	opts := TranscodeOptions{
		InputPath:   "/tmp/frames",
		OutputPath:  "/tmp/out.mp4",
		InputCodec:  "jpeg",
		OutputCodec: "h265",
		Framerate:   15,
	}
	args, err := BuildFFmpegCommand(opts, softwareCaps())
	if err != nil {
		t.Fatalf("JPEG→H.265 should be supported, got error: %v", err)
	}
	argsContain(t, args, "-c:v", "libx265")
}

// --- ARM Software Encoding Prohibition Tests ---

// Helper: ARM software-only capabilities (no hardware encoders).
func armSoftwareCaps() HardwareCapabilities {
	return HardwareCapabilities{
		Arch:             "arm64",
		H264Encoder:      "libx264",
		H265Encoder:      "libx265",
		H264EncoderType:  EncoderSoftware,
		H265EncoderType:  EncoderSoftware,
		FFmpegAvailable:  true,
		FFmpegPath:       "/usr/bin/ffmpeg",
	}
}

// Helper: ARM (32-bit) software-only capabilities.
func arm32SoftwareCaps() HardwareCapabilities {
	return HardwareCapabilities{
		Arch:             "arm",
		H264Encoder:      "libx264",
		H265Encoder:      "libx265",
		H264EncoderType:  EncoderSoftware,
		H265EncoderType:  EncoderSoftware,
		FFmpegAvailable:  true,
		FFmpegPath:       "/usr/bin/ffmpeg",
	}
}

// TestARMSoftwareAllowed verifies that software encoding is now allowed on arm64
// as a fallback when v4l2m2m is listed but the device lacks actual encoding capability.
func TestARMSoftwareAllowed(t *testing.T) {
	opts := h264ToH265Opts()
	opts.ForceSoftware = false
	args, err := BuildFFmpegCommand(opts, armSoftwareCaps())
	if err != nil {
		t.Fatalf("software encoding should now be allowed on ARM as fallback, got: %v", err)
	}
	argsContain(t, args, "-c:v", "libx265")
}

// TestARM32SoftwareAllowed verifies that software encoding is also allowed on 32-bit ARM.
func TestARM32SoftwareAllowed(t *testing.T) {
	opts := h264ToH265Opts()
	opts.ForceSoftware = false
	args, err := BuildFFmpegCommand(opts, arm32SoftwareCaps())
	if err != nil {
		t.Fatalf("software encoding should be allowed on ARM32 as fallback, got: %v", err)
	}
	argsContain(t, args, "-c:v", "libx265")
}

// TestX86SoftwareAllowed verifies that software encoding works on amd64.
func TestX86SoftwareAllowed(t *testing.T) {
	opts := h264ToH265Opts()
	args, err := BuildFFmpegCommand(opts, softwareCaps())
	if err != nil {
		t.Fatalf("unexpected error on amd64: %v", err)
	}
	argsContain(t, args, "-c:v", "libx265")
}

// TestARMHardwareAllowed verifies that hardware encoding works on ARM.
func TestARMHardwareAllowed(t *testing.T) {
	opts := h264ToH265Opts()
	args, err := BuildFFmpegCommand(opts, v4l2m2mCaps())
	if err != nil {
		t.Fatalf("unexpected error on ARM with V4L2M2M: %v", err)
	}
	argsContain(t, args, "-c:v", "hevc_v4l2m2m")
}

// TestARMForceSoftwarePreserved verifies ForceSoftware bypass works (for testing).
func TestARMForceSoftwarePreserved(t *testing.T) {
	opts := h264ToH265Opts()
	opts.ForceSoftware = true
	// Even with ARM caps + V4L2M2M, ForceSoftware should allow software encoding
	args, err := BuildFFmpegCommand(opts, v4l2m2mCaps())
	if err != nil {
		t.Fatalf("unexpected error with ForceSoftware on ARM: %v", err)
	}
	argsContain(t, args, "-c:v", "libx265")
}

// TestARMSoftwareAllowed_H264Output verifies software encoding for H.264 output on ARM.
func TestARMSoftwareAllowed_H264Output(t *testing.T) {
	opts := TranscodeOptions{
		InputPath:   "/tmp/h265input.mp4",
		OutputPath:  "/tmp/h264output.mp4",
		InputCodec:  "h265",
		OutputCodec: "h264",
		Framerate:   30,
	}
	args, err := BuildFFmpegCommand(opts, armSoftwareCaps())
	if err != nil {
		t.Fatalf("software encoding should be allowed on ARM for H.264 output, got: %v", err)
	}
	argsContain(t, args, "-c:v", "libx264")
}

// TestARMMJPEGSoftwareAllowed verifies that software encoding IS allowed on ARM for MJPEG input.
// MJPEG is always low-resolution and v4l2m2m may hang on it, so software is the safe fallback.
func TestARMMJPEGSoftwareAllowed(t *testing.T) {
	tests := []struct {
		name  string
		codec string
	}{
		{"mjpeg", "mjpeg"},
		{"jpeg", "jpeg"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := TranscodeOptions{
				InputPath:   "/tmp/frames",
				OutputPath:  "/tmp/out.mp4",
				InputCodec:  tc.codec,
				OutputCodec: "h264",
				Framerate:   10,
			}
			args, err := BuildFFmpegCommand(opts, armSoftwareCaps())
			if err != nil {
				t.Fatalf("MJPEG input should allow software encoding on ARM, got error: %v", err)
			}
			argsContain(t, args, "-c:v", "libx264")
			argsContain(t, args, "-preset", "faster")
		})
	}
}

// TestARMMJPEGForcesSoftwareEvenWithHardware verifies that even when v4l2m2m is available,
// MJPEG input forces software encoding (because v4l2m2m hangs on MJPEG input).
func TestARMMJPEGForcesSoftwareEvenWithHardware(t *testing.T) {
	tests := []struct {
		name  string
		codec string
	}{
		{"mjpeg", "mjpeg"},
		{"jpeg", "jpeg"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := TranscodeOptions{
				InputPath:   "/tmp/frames",
				OutputPath:  "/tmp/out.mp4",
				InputCodec:  tc.codec,
				OutputCodec: "h264",
				Framerate:   10,
			}
			// Use v4l2m2m caps — should STILL use libx264 for MJPEG input
			args, err := BuildFFmpegCommand(opts, v4l2m2mCaps())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			argsContain(t, args, "-c:v", "libx264")
			argsNotContain(t, args, "v4l2m2m")
		})
	}
}
