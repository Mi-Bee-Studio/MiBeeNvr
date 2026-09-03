// Package livetranscode provides pure-Go utilities for live transcoding of
// video streams, wrapping FFmpeg as a subprocess for H.265→H.264 conversion.
package livetranscode

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
)

// EncoderType represents the encoder selection strategy.
type EncoderType int

const (
	// EncoderAuto auto-detects whether hardware encoding is available.
	EncoderAuto EncoderType = iota
	// EncoderSW forces software encoding (libx264).
	EncoderSW
	// EncoderHW forces hardware encoding (e.g. h264_v4l2m2m).
	EncoderHW
)

// ResolvedPreset mirrors relay.ResolvedPreset to avoid an import cycle between
// livetranscode and relay. It contains the fully-resolved set of encoding
// parameters after merging a platform preset with per-target overrides.
type ResolvedPreset struct {
	Name             string
	GopSeconds       int
	VideoBitrateKbps int
	AudioBitrateKbps int
	Resolution       string
	Framerate        int
	Profile          string
	Bframes          int
}

// TranscoderConfig holds configuration for the LiveTranscoder.
type TranscoderConfig struct {
	InputCodec  Codec // CodecH265 for now (future: CodecH264)
	EncoderType EncoderType
	FFmpegPath  string
	Preset      ResolvedPreset
	HardwareCap *transcoding.HardwareCapabilities
}

// LiveTranscoder wraps FFmpeg as a subprocess for live H.265→H.264
// transcoding. It pipes raw H.265 bitstream into FFmpeg's stdin and reads
// H.264 Annex-B byte stream from its stdout, splitting it into Access Units
// via AnnexBStreamParser.
//
// Lifecycle:
//
//	lt := NewLiveTranscoder(cfg)
//	lt.Start(ctx)
//	lt.WriteInput(au)
//	aus := <-lt.Output()
//	lt.Stop()
type LiveTranscoder struct {
	cfg           TranscoderConfig
	cmd           *exec.Cmd
	stdinPipe     io.WriteCloser
	outputCh      chan AccessUnit
	paramSets     ParamSets
	paramSetsMu   sync.RWMutex
	cancel        context.CancelFunc
	stopped       atomic.Bool
	inputQueue    chan []byte // bounded queue (cap=30)
	wg            sync.WaitGroup
	encoderName   string
	inputDecoder  string // "" means use -f hevc (SW), otherwise HW decoder name
	outputEncoder string // e.g. "libx264" or "h264_v4l2m2m"

	doneCh    chan struct{}
	monitorWg sync.WaitGroup
}

var ltLogger = slogx.Component("livetranscode")

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

// NewLiveTranscoder creates a new LiveTranscoder with the given config.
// The hardware capabilities are used to select the encoder and decoder.
// If EncoderAuto is set, hardware encoding is used when available.
func NewLiveTranscoder(cfg TranscoderConfig) *LiveTranscoder {
	lt := &LiveTranscoder{
		cfg:        cfg,
		outputCh:   make(chan AccessUnit, 30),
		inputQueue: make(chan []byte, 30),
	}

	inputDecoder, outputEncoder, isHW := lt.resolveEncoders()
	lt.inputDecoder = inputDecoder
	lt.outputEncoder = outputEncoder

	if isHW {
		lt.encoderName = outputEncoder
	} else {
		lt.encoderName = "libx264"
	}

	// Log warning on ARM64 when SW encoder is auto-selected (user didn't
	// explicitly choose EncoderHW). SW encode on ARM is slow but may be the
	// only option when V4L2 lacks encode support (e.g. Amlogic S905X3).
	if runtime.GOARCH == "arm64" && !isHW && cfg.EncoderType == EncoderAuto {
		ltLogger.Warn("using software H.264 encoder on ARM64 — transcoding will be slow; consider hardware encoder if available",
			"encoder", lt.encoderName, "arch", runtime.GOARCH)
	}

	return lt
}

