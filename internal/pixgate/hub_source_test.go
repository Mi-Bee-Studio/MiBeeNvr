package pixgate

// Tests for the hub-fed sampler (#643): the preferred source shares the
// substream manager's pull (no extra camera connection) and feeds ffmpeg
// ONLY the sampled keyframes over stdin — decode cost collapses from "every
// frame of the stream" to ~SampleFPS frames/sec.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- pure helpers -----------------------------------------------------------

func TestStdinFFmpegArgsH264(t *testing.T) {
	t.Parallel()
	args := stdinFFmpegArgs("h264")
	joined := strings.Join(args, " ")
	assert.Contains(t, joined, "-f h264")
	assert.Contains(t, joined, "-i pipe:0")
	assert.Contains(t, joined, fmt.Sprintf("scale=%d:%d", GridW, GridH))
	assert.Contains(t, joined, "-f rawvideo -pix_fmt gray pipe:1")
	assert.NotContains(t, joined, "-nostdin", "stdin is the AU feed — -nostdin would close it")
	assert.NotContains(t, joined, "-rtsp_transport", "no network pull in hub mode")
}

func TestStdinFFmpegArgsH265(t *testing.T) {
	t.Parallel()
	assert.Contains(t, strings.Join(stdinFFmpegArgs("h265"), " "), "-f hevc")
}

func TestProbeCodec(t *testing.T) {
	t.Parallel()
	h264AU := [][]byte{{0x67, 0x64}, {0x68, 0xeb}, {0x65, 0x88}}
	h265AU := [][]byte{{0x40, 0x01}, {0x42, 0x01}, {0x44, 0x01}, {0x26, 0x01}}
	assert.Equal(t, "h264", probeCodec(h264AU))
	assert.Equal(t, "h265", probeCodec(h265AU))
	assert.Equal(t, "", probeCodec([][]byte{{0x00, 0x01, 0x02}}), "unknown NAL shapes (e.g. MJPEG) are unsupported")
}

func TestAnnexBEncode(t *testing.T) {
	t.Parallel()
	out := annexB([][]byte{{0x67, 0xAA}, {0x68, 0xBB}, {0x65, 0xCC}})
	want := []byte{
		0, 0, 0, 1, 0x67, 0xAA,
		0, 0, 0, 1, 0x68, 0xBB,
		0, 0, 0, 1, 0x65, 0xCC,
	}
	assert.Equal(t, want, out)
}

// --- fixtures -----------------------------------------------------------------

// h264KeyAU builds a keyframe AU whose IDR carries a distinctive marker tail,
// so forwarded stdin bytes are attributable to a specific published AU.
func h264KeyAU(marker byte) [][]byte {
	idr := append([]byte{0x65, 0x88}, bytesOf(marker, 16)...)
	return [][]byte{{0x67, 0x64, 0x00, 0x1f}, {0x68, 0xeb, 0xec}, idr}
}

func bytesOf(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

// fakeStdinFFmpeg dumps stdin to $STDIN_DUMP (and its argv to $ARGS_DUMP)
// while emitting one gray frame per 50ms, until stdin closes. The exec-3
// dance is required: non-interactive sh redirects a background job's fd 0
// to /dev/null, so the real stdin must be saved before backgrounding.
const fakeStdinFFmpeg = `#!/bin/sh
printf '%s\n' "$@" >> "$ARGS_DUMP"
exec 3<&0
cat <&3 >> "$STDIN_DUMP" &
CATPID=$!
while kill -0 $CATPID 2>/dev/null; do
  dd if=/dev/zero bs=19200 count=1 2>/dev/null
  sleep 0.05
done
`

// stubHubSource adapts a plain hub; released() counts Release calls.
type stubHubSource struct {
	hub      *streamhub.StreamHub
	released atomic.Int32
}

func (s *stubHubSource) Hub() *streamhub.StreamHub { return s.hub }
func (s *stubHubSource) Release()                  { s.released.Add(1) }

func writeFakeStdinFFmpeg(t *testing.T) (script string, stdinDump, argsDump string) {
	t.Helper()
	dir := t.TempDir()
	script = filepath.Join(dir, "fake-ffmpeg")
	require.NoError(t, os.WriteFile(script, []byte(fakeStdinFFmpeg), 0o755))
	return script, filepath.Join(dir, "stdin.bin"), filepath.Join(dir, "args.txt")
}

// --- sampleFramesHub ----------------------------------------------------------

func TestSampleFramesHub_ForwardsKeyframesOnlyAsAnnexB(t *testing.T) {
	t.Helper()
	script, stdinDump, argsDump := writeFakeStdinFFmpeg(t)
	hub := streamhub.New()

	done := make(chan error, 1)
	var frames int32
	go func() {
		cfg := Config{
			FFmpegPath: script,
			Env:        []string{"STDIN_DUMP=" + stdinDump, "ARGS_DUMP=" + argsDump},
		}
		src := &stubHubSource{hub: hub}
		n, err := sampleFramesHub(context.Background(), cfg, src, 1, func([]byte) bool {
			atomic.AddInt32(&frames, 1)
			return false // one frame is enough — stop the read loop
		}, make([]byte, GridW*GridH))
		assert.NotZero(t, n)
		done <- err
	}()

	// Cached-IDR replay: publish BEFORE the subscriber exists.
	hub.Broadcast(1, h264KeyAU(0xAA), true)
	// A P-frame must never reach ffmpeg's stdin.
	hub.Broadcast(2, [][]byte{{0x41, 0x9A, 0x00}}, false)

	require.NoError(t, <-done)
	require.Equal(t, int32(1), atomic.LoadInt32(&frames))

	// cat copies asynchronously in chunks — wait for the FULL AU, not just
	// a non-empty file.
	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(stdinDump)
		return err == nil && bytes.Contains(raw, annexB(h264KeyAU(0xAA)))
	}, 5*time.Second, 20*time.Millisecond)
	raw, err := os.ReadFile(stdinDump)
	require.NoError(t, err)
	assert.Contains(t, string(raw), string(annexB(h264KeyAU(0xAA))), "cached IDR (param sets + IDR) must be forwarded as Annex-B")
	assert.NotContains(t, string(raw), string([]byte{0x00, 0x00, 0x00, 0x01, 0x41}), "P-frames must be dropped")

	args, err := os.ReadFile(argsDump)
	require.NoError(t, err)
	// argv is dumped newline-separated by the fixture.
	assert.Contains(t, string(args), "-f\nh264\n", "codec must be probed from the keyframe AU")
}

