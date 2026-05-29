package transcoding

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// --- Helpers ---

// mustWriteFile is a test helper that creates a temp file with content.
func mustWriteFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// reset is a test helper that resets the global probe cache.
func reset(t *testing.T) {
	t.Helper()
	ResetProbe()
}

// --- Tests ---

// TestProbeHardware_Idempotent verifies sync.Once: second call returns same pointer.
func TestProbeHardware_Idempotent(t *testing.T) {
	reset(t)

	first := ProbeHardwareCapabilities("")
	second := ProbeHardwareCapabilities("")

	if first != second {
		t.Fatal("ProbeHardwareCapabilities: second call returned different pointer (sync.Once not working)")
	}
}

// TestProbeHardware_FFmpegNotFound verifies graceful degradation when FFmpeg is absent.
func TestProbeHardware_FFmpegNotFound(t *testing.T) {
	reset(t)

	caps := ProbeHardwareCapabilities("/nonexistent/ffmpeg/path")

	if caps.FFmpegAvailable {
		t.Fatal("FFmpegAvailable should be false when ffmpeg not found")
	}
	if caps.H264Encoder != "libx264" {
		t.Fatalf("H264Encoder = %q, want %q", caps.H264Encoder, "libx264")
	}
	if caps.H265Encoder != "libx265" {
		t.Fatalf("H265Encoder = %q, want %q", caps.H265Encoder, "libx265")
	}
	if caps.H264EncoderType != EncoderSoftware {
		t.Fatalf("H264EncoderType = %q, want %q", caps.H264EncoderType, EncoderSoftware)
	}
	if caps.H265EncoderType != EncoderSoftware {
		t.Fatalf("H265EncoderType = %q, want %q", caps.H265EncoderType, EncoderSoftware)
	}
	// Decoder fields should default to empty/zero on probe failure
	if caps.H264Decoder != "" {
		t.Fatalf("H264Decoder = %q, want empty", caps.H264Decoder)
	}
	if caps.H265Decoder != "" {
		t.Fatalf("H265Decoder = %q, want empty", caps.H265Decoder)
	}
	if caps.H264DecoderType != "" {
		t.Fatalf("H264DecoderType = %q, want empty", caps.H264DecoderType)
	}
	if caps.H265DecoderType != "" {
		t.Fatalf("H265DecoderType = %q, want empty", caps.H265DecoderType)
	}
}