// resolveEncoders determines the input decoder and output encoder names based
// on config and hardware capabilities. Returns (inputDecoder, outputEncoder,
// isHW). inputDecoder is empty for SW decode (-f hevc), otherwise contains
// the decoder name (e.g. "hevc_v4l2m2m").
func (lt *LiveTranscoder) resolveEncoders() (string, string, bool) {
	caps := lt.cfg.HardwareCap
	isHW := false

	switch lt.cfg.EncoderType {
	case EncoderHW:
		isHW = true
	case EncoderSW:
		isHW = false
	case EncoderAuto:
		// Auto-detect: use HW if a hardware H.264 encoder is available
		if caps != nil && caps.H264EncoderType != transcoding.EncoderSoftware && caps.H264Encoder != "" {
			isHW = true
		}
	}

	if isHW && caps != nil {
		// Use the hardware H.264 encoder
		outputEncoder := caps.H264Encoder

		// Use hardware HEVC decoder if available
		if caps.H265Decoder != "" && caps.H265DecoderType != transcoding.EncoderSoftware {
			return caps.H265Decoder, outputEncoder, true
		}
		// HW encoder selected but no HW HEVC decoder — use SW decode
		ltLogger.Warn("hardware encoder selected but no HW HEVC decoder available, using software decode",
			"encoder", outputEncoder)
		return "", outputEncoder, true
	}

	// Software path
	return "", "libx264", false
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start spawns the FFmpeg subprocess and begins transcoding. It creates stdin
// and stdout pipes, starts the writer goroutine (inputQueue → stdin) and
// reader goroutine (stdout → AnnexBStreamParser → outputCh).
//
// The FFmpeg command is:
//
//	[S|W decode] -i pipe:0 [HW|SW encode] -preset ultrafast -tune zerolatency
//	  -g <gop*fps> -keyint_min <same> -sc_threshold 0 -bf <n>
//	  -b:v <kbps>k -maxrate <kbps>k -bufsize <2*kbps>k
//	  -s <resolution> -r <fps> -f h264 pipe:1
func (lt *LiveTranscoder) Start(ctx context.Context) error {
	ffmpegPath := lt.cfg.FFmpegPath
	if ffmpegPath == "" {
		if lt.cfg.HardwareCap != nil && lt.cfg.HardwareCap.FFmpegPath != "" {
			ffmpegPath = lt.cfg.HardwareCap.FFmpegPath
		} else {
			ffmpegPath = "ffmpeg"
		}
	}

	args := lt.buildFFmpegArgs()
	ctx, lt.cancel = context.WithCancel(ctx)

	lt.cmd = exec.CommandContext(ctx, ffmpegPath, args...)
	lt.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Override default SIGKILL with SIGTERM for graceful shutdown
	lt.cmd.Cancel = func() error {
		if lt.cmd.Process != nil {
			return lt.cmd.Process.Signal(syscall.SIGTERM)
		}
		return nil
	}

	// Create stdin pipe
	stdin, err := lt.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("livetranscode: stdin pipe: %w", err)
	}
	lt.stdinPipe = stdin

	// Create stdout pipe
	stdout, err := lt.cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return fmt.Errorf("livetranscode: stdout pipe: %w", err)
	}

	// Start FFmpeg
	if err := lt.cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return fmt.Errorf("livetranscode: start: %w", err)
	}

	// Set low priority (nice 10) — don't starve recording pipeline
	if err := syscall.Setpriority(syscall.PRIO_PROCESS, lt.cmd.Process.Pid, 10); err != nil {
		ltLogger.Warn("failed to set process priority", "pid", lt.cmd.Process.Pid, "error", err)
	}

	// Start writer goroutine: inputQueue → stdin
	lt.wg.Add(1)
	go lt.writerLoop(ctx)

	// Start reader goroutine: stdout → parser → outputCh
	lt.wg.Add(1)
	go lt.readerLoop(ctx, stdout)

	// Start process monitor goroutine — waits for FFmpeg exit and closes doneCh
	lt.doneCh = make(chan struct{})
	lt.monitorWg.Add(1)
	go lt.monitorExit()

	return nil
}

// monitorExit waits for FFmpeg to exit and closes the doneCh to signal
// the transcoder has stopped (either via Stop() or unexpected crash).
func (lt *LiveTranscoder) monitorExit() {
	defer lt.monitorWg.Done()
	if lt.cmd != nil {
		lt.cmd.Wait()
	}
	close(lt.doneCh)
}

