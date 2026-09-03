// FFmpeg-gated single-frame snapshot decoding (#657).
//
// H.264/H.265 cameras have no pure-Go decoder (verified — see AGENTS.md), so
// latest-frame snapshots for them decode one cached IDR access unit through
// the OPTIONAL FFmpeg subprocess. This stays inside the transcoding package
// (the sanctioned FFmpeg zone); callers treat ErrFFmpegUnavailable as "not
// supported here" and degrade gracefully — the NVR runs fully without FFmpeg.

package transcoding

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// ErrFFmpegUnavailable is returned when FFmpeg is not installed: callers keep
// their legacy behavior (e.g. the latest-frame endpoint stays 404).
var ErrFFmpegUnavailable = errors.New("transcoding: ffmpeg not available")

// snapshotFFmpegPath resolves the ffmpeg binary ("" = unavailable). Injectable
// for tests — hermetic fake-script fixtures per the anti-flaky test rules.
var snapshotFFmpegPath = func() string {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	return ""
}

const (
	// snapshotDecodeTimeout bounds one frame decode; a hung pipe (broken AU,
	// exotic bitstream) must never wedge the calling handler/trigger.
	snapshotDecodeTimeout = 10 * time.Second
	// maxSnapshotJPEG caps the collected JPEG bytes (a 4K JPEG is ~1-2MB; a
	// runaway ffmpeg must not buffer unbounded output in RAM).
	maxSnapshotJPEG = 4 << 20
)

// DecodeAUToJPEG decodes ONE Annex-B access unit (parameter sets + IDR, NALUs
// without start codes) into a single JPEG image. The codec is detected from
// the parameter-set NAL types (H.264 SPS=7 / H.265 SPS=33).
func DecodeAUToJPEG(au [][]byte) ([]byte, error) {
	ffmpeg := snapshotFFmpegPath()
	if ffmpeg == "" {
		return nil, ErrFFmpegUnavailable
	}
	if len(au) == 0 {
		return nil, errors.New("transcoding: empty access unit")
	}

	format := "h264"
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		if nalu[0]&0x1F == 7 { // H.264 SPS
			format = "h264"
			break
		}
		if (nalu[0]>>1)&0x3F == 33 { // H.265 SPS
			format = "hevc"
			break
		}
	}

	// Reassemble Annex-B with 4-byte start codes.
	var annexb bytes.Buffer
	for _, nalu := range au {
		annexb.WriteString("\x00\x00\x00\x01")
		annexb.Write(nalu)
	}

	cmd := exec.Command(ffmpeg,
		"-hide_banner", "-loglevel", "error",
		"-f", format,
		"-i", "pipe:0",
		"-frames:v", "1",
		"-f", "image2pipe",
		"-c:v", "mjpeg", "-q:v", "3",
		"pipe:1",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	// Own process group BEFORE start: cleanup kills must reach ffmpeg's
	// children too, or they keep the stdio pipes open and deadlock Wait.
	prepareSnapshotProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Watchdog: kill a hung decode so Wait always returns within the budget.
	kill := func() { killSnapshotProcessGroup(cmd) }
	timer := time.AfterFunc(snapshotDecodeTimeout, kill)
	defer timer.Stop()

	writeErr := make(chan error, 1)
	go func() {
		_, err := stdin.Write(annexb.Bytes())
		if cerr := stdin.Close(); err == nil {
			err = cerr
		}
		writeErr <- err
	}()

	var jpeg []byte
	var readErr error
	jpeg, readErr = readBounded(stdout, kill)
	stderrBuf, _ := io.ReadAll(io.LimitReader(stderr, 4<<10))
	waitErr := cmd.Wait()
	<-writeErr

	if readErr != nil {
		return nil, fmt.Errorf("transcoding: snapshot decode: %w", readErr)
	}
	if waitErr != nil {
		detail := string(stderrBuf)
		if detail == "" {
			detail = waitErr.Error()
		}
		return nil, fmt.Errorf("transcoding: ffmpeg frame decode failed: %s", detail)
	}
	if len(jpeg) == 0 {
		return nil, errors.New("transcoding: ffmpeg produced no frame (incomplete access unit?)")
	}
	return jpeg, nil
}

// readBounded collects stdout capped at maxSnapshotJPEG. On overflow it kills
// the producer BEFORE returning — otherwise cmd.Wait would block forever on a
// process stalled writing into a pipe nobody reads. It errors (never
// truncates): a silent truncation would emit a corrupt JPEG downstream.
func readBounded(r io.Reader, kill func()) ([]byte, error) {
	buf := make([]byte, 0, 256<<10)
	tmp := make([]byte, 32<<10)
	for {
		n, err := r.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if len(buf) > maxSnapshotJPEG {
			kill()
			return nil, errors.New("output exceeds cap")
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return buf, nil
			}
			return nil, err
		}
	}
}
