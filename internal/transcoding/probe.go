package transcoding

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	probeOnce  sync.Once
	cachedCaps *HardwareCapabilities

	cachedEncoderList   map[string]bool
	cachedEncoderStderr string

	cachedDecoderList   map[string]bool
	cachedDecoderStderr string
)

// ResetProbe clears the cached probe result (for testing).
func ResetProbe() {
	probeOnce = sync.Once{}
	cachedCaps = nil
	cachedEncoderList = nil
	cachedEncoderStderr = ""
	cachedDecoderList = nil
	cachedDecoderStderr = ""
}

// ProbeHardwareCapabilities probes the system for transcoding capabilities.
// Uses sync.Once for idempotent caching — zero overhead after first call.
func ProbeHardwareCapabilities(ffmpegPath string) *HardwareCapabilities {
	probeOnce.Do(func() {
		cachedCaps = probeHardware(ffmpegPath)
	})
	return cachedCaps
}

// ProbeHardwareCapabilitiesExplicit probes with a specific FFmpeg path without caching.
// Used when FFmpeg was downloaded after startup and the cached probe result is stale.
func ProbeHardwareCapabilitiesExplicit(ffmpegPath string) *HardwareCapabilities {
	return probeHardware(ffmpegPath)
}

func probeHardware(ffmpegPath string) *HardwareCapabilities {
	caps := &HardwareCapabilities{
		Arch:            runtime.GOARCH,
		H264Encoder:     "libx264",
		H265Encoder:     "libx265",
		H264EncoderType: EncoderSoftware,
		H265EncoderType: EncoderSoftware,
	}

	// Detect CPU and memory from /proc
	caps.TotalCores = detectCPU()
	caps.TotalMemoryMB = detectMemory()

	// Resolve FFmpeg path
	if ffmpegPath == "" {
		if p, err := exec.LookPath("ffmpeg"); err == nil {
			ffmpegPath = p
		}
	}

	if ffmpegPath != "" {
		// Verify the binary actually exists
		if _, err := os.Stat(ffmpegPath); err == nil {
			caps.FFmpegAvailable = true
			caps.FFmpegPath = ffmpegPath
caps = probeEncoders(ffmpegPath, caps)
		caps = probeDecoders(ffmpegPath, caps)
		setResolutionLimits(caps)
		}
	}

	// Detect V4L2 devices
	caps.Devices = detectVideoDevices()

	// Estimate performance
	caps.EstimatedFPS = estimateFPS(caps)
	caps.MaxConcurrentStreams = estimateMaxConcurrent(caps)

	return caps
}

// detectCPU reads /proc/cpuinfo and counts processor lines.
func detectCPU() int {
	return detectCPUFromFile("/proc/cpuinfo")
}

// detectCPUFromFile reads a cpuinfo-format file and counts processor entries.
// Extracted for testability.
func detectCPUFromFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("Failed to read cpuinfo", "path", path, "error", err)
		return runtime.NumCPU()
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "processor") {
			count++
		}
	}
	if count == 0 {
		return runtime.NumCPU()
	}
	return count
}

// detectMemory reads /proc/meminfo and returns total RAM in MB.
func detectMemory() uint64 {
	return detectMemoryFromFile("/proc/meminfo")
}

// detectMemoryFromFile reads a meminfo-format file and returns total MB.
// Extracted for testability.
func detectMemoryFromFile(path string) uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("Failed to read meminfo", "path", path, "error", err)
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if kb, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
					return kb / 1024 // KB → MB
				}
			}
		}
	}
	return 0
}

// probeEncoders probes available hardware encoders via ffmpeg -encoders output parsing.
// Falls back to software on failure.
func probeEncoders(ffmpegPath string, caps *HardwareCapabilities) *HardwareCapabilities {
	type encoder struct {
		name    string
		encType EncoderType
	}

	var h264Candidates, h265Candidates []encoder

	switch runtime.GOARCH {
	case "arm64", "arm":
		h264Candidates = []encoder{
			{"h264_v4l2m2m", EncoderV4L2M2M},
			{"libx264", EncoderSoftware},
		}
		h265Candidates = []encoder{
			{"hevc_v4l2m2m", EncoderV4L2M2M},
			{"libx265", EncoderSoftware},
		}
	case "amd64":
		h264Candidates = []encoder{
			{"h264_vaapi", EncoderVAAPI},
			{"h264_nvenc", EncoderNVENC},
			{"libx264", EncoderSoftware},
		}
		h265Candidates = []encoder{
			{"hevc_vaapi", EncoderVAAPI},
			{"hevc_nvenc", EncoderNVENC},
			{"libx265", EncoderSoftware},
		}
	default:
		h264Candidates = []encoder{{"libx264", EncoderSoftware}}
		h265Candidates = []encoder{{"libx265", EncoderSoftware}}
	}

	// Probe H.264 — pick first working encoder
	for _, enc := range h264Candidates {
		if testEncoder(ffmpegPath, enc.name) {
			caps.H264Encoder = enc.name
			caps.H264EncoderType = enc.encType
			break
		}
	}

	// Probe H.265 — pick first working encoder
	for _, enc := range h265Candidates {
		if testEncoder(ffmpegPath, enc.name) {
			caps.H265Encoder = enc.name
			caps.H265EncoderType = enc.encType
			break
		}
	}

	return caps
}

