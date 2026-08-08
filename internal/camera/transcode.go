package camera

// This file holds the post-recording transcoding integration: the setter for
// the (optional) TranscodeManager and EnqueueTranscode, which the recorder
// pipeline calls after a segment completes to enqueue an async transcode task
// when per-camera transcoding is configured.
//
// Extracted from manager.go (#225).

import (
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
)

// SetTranscodeManager sets the transcoding manager for post-recording enqueue.
// Can be called with nil to disable transcoding. Thread-safe.
func (cm *CameraManager) SetTranscodeManager(m *transcoding.TranscodeManager) {
	cm.configMu.Lock()
	cm.transcodeMgr = m
	cm.configMu.Unlock()
}

// EnqueueTranscode checks per-camera transcoding config and enqueues a
// transcoding task if enabled. Non-blocking — runs the enqueue in a goroutine.
func (cm *CameraManager) EnqueueTranscode(cameraID, recordingID, inputPath, inputFormat string) {
	cm.configMu.Lock()
	tm := cm.transcodeMgr
	cm.configMu.Unlock()

	if tm == nil {
		return
	}

	// Resolve per-camera transcoding config
	tcfg := cm.cfg.ResolveTranscodingConfig(cameraID)
	if tcfg == nil || !tcfg.Enabled {
		return
	}

	// Determine target codec (default to h264)
	targetCodec := tcfg.TargetCodec
	if targetCodec == "" {
		targetCodec = "h264"
	}

	// Non-blocking enqueue — don't block recording pipeline
	bitrate := tcfg.Bitrate
	crf := tcfg.CRF
	go func() {
		if err := tm.EnqueueRecording(cameraID, recordingID, inputPath, inputFormat, targetCodec, bitrate, crf); err != nil {
			logger.Warn("failed to enqueue transcode task",
				"camera_id", cameraID,
				"recording_id", recordingID,
				"error", err)
		}
	}()
}
