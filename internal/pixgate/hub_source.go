// hub_source.go — shared-pull sampler feed (#643).
//
// The legacy sampler let ffmpeg open its own RTSP connection to the camera's
// sub stream and decode EVERY frame (the fps=1 filter only drops output, not
// decode work) — ~16% of one core 24/7 on M5, plus a duplicate camera
// connection competing for connection-limited devices.
//
// The hub path instead subscribes to the SAME substream.Source hub the live
// quality=sub egress uses (one shared pull, refcounted), and feeds ffmpeg
// over stdin ONLY the sampled keyframes as Annex-B: decode cost collapses to
// ~SampleFPS frames/sec, there is no network read inside ffmpeg to wedge,
// and the camera never sees a second connection. ffmpeg remains the decoder
// (no CGO-free H.264/H.265 decoder exists in Go) — but it now decodes only
// what the gate actually samples.

package pixgate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

// HubSource is the refcounted sub-stream feed (satisfied by an adapter over
// camera.CameraManager Acquire/ReleaseSubStream). Release must be called
// exactly once per successful acquire.
type HubSource interface {
	Hub() *streamhub.StreamHub
	Release()
}

// ErrHubUnsupportedCodec marks a hub whose keyframes are not H.264/H.265
// (e.g. MJPEG sub streams): the caller falls back to the direct sampler.
var ErrHubUnsupportedCodec = errors.New("pixgate: hub codec not supported for stdin decode")

const (
	// probeWindow bounds the wait for the first keyframe (the hub's cached
	// IDR replay makes this near-instant whenever frames flow at all).
	probeWindow = 5 * time.Second
	// stderrCap bounds ffmpeg's stderr capture (loglevel error keeps it tiny;
	// the cap is a paranoia guard for spammy builds).
	stderrCap = 8 << 10
	// defaultHubBatchWindow is how long one hub-fed ffmpeg batch keeps its
	// stdin open before closing (issue #688): real ffmpeg builds flush
	// decoded rawvideo only at stdin EOF, so the sampler runs in bounded
	// batches — feed keyframes for the window, close stdin, drain the
	// flushed frames, let the run loop spawn the next batch. Spawn cost is
	// ~tens of ms per multi-second batch, and sample latency stays bounded
	// by window + flush delay.
	defaultHubBatchWindow = 5 * time.Second
)

var hubSubSeq atomic.Int64

// stdinFFmpegArgs builds the decode pipeline: raw Annex-B H.264/H.265 on
// stdin, fixed gray grid on stdout. No -nostdin (stdin is the feed), no
// network options.
func stdinFFmpegArgs(codec string) []string {
	return []string{
		"-hide_banner", "-loglevel", "error",
		"-f", codecInputFormat(codec),
		"-i", "pipe:0",
		"-vf", fmt.Sprintf("scale=%d:%d", GridW, GridH),
		"-f", "rawvideo", "-pix_fmt", "gray",
		"pipe:1",
	}
}

func codecInputFormat(codec string) string {
	if codec == "h265" {
		return "hevc"
	}
	return "h264"
}

// probeCodec identifies the stream codec from a keyframe AU's NAL headers:
// H.264 SPS (type 7) vs H.265 SPS (type 33). "" = unsupported (MJPEG etc.).
func probeCodec(au [][]byte) string {
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		switch {
		case nalu[0]&0x1F == 7:
			return "h264"
		case (nalu[0]>>1)&0x3F == 33:
			return "h265"
		}
	}
	return ""
}

// annexB serializes one AU (raw NALUs) with start-code prefixes.
func annexB(au [][]byte) []byte {
	var out []byte
	for _, nalu := range au {
		out = append(out, 0, 0, 0, 1)
		out = append(out, nalu...)
	}
	return out
}

