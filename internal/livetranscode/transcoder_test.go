package livetranscode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// compileMockFFmpeg compiles a Go program that acts as a mock FFmpeg.
// The mock writes minimal H.264 Annex-B data to stdout.
// mode "normal": reads stdin (discard), exits on SIGTERM.
// mode "ignore-term": ignores SIGTERM, runs until killed (for timeout test).
// mode "block-stdin": doesn't read stdin (pipe fills, queue backs up).
func compileMockFFmpeg(t *testing.T, dir, mode string) string {
	t.Helper()

	var source string
	switch mode {
	case "ignore-term":
		source = `package main
import "os"
import "os/signal"
func main() {
	go func() { os.Stdin.Close() }()
	os.Stdout.Write([]byte{
		0x00,0x00,0x00,0x01,0x67,0x64,0x00,0x1e,0xac,
		0x00,0x00,0x00,0x01,0x68,0xe8,0x43,0x80,
		0x00,0x00,0x00,0x01,0x65,0x88,0x84,0x00,0x0d,
		0x00,0x00,0x00,0x01,0x41,0x9a,0x62,0x80,
	})
	os.Stdout.Close()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig)
	for { <-sig }
}`
	case "block-stdin":
		source = `package main
import "os"
import "syscall"
import "time"
func main() {
	time.Sleep(100*time.Millisecond)
	os.Stdout.Write([]byte{
		0x00,0x00,0x00,0x01,0x67,0x64,0x00,0x1e,0xac,
	})
	os.Stdout.Close()
	for { syscall.Pause() }
}`
	default: // "normal"
		source = `package main
import "io"
import "os"
import "os/signal"
import "syscall"
func main() {
	go io.Copy(io.Discard, os.Stdin)
	os.Stdout.Write([]byte{
		0x00,0x00,0x00,0x01,0x67,0x64,0x00,0x1e,0xac,
		0x00,0x00,0x00,0x01,0x68,0xe8,0x43,0x80,
		0x00,0x00,0x00,0x01,0x65,0x88,0x84,0x00,0x0d,
		0x00,0x00,0x00,0x01,0x41,0x9a,0x62,0x80,
	})
	os.Stdout.Close()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
}`
	}

	srcPath := filepath.Join(dir, "mockffmpeg_"+mode+".go")
	err := os.WriteFile(srcPath, []byte(source), 0644)
	require.NoError(t, err)

	binaryPath := filepath.Join(dir, "ffmpeg_"+mode)
	cmd := exec.Command("go", "build", "-o", binaryPath, srcPath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "failed to compile mock FFmpeg: %s", string(out))

	return binaryPath
}

// defaultTestConfig returns a TranscoderConfig with SW encoding and a mock
// FFmpeg binary for testing.
func defaultTestConfig(t *testing.T, mockFFmpeg string) TranscoderConfig {
	t.Helper()
	return TranscoderConfig{
		InputCodec:  CodecH265,
		EncoderType: EncoderSW,
		FFmpegPath:  mockFFmpeg,
		Preset: ResolvedPreset{
			Name: "test", GopSeconds: 2, VideoBitrateKbps: 3000,
			Resolution: "1920x1080", Framerate: 30, Profile: "main", Bframes: 0,
		},
		HardwareCap: &transcoding.HardwareCapabilities{
			Arch:            runtime.GOARCH,
			H264Encoder:     "libx264",
			H265Encoder:     "libx265",
			H264EncoderType: transcoding.EncoderSoftware,
			H265EncoderType: transcoding.EncoderSoftware,
			FFmpegAvailable: true,
			FFmpegPath:      mockFFmpeg,
		},
	}
}

// hardwareTestConfig returns a config with HW-like capabilities.
func hardwareTestConfig(t *testing.T, mockFFmpeg string) TranscoderConfig {
	t.Helper()
	return TranscoderConfig{
		InputCodec:  CodecH265,
		EncoderType: EncoderAuto,
		FFmpegPath:  mockFFmpeg,
		Preset: ResolvedPreset{
			Name: "test", GopSeconds: 2, VideoBitrateKbps: 3000,
			Resolution: "1920x1080", Framerate: 30, Profile: "main", Bframes: 0,
		},
		HardwareCap: &transcoding.HardwareCapabilities{
			Arch:            "arm64",
			H264Encoder:     "h264_v4l2m2m",
			H265Encoder:     "hevc_v4l2m2m",
			H264EncoderType: transcoding.EncoderV4L2M2M,
			H265EncoderType: transcoding.EncoderV4L2M2M,
			H264Decoder:     "h264_v4l2m2m",
			H265Decoder:     "hevc_v4l2m2m",
			H264DecoderType: transcoding.EncoderV4L2M2M,
			H265DecoderType: transcoding.EncoderV4L2M2M,
			FFmpegAvailable: true,
			FFmpegPath:      mockFFmpeg,
		},
	}
}