// TestProbeHardware_FFmpegProbeFails verifies fallback when FFmpeg exists but encoder probe fails.
func TestProbeHardware_FFmpegProbeFails(t *testing.T) {
	reset(t)

	// Create a fake "ffmpeg" that immediately exits with error
	dir := t.TempDir()
	fakeFFmpeg := mustWriteFile(t, dir, "ffmpeg", "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(fakeFFmpeg, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	caps := ProbeHardwareCapabilities(fakeFFmpeg)

	// FFmpeg is "available" (exists on disk) but probes failed → software fallback
	if !caps.FFmpegAvailable {
		t.Fatal("FFmpegAvailable should be true (binary exists)")
	}
	if caps.H264EncoderType != EncoderSoftware {
		t.Fatalf("H264EncoderType = %q, want software fallback", caps.H264EncoderType)
	}
	if caps.H265EncoderType != EncoderSoftware {
		t.Fatalf("H265EncoderType = %q, want software fallback", caps.H265EncoderType)
	}
}

// TestDetectCPU verifies CPU core detection from a fake cpuinfo file.
func TestDetectCPU(t *testing.T) {
	cpuinfo := `processor	: 0
BogoMIPS	: 38.40
Features	: fp asimd

processor	: 1
BogoMIPS	: 38.40
Features	: fp asimd

processor	: 2
BogoMIPS	: 38.40

processor	: 3
BogoMIPS	: 38.40
`
	dir := t.TempDir()
	cpuPath := mustWriteFile(t, dir, "cpuinfo", cpuinfo)

	got := detectCPUFromFile(cpuPath)
	if want := 4; got != want {
		t.Fatalf("detectCPUFromFile = %d, want %d", got, want)
	}
}

// TestDetectCPU_Fallback verifies fallback when cpuinfo doesn't exist.
func TestDetectCPU_Fallback(t *testing.T) {
	got := detectCPUFromFile("/nonexistent/cpuinfo/path")
	if got != runtime.NumCPU() {
		t.Fatalf("detectCPUFromFile fallback = %d, want runtime.NumCPU() = %d", got, runtime.NumCPU())
	}
}

// TestDetectMemory verifies memory detection from a fake meminfo file.
func TestDetectMemory(t *testing.T) {
	meminfo := `MemTotal:       1024000 kB
MemFree:         512000 kB
MemAvailable:    768000 kB
Buffers:          16384 kB
`
	dir := t.TempDir()
	memPath := mustWriteFile(t, dir, "meminfo", meminfo)

	got := detectMemoryFromFile(memPath)
	// 1024000 kB / 1024 = 1000 MB
	if want := uint64(1000); got != want {
		t.Fatalf("detectMemoryFromFile = %d, want %d", got, want)
	}
}

// TestDetectMemory_Fallback verifies fallback when meminfo doesn't exist.
func TestDetectMemory_Fallback(t *testing.T) {
	got := detectMemoryFromFile("/nonexistent/meminfo/path")
	if got != 0 {
		t.Fatalf("detectMemoryFromFile = %d, want 0 for missing file", got)
	}
}

// TestCheckSufficient_Sufficient verifies good caps pass the check.
func TestCheckSufficient_Sufficient(t *testing.T) {
	caps := &HardwareCapabilities{
		FFmpegAvailable: true,
		H264Encoder:     "libx264",
		H265Encoder:     "libx265",
		EstimatedFPS:    30.0,
	}

	ok, reason := CheckSufficient(caps, "h264")
	if !ok {
		t.Fatalf("CheckSufficient h264 = false, reason = %q; want true", reason)
	}

	ok, reason = CheckSufficient(caps, "h265")
	if !ok {
		t.Fatalf("CheckSufficient h265 = false, reason = %q; want true", reason)
	}
}

// TestCheckSufficient_InsufficientFPS verifies low FPS returns false with reason.
func TestCheckSufficient_InsufficientFPS(t *testing.T) {
	caps := &HardwareCapabilities{
		FFmpegAvailable: true,
		H264Encoder:     "libx264",
		EstimatedFPS:    0.5,
	}

	ok, reason := CheckSufficient(caps, "h264")
	if ok {
		t.Fatal("CheckSufficient should fail with FPS < 1.0")
	}
	if reason == "" {
		t.Fatal("reason should not be empty for insufficient FPS")
	}
}

// TestCheckSufficient_FFmpegUnavailable verifies check fails when FFmpeg not installed.
func TestCheckSufficient_FFmpegUnavailable(t *testing.T) {
	caps := &HardwareCapabilities{
		FFmpegAvailable: false,
	}

	ok, reason := CheckSufficient(caps, "h264")
	if ok {
		t.Fatal("CheckSufficient should fail when FFmpeg unavailable")
	}
	if reason != "FFmpeg is not installed" {
		t.Fatalf("reason = %q, want %q", reason, "FFmpeg is not installed")
	}
}

// TestDetectVideoDevices_Exists verifies device detection when /dev/video* exists.
func TestDetectVideoDevices_Exists(t *testing.T) {
	dir := t.TempDir()
	// Create fake video devices
	for i := 10; i <= 12; i++ {
		p := filepath.Join(dir, "video"+strconv.Itoa(i))
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	devices := detectVideoDevicesInDir(dir)
	if len(devices) != 3 {
		t.Fatalf("detectVideoDevicesInDir = %d devices, want 3", len(devices))
	}
	// Check first device path
	want := filepath.Join(dir, "video10")
	if devices[0] != want {
		t.Fatalf("devices[0] = %q, want %q", devices[0], want)
	}
}

// TestDetectVideoDevices_NotExists verifies empty result when no devices.
func TestDetectVideoDevices_NotExists(t *testing.T) {
	dir := t.TempDir()
	devices := detectVideoDevicesInDir(dir)
	if len(devices) != 0 {
		t.Fatalf("detectVideoDevicesInDir = %d devices, want 0", len(devices))
	}
}

// TestEstimateFPS verifies FPS estimation for different encoder types.
func TestEstimateFPS(t *testing.T) {
	tests := []struct {
		name    string
		encType EncoderType
		cores   int
		memMB   uint64
		wantGT  float64 // estimated FPS should be > this
	}{
		{
			name:    "V4L2M2M with 4 cores",
			encType: EncoderV4L2M2M,
			cores:   4,
			memMB:   1024,
			wantGT:  15.0, // 4 * 5 = 20
		},
		{
			name:    "software ARM64 with 4 cores",
			encType: EncoderSoftware,
			cores:   4,
			memMB:   1024,
			wantGT:  3.0, // 4 * 1 = 4
		},
		{
			name:    "software with low memory halved",
			encType: EncoderSoftware,
			cores:   4,
			memMB:   256, // < 512 → halved
			wantGT:  1.0, // 4 * 1 * 0.5 = 2
		},
		{
			name:    "VAAPI",
			encType: EncoderVAAPI,
			cores:   2,
			memMB:   2048,
			wantGT:  25.0, // 30
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			caps := &HardwareCapabilities{
				Arch:            "arm64",
				TotalCores:      tc.cores,
				TotalMemoryMB:   tc.memMB,
				H264EncoderType: tc.encType,
			}
			got := estimateFPS(caps)
			if got <= tc.wantGT {
				t.Fatalf("estimateFPS = %.1f, want > %.1f", got, tc.wantGT)
			}
		})
	}
}

// TestEstimateMaxConcurrent verifies concurrency estimation.
func TestEstimateMaxConcurrent(t *testing.T) {
	tests := []struct {
		cores int
		want  int
	}{
		{1, 1},
		{2, 1},
		{3, 2},
		{4, 2},
		{6, 4},
		{8, 4},
	}
	for _, tc := range tests {
		t.Run("", func(t *testing.T) {
			caps := &HardwareCapabilities{TotalCores: tc.cores}
			got := estimateMaxConcurrent(caps)
			if got != tc.want {
				t.Fatalf("estimateMaxConcurrent(%d cores) = %d, want %d", tc.cores, got, tc.want)
			}
		})
	}
}

// testEncoderOutput is a fake ffmpeg -encoders output for testing.
const testEncoderOutput = `Encoders:
 V..... = Video
 A..... = Audio
 S..... = Subtitle
 .F.... = Frame-level multithreading
 ..S... = Slice-level multithreading
 ...X.. = Codec is experimental
 ....B. = Supports draw_horiz_band
 .....D = Supports direct rendering methods
 ------
 V..... libx264              libx264 H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10
 V..... libx265              libx265 MPEG-H/HEVC/H.265
 V..... h264_v4l2m2m         V4L2 mem2mem H.264 encoder wrapper
 V..... h264_vaapi           VAAPI H.264 encoder
 V..... h264_nvenc           NVIDIA NVENC H.264 encoder
`

// createFakeFFmpeg creates a fake ffmpeg script that outputs the given encoder and decoder lists.
func createFakeFFmpeg(t *testing.T, encoderOutput, decoderOutput string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"-encoders\" ]; then\n" +
		"cat <<'EOF'\n" + encoderOutput + "EOF\n" +
		"elif [ \"$1\" = \"-decoders\" ]; then\n" +
		"cat <<'EOF'\n" + decoderOutput + "EOF\n" +
		"else\n" +
		"  exit 0\n" +
		"fi\n"
	path := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return path
}

// TestParseEncoderListFromOutput verifies parsing of ffmpeg -encoders output.
func TestParseEncoderListFromOutput(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   map[string]bool
	}{
		{
			name:   "typical ffmpeg output",
			output: testEncoderOutput,
			want: map[string]bool{
				"libx264":      true,
				"libx265":      true,
				"h264_v4l2m2m": true,
				"h264_vaapi":   true,
				"h264_nvenc":   true,
			},
		},
		{
			name:   "empty output",
			output: "",
			want:   map[string]bool{},
		},
		{
			name:   "no video encoders",
			output: "Encoders:\n ------\n A..... aac    AAC encoder\n",
			want:   map[string]bool{},
		},
		{
			name:   "missing header separator",
			output: " V..... libx264              desc\n",
			want:   map[string]bool{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseEncoderListFromOutput(tc.output)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d encoders, want %d", len(got), len(tc.want))
			}
			for k := range tc.want {
				if !got[k] {
					t.Errorf("missing encoder %q", k)
				}
			}
			for k := range got {
				if !tc.want[k] {
					t.Errorf("unexpected encoder %q", k)
				}
			}
		})
	}
}