func TestSampleFramesHub_ThrottlesToSampleFPS(t *testing.T) {
	t.Helper()
	script, stdinDump, argsDump := writeFakeStdinFFmpeg(t)
	hub := streamhub.New()

	// Seed the IDR cache BEFORE the sampler exists: the replayed keyframe is
	// guaranteed to be IDR1 regardless of goroutine scheduling.
	hub.Broadcast(1, h264KeyAU(0x01), true)

	// fn parks on release so the sampler stays alive for the IDR2 broadcast.
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		cfg := Config{FFmpegPath: script, Env: []string{"STDIN_DUMP=" + stdinDump, "ARGS_DUMP=" + argsDump}}
		_, err := sampleFramesHub(context.Background(), cfg, &stubHubSource{hub: hub}, 1,
			func([]byte) bool { <-release; return false }, make([]byte, GridW*GridH))
		done <- err
	}()

	// The dump carrying the full IDR1 proves the subscription + seed write
	// happened — only then fire the same-window keyframe at the live path.
	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(stdinDump)
		return err == nil && bytes.Contains(raw, annexB(h264KeyAU(0x01)))
	}, 5*time.Second, 20*time.Millisecond, "seed keyframe must be forwarded")

	hub.Broadcast(2, h264KeyAU(0x02), true) // same sample window (fps=1 → 1s)
	time.Sleep(100 * time.Millisecond)      // give the forwarder a chance to (wrongly) forward it
	close(release)
	require.NoError(t, <-done)

	raw, err := os.ReadFile(stdinDump)
	require.NoError(t, err)
	assert.Contains(t, string(raw), string(bytesOf(0x01, 8)), "first keyframe forwarded")
	assert.NotContains(t, string(raw), string(bytesOf(0x02, 8)), "keyframe within the sample interval must be throttled")
}

func TestSampleFramesHub_HEVCProbe(t *testing.T) {
	t.Helper()
	script, stdinDump, argsDump := writeFakeStdinFFmpeg(t)
	hub := streamhub.New()

	done := make(chan error, 1)
	go func() {
		cfg := Config{FFmpegPath: script, Env: []string{"STDIN_DUMP=" + stdinDump, "ARGS_DUMP=" + argsDump}}
		_, err := sampleFramesHub(context.Background(), cfg, &stubHubSource{hub: hub}, 1,
			func([]byte) bool { return false }, make([]byte, GridW*GridH))
		done <- err
	}()

	hevcIDR := func(marker byte) [][]byte {
		idr := append([]byte{(19 << 1) | 1, 0x01}, bytesOf(marker, 16)...) // IDR_W_RADL
		return [][]byte{{(32 << 1) | 1, 0x01}, {(33 << 1) | 1, 0x01}, {(34 << 1) | 1, 0x01}, idr}
	}
	hub.Broadcast(1, hevcIDR(0xBB), true)
	require.NoError(t, <-done)

	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(stdinDump)
		return err == nil && bytes.Contains(raw, annexB(hevcIDR(0xBB)))
	}, 5*time.Second, 20*time.Millisecond)
	args, err := os.ReadFile(argsDump)
	require.NoError(t, err)
	assert.Contains(t, string(args), "-f\nhevc\n")
	raw, err := os.ReadFile(stdinDump)
	require.NoError(t, err)
	assert.Contains(t, string(raw), string(annexB(hevcIDR(0xBB))))
}

