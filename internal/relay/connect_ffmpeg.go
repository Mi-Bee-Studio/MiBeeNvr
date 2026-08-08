package relay

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// connectViaFFmpeg spawns an FFmpeg subprocess that pulls from the camera's
// stream URL and pushes to the target. Used when use_ffmpeg is enabled —
// some strict RTMP receivers (e.g. Douyu Live Companion) reject the native
// Go RTMP writer. The subprocess lifecycle is tied to ctx.
func (t *PushTarget) connectViaFFmpeg(ctx context.Context) error {
	ffmpegPath := t.ffmpegPath
	if ffmpegPath == "" {
		var err error
		ffmpegPath, err = exec.LookPath("ffmpeg")
		if err != nil {
			t.setStatus(StatusError, "FFmpeg not found for relay")
			return errPermanent
		}
	}

	// Resolve source URL: explicit override > auto-resolved from camera.
	sourceURL := t.Config.SourceURL
	if sourceURL == "" && t.streamURLProvider != nil {
		sourceURL = t.streamURLProvider(t.CameraID)
	}
	if sourceURL == "" {
		t.setStatus(StatusError, "cannot resolve source URL for FFmpeg relay")
		return errPermanent
	}
	targetURL := t.Config.URL

	args := []string{"-hide_banner", "-loglevel", "info"}
	if strings.HasPrefix(sourceURL, "rtsp://") {
		args = append(args, "-rtsp_transport", "tcp")
	}
	args = append(args, "-i", sourceURL, "-c", "copy")
	// Correct the FLV onMetaData frame rate to the source's ACTUAL fps.
	// RTSP cameras frequently declare an inflated fps in their SDP (e.g. 30)
	// while actually emitting fewer frames (e.g. 15). With plain -c copy,
	// FFmpeg writes the SDP-declared (wrong) fps into the FLV onMetaData, and
	// strict RTMP receivers (e.g. Douyu Live Companion) initialize a decoder
	// for the declared rate, then freeze after a few seconds of half-rate
	// input. -r only rewrites the metadata fps here -- frame data and PTS
	// intervals stay identical (verified in production: 66ms @ 15fps).
	if fps := probeSourceVideoFPS(ffmpegPath, sourceURL); fps > 0 {
		args = append(args, "-r", strconv.Itoa(fps))
		engineLogger.Info("ffmpeg relay corrected output fps",
			"camera_id", t.CameraID, "target_id", t.Config.ID, "fps", fps)
	}
	// I/O timeout (15s) for both RTSP input and RTMP output sockets. Without
	// this, a mid-stream RTMP rejection (e.g. Douyu auth token expiring hours
	// into a healthy stream) causes FFmpeg's muxer thread to die while the main
	// thread is blocked on RTSP read — a silent deadlock that stalls the relay
	// indefinitely and freezes the receiver's last frame forever.
	//
	// Pass via TWO channels because FFmpeg 7.1.5's RTMP handler doesn't honor
	// -rw_timeout as a bare CLI flag (it's silently consumed by the FLV muxer
	// instead of the TCP socket): as a CLI flag AND as a URL query parameter
	// (which the RTMP URL parser DOES forward to the URLContext/I/O layer).
	args = append(args, "-rw_timeout", "15000000")
	if strings.Contains(targetURL, "?") {
		targetURL += "&rw_timeout=15000000"
	} else {
		targetURL += "?rw_timeout=15000000"
	}
	args = append(args, "-f", "flv", "-flvflags", "no_duration_filesize", targetURL)

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.setStatus(StatusError, "ffmpeg stderr pipe: "+err.Error())
		return err
	}

	t.setStatus(StatusConnecting, "ffmpeg relay starting")
	engineLogger.Info("relay target starting FFmpeg relay",
		"camera_id", t.CameraID, "target_id", t.Config.ID,
		"source", sourceURL, "target", targetURL)

	if err := cmd.Start(); err != nil {
		t.setStatus(StatusError, "ffmpeg start: "+err.Error())
		return err
	}

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			if line != "" && !strings.HasPrefix(line, "frame=") {
				engineLogger.Info("ffmpeg relay stderr",
					"camera_id", t.CameraID, "target_id", t.Config.ID,
					"line", line)
			}
		}
	}()

	t.setStatus(StatusStreaming, "")
	engineLogger.Info("relay target streaming (FFmpeg)",
		"camera_id", t.CameraID, "target_id", t.Config.ID,
		"protocol", t.Config.Protocol, "url", targetURL)

	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if waitErr != nil {
		return fmt.Errorf("ffmpeg relay exited: %w", waitErr)
	}
	return nil
}

// probeSourceVideoFPS returns the actual video frame rate of a source stream
// via ffprobe's r_frame_rate. Returns 0 on any failure (the caller then skips
// -r and falls back to FFmpeg's default, preserving previous behavior).
func probeSourceVideoFPS(ffmpegPath, sourceURL string) int {
	ffprobePath := "ffprobe"
	if ffmpegPath != "" {
		cand := filepath.Join(filepath.Dir(ffmpegPath), "ffprobe")
		if _, err := exec.LookPath(cand); err == nil {
			ffprobePath = cand
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffprobePath, "-hide_banner",
		"-of", "csv=p=0", "-select_streams", "v:0",
		"-show_entries", "stream=r_frame_rate", sourceURL).Output()
	if err != nil {
		return 0
	}
	// r_frame_rate is "num/den", e.g. "15/1".
	numStr, denStr, ok := strings.Cut(strings.TrimSpace(string(out)), "/")
	if !ok {
		return 0
	}
	num, err1 := strconv.Atoi(numStr)
	den, err2 := strconv.Atoi(denStr)
	if err1 != nil || err2 != nil || den == 0 || num <= 0 {
		return 0
	}
	return num / den
}