// parseEncoderListFromOutput parses ffmpeg -encoders output and returns video encoder names.
func parseEncoderListFromOutput(output string) map[string]bool {
	encoders := map[string]bool{}
	afterHeader := false
	for _, line := range strings.Split(output, "\n") {
		if !afterHeader {
			if strings.HasPrefix(strings.TrimSpace(line), "------") {
				afterHeader = true
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if len(fields[0]) >= 1 && fields[0][0] == 'V' {
			encoders[fields[1]] = true
		}
	}
	return encoders
}

// parseEncoderList runs ffmpeg -encoders and returns video encoder names + stderr for diagnostics.
func parseEncoderList(ffmpegPath string) (map[string]bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffmpegPath, "-encoders")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		slog.Debug("ffmpeg -encoders failed", "path", ffmpegPath, "error", err, "stderr", stderr.String())
		return map[string]bool{}, stderr.String()
	}

	return parseEncoderListFromOutput(stdout.String()), stderr.String()
}

// videoDeviceExists checks for /dev/video* devices. Overridden in tests.
var videoDeviceExists = func() bool {
	matches, _ := filepath.Glob("/dev/video*")
	return len(matches) > 0
}

// testEncoder checks if an encoder is available via ffmpeg -encoders list parsing.
// For V4L2M2M encoders, also verifies /dev/video* device existence AND does a smoke test
// (encoders may be listed but the device may only support decode, not encode).
func testEncoder(ffmpegPath, encoder string) bool {
	if cachedEncoderList == nil {
		cachedEncoderList, cachedEncoderStderr = parseEncoderList(ffmpegPath)
		slog.Debug("ffmpeg -encoders probe complete", "path", ffmpegPath, "stderr", cachedEncoderStderr)
	}

	if !cachedEncoderList[encoder] {
		slog.Debug("Encoder not in ffmpeg -encoders list", "encoder", encoder, "stderr", cachedEncoderStderr)
		return false
	}

	if strings.Contains(encoder, "v4l2m2m") {
		if !videoDeviceExists() {
			slog.Debug("V4L2M2M encoder listed but no video devices", "encoder", encoder)
			return false
		}
		// Device exists but may only support decode. Smoke test: encode 1 frame.
		if !smokeTestEncoder(ffmpegPath, encoder) {
			slog.Debug("V4L2M2M encoder smoke test failed — device likely lacks encode support", "encoder", encoder)
			return false
		}
	}

	return true
}

// smokeTestEncoder runs a minimal FFmpeg encode (1 frame of black) to verify the encoder
// actually works. Some V4L2 devices advertise encoders in ffmpeg -encoders but only support decode.
var smokeTestEncoder = func(ffmpegPath, encoder string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-f", "lavfi", "-i", "color=c=black:s=320x240:d=0.1:r=1",
		"-frames:v", "1",
		"-c:v", encoder,
		"-f", "null", "-",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		slog.Debug("Encoder smoke test failed", "encoder", encoder, "error", err, "stderr", stderr.String())
		return false
	}
	return true
}

// detectVideoDevices checks for /dev/video* devices.
func detectVideoDevices() []string {
	return detectVideoDevicesInDir("/dev")
}

// detectVideoDevicesInDir checks for video* device files in the given directory.
// Extracted for testability.
func detectVideoDevicesInDir(dir string) []string {
	var devices []string
	for i := 0; i <= 32; i++ {
		name := fmt.Sprintf("video%d", i)
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			devices = append(devices, path)
		}
	}
	return devices
}

// estimateFPS estimates max transcode FPS for 1080p based on hardware capabilities.
func estimateFPS(caps *HardwareCapabilities) float64 {
	var fps float64

	switch caps.H264EncoderType {
	case EncoderV4L2M2M:
		fps = float64(caps.TotalCores) * 5.0
	case EncoderVAAPI:
		fps = 30.0
	case EncoderNVENC:
		fps = 60.0
	case EncoderSoftware:
		switch caps.Arch {
		case "arm64", "arm":
			fps = float64(caps.TotalCores) * 1.0
		default:
			fps = float64(caps.TotalCores) * 5.0
		}
	}

	// Memory constraint: halve estimate if < 512 MB
	if caps.TotalMemoryMB > 0 && caps.TotalMemoryMB < 512 {
		fps *= 0.5
	}

	return fps
}