// TestTestEncoder verifies testEncoder with fake ffmpeg output.
func TestTestEncoder(t *testing.T) {
	fakeFFmpeg := createFakeFFmpeg(t, testEncoderOutput, testDecoderOutput)

	tests := []struct {
		name       string
		encoder    string
		hasDevices bool
		want       bool
	}{
		{"VAAPI detected", "h264_vaapi", false, true},
		{"NVENC detected", "h264_nvenc", false, true},
		{"software detected", "libx264", false, true},
		{"V4L2M2M no devices", "h264_v4l2m2m", false, false},
		{"V4L2M2M with devices", "h264_v4l2m2m", true, true},
		{"encoder not in list", "h264_nonexistent", false, false},
		{"V4L2M2M not in list", "hevc_v4l2m2m", true, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cachedEncoderList = nil
			cachedEncoderStderr = ""

			orig := videoDeviceExists
			videoDeviceExists = func() bool { return tc.hasDevices }
			defer func() { videoDeviceExists = orig }()

			got := testEncoder(fakeFFmpeg, tc.encoder)
			if got != tc.want {
				t.Fatalf("testEncoder(%q) = %v, want %v", tc.encoder, got, tc.want)
			}
		})
	}
}

// TestTestEncoder_FFmpegFails verifies fallback when ffmpeg -encoders fails.
func TestTestEncoder_FFmpegFails(t *testing.T) {
	dir := t.TempDir()
	fakeFFmpeg := mustWriteFile(t, dir, "ffmpeg", "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(fakeFFmpeg, 0o755); err != nil {
		t.Fatal(err)
	}

	cachedEncoderList = nil
	cachedEncoderStderr = ""

	if testEncoder(fakeFFmpeg, "libx264") {
		t.Fatal("testEncoder with failing ffmpeg = true, want false")
	}
}