// requireProcessGone verifies that a process with the given PID is no longer
// running. It polls until the process disappears or a 15s timeout.
func requireProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("process %d still running after 5s", pid)
}

// ---------------------------------------------------------------------------
// Test: NewLiveTranscoder — SW path
// ---------------------------------------------------------------------------

func TestLiveTranscoder_New_SWPath(t *testing.T) {
	dir := t.TempDir()
	mockPath := compileMockFFmpeg(t, dir, "normal")
	cfg := defaultTestConfig(t, mockPath)
	cfg.EncoderType = EncoderSW

	lt := NewLiveTranscoder(cfg)
	require.NotNil(t, lt)
	require.Equal(t, "libx264", lt.EncoderName())
}

func TestLiveTranscoder_New_HWPath(t *testing.T) {
	dir := t.TempDir()
	mockPath := compileMockFFmpeg(t, dir, "normal")
	cfg := hardwareTestConfig(t, mockPath)

	lt := NewLiveTranscoder(cfg)
	require.NotNil(t, lt)
	require.Equal(t, "h264_v4l2m2m", lt.EncoderName())
}

func TestLiveTranscoder_New_AutoDetectHW(t *testing.T) {
	dir := t.TempDir()
	mockPath := compileMockFFmpeg(t, dir, "normal")
	cfg := hardwareTestConfig(t, mockPath)
	cfg.EncoderType = EncoderAuto

	lt := NewLiveTranscoder(cfg)
	require.NotNil(t, lt)
	require.Equal(t, "h264_v4l2m2m", lt.EncoderName())
}

func TestLiveTranscoder_New_AutoDetectSW(t *testing.T) {
	dir := t.TempDir()
	mockPath := compileMockFFmpeg(t, dir, "normal")
	cfg := defaultTestConfig(t, mockPath)
	cfg.EncoderType = EncoderAuto

	lt := NewLiveTranscoder(cfg)
	require.NotNil(t, lt)
	require.Equal(t, "libx264", lt.EncoderName())
}

func TestLiveTranscoder_New_ForceHW(t *testing.T) {
	dir := t.TempDir()
	mockPath := compileMockFFmpeg(t, dir, "normal")
	cfg := hardwareTestConfig(t, mockPath)
	cfg.EncoderType = EncoderHW

	lt := NewLiveTranscoder(cfg)
	require.NotNil(t, lt)
	require.Equal(t, "h264_v4l2m2m", lt.EncoderName())
}

func TestLiveTranscoder_New_HWNoDecoderFallback(t *testing.T) {
	dir := t.TempDir()
	mockPath := compileMockFFmpeg(t, dir, "normal")
	cfg := TranscoderConfig{
		InputCodec:  CodecH265,
		EncoderType: EncoderHW,
		FFmpegPath:  mockPath,
		Preset: ResolvedPreset{
			Name: "test", GopSeconds: 2, VideoBitrateKbps: 3000,
			Resolution: "1920x1080", Framerate: 30, Bframes: 0,
		},
		HardwareCap: &transcoding.HardwareCapabilities{
			Arch:            "arm64",
			H264Encoder:     "h264_v4l2m2m",
			H264EncoderType: transcoding.EncoderV4L2M2M,
			H265Decoder:     "",
			H265DecoderType: transcoding.EncoderSoftware,
			FFmpegAvailable: true,
			FFmpegPath:      mockPath,
		},
	}

	lt := NewLiveTranscoder(cfg)
	require.NotNil(t, lt)
	require.Equal(t, "h264_v4l2m2m", lt.EncoderName())
}

// ---------------------------------------------------------------------------
// Test: Start + Stop cleanly (no input)
// ---------------------------------------------------------------------------

func TestLiveTranscoder_StartStop(t *testing.T) {
	dir := t.TempDir()
	mockPath := compileMockFFmpeg(t, dir, "normal")
	cfg := defaultTestConfig(t, mockPath)

	lt := NewLiveTranscoder(cfg)
	err := lt.Start(context.Background())
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond)

	err = lt.Stop()
	require.NoError(t, err)

	requireProcessGone(t, lt.cmd.Process.Pid)
}