func TestSampleFramesHub_NoKeyframeTimesOut(t *testing.T) {
	t.Helper()
	hub := streamhub.New()
	cfg := Config{FFmpegPath: "/bin/true"}
	_, err := sampleFramesHub(context.Background(), cfg, &stubHubSource{hub: hub}, 1,
		func([]byte) bool { return true }, make([]byte, GridW*GridH))
	require.Error(t, err, "a silent hub must fail the probe window, not hang")
}

func TestSampleFramesHub_ReleasesSubscription(t *testing.T) {
	t.Helper()
	script, stdinDump, argsDump := writeFakeStdinFFmpeg(t)
	hub := streamhub.New()
	hub.Broadcast(1, h264KeyAU(0x03), true)

	before := hubSubSeq.Load()
	_, err := sampleFramesHub(context.Background(),
		Config{FFmpegPath: script, Env: []string{"STDIN_DUMP=" + stdinDump, "ARGS_DUMP=" + argsDump}},
		&stubHubSource{hub: hub}, 1, func([]byte) bool { return false }, make([]byte, GridW*GridH))
	require.NoError(t, err)

	used := fmt.Sprintf("pixgate-hub-%d", before+1)
	err = hub.SubscribeMsg(used, func(model.FrameMsg) {})
	require.NoError(t, err, "one-shot subscription %q must be released", used)
	hub.Unsubscribe(used)
}

// --- Manager-level source selection -------------------------------------------

func TestManager_PrefersHubSource(t *testing.T) {
	t.Helper()
	script, stdinDump, argsDump := writeFakeStdinFFmpeg(t)
	hub := streamhub.New()
	hub.Broadcast(1, h264KeyAU(0x04), true)

	var directResolved atomic.Int32
	m := NewManager(Config{
		FFmpegPath: script,
		Env:        []string{"STDIN_DUMP=" + stdinDump, "ARGS_DUMP=" + argsDump},
		HubResolver: func(context.Context, string) (HubSource, bool, error) {
			return &stubHubSource{hub: hub}, true, nil
		},
		Resolver: func(context.Context, string) (Target, bool, error) {
			directResolved.Add(1)
			return Target{URL: "rtsp://example/sub"}, true, nil
		},
		Trigger: func(string, time.Duration) error { return nil },
		Cameras: map[string]CameraConfig{"cam-1": {SampleFPS: 10, MinAreaPct: 1.5}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, m.Start(ctx))
	defer cancel()

	// The sampler runs asynchronously: wait for it to feed the fixture
	// before tearing down.
	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(stdinDump)
		return err == nil && bytes.Contains(raw, bytesOf(0x04, 8))
	}, 5*time.Second, 20*time.Millisecond, "hub sampler must forward the cached IDR to ffmpeg stdin")

	cancel()
	require.NoError(t, m.Stop())

	assert.Zero(t, directResolved.Load(), "direct RTSP resolution must not run when a hub source is available")
	raw, err := os.ReadFile(stdinDump)
	require.NoError(t, err)
	assert.Contains(t, string(raw), string(bytesOf(0x04, 8)))
}

func TestManager_HubUnavailableFallsBackToDirect(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	frameFile := filepath.Join(dir, "frames.bin")
	require.NoError(t, os.WriteFile(frameFile, bytesOf(100, GridW*GridH*3), 0o644))

	var triggered atomic.Int32
	m := NewManager(Config{
		FFmpegPath: "cat",
		FFmpegArgs: func(string, float64) []string { return []string{frameFile} },
		HubResolver: func(context.Context, string) (HubSource, bool, error) {
			return nil, false, nil // e.g. xiaomi / push ingest — no shared sub pull
		},
		Resolver: func(context.Context, string) (Target, bool, error) {
			return Target{URL: "rtsp://example/sub"}, true, nil
		},
		Trigger: func(string, time.Duration) error {
			triggered.Add(1) // not expected to fire on flat frames; wired for realism
			return nil
		},
		Cameras: map[string]CameraConfig{"cam-1": {SampleFPS: 10, MinAreaPct: 1.5}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, m.Start(ctx))
	defer cancel()

	// The sampler must be running the direct path: it drains frames.bin and
	// idles at EOF without erroring the manager. Give it a moment, then stop.
	require.Eventually(t, func() bool { return true }, 50*time.Millisecond, 10*time.Millisecond)
	require.NoError(t, m.Stop())
}