// TestTestEncoder_StderrCaptured verifies stderr is captured from ffmpeg -encoders.
func TestTestEncoder_StderrCaptured(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho 'error: config not found' >&2\necho '------'\necho ' V..... libx264 desc'\nexit 0\n"
	fakeFFmpeg := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(fakeFFmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cachedEncoderList = nil
	cachedEncoderStderr = ""

	testEncoder(fakeFFmpeg, "libx264")

	if cachedEncoderStderr == "" {
		t.Fatal("cachedEncoderStderr is empty, expected stderr capture")
	}
	if !strings.Contains(cachedEncoderStderr, "error: config not found") {
		t.Fatalf("cachedEncoderStderr = %q, want to contain diagnostic message", cachedEncoderStderr)
	}
}

// testDecoderOutput is a fake ffmpeg -decoders output for testing.
const testDecoderOutput = `Decoders:
 V..... = Video
 A..... = Audio
 ------
 V..... h264                H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10
 V..... hevc                HEVC (High Efficiency Video Coding)
 V..... h264_v4l2m2m        V4L2 mem2mem H.264 decoder wrapper
 V..... hevc_v4l2m2m        V4L2 mem2mem HEVC decoder wrapper
 V..... mjpeg               MJPEG (Motion JPEG)
`

// TestParseDecoderListFromOutput verifies parsing of ffmpeg -decoders output.
func TestParseDecoderListFromOutput(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   map[string]bool
	}{
		{
			name:   "typical ffmpeg output",
			output: testDecoderOutput,
			want: map[string]bool{
				"h264":         true,
				"hevc":         true,
				"h264_v4l2m2m": true,
				"hevc_v4l2m2m": true,
				"mjpeg":        true,
			},
		},
		{
			name:   "empty output",
			output: "",
			want:   map[string]bool{},
		},
		{
			name:   "no video decoders",
			output: "Decoders:\n ------\n A..... aac    AAC decoder\n",
			want:   map[string]bool{},
		},
		{
			name:   "missing header separator",
			output: " V..... h264              desc\n",
			want:   map[string]bool{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseDecoderListFromOutput(tc.output)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d decoders, want %d", len(got), len(tc.want))
			}
			for k := range tc.want {
				if !got[k] {
					t.Errorf("missing decoder %q", k)
				}
			}
			for k := range got {
				if !tc.want[k] {
					t.Errorf("unexpected decoder %q", k)
				}
			}
		})
	}
}