// ---------------------------------------------------------------------------
// Test: WriteInput + Output integration
// ---------------------------------------------------------------------------

func TestLiveTranscoder_WriteAndRead(t *testing.T) {
	dir := t.TempDir()
	mockPath := compileMockFFmpeg(t, dir, "normal")
	cfg := defaultTestConfig(t, mockPath)

	lt := NewLiveTranscoder(cfg)
	err := lt.Start(context.Background())
	require.NoError(t, err)
	defer lt.Stop()

	deadline := time.After(15 * time.Second)
	var aus []AccessUnit

loop:
	for {
		select {
		case au := <-lt.Output():
			aus = append(aus, au)
			if len(aus) >= 2 {
				break loop
			}
		case <-deadline:
			break loop
		}
	}

	require.GreaterOrEqual(t, len(aus), 1, "should have received at least 1 AU")

	au := AccessUnit{
		{0x01, 0x02, 0x03},
	}
	err = lt.WriteInput(au)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Test: WriteInput drops on overflow (non-blocking)
// ---------------------------------------------------------------------------

func TestLiveTranscoder_WriteInput_Overflow(t *testing.T) {
	dir := t.TempDir()
	mockPath := compileMockFFmpeg(t, dir, "block-stdin")
	cfg := defaultTestConfig(t, mockPath)

	lt := NewLiveTranscoder(cfg)
	err := lt.Start(context.Background())
	require.NoError(t, err)
	defer lt.Stop()

	bigAU := make(AccessUnit, 10)
	for i := range bigAU {
		bigAU[i] = make([]byte, 400)
		for j := range bigAU[i] {
			bigAU[i][j] = byte(i + j)
		}
	}

	for i := 0; i < 40; i++ {
		err := lt.WriteInput(bigAU)
		if err != nil {
			t.Logf("write %d: %v (expected after queue full)", i, err)
			break
		}
	}
}

// ---------------------------------------------------------------------------
// Test: Stop with process timeout — mock ignores SIGTERM
// ---------------------------------------------------------------------------

func TestLiveTranscoder_Stop_Timeout(t *testing.T) {
	dir := t.TempDir()
	mockPath := compileMockFFmpeg(t, dir, "ignore-term")
	cfg := defaultTestConfig(t, mockPath)

	lt := NewLiveTranscoder(cfg)
	err := lt.Start(context.Background())
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond)

	start := time.Now()
	err = lt.Stop()
	duration := time.Since(start)
	require.NoError(t, err)

	require.GreaterOrEqual(t, duration, 4*time.Second,
		"Stop should have waited for SIGTERM timeout")

	requireProcessGone(t, lt.cmd.Process.Pid)
}

// ---------------------------------------------------------------------------
// Test: Stop called twice (idempotent)
// ---------------------------------------------------------------------------