// Stop gracefully shuts down the transcoder. Steps:
//  1. Cancel context (triggers SIGTERM via cmd.Cancel)
//  2. Close stdin pipe (signals EOF to FFmpeg)
//  3. Wait for writer/reader goroutines (they exit when context cancels)
//  4. Wait for FFmpeg process with 5s timeout via doneCh
//  5. If timeout: kill process group (-cmd.Process.Pid) via SIGKILL
//  6. Wait for monitor goroutine to finish
func (lt *LiveTranscoder) Stop() error {
	if !lt.stopped.CompareAndSwap(false, true) {
		return nil // already stopped
	}

	// Cancel context → SIGTERM via cmd.Cancel
	if lt.cancel != nil {
		lt.cancel()
	}

	// Close stdin → signal EOF to FFmpeg
	if lt.stdinPipe != nil {
		lt.stdinPipe.Close()
	}

	// Wait for writer/reader goroutines
	lt.wg.Wait()

	// Wait for FFmpeg process with timeout via doneCh
	if lt.doneCh != nil {
		select {
		case <-lt.doneCh:
			// Process exited normally
		case <-time.After(5 * time.Second):
			// Timeout — force kill process group
			if lt.cmd != nil && lt.cmd.Process != nil {
				ltLogger.Warn("FFmpeg did not exit after SIGTERM, sending SIGKILL",
					"pid", lt.cmd.Process.Pid)
				killProcessGroup(lt.cmd)
			}
			<-lt.doneCh // wait for process after kill
		}
	}

	// Wait for monitor goroutine
	lt.monitorWg.Wait()

	return nil
}

// ---------------------------------------------------------------------------
// Input / Output
// ---------------------------------------------------------------------------

// WriteInput writes an H.265 Access Unit to the transcoder's input queue.
// Each NALU in the AU gets an Annex-B start code (00 00 00 01) prepended.
//
// The input queue is bounded (capacity 30). If the queue is full, the oldest
// AU is dropped and a warning is logged. This prevents the caller from
// blocking on slow transcoding.
func (lt *LiveTranscoder) WriteInput(au AccessUnit) error {
	if lt.stopped.Load() {
		return fmt.Errorf("livetranscode: transcoder stopped")
	}

	// Build flat byte buffer with Annex-B start codes
	buf := estimateAnnexBBuf(au)
	for _, nalu := range au {
		buf = append(buf, 0x00, 0x00, 0x00, 0x01)
		buf = append(buf, nalu...)
	}

	// Non-blocking send with drop-oldest-on-overflow
	select {
	case lt.inputQueue <- buf:
		return nil
	default:
		// Channel full — drain oldest entry, then retry
		ltLogger.Warn("input queue full, dropping oldest AU",
			"capacity", cap(lt.inputQueue))
		select {
		case <-lt.inputQueue:
			// drained oldest
		default:
		}
		select {
		case lt.inputQueue <- buf:
			return nil
		default:
			return fmt.Errorf("livetranscode: input queue still full after drain")
		}
	}
}

// Output returns a receive-only channel of parsed H.264 Access Units from
// the FFmpeg stdout.
func (lt *LiveTranscoder) Output() <-chan AccessUnit {
	return lt.outputCh
}

// ParamSets returns the latest SPS/PPS (and VPS for H.265) observed in the
// transcoder output stream. Thread-safe.
func (lt *LiveTranscoder) ParamSets() ParamSets {
	lt.paramSetsMu.RLock()
	defer lt.paramSetsMu.RUnlock()
	return lt.paramSets
}

// EncoderName returns the selected encoder name for status reporting.
// Returns e.g. "libx264" or "h264_v4l2m2m".
func (lt *LiveTranscoder) EncoderName() string {
	return lt.encoderName
}

// Done returns a channel that is closed when the underlying FFmpeg process
// exits (either gracefully or due to a crash). Returns nil if Start() has
// not been called yet.
func (lt *LiveTranscoder) Done() <-chan struct{} {
	return lt.doneCh
}

// PresetResolution returns the configured output resolution for status reporting.
func (lt *LiveTranscoder) PresetResolution() string {
	return lt.cfg.Preset.Resolution
}

// ---------------------------------------------------------------------------
// Internal: FFmpeg command building
// ---------------------------------------------------------------------------

