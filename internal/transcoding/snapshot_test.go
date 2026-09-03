package transcoding

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeFFmpeg creates a fake ffmpeg executable. Behaviors (selected by the
// FAKE_FFMPEG_MODE env var it sets for itself... actually by argv inspection):
//   - default: consume stdin, write fakeJPEG to stdout, exit 0. The invoked
//     args are also written to <dir>/args.txt for assertions.
//   - FAKE_FAIL=1: exit 1 with stderr noise.
func writeFakeFFmpeg(t *testing.T, fakeJPEG []byte) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-ffmpeg")
	body := `#!/bin/sh
echo "$@" > "` + dir + `/args.txt"
cat > /dev/null
if [ -n "$FAKE_FAIL" ]; then
  echo "fake ffmpeg failure" >&2
  exit 1
fi
printf '%s' "` + string(fakeJPEG) + `"
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestDecodeAUToJPEG_SuccessH264(t *testing.T) {
	fakeJPEG := []byte{0xFF, 0xD8, 0xFF, 0xE0, 'f', 'a', 'k', 'e', 0xFF, 0xD9}
	fake := writeFakeFFmpeg(t, fakeJPEG)

	prev := snapshotFFmpegPath
	snapshotFFmpegPath = func() string { return fake }
	t.Cleanup(func() { snapshotFFmpegPath = prev })

	// H.264 access unit: SPS(7) + PPS(8) + IDR(5).
	got, err := DecodeAUToJPEG([][]byte{{0x67, 0x42}, {0x68, 0xCE}, {0x65, 0x01, 0x02}})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(got) != string(fakeJPEG) {
		t.Fatalf("jpeg bytes wrong: %x", got)
	}

	args, err := os.ReadFile(filepath.Join(filepath.Dir(fake), "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "-f h264") {
		t.Fatalf("H.264 AU must select -f h264, args: %s", args)
	}
	if !strings.Contains(string(args), "-frames:v 1") {
		t.Fatalf("must decode exactly one frame, args: %s", args)
	}
}

func TestDecodeAUToJPEG_HEVCSelectsHevcFormat(t *testing.T) {
	fake := writeFakeFFmpeg(t, []byte("jpg"))
	prev := snapshotFFmpegPath
	snapshotFFmpegPath = func() string { return fake }
	t.Cleanup(func() { snapshotFFmpegPath = prev })

	// H.265 access unit: VPS(32) + SPS(33) + PPS(34) + IDR_W_RADL(19).
	if _, err := DecodeAUToJPEG([][]byte{{0x40, 0x01}, {0x42, 0x01}, {0x44, 0x01}, {0x26, 0x01}}); err != nil {
		t.Fatalf("decode: %v", err)
	}
	args, _ := os.ReadFile(filepath.Join(filepath.Dir(fake), "args.txt"))
	if !strings.Contains(string(args), "-f hevc") {
		t.Fatalf("H.265 AU must select -f hevc, args: %s", args)
	}
}

func TestDecodeAUToJPEG_FFmpegUnavailable(t *testing.T) {
	prev := snapshotFFmpegPath
	snapshotFFmpegPath = func() string { return "" }
	t.Cleanup(func() { snapshotFFmpegPath = prev })

	_, err := DecodeAUToJPEG([][]byte{{0x67}})
	if !errors.Is(err, ErrFFmpegUnavailable) {
		t.Fatalf("expected ErrFFmpegUnavailable, got %v", err)
	}
}

func TestDecodeAUToJPEG_FFmpegFailure(t *testing.T) {
	fake := writeFakeFFmpeg(t, []byte("jpg"))
	prev := snapshotFFmpegPath
	snapshotFFmpegPath = func() string { return fake }
	t.Cleanup(func() { snapshotFFmpegPath = prev })
	t.Setenv("FAKE_FAIL", "1")

	_, err := DecodeAUToJPEG([][]byte{{0x67}, {0x68}, {0x65}})
	if err == nil || !strings.Contains(err.Error(), "fake ffmpeg failure") {
		t.Fatalf("ffmpeg failure must propagate with stderr, got %v", err)
	}
}

func TestDecodeAUToJPEG_BoundedOutput(t *testing.T) {
	// A runaway "ffmpeg" writing unbounded stdout must not OOM the NVR: the
	// decoder caps collected bytes and errors out.
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-ffmpeg-runaway")
	body := "#!/bin/sh\ncat > /dev/null\nyes A | head -c 100000000\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := snapshotFFmpegPath
	snapshotFFmpegPath = func() string { return script }
	t.Cleanup(func() { snapshotFFmpegPath = prev })

	_, err := DecodeAUToJPEG([][]byte{{0x67}, {0x68}, {0x65}})
	if err == nil {
		t.Fatal("runaway output must fail, not buffer 100MB")
	}
}
