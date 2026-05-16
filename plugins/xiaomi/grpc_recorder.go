// SPDX-License-Identifier: MIT
//
// Streaming Xiaomi camera recorder that sends NAL frames over gRPC
// instead of writing to local muxer/storage.

package xiaomi

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/rand"
	"runtime"
	"sync"
	"time"

	gen "github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen"
)

var streamLogger = slog.Default().With("component", "xiaomi-stream-recorder")

const (
	defaultStreamMaxBackoff  = 60 * time.Second
	defaultStreamInitBackoff = 1 * time.Second
)

// FrameSender sends encoded frames to a consumer (typically a gRPC stream).
type FrameSender interface {
	SendFrame(ctx context.Context, frame *gen.Frame) error
}

// StreamRecorderConfig holds configuration for the gRPC streaming recorder.
type StreamRecorderConfig struct {
	CameraID    string
	DID         string
	Model       string
	CloudCfg    XiaomiCloudConfig
	MaxBackoff  time.Duration
	InitBackoff time.Duration
}

// StreamRecorder records H.264/H.265 video from a Xiaomi camera via MISS protocol
// and sends each NAL frame over gRPC via a FrameSender callback. It has zero
// internal package dependencies — no muxer, storage, or metrics.
type StreamRecorder struct {
	cfg    StreamRecorderConfig
	sender FrameSender

	mu     sync.Mutex
	status gen.RecorderState
	cancel context.CancelFunc
	done   chan struct{}

	codec   gen.Codec
	sps     []byte
	pps     []byte
	vps     []byte
	codecOK bool

	streamStart time.Time
	frameCount  int64
	bytesTotal  int64
}

// NewStreamRecorder creates a new streaming Xiaomi recorder.
func NewStreamRecorder(cfg StreamRecorderConfig, sender FrameSender) *StreamRecorder {
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = defaultStreamMaxBackoff
	}
	if cfg.InitBackoff == 0 {
		cfg.InitBackoff = defaultStreamInitBackoff
	}
	return &StreamRecorder{
		cfg:    cfg,
		sender: sender,
		status: gen.RecorderState_RECORDER_STATE_STOPPED,
	}
}

// Start begins recording from the Xiaomi camera in a background goroutine.
// The provided context controls the recorder lifecycle.
func (r *StreamRecorder) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == gen.RecorderState_RECORDER_STATE_RECORDING || r.status == gen.RecorderState_RECORDER_STATE_CONNECTING {
		return fmt.Errorf("stream recorder for %q already running", r.cfg.CameraID)
	}
	// Derive a child context so Stop() can cancel independently.
	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.done = make(chan struct{})
	r.status = gen.RecorderState_RECORDER_STATE_RECORDING
	go r.run(ctx)
	return nil
}

// Stop terminates the recording goroutine and waits for it to finish.
func (r *StreamRecorder) Stop() error {
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	if r.done != nil {
		<-r.done
	}
	return nil
}

// Status returns the current recorder state.
func (r *StreamRecorder) Status() gen.RecorderState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *StreamRecorder) setStatus(s gen.RecorderState) {
	r.mu.Lock()
	r.status = s
	r.mu.Unlock()
}

func (r *StreamRecorder) run(ctx context.Context) {
	defer func() {
		if panicErr := recover(); panicErr != nil {
			buf := make([]byte, 4096)
			buf = buf[:runtime.Stack(buf, false)]
			streamLogger.Error("PANIC recovered in run", "camera_id", r.cfg.CameraID, "panic", panicErr, "stack", string(buf))
		}
	}()
	defer close(r.done)
	defer r.setStatus(gen.RecorderState_RECORDER_STATE_STOPPED)

	backoff := r.cfg.InitBackoff
	for {
		missURL, err := ResolveMISSURL(r.cfg.CloudCfg, r.cfg.DID, r.cfg.Model)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			streamLogger.Error("failed to resolve MISS URL, retrying", "camera_id", r.cfg.CameraID, "error", err, "backoff", backoff)
			r.setStatus(gen.RecorderState_RECORDER_STATE_ERROR)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = nextBackoff(backoff, r.cfg.MaxBackoff)
			continue
		}

		err = r.connectAndRecord(ctx, missURL)
		if ctx.Err() != nil {
			return
		}
		streamLogger.Error("connection error, reconnecting", "camera_id", r.cfg.CameraID, "error", err, "backoff", backoff)
		r.setStatus(gen.RecorderState_RECORDER_STATE_ERROR)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = nextBackoff(backoff, r.cfg.MaxBackoff)
	}
}

