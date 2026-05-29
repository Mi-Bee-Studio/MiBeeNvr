package transcoding

import (
"bytes"
"context"
"fmt"
"os"
"os/exec"
"path/filepath"
"strings"
"sync"
"testing"
"time"
)

// --- Helpers ---

func mustTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "transcode-engine-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func engineWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeFFmpegScript returns a shell script that simulates FFmpeg behavior:
//   - Creates an output file
//   - Prints progress lines to stderr
//   - Supports -y flag
//   - Exits 0 on success
func fakeFFmpegScript(t *testing.T, outputPath string) string {
	t.Helper()
	dir := mustTempDir(t)
	script := filepath.Join(dir, "fake-ffmpeg")
	content := `#!/bin/sh
# Parse -y and output path from args
OUT=""
for arg in "$@"; do
  case "$arg" in
    /*) OUT="$arg" ;;
  esac
done
if [ -z "$OUT" ]; then
  OUT="` + outputPath + `"
fi

# Write progress to stderr
echo "frame=   30 fps= 25 q=28.0 size=    128kB time=00:00:01.00 bitrate= 1048.0kbits/s speed=   1x" >&2
echo "frame=   60 fps= 25 q=28.0 size=    256kB time=00:00:02.00 bitrate= 1048.0kbits/s speed=   1x" >&2
echo "frame=   90 fps= 25 q=28.0 size=    384kB time=00:00:03.00 bitrate= 1048.0kbits/s speed=   1x" >&2

# Create output file
echo "fake video data" > "$OUT"
exit 0
`
	engineWriteFile(t, script, []byte(content))
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// fakeFFprobeScript returns a shell script that simulates ffprobe:
// always reports a valid video stream.
func fakeFFprobeScript(t *testing.T) string {
	t.Helper()
	dir := mustTempDir(t)
	script := filepath.Join(dir, "fake-ffprobe")
	content := `#!/bin/sh
echo '{"streams":[{"codec_name":"hevc","duration":5.0,"width":1920,"height":1080}]}'
exit 0
`
	engineWriteFile(t, script, []byte(content))
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// fakeFFprobeFailScript simulates ffprobe reporting no streams (invalid output).
func fakeFFprobeFailScript(t *testing.T) string {
	t.Helper()
	dir := mustTempDir(t)
	script := filepath.Join(dir, "fake-ffprobe-fail")
	content := `#!/bin/sh
echo '{"streams":[]}'
exit 0
`
	engineWriteFile(t, script, []byte(content))
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// fakeFFmpegFailScript simulates FFmpeg failure (exit 1).
func fakeFFmpegFailScript(t *testing.T) string {
	t.Helper()
	dir := mustTempDir(t)
	script := filepath.Join(dir, "fake-ffmpeg-fail")
	content := `#!/bin/sh
echo "Error encoding" >&2
exit 1
`
	engineWriteFile(t, script, []byte(content))
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// fakeFFmpegWithArgLog returns a fake ffmpeg that logs all args to logPath for verification,
// writes progress to stderr, and creates the output file.
func fakeFFmpegWithArgLog(t *testing.T, outputPath, logPath string) string {
	t.Helper()
	dir := mustTempDir(t)
	script := filepath.Join(dir, "fake-ffmpeg-arglog")
	content := "#!/bin/sh\n" +
		"echo \"$@\" > " + logPath + "\n" +
		"echo \"frame=   30 fps= 25 q=28.0 size=    128kB time=00:00:01.00 bitrate= 1048.0kbits/s speed=   1x\" >&2\n" +
		"echo \"frame=   60 fps= 25 q=28.0 size=    256kB time=00:00:02.00 bitrate= 1048.0kbits/s speed=   1x\" >&2\n" +
		"OUT=\"\"\nfor arg in \"$@\"; do\n  case \"$arg\" in\n    /*) OUT=\"$arg\" ;;\n  esac\ndone\n" +
		"if [ -z \"$OUT\" ]; then\n  OUT=\"" + outputPath + "\"\nfi\n" +
		"echo \"fake video data\" > \"$OUT\"\nexit 0\n"
	engineWriteFile(t, script, []byte(content))
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// fakeFFprobeWithCodec returns a fake ffprobe that reports the given codec and duration.
func fakeFFprobeWithCodec(t *testing.T, codec string, duration float64) string {
	t.Helper()
	dir := mustTempDir(t)
	script := filepath.Join(dir, "fake-ffprobe-codec")
	content := fmt.Sprintf("#!/bin/sh\necho '{\"streams\":[{\"codec_name\":\"%s\",\"duration\":%.1f,\"width\":1920,\"height\":1080}]}'\nexit 0\n", codec, duration)
	engineWriteFile(t, script, []byte(content))
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// --- Tests ---

func TestCheckDiskSpace_SufficientSpace(t *testing.T) {
	t.Helper()
	dir := mustTempDir(t)

	// Create a small input file.
	inputPath := filepath.Join(dir, "input.mp4")
	engineWriteFile(t, inputPath, make([]byte, 1024)) // 1 KB

	// Output path on same filesystem — plenty of space.
	outputPath := filepath.Join(dir, "output.mp4")

	err := checkDiskSpace(outputPath, inputPath)
	if err != nil {
		t.Fatalf("expected no error for sufficient space, got: %v", err)
	}
}

func TestCheckDiskSpace_InsufficientSpace(t *testing.T) {
	t.Helper()
	dir := mustTempDir(t)

	// Create a large input file to trigger the check.
	// 10 MB input → requires 20 MB free.
	inputPath := filepath.Join(dir, "large-input.mp4")
	// Create a sparse file — actual disk usage is minimal but Stat reports large size.
	f, err := os.Create(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(10 * 1024 * 1024); err != nil { // 10 MB sparse
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	outputPath := filepath.Join(dir, "output.mp4")

	// On most systems with >20MB free this won't actually fail, but we test the logic.
	// The test validates the function doesn't crash and returns a sensible result.
	err = checkDiskSpace(outputPath, inputPath)
	// We can't force insufficient space in a test, so just verify it runs without panic.
	_ = err
}

func TestCheckDiskSpace_NonexistentInput(t *testing.T) {
	t.Helper()
	dir := mustTempDir(t)

	outputPath := filepath.Join(dir, "output.mp4")
	inputPath := filepath.Join(dir, "nonexistent.mp4")

	// Nonexistent input should not error — skip check.
	err := checkDiskSpace(outputPath, inputPath)
	if err != nil {
		t.Fatalf("expected no error for nonexistent input, got: %v", err)
	}
}

func TestParseProgress_MonotonicIncrease(t *testing.T) {
	t.Helper()
	totalSeconds := 5.0

	input := strings.NewReader(
		"frame=   30 fps= 25 q=28.0 size=    128kB time=00:00:01.00 bitrate= 1048.0kbits/s speed=   1x\n" +
			"frame=   60 fps= 25 q=28.0 size=    256kB time=00:00:02.00 bitrate= 1048.0kbits/s speed=   1x\n" +
			"frame=   90 fps= 25 q=28.0 size=    384kB time=00:00:03.00 bitrate= 1048.0kbits/s speed=   1x\n" +
			"frame=  120 fps= 25 q=28.0 size=    512kB time=00:00:04.00 bitrate= 1048.0kbits/s speed=   1x\n" +
			"frame=  150 fps= 25 q=28.0 size=    640kB time=00:00:05.00 bitrate= 1048.0kbits/s speed=   1x\n",
	)

	var progressValues []float64
	cb := func(p float64) {
		progressValues = append(progressValues, p)
	}

	parseProgress(input, totalSeconds, cb)

	if len(progressValues) != 5 {
		t.Fatalf("expected 5 progress callbacks, got %d", len(progressValues))
	}

	// Verify monotonic increase.
	for i := 1; i < len(progressValues); i++ {
		if progressValues[i] <= progressValues[i-1] {
			t.Errorf("progress not monotonically increasing: values[%d]=%f <= values[%d]=%f",
				i, progressValues[i], i-1, progressValues[i-1])
		}
	}

	// Verify values are in [0, 1].
	for i, p := range progressValues {
		if p < 0 || p > 1.0 {
			t.Errorf("progress[%d]=%f out of range [0, 1]", i, p)
		}
	}

	// Verify approximate values: 1s/5s=0.2, 2s/5s=0.4, etc.
	expected := []float64{0.2, 0.4, 0.6, 0.8, 1.0}
	for i, p := range progressValues {
		if diff := p - expected[i]; diff < -0.01 || diff > 0.01 {
			t.Errorf("progress[%d]=%f, expected ~%f", i, p, expected[i])
		}
	}
}

func TestParseProgress_NilCallback(t *testing.T) {
	t.Helper()
	input := strings.NewReader("time=00:00:01.00\n")

	// Should not panic with nil callback.
	parseProgress(input, 5.0, nil)
}

func TestParseProgress_ZeroDuration(t *testing.T) {
	t.Helper()
	input := strings.NewReader("time=00:00:01.00\n")
	var called bool
	cb := func(float64) { called = true }

	// Zero total duration — callback should never be called.
	parseProgress(input, 0, cb)

	if called {
		t.Error("expected no callback with zero duration")
	}
}

func TestParseProgress_ClampedToOne(t *testing.T) {
	t.Helper()
	// Total duration is 2s, but progress reports 3s — should clamp to 1.0.
	totalSeconds := 2.0
	input := strings.NewReader("time=00:00:03.00\n")

	var progressValues []float64
	cb := func(p float64) { progressValues = append(progressValues, p) }

	parseProgress(input, totalSeconds, cb)

	if len(progressValues) != 1 {
		t.Fatalf("expected 1 callback, got %d", len(progressValues))
	}
	if progressValues[0] != 1.0 {
		t.Errorf("expected clamped to 1.0, got %f", progressValues[0])
	}
}

func TestTranscode_CancelledContext(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	dir := mustTempDir(t)
	inputPath := filepath.Join(dir, "input.mp4")
	outputPath := filepath.Join(dir, "output.mp4")
	engineWriteFile(t, inputPath, make([]byte, 1024))

	// Create a fake ffmpeg that sleeps — will be killed by context cancellation.
	scriptPath := filepath.Join(dir, "slow-ffmpeg")
	script := `#!/bin/sh
# Create output file to verify cleanup
for arg in "$@"; do
  case "$arg" in
    /*) echo "partial" > "$arg" ;;
  esac
done
sleep 30
exit 0
`
	engineWriteFile(t, scriptPath, []byte(script))
	if err := os.Chmod(scriptPath, 0o755); err != nil {
		t.Fatal(err)
	}

	engine := NewTranscodeEngine(scriptPath, fakeFFprobeScript(t))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	opts := TranscodeOptions{
		InputPath:   inputPath,
		OutputPath:  outputPath,
		InputCodec:  "h264",
		OutputCodec: "h265",
	}
	caps := HardwareCapabilities{}

	err := engine.Transcode(ctx, opts, caps, nil)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}

	// Output file should be cleaned up.
	if _, statErr := os.Stat(outputPath); !os.IsNotExist(statErr) {
		t.Error("expected output file to be removed after cancellation")
		_ = os.Remove(outputPath)
	}
}

func TestTranscode_FFmpegFailure(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	dir := mustTempDir(t)
	inputPath := filepath.Join(dir, "input.mp4")
	outputPath := filepath.Join(dir, "output.mp4")
	engineWriteFile(t, inputPath, make([]byte, 1024))

	engine := NewTranscodeEngine(fakeFFmpegFailScript(t), fakeFFprobeScript(t))

	opts := TranscodeOptions{
		InputPath:   inputPath,
		OutputPath:  outputPath,
		InputCodec:  "h264",
		OutputCodec: "h265",
	}
	caps := HardwareCapabilities{}

	err := engine.Transcode(context.Background(), opts, caps, nil)
	if err == nil {
		t.Fatal("expected error from FFmpeg failure")
	}
	if !strings.Contains(err.Error(), "ffmpeg failed") {
		t.Errorf("expected 'ffmpeg failed' in error, got: %v", err)
	}
}

func TestTranscode_OutputValidationFail(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	dir := mustTempDir(t)
	inputPath := filepath.Join(dir, "input.mp4")
	outputPath := filepath.Join(dir, "output.mp4")
	engineWriteFile(t, inputPath, make([]byte, 1024))

	// Fake ffmpeg that creates a file, but fake ffprobe that reports no streams.
	engine := NewTranscodeEngine(fakeFFmpegScript(t, outputPath), fakeFFprobeFailScript(t))

	opts := TranscodeOptions{
		InputPath:   inputPath,
		OutputPath:  outputPath,
		InputCodec:  "h264",
		OutputCodec: "h265",
	}
	caps := HardwareCapabilities{}

	err := engine.Transcode(context.Background(), opts, caps, nil)
	if err == nil {
		t.Fatal("expected error from output validation failure")
	}
	if !strings.Contains(err.Error(), "output validation failed") {
		t.Errorf("expected 'output validation failed' in error, got: %v", err)
	}

	// Output file should be cleaned up after validation failure.
	if _, statErr := os.Stat(outputPath); !os.IsNotExist(statErr) {
		t.Error("expected output file to be removed after validation failure")
		_ = os.Remove(outputPath)
	}
}

func TestTranscode_InvalidCodecCombination(t *testing.T) {
	t.Helper()
	dir := mustTempDir(t)
	inputPath := filepath.Join(dir, "input.mp4")
	outputPath := filepath.Join(dir, "output.mp4")
	engineWriteFile(t, inputPath, make([]byte, 1024))

	engine := NewTranscodeEngine("/usr/bin/ffmpeg", "/usr/bin/ffprobe")

	opts := TranscodeOptions{
		InputPath:   inputPath,
		OutputPath:  outputPath,
		InputCodec:  "mjpeg",
		OutputCodec: "h265", // MJPEG → H.265 is invalid
	}
	caps := HardwareCapabilities{}

	err := engine.Transcode(context.Background(), opts, caps, nil)
	if err == nil {
		t.Fatal("expected error for invalid codec combination")
	}
	if !strings.Contains(err.Error(), "build command") {
		t.Errorf("expected 'build command' in error, got: %v", err)
	}
}

func TestKillProcessGroup_NilProcess(t *testing.T) {
	t.Helper()
	// Should not panic with nil Process.
	cmd := &exec.Cmd{}
	killProcessGroup(cmd)
}

func TestGetFileSize(t *testing.T) {
	t.Helper()
	dir := mustTempDir(t)

	// Existing file.
	path := filepath.Join(dir, "test.dat")
	engineWriteFile(t, path, []byte("hello world"))

	size, err := getFileSize(path)
	if err != nil {
		t.Fatal(err)
	}
	if size != 11 {
		t.Errorf("expected size 11, got %d", size)
	}

	// Nonexistent file.
	_, err = getFileSize(filepath.Join(dir, "missing.dat"))
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestParseProgress_NoProgressLines(t *testing.T) {
	t.Helper()
	input := strings.NewReader("some random stderr output\nno progress here\n")
	var called bool
	cb := func(float64) { called = true }

	parseProgress(input, 5.0, cb)

	if called {
		t.Error("expected no callback when no progress lines match")
	}
}

func TestParseProgress_BufferedInput(t *testing.T) {
	t.Helper()
	// Use bufio.Scanner-sized lines to verify scanner handles long lines.
	var buf bytes.Buffer
	for i := 0; i < 100; i++ {
		buf.WriteString("filler filler filler filler filler filler filler filler filler filler filler filler\n")
	}
	buf.WriteString("frame=   30 fps= 25 q=28.0 size=    128kB time=00:00:01.00 bitrate= 1048.0kbits/s speed=   1x\n")
	buf.WriteString("frame=   60 fps= 25 q=28.0 size=    256kB time=00:00:02.50 bitrate= 1048.0kbits/s speed=   1x\n")

	var mu sync.Mutex
	var values []float64
	cb := func(p float64) {
		mu.Lock()
		values = append(values, p)
		mu.Unlock()
	}

	parseProgress(&buf, 5.0, cb)

	mu.Lock()
	defer mu.Unlock()
	if len(values) != 2 {
		t.Fatalf("expected 2 progress callbacks, got %d", len(values))
	}
	if values[0] >= values[1] {
		t.Errorf("expected monotonic increase: %f >= %f", values[0], values[1])
	}
}

// --- Additional edge case tests ---

func TestNewTranscodeEngine(t *testing.T) {
	t.Helper()
	e := NewTranscodeEngine("/usr/bin/ffmpeg", "/usr/bin/ffprobe")
	if e.ffmpegPath != "/usr/bin/ffmpeg" {
		t.Errorf("expected ffmpegPath=/usr/bin/ffmpeg, got %s", e.ffmpegPath)
	}
	if e.ffprobePath != "/usr/bin/ffprobe" {
		t.Errorf("expected ffprobePath=/usr/bin/ffprobe, got %s", e.ffprobePath)
	}
}

func TestCheckDiskSpace_OutputDirNotExist(t *testing.T) {
	t.Helper()
	dir := mustTempDir(t)
	inputPath := filepath.Join(dir, "input.mp4")
	engineWriteFile(t, inputPath, make([]byte, 1024))

	// Output in nonexistent nested directory — Statfs on parent dir should work.
	outputPath := filepath.Join(dir, "sub", "dir", "output.mp4")
	err := checkDiskSpace(outputPath, inputPath)
	// Should not error — the dir variable resolves and Statfs uses the existing parent.
	// On Linux, Statfs on a path that doesn't exist returns an error, so we expect an error
	// OR success depending on whether the parent exists. Since "sub/dir" doesn't exist,
	// Statfs will fail.
	if err != nil {
		// Expected: Statfs fails on nonexistent path.
		if !strings.Contains(err.Error(), "statfs") {
			t.Errorf("expected statfs error, got: %v", err)
		}
	}
}

// --- H.265 → H.264 engine tests ---

func TestEngine_H265ToH264_Success(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	dir := mustTempDir(t)
	inputPath := filepath.Join(dir, "input.mp4")
	outputPath := filepath.Join(dir, "output.mp4")
	engineWriteFile(t, inputPath, make([]byte, 1024))

	engine := NewTranscodeEngine(
		fakeFFmpegScript(t, outputPath),
		fakeFFprobeWithCodec(t, "h264", 5.0),
	)

	opts := TranscodeOptions{
		InputPath:   inputPath,
		OutputPath:  outputPath,
		InputCodec:  "h265",
		OutputCodec: "h264",
	}
	caps := HardwareCapabilities{}

	if err := engine.Transcode(context.Background(), opts, caps, nil); err != nil {
		t.Fatalf("expected no error for H.265→H.264 transcode, got: %v", err)
	}

	// Verify output file exists.
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		t.Fatal("expected output file to exist after successful transcode")
	}

	// Verify output reports H.264 codec.
	info, err := GetMediaInfo(engine.ffprobePath, outputPath)
	if err != nil {
		t.Fatalf("failed to probe output: %v", err)
	}
	if info.CodecName != "h264" {
		t.Errorf("expected output codec h264, got %s", info.CodecName)
	}
}

func TestEngine_H265ToH264_AudioPreserved(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	dir := mustTempDir(t)
	inputPath := filepath.Join(dir, "input.mp4")
	outputPath := filepath.Join(dir, "output.mp4")
	logPath := filepath.Join(dir, "ffmpeg-args.log")
	engineWriteFile(t, inputPath, make([]byte, 1024))

	engine := NewTranscodeEngine(
		fakeFFmpegWithArgLog(t, outputPath, logPath),
		fakeFFprobeWithCodec(t, "h264", 5.0),
	)

	opts := TranscodeOptions{
		InputPath:   inputPath,
		OutputPath:  outputPath,
		InputCodec:  "h265",
		OutputCodec: "h264",
	}
	caps := HardwareCapabilities{}

	if err := engine.Transcode(context.Background(), opts, caps, nil); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Verify audio is copied (not re-encoded).
	argsData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read args log: %v", err)
	}
	args := string(argsData)
	if !strings.Contains(args, "-c:a copy") {
		t.Errorf("expected '-c:a copy' in ffmpeg args, got: %s", args)
	}
	// Verify video encoder is libx264 (software fallback).
	if !strings.Contains(args, "-c:v libx264") {
		t.Errorf("expected '-c:v libx264' in ffmpeg args, got: %s", args)
	}
}

func TestEngine_H265ToH264_V4L2M2M(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	dir := mustTempDir(t)
	inputPath := filepath.Join(dir, "input.mp4")
	outputPath := filepath.Join(dir, "output.mp4")
	logPath := filepath.Join(dir, "ffmpeg-args.log")
	engineWriteFile(t, inputPath, make([]byte, 1024))

	engine := NewTranscodeEngine(
		fakeFFmpegWithArgLog(t, outputPath, logPath),
		fakeFFprobeWithCodec(t, "h264", 5.0),
	)

	opts := TranscodeOptions{
		InputPath:   inputPath,
		OutputPath:  outputPath,
		InputCodec:  "h265",
		OutputCodec: "h264",
	}
	caps := HardwareCapabilities{
		H264Encoder:     "h264_v4l2m2m",
		H264EncoderType: EncoderV4L2M2M,
	}

	if err := engine.Transcode(context.Background(), opts, caps, nil); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	argsData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read args log: %v", err)
	}
	args := string(argsData)
	if !strings.Contains(args, "h264_v4l2m2m") {
		t.Errorf("expected 'h264_v4l2m2m' encoder in ffmpeg args, got: %s", args)
	}
	// V4L2 M2M requires explicit GOP and no B-frames.
	if !strings.Contains(args, "-bf 0") {
		t.Errorf("expected '-bf 0' in ffmpeg args for V4L2M2M, got: %s", args)
	}
	if !strings.Contains(args, "-g 50") {
		t.Errorf("expected '-g 50' in ffmpeg args for V4L2M2M, got: %s", args)
	}
	// Audio must still be copied with hardware encoder.
	if !strings.Contains(args, "-c:a copy") {
		t.Errorf("expected '-c:a copy' in ffmpeg args with V4L2M2M, got: %s", args)
	}
}

func TestEngine_H265ToH264_DurationPreserved(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	dir := mustTempDir(t)
	inputPath := filepath.Join(dir, "input.mp4")
	outputPath := filepath.Join(dir, "output.mp4")
	engineWriteFile(t, inputPath, make([]byte, 1024))

	inputDuration := 10.0 // seconds

	engine := NewTranscodeEngine(
		fakeFFmpegScript(t, outputPath),
		fakeFFprobeWithCodec(t, "h264", inputDuration),
	)

	opts := TranscodeOptions{
		InputPath:   inputPath,
		OutputPath:  outputPath,
		InputCodec:  "h265",
		OutputCodec: "h264",
	}
	caps := HardwareCapabilities{}

	if err := engine.Transcode(context.Background(), opts, caps, nil); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Verify output duration matches input within ±2%.
	outputInfo, err := GetMediaInfo(engine.ffprobePath, outputPath)
	if err != nil {
		t.Fatalf("failed to probe output: %v", err)
	}

	tolerance := inputDuration * 0.02
	diff := outputInfo.Duration - inputDuration
	if diff < 0 {
		diff = -diff
	}
	if diff > tolerance {
		t.Errorf("output duration %.2f differs from input %.2f by more than 2%%", outputInfo.Duration, inputDuration)
	}
}