// buildFFmpegArgs constructs the FFmpeg argument list for live transcoding.
func (lt *LiveTranscoder) buildFFmpegArgs() []string {
	var args []string

	// Input — raw bitstream via pipe
	if lt.inputDecoder != "" {
		// HW decoder
		args = append(args, "-c:v", lt.inputDecoder)
	} else {
		// SW decoder via format demuxer
		switch lt.cfg.InputCodec {
		case CodecH265:
			args = append(args, "-f", "hevc")
		default:
			args = append(args, "-f", "hevc")
		}
	}
	args = append(args, "-i", "pipe:0")

	// Output encoder
	args = append(args, "-c:v", lt.outputEncoder)

	// Live encoding flags
	args = append(args, "-preset", "ultrafast")
	args = append(args, "-tune", "zerolatency")

	// GOP control: force keyframe interval to GopSeconds
	gop := lt.cfg.Preset.GopSeconds * lt.cfg.Preset.Framerate
	args = append(args, "-g", strconv.Itoa(gop))
	args = append(args, "-keyint_min", strconv.Itoa(gop))
	args = append(args, "-sc_threshold", "0")

	// B-frames (0 for max compatibility)
	args = append(args, "-bf", strconv.Itoa(lt.cfg.Preset.Bframes))

	// Bitrate control
	bitrate := lt.cfg.Preset.VideoBitrateKbps
	args = append(args, "-b:v", fmt.Sprintf("%dk", bitrate))
	args = append(args, "-maxrate", fmt.Sprintf("%dk", bitrate))
	args = append(args, "-bufsize", fmt.Sprintf("%dk", bitrate*2))

	// Resolution
	if lt.cfg.Preset.Resolution != "" {
		args = append(args, "-s", lt.cfg.Preset.Resolution)
	}

	// Framerate
	args = append(args, "-r", strconv.Itoa(lt.cfg.Preset.Framerate))

	// Output — raw H.264 Annex-B byte stream to stdout
	args = append(args, "-f", "h264")
	args = append(args, "pipe:1")

	return args
}

// ---------------------------------------------------------------------------
// Internal: writer / reader goroutines
// ---------------------------------------------------------------------------

// writerLoop reads from the input queue and writes to FFmpeg stdin.
// Exits when context is cancelled or stdin write fails.
func (lt *LiveTranscoder) writerLoop(ctx context.Context) {
	defer lt.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-lt.inputQueue:
			if !ok {
				return
			}
			if _, err := lt.stdinPipe.Write(data); err != nil {
				ltLogger.Warn("failed to write to FFmpeg stdin", "error", err)
				return
			}
		}
	}
}

// readerLoop reads from FFmpeg stdout, parses the Annex-B byte stream into
// Access Units, and sends them to the output channel. It updates paramSets
// as new SPS/PPS are observed.
func (lt *LiveTranscoder) readerLoop(ctx context.Context, stdout io.ReadCloser) {
	defer lt.wg.Done()
	defer stdout.Close()

	parser := NewAnnexBStreamParser(CodecH264)
	buf := make([]byte, 65536) // 64KB read buffer

	for {
		select {
		case <-ctx.Done():
			// Flush remaining AUs
			if aus := parser.Flush(); len(aus) > 0 {
				lt.sendAUs(aus)
				lt.updateParamSets(parser)
			}
			return
		default:
		}

		n, err := stdout.Read(buf)
		if n > 0 {
			aus := parser.Feed(buf[:n])
			if len(aus) > 0 {
				lt.sendAUs(aus)
				lt.updateParamSets(parser)
			}
		}
		if err != nil {
			if err != io.EOF {
				ltLogger.Warn("FFmpeg stdout read error", "error", err)
			}
			// Flush remaining AUs
			if aus := parser.Flush(); len(aus) > 0 {
				lt.sendAUs(aus)
				lt.updateParamSets(parser)
			}
			return
		}
	}
}

// sendAUs sends access units to the output channel. Non-blocking — drops if
// the channel is full.
func (lt *LiveTranscoder) sendAUs(aus []AccessUnit) {
	for _, au := range aus {
		select {
		case lt.outputCh <- au:
		default:
			ltLogger.Warn("output channel full, dropping AU")
		}
	}
}

// updateParamSets extracts the latest parameter sets from the stream parser.
func (lt *LiveTranscoder) updateParamSets(parser *AnnexBStreamParser) {
	ps := parser.ParamSets()
	lt.paramSetsMu.Lock()
	lt.paramSets = ps
	lt.paramSetsMu.Unlock()
}

// ---------------------------------------------------------------------------
// Internal: helpers
// ---------------------------------------------------------------------------

// estimateAnnexBBuf pre-allocates a buffer large enough for the Annex-B
// encoded Access Unit: 4 bytes start code per NALU + NALU data.
func estimateAnnexBBuf(au AccessUnit) []byte {
	total := 0
	for _, nalu := range au {
		total += 4 + len(nalu) // 4-byte start code + NALU
	}
	if total == 0 {
		total = 4096
	}
	return make([]byte, 0, total)
}

// killProcessGroup sends SIGKILL to the entire FFmpeg process group to ensure
// no orphaned child processes remain on the system.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		ltLogger.Warn("failed to kill process group",
			"pid", cmd.Process.Pid, "error", err)
	}
}