// sampleFramesHub runs one ffmpeg stdin decode to completion for one hub
// acquisition. Keyframe AUs are forwarded at most one per sample interval;
// non-keyframes are dropped (a lone P-frame has no reference chain to decode
// against). The subscription is ALWAYS released before returning.
func sampleFramesHub(ctx context.Context, cfg Config, src HubSource, fps float64, fn func([]byte) bool, frame []byte) (int, error) {
	hub := src.Hub()
	if hub == nil {
		return 0, errors.New("pixgate: hub source carries no hub")
	}

	id := fmt.Sprintf("pixgate-hub-%d", hubSubSeq.Add(1))
	auCh := make(chan model.FrameMsg, 8)
	if err := hub.SubscribeMsg(id, func(m model.FrameMsg) {
		// Never block the hub's drain goroutine — overflow drops (the next
		// keyframe is never far).
		select {
		case auCh <- m:
		default:
		}
	}); err != nil {
		return 0, fmt.Errorf("pixgate: hub subscribe: %w", err)
	}
	// Unified cleanup, LIFO-safe: stop hub sends FIRST (Unsubscribe waits
	// for the drain goroutine, so no callback can race the channel close),
	// then release the forwarder (closing stdin lets the fixture's reader
	// exit), then the watchdog, then the process.
	var cmd *exec.Cmd
	var fwdDone chan struct{}
	var watchStop chan struct{}
	defer func() {
		hub.Unsubscribe(id)
		if fwdDone != nil {
			close(auCh)
			<-fwdDone
		}
		if watchStop != nil {
			close(watchStop)
		}
		if cmd != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	}()

	// Probe phase: wait for the first keyframe (cached-IDR replay) to learn
	// the codec, keeping the AU to seed the forwarder.
	var firstKey model.FrameMsg
	probeCtx, probeCancel := context.WithTimeout(ctx, probeWindow)
	defer probeCancel()
probe:
	for {
		select {
		case m := <-auCh:
			if !m.IsKeyframe || probeCodec(m.AU) == "" {
				continue
			}
			firstKey = m
			break probe
		case <-probeCtx.Done():
			return 0, fmt.Errorf("pixgate: no keyframe within %s (camera offline?)", probeWindow)
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	codec := probeCodec(firstKey.AU)
	if codec == "" {
		// A keyframe whose NALs match neither H.264 nor H.265 (MJPEG sub).
		return 0, ErrHubUnsupportedCodec
	}

	args := stdinFFmpegArgs(codec)
	if cfg.HubFFmpegArgs != nil {
		args = cfg.HubFFmpegArgs(codec)
	}

	var stderr boundedBuf
	cmd = exec.CommandContext(ctx, cfg.FFmpegPath, args...)
	if len(cfg.Env) > 0 {
		cmd.Env = append(os.Environ(), cfg.Env...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 0, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return 0, err
	}

	// Forwarder: seed with the probe keyframe, then throttle live keyframes
	// to the sample rate. Exits when auCh closes (after Unsubscribe), the
	// batch window elapses (#688: closing stdin makes ffmpeg flush its
	// buffered decoded frames), or the stdin write breaks (ffmpeg gone).
	batchWindow := cfg.HubBatchWindow
	if batchWindow <= 0 {
		batchWindow = defaultHubBatchWindow
	}
	fwdDone = make(chan struct{})
	go func() {
		defer close(fwdDone)
		defer stdin.Close()
		interval := time.Duration(float64(time.Second) / fps)
		if interval <= 0 {
			interval = time.Second
		}
		if _, err := stdin.Write(annexB(firstKey.AU)); err != nil {
			return
		}
		last := time.Now()
		batch := time.NewTimer(batchWindow)
		defer batch.Stop()
		for {
			select {
			case m, ok := <-auCh:
				if !ok {
					return
				}
				if !m.IsKeyframe {
					continue
				}
				if time.Since(last) < interval {
					continue
				}
				if _, err := stdin.Write(annexB(m.AU)); err != nil {
					return
				}
				last = time.Now()
			case <-batch.C:
				// Batch window elapsed: close stdin (the deferred Close above)
				// so an EOF-gated ffmpeg flushes its decoded frames (#688).
				return
			}
		}
	}()

	stall := cfg.FrameStallTimeout
	if stall <= 0 {
		stall = 30 * time.Second
	}
	var lastFrameNS atomic.Int64
	var stalled atomic.Bool
	lastFrameNS.Store(time.Now().UnixNano())
	watchStop = make(chan struct{})
	go func() {
		t := time.NewTicker(stall / 3)
		defer t.Stop()
		for {
			select {
			case <-watchStop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if time.Since(time.Unix(0, lastFrameNS.Load())) > stall {
					stalled.Store(true)
					_ = cmd.Process.Kill()
					return
				}
			}
		}
	}()

	n := 0
	for {
		if _, err := io.ReadFull(stdout, frame); err != nil {
			if stalled.Load() {
				return n, fmt.Errorf("no frame for %s — sampler source stalled", stall)
			}
			if ctx.Err() != nil {
				return n, ctx.Err()
			}
			// #688 batch-close: stdin closed at the window boundary → ffmpeg
			// flushed its frames and exited. EOF with a silent stderr is a
			// clean batch completion; stderr content still means a decode
			// failure the caller must see (and back off from).
			if (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) && stderr.String() == "" {
				return n, nil
			}
			return n, fmt.Errorf("ffmpeg stdin decode ended: %w; stderr: %s", err, stderr.String())
		}
		lastFrameNS.Store(time.Now().UnixNano())
		n++
		if !fn(frame) {
			return n, nil
		}
	}
}

// boundedBuf caps captured stderr so a spammy ffmpeg cannot balloon memory.
type boundedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *boundedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() >= stderrCap {
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *boundedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(b.buf.String())
}