func (r *StreamRecorder) connectAndRecord(ctx context.Context, missURL string) error {
	client, err := NewMISSClient(missURL)
	if err != nil {
		return fmt.Errorf("miss connect: %w", err)
	}
	defer client.Conn.Close()

	if err := client.StartMedia("", "hd"); err != nil {
		return fmt.Errorf("miss start media: %w", err)
	}
	defer func() {
		_ = client.StopMedia()
	}()

	r.setStatus(gen.RecorderState_RECORDER_STATE_RECORDING)

	r.codecOK = false
	r.sps = nil
	r.pps = nil
	r.vps = nil
	r.streamStart = time.Now()
	r.frameCount = 0
	r.bytesTotal = 0

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		pkt, err := client.ReadPacket()
		if err != nil {
			return fmt.Errorf("miss read: %w", err)
		}

		if pkt.CodecID != missCodecH264 && pkt.CodecID != missCodecH265 {
			continue
		}

		if !r.codecOK {
			switch pkt.CodecID {
			case missCodecH264:
				r.codec = gen.Codec_CODEC_H264
			case missCodecH265:
				r.codec = gen.Codec_CODEC_H265
			}
			r.codecOK = true
			streamLogger.Info("codec detected", "camera_id", r.cfg.CameraID, "codec", r.codec)
		}

		nalus := splitAnnexBNALUs(pkt.Payload)
		for _, nalu := range nalus {
			r.processNALU(ctx, nalu)
		}
	}
}

func (r *StreamRecorder) processNALU(ctx context.Context, nalu []byte) {
	if len(nalu) == 0 {
		return
	}
	switch r.codec {
	case gen.Codec_CODEC_H264:
		r.processH264NALU(ctx, nalu)
	case gen.Codec_CODEC_H265:
		r.processH265NALU(ctx, nalu)
	}
}

func (r *StreamRecorder) processH264NALU(ctx context.Context, nalu []byte) {
	naluType := nalu[0] & 0x1F

	switch naluType {
	case 7:
		if r.sps != nil && !bytes.Equal(r.sps, nalu) {
			streamLogger.Info("SPS change detected", "camera_id", r.cfg.CameraID)
		}
		r.sps = append([]byte(nil), nalu...)
		r.sendCodecInfoFrame(ctx, nalu, "sps_hex")
		return
	case 8:
		if r.pps != nil && !bytes.Equal(r.pps, nalu) {
			streamLogger.Info("PPS change detected", "camera_id", r.cfg.CameraID)
		}
		r.pps = append([]byte(nil), nalu...)
		r.sendCodecInfoFrame(ctx, nalu, "pps_hex")
		return
	}

	if naluType != 5 && naluType != 1 {
		return
	}

	frame := &gen.Frame{
		Data:        nalu,
		PtsNs:       r.ptsNanoseconds(),
		IsIdr:       naluType == 5,
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: false,
	}
	r.sendFrame(ctx, frame)
}

func (r *StreamRecorder) processH265NALU(ctx context.Context, nalu []byte) {
	naluType := (nalu[0] >> 1) & 0x3F

	switch naluType {
	case 32:
		if r.vps != nil && !bytes.Equal(r.vps, nalu) {
			streamLogger.Info("VPS change detected", "camera_id", r.cfg.CameraID)
		}
		r.vps = append([]byte(nil), nalu...)
		r.sendCodecInfoFrame(ctx, nalu, "vps_hex")
		return
	case 33:
		if r.sps != nil && !bytes.Equal(r.sps, nalu) {
			streamLogger.Info("SPS change detected", "camera_id", r.cfg.CameraID)
		}
		r.sps = append([]byte(nil), nalu...)
		r.sendCodecInfoFrame(ctx, nalu, "sps_hex")
		return
	case 34:
		if r.pps != nil && !bytes.Equal(r.pps, nalu) {
			streamLogger.Info("PPS change detected", "camera_id", r.cfg.CameraID)
		}
		r.pps = append([]byte(nil), nalu...)
		r.sendCodecInfoFrame(ctx, nalu, "pps_hex")
		return
	}

	if naluType >= 32 {
		return
	}

	isIDR := naluType == 19 || naluType == 20

	frame := &gen.Frame{
		Data:        nalu,
		PtsNs:       r.ptsNanoseconds(),
		IsIdr:       isIDR,
		Codec:       gen.Codec_CODEC_H265,
		IsCodecInfo: false,
	}
	r.sendFrame(ctx, frame)
}

func (r *StreamRecorder) sendCodecInfoFrame(ctx context.Context, nalu []byte, keyName string) {
	frame := &gen.Frame{
		Data:        nalu,
		PtsNs:       r.ptsNanoseconds(),
		IsIdr:       false,
		Codec:       r.codec,
		IsCodecInfo: true,
		Extra:       map[string]string{keyName: hex.EncodeToString(nalu)},
	}
	r.sendFrame(ctx, frame)
}

func (r *StreamRecorder) sendFrame(ctx context.Context, frame *gen.Frame) {
	if err := r.sender.SendFrame(ctx, frame); err != nil {
		streamLogger.Error("failed to send frame", "camera_id", r.cfg.CameraID, "error", err)
		return
	}
	r.frameCount++
	r.bytesTotal += int64(len(frame.Data))
}

func (r *StreamRecorder) ptsNanoseconds() uint64 {
	if r.streamStart.IsZero() {
		return 0
	}
	return uint64(time.Since(r.streamStart).Nanoseconds())
}

func nextBackoff(current, max time.Duration) time.Duration {
	jitter := time.Duration(rand.Int63n(int64(current / 2)))
	next := current*2 + jitter
	if next > max {
		return max
	}
	return next
}