// estimateMaxConcurrent returns a safe concurrency limit based on core count.
func estimateMaxConcurrent(caps *HardwareCapabilities) int {
	switch {
	case caps.TotalCores <= 2:
		return 1
	case caps.TotalCores <= 4:
		return 2
	default:
		return 4
	}
}

// CheckSufficient checks whether the hardware can handle transcoding for the given codec.
// Returns (true, "") if sufficient, or (false, reason) if not.
func CheckSufficient(caps *HardwareCapabilities, requiredCodec string) (bool, string) {
	if !caps.FFmpegAvailable {
		return false, "FFmpeg is not installed"
	}

	switch requiredCodec {
	case "h264":
		if caps.H264Encoder == "" {
			return false, "no H.264 encoder available"
		}
	case "h265":
		if caps.H265Encoder == "" {
			return false, "no H.265 encoder available"
		}
	}

	if caps.EstimatedFPS < 1.0 {
		return false, fmt.Sprintf("estimated FPS too low (%.1f) for practical transcoding", caps.EstimatedFPS)
	}

	return true, ""
}

// probeDecoders probes available hardware decoders via ffmpeg -decoders output parsing.
// Mirrors probeEncoders pattern. On ARM, software decoders are too slow → treat as unavailable.
func probeDecoders(ffmpegPath string, caps *HardwareCapabilities) *HardwareCapabilities {
	type decoder struct {
		name    string
		decType EncoderType
	}

	var h264Candidates, h265Candidates []decoder

	switch caps.Arch {
	case "arm64", "arm":
		h264Candidates = []decoder{
			{"h264_v4l2m2m", EncoderV4L2M2M},
		}
		h265Candidates = []decoder{
			{"hevc_v4l2m2m", EncoderV4L2M2M},
		}
	case "amd64":
		// Software decoders always fast enough on x86
		h264Candidates = []decoder{{"h264", EncoderSoftware}}
		h265Candidates = []decoder{{"hevc", EncoderSoftware}}
	default:
		h264Candidates = []decoder{{"h264", EncoderSoftware}}
		h265Candidates = []decoder{{"hevc", EncoderSoftware}}
	}

	// Probe H.264 decoder
	for _, dec := range h264Candidates {
		if testDecoder(ffmpegPath, dec.name) {
			caps.H264Decoder = dec.name
			caps.H264DecoderType = dec.decType
			break
		}
	}

	// Probe H.265 decoder
	for _, dec := range h265Candidates {
		if testDecoder(ffmpegPath, dec.name) {
			caps.H265Decoder = dec.name
			caps.H265DecoderType = dec.decType
			break
		}
	}

	return caps
}

// parseDecoderListFromOutput parses ffmpeg -decoders output and returns video decoder names.
func parseDecoderListFromOutput(output string) map[string]bool {
	decoders := map[string]bool{}
	afterHeader := false
	for _, line := range strings.Split(output, "\n") {
		if !afterHeader {
			if strings.HasPrefix(strings.TrimSpace(line), "------") {
				afterHeader = true
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if len(fields[0]) >= 1 && fields[0][0] == 'V' {
			decoders[fields[1]] = true
		}
	}
	return decoders
}

// parseDecoderList runs ffmpeg -decoders and returns video decoder names + stderr for diagnostics.
func parseDecoderList(ffmpegPath string) (map[string]bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffmpegPath, "-decoders")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		slog.Debug("ffmpeg -decoders failed", "path", ffmpegPath, "error", err, "stderr", stderr.String())
		return map[string]bool{}, stderr.String()
	}

	return parseDecoderListFromOutput(stdout.String()), stderr.String()
}

// testDecoder checks if a decoder is available via ffmpeg -decoders list parsing.
// For V4L2M2M decoders, also verifies /dev/video* device existence.
func testDecoder(ffmpegPath, decoder string) bool {
	if cachedDecoderList == nil {
		cachedDecoderList, cachedDecoderStderr = parseDecoderList(ffmpegPath)
		slog.Debug("ffmpeg -decoders probe complete", "path", ffmpegPath, "stderr", cachedDecoderStderr)
	}

	if !cachedDecoderList[decoder] {
		slog.Debug("Decoder not in ffmpeg -decoders list", "decoder", decoder, "stderr", cachedDecoderStderr)
		return false
	}

	if strings.Contains(decoder, "v4l2m2m") && !videoDeviceExists() {
		slog.Debug("V4L2M2M decoder listed but no video devices", "decoder", decoder)
		return false
	}

	return true
}

// setResolutionLimits sets MaxEncodeWidth/MaxEncodeHeight based on encoder type.
func setResolutionLimits(caps *HardwareCapabilities) {
	switch caps.H264EncoderType {
	case EncoderV4L2M2M:
		caps.MaxEncodeWidth = 1920
		caps.MaxEncodeHeight = 1440
	case EncoderVAAPI, EncoderNVENC:
		caps.MaxEncodeWidth = 4096
		caps.MaxEncodeHeight = 4096
	default:
		// Software encoder: unlimited
	}
}