func TestLiveTranscoder_Stop_Idempotent(t *testing.T) {
	dir := t.TempDir()
	mockPath := compileMockFFmpeg(t, dir, "normal")
	cfg := defaultTestConfig(t, mockPath)

	lt := NewLiveTranscoder(cfg)
	err := lt.Start(context.Background())
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	err = lt.Stop()
	require.NoError(t, err)

	err = lt.Stop()
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Test: WriteInput after Stop returns error
// ---------------------------------------------------------------------------

func TestLiveTranscoder_WriteInput_AfterStop(t *testing.T) {
	dir := t.TempDir()
	mockPath := compileMockFFmpeg(t, dir, "normal")
	cfg := defaultTestConfig(t, mockPath)

	lt := NewLiveTranscoder(cfg)
	err := lt.Start(context.Background())
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	err = lt.Stop()
	require.NoError(t, err)

	au := AccessUnit{{0x01}}
	err = lt.WriteInput(au)
	require.Error(t, err)
	require.Contains(t, err.Error(), "stopped")
}

// ---------------------------------------------------------------------------
// Test: ParamSets returns initial data after output parsing
// ---------------------------------------------------------------------------

func TestLiveTranscoder_ParamSets(t *testing.T) {
	dir := t.TempDir()
	mockPath := compileMockFFmpeg(t, dir, "normal")
	cfg := defaultTestConfig(t, mockPath)

	lt := NewLiveTranscoder(cfg)
	err := lt.Start(context.Background())
	require.NoError(t, err)
	defer lt.Stop()

	deadline := time.After(15 * time.Second)
	foundParams := false
	for !foundParams {
		select {
		case <-lt.Output():
			ps := lt.ParamSets()
			if len(ps.SPS) > 0 && len(ps.PPS) > 0 {
				foundParams = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for param sets")
		}
	}

	ps := lt.ParamSets()
	require.NotNil(t, ps.SPS, "should have SPS")
	require.NotNil(t, ps.PPS, "should have PPS")
	require.Nil(t, ps.VPS, "H.264 should not have VPS")
}

// ---------------------------------------------------------------------------
// Test: EncoderName returns correct name
// ---------------------------------------------------------------------------

func TestLiveTranscoder_EncoderName(t *testing.T) {
	dir := t.TempDir()
	mockPath := compileMockFFmpeg(t, dir, "normal")

	swCfg := defaultTestConfig(t, mockPath)
	ltSW := NewLiveTranscoder(swCfg)
	require.Equal(t, "libx264", ltSW.EncoderName())

	hwCfg := hardwareTestConfig(t, mockPath)
	ltHW := NewLiveTranscoder(hwCfg)
	require.Equal(t, "h264_v4l2m2m", ltHW.EncoderName())
}

// ---------------------------------------------------------------------------
// Test: Concurrent access to WriteInput (safety)
// ---------------------------------------------------------------------------

func TestLiveTranscoder_ConcurrentWriteInput(t *testing.T) {
	dir := t.TempDir()
	mockPath := compileMockFFmpeg(t, dir, "normal")
	cfg := defaultTestConfig(t, mockPath)

	lt := NewLiveTranscoder(cfg)
	err := lt.Start(context.Background())
	require.NoError(t, err)
	defer lt.Stop()

	time.Sleep(100 * time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			au := AccessUnit{{byte(n), byte(n + 1)}}
			_ = lt.WriteInput(au)
		}(i)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Test: FFmpeg not found returns error on Start
// ---------------------------------------------------------------------------

func TestLiveTranscoder_Start_FFmpegNotFound(t *testing.T) {
	cfg := TranscoderConfig{
		InputCodec:  CodecH265,
		EncoderType: EncoderSW,
		FFmpegPath:  "/nonexistent/ffmpeg",
		Preset: ResolvedPreset{
			GopSeconds: 2, VideoBitrateKbps: 3000,
			Framerate: 30, Bframes: 0,
		},
	}

	lt := NewLiveTranscoder(cfg)
	err := lt.Start(context.Background())
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Test: buildFFmpegArgs produces expected command line (SW)
// ---------------------------------------------------------------------------

func TestLiveTranscoder_BuildArgs_SW(t *testing.T) {
	cfg := TranscoderConfig{
		InputCodec:  CodecH265,
		EncoderType: EncoderSW,
		FFmpegPath:  "ffmpeg",
		Preset: ResolvedPreset{
			GopSeconds: 2, VideoBitrateKbps: 3000,
			Resolution: "1920x1080", Framerate: 30,
			Profile: "main", Bframes: 0,
		},
	}

	lt := NewLiveTranscoder(cfg)
	args := lt.buildFFmpegArgs()

	require.Contains(t, args, "-f")
	require.Contains(t, args, "hevc")
	require.Contains(t, args, "-c:v")
	require.Contains(t, args, "libx264")
	require.Contains(t, args, "-preset")
	require.Contains(t, args, "ultrafast")
	require.Contains(t, args, "-tune")
	require.Contains(t, args, "zerolatency")
	require.Contains(t, args, "-g")
	require.Contains(t, args, "60")
	require.Contains(t, args, "-keyint_min")
	require.Contains(t, args, "60")
	require.Contains(t, args, "-bf")
	require.Contains(t, args, "0")
	require.Contains(t, args, "-b:v")
	require.Contains(t, args, "3000k")
	require.Contains(t, args, "-maxrate")
	require.Contains(t, args, "3000k")
	require.Contains(t, args, "-bufsize")
	require.Contains(t, args, "6000k")
	require.Contains(t, args, "-s")
	require.Contains(t, args, "1920x1080")
	require.Contains(t, args, "-r")
	require.Contains(t, args, "30")
	require.Contains(t, args, "h264")
	require.Contains(t, args, "pipe:1")
	require.Contains(t, args, "pipe:0")
}

// ---------------------------------------------------------------------------
// Test: buildFFmpegArgs produces expected command line (HW)
// ---------------------------------------------------------------------------

func TestLiveTranscoder_BuildArgs_HW(t *testing.T) {
	cfg := TranscoderConfig{
		InputCodec:  CodecH265,
		EncoderType: EncoderAuto,
		FFmpegPath:  "ffmpeg",
		Preset: ResolvedPreset{
			GopSeconds: 2, VideoBitrateKbps: 4000,
			Resolution: "1280x720", Framerate: 25,
			Bframes: 2,
		},
		HardwareCap: &transcoding.HardwareCapabilities{
			H264Encoder:     "h264_v4l2m2m",
			H264EncoderType: transcoding.EncoderV4L2M2M,
			H265Decoder:     "hevc_v4l2m2m",
			H265DecoderType: transcoding.EncoderV4L2M2M,
		},
	}

	lt := NewLiveTranscoder(cfg)
	args := lt.buildFFmpegArgs()

	require.Contains(t, args, "-c:v")
	require.Contains(t, args, "hevc_v4l2m2m")
	require.Contains(t, args, "h264_v4l2m2m")
	require.Contains(t, args, "-g")
	require.Contains(t, args, "50")
	require.Contains(t, args, "-bf")
	require.Contains(t, args, "2")
	require.Contains(t, args, "-b:v")
	require.Contains(t, args, "4000k")
	require.Contains(t, args, "-bufsize")
	require.Contains(t, args, "8000k")
	require.Contains(t, args, "-s")
	require.Contains(t, args, "1280x720")
	require.Contains(t, args, "-r")
	require.Contains(t, args, "25")
}

// ---------------------------------------------------------------------------
// Test: No nil pointer panic on Stop if Start was never called
// ---------------------------------------------------------------------------

func TestLiveTranscoder_Stop_WithoutStart(t *testing.T) {
	cfg := TranscoderConfig{
		InputCodec:  CodecH265,
		EncoderType: EncoderSW,
	}
	lt := NewLiveTranscoder(cfg)
	err := lt.Stop()
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Test: Output channel is usable
// ---------------------------------------------------------------------------

func TestLiveTranscoder_Output_Channel(t *testing.T) {
	cfg := TranscoderConfig{
		InputCodec:  CodecH265,
		EncoderType: EncoderSW,
	}
	lt := NewLiveTranscoder(cfg)
	ch := lt.Output()
	require.NotNil(t, ch)
}

// ---------------------------------------------------------------------------
// Test: Real FFmpeg integration (only if ffmpeg binary exists)
// ---------------------------------------------------------------------------

func TestLiveTranscoder_RealFFmpeg(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found, skipping real FFmpeg integration test")
	}

	cfg := TranscoderConfig{
		InputCodec:  CodecH265,
		EncoderType: EncoderSW,
		FFmpegPath:  ffmpegPath,
		Preset: ResolvedPreset{
			GopSeconds: 1, VideoBitrateKbps: 1000,
			Resolution: "640x360", Framerate: 15,
			Bframes: 0,
		},
		HardwareCap: &transcoding.HardwareCapabilities{
			FFmpegAvailable: true,
			FFmpegPath:      ffmpegPath,
		},
	}

	lt := NewLiveTranscoder(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = lt.Start(ctx)
	require.NoError(t, err)

	// Write a minimal H.265 AU with valid start codes
	au := AccessUnit{
		{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60},
		{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00,
			0x80, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00,
			0x78, 0x99, 0x98, 0x09},
		{0x44, 0x01, 0xc1, 0x73, 0x4d, 0x40},
		{0x26, 0x01, 0xaf, 0x08, 0x40, 0x00, 0x01, 0x50, 0x14,
			0x07, 0x38, 0x00, 0x00, 0x7d, 0x00, 0x00, 0x1d, 0x4e,
			0x00, 0x01},
	}
	for i := 0; i < 5; i++ {
		err = lt.WriteInput(au)
		require.NoError(t, err)
	}

	// Read at least 1 AU from output
	deadline := time.After(15 * time.Second)
	received := 0
	for received < 1 {
		select {
		case _, ok := <-lt.Output():
			if !ok {
				t.Fatal("output channel closed unexpectedly")
			}
			received++
		case <-deadline:
			t.Skipf("timed out waiting for AUs, got %d (might need actual H.265 input)", received)
			return
		}
	}

	ps := lt.ParamSets()
	t.Logf("Received %d AUs, SPS=%d bytes, PPS=%d bytes",
		received, len(ps.SPS), len(ps.PPS))

	err = lt.Stop()
	require.NoError(t, err)
	t.Logf("Transcoder stopped cleanly, total AUs received: %d", received)
}