// TestTestDecoder verifies testDecoder with fake ffmpeg output.
func TestTestDecoder(t *testing.T) {
	fakeFFmpeg := createFakeFFmpeg(t, testEncoderOutput, testDecoderOutput)

	tests := []struct {
		name       string
		decoder    string
		hasDevices bool
		want       bool
	}{
		{"software h264 detected", "h264", false, true},
		{"software hevc detected", "hevc", false, true},
		{"V4L2M2M H.264 no devices", "h264_v4l2m2m", false, false},
		{"V4L2M2M H.264 with devices", "h264_v4l2m2m", true, true},
		{"V4L2M2M HEVC no devices", "hevc_v4l2m2m", false, false},
		{"V4L2M2M HEVC with devices", "hevc_v4l2m2m", true, true},
		{"decoder not in list", "h264_nonexistent", false, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cachedDecoderList = nil
			cachedDecoderStderr = ""

			orig := videoDeviceExists
			videoDeviceExists = func() bool { return tc.hasDevices }
			defer func() { videoDeviceExists = orig }()

			got := testDecoder(fakeFFmpeg, tc.decoder)
			if got != tc.want {
				t.Fatalf("testDecoder(%q) = %v, want %v", tc.decoder, got, tc.want)
			}
		})
	}
}

// TestProbeDecoders_ARMNoHardware verifies ARM with no v4l2m2m decoder → decoder fields empty.
func TestProbeDecoders_ARMNoHardware(t *testing.T) {
	reset(t)
	// Encoder output with v4l2m2m but decoder output WITHOUT v4l2m2m
	decoderOutput := "Decoders:\n ------\n V..... h264  H.264\n V..... hevc  HEVC\n"
	fakeFFmpeg := createFakeFFmpeg(t, testEncoderOutput, decoderOutput)

	orig := videoDeviceExists
	videoDeviceExists = func() bool { return true }
	defer func() { videoDeviceExists = orig }()

	caps := probeDecoders(fakeFFmpeg, &HardwareCapabilities{Arch: "arm64"})
	if caps.H264Decoder != "" {
		t.Fatalf("H264Decoder = %q, want empty on ARM without v4l2m2m decoder", caps.H264Decoder)
	}
	if caps.H265Decoder != "" {
		t.Fatalf("H265Decoder = %q, want empty on ARM without v4l2m2m decoder", caps.H265Decoder)
	}
	if caps.H264DecoderType != "" {
		t.Fatalf("H264DecoderType = %q, want empty", caps.H264DecoderType)
	}
	if caps.H265DecoderType != "" {
		t.Fatalf("H265DecoderType = %q, want empty", caps.H265DecoderType)
	}
}

// TestProbeDecoders_ARMWithHardware verifies ARM with v4l2m2m decoder → decoder fields set.
func TestProbeDecoders_ARMWithHardware(t *testing.T) {
	reset(t)
	fakeFFmpeg := createFakeFFmpeg(t, testEncoderOutput, testDecoderOutput)

	orig := videoDeviceExists
	videoDeviceExists = func() bool { return true }
	defer func() { videoDeviceExists = orig }()

	caps := probeDecoders(fakeFFmpeg, &HardwareCapabilities{Arch: "arm64"})
	if caps.H264Decoder != "h264_v4l2m2m" {
		t.Fatalf("H264Decoder = %q, want h264_v4l2m2m", caps.H264Decoder)
	}
	if caps.H265Decoder != "hevc_v4l2m2m" {
		t.Fatalf("H265Decoder = %q, want hevc_v4l2m2m", caps.H265Decoder)
	}
	if caps.H264DecoderType != EncoderV4L2M2M {
		t.Fatalf("H264DecoderType = %q, want v4l2m2m", caps.H264DecoderType)
	}
	if caps.H265DecoderType != EncoderV4L2M2M {
		t.Fatalf("H265DecoderType = %q, want v4l2m2m", caps.H265DecoderType)
	}
}

// TestProbeDecoders_X86Software verifies x86 always has software decoders.
func TestProbeDecoders_X86Software(t *testing.T) {
	reset(t)
	// Even without hardware decoders, x86 software decoders should be detected
	decoderOutput := "Decoders:\n ------\n V..... h264  H.264\n V..... hevc  HEVC\n"
	fakeFFmpeg := createFakeFFmpeg(t, testEncoderOutput, decoderOutput)

	caps := probeDecoders(fakeFFmpeg, &HardwareCapabilities{Arch: "amd64"})
	if caps.H264Decoder != "h264" {
		t.Fatalf("H264Decoder = %q, want h264 on amd64", caps.H264Decoder)
	}
	if caps.H265Decoder != "hevc" {
		t.Fatalf("H265Decoder = %q, want hevc on amd64", caps.H265Decoder)
	}
	if caps.H264DecoderType != EncoderSoftware {
		t.Fatalf("H264DecoderType = %q, want software", caps.H264DecoderType)
	}
	if caps.H265DecoderType != EncoderSoftware {
		t.Fatalf("H265DecoderType = %q, want software", caps.H265DecoderType)
	}
}

// TestResolutionLimits_V4L2M2M verifies 1920×1440 limit for V4L2M2M encoder.
func TestResolutionLimits_V4L2M2M(t *testing.T) {
	caps := &HardwareCapabilities{H264EncoderType: EncoderV4L2M2M}
	setResolutionLimits(caps)
	if caps.MaxEncodeWidth != 1920 {
		t.Fatalf("MaxEncodeWidth = %d, want 1920", caps.MaxEncodeWidth)
	}
	if caps.MaxEncodeHeight != 1440 {
		t.Fatalf("MaxEncodeHeight = %d, want 1440", caps.MaxEncodeHeight)
	}
}

// TestResolutionLimits_Software verifies 0 (unlimited) for software encoder.
func TestResolutionLimits_Software(t *testing.T) {
	caps := &HardwareCapabilities{H264EncoderType: EncoderSoftware}
	setResolutionLimits(caps)
	if caps.MaxEncodeWidth != 0 {
		t.Fatalf("MaxEncodeWidth = %d, want 0 (unlimited)", caps.MaxEncodeWidth)
	}
	if caps.MaxEncodeHeight != 0 {
		t.Fatalf("MaxEncodeHeight = %d, want 0 (unlimited)", caps.MaxEncodeHeight)
	}
}

// TestResolutionLimits_VAAPI verifies 4096×4096 for VAAPI encoder.
func TestResolutionLimits_VAAPI(t *testing.T) {
	caps := &HardwareCapabilities{H264EncoderType: EncoderVAAPI}
	setResolutionLimits(caps)
	if caps.MaxEncodeWidth != 4096 {
		t.Fatalf("MaxEncodeWidth = %d, want 4096", caps.MaxEncodeWidth)
	}
	if caps.MaxEncodeHeight != 4096 {
		t.Fatalf("MaxEncodeHeight = %d, want 4096", caps.MaxEncodeHeight)
	}
}

// TestResolutionLimits_NVENC verifies 4096×4096 for NVENC encoder.
func TestResolutionLimits_NVENC(t *testing.T) {
	caps := &HardwareCapabilities{H264EncoderType: EncoderNVENC}
	setResolutionLimits(caps)
	if caps.MaxEncodeWidth != 4096 {
		t.Fatalf("MaxEncodeWidth = %d, want 4096", caps.MaxEncodeWidth)
	}
	if caps.MaxEncodeHeight != 4096 {
		t.Fatalf("MaxEncodeHeight = %d, want 4096", caps.MaxEncodeHeight)
	}
}
