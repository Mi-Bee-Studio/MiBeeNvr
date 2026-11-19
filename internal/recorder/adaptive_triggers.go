package recorder

// Adaptive-recording triggers on the base recorder: the tracker reset that runs
// on every reconnect, the audio-ring flush and the audio/pixel/motion external
// trigger entry points. Split out of base.go.

import (
	"fmt"
	"time"
)

// resetAdaptive (re)arms the per-connection adaptive state: the tracker and,
// when audio_trigger is configured, the loudness runtime. Called from
// connectAndRecord BEFORE the writeFrames goroutine and the audio RTP
// callbacks start — goroutine creation provides the happens-before, so both
// pointers are stable for the connection's lifetime.
func (b *baseRecorder) resetAdaptive() {
	if b.cfg.Adaptive == nil {
		b.adaptive = nil
		b.audioTrig = nil
		return
	}
	b.adaptive = newAdaptiveTracker(*b.cfg.Adaptive, b.cfg.CameraID, b.log)
	b.audioSparse.Store(false)
	if b.cfg.AudioTrigger != nil && b.cfg.AudioTrigger.Enabled {
		b.audioTrig = NewAudioTriggerRuntime(*b.cfg.AudioTrigger, b.cfg.CameraID, b.log)
		b.log.Info("audio trigger armed",
			"min_dbfs", b.cfg.AudioTrigger.MinDBFS,
			"pre_capture", b.cfg.AudioTrigger.PreCapture.String(),
			"codec", "g711")
	} else {
		b.audioTrig = nil
	}
}

// audioTriggerIngest feeds one decoded G.711 packet (audio RTP callback
// goroutine) into the loudness meter + pre-trigger ring; a loud window
// pokes the tracker's cross-thread audio input.
func (b *baseRecorder) audioTriggerIngest(muLaw bool, payload []byte, sampleRate int, at time.Time) {
	if b.audioTrig == nil || b.adaptive == nil {
		return
	}
	if sampleRate <= 0 {
		sampleRate = 8000
	}
	dur := time.Duration(len(payload)) * time.Second / time.Duration(sampleRate)
	b.audioTrig.Ingest(muLaw, payload, dur, at, func(loudAt time.Time) {
		b.adaptive.audioLoud(loudAt, 0)
	})
}

// audioTriggerMarkWritten flags the newest retained audio packet as written
// by the live path (audio RTP callback goroutine, after a successful
// WriteAudioSample — the audio twin of markTailWritten).
func (b *baseRecorder) audioTriggerMarkWritten() {
	if b.audioTrig != nil {
		b.audioTrig.MarkWritten()
	}
}

// flushAudioRing back-fills pre-trigger audio at a timelapse→normal exit:
// unwritten ring samples are written into the current segment's audio track,
// pts-rebased to the (possibly just back-dated) segStart. Samples predating
// the segment are dropped — at worst a fraction of a second at a rotation
// boundary, mirroring the video flush's rotation semantics.
func (b *baseRecorder) flushAudioRing() {
	if b.audioTrig == nil {
		return
	}
	b.mu.Lock()
	m := b.muxer
	aid := b.audioTrackID
	start := b.segStart
	b.mu.Unlock()
	if m == nil || aid <= 0 {
		b.audioTrig.Drain() // nowhere to write — drop the backlog
		return
	}
	for _, s := range b.audioTrig.Drain() {
		if s.Written {
			continue
		}
		pts := s.At.Sub(start)
		if pts < 0 {
			continue
		}
		if err := m.WriteAudioSample(aid, s.Data, pts, s.Dur); err != nil {
			b.log.Error("failed to write pre-trigger audio sample",
				"camera_id", b.cfg.CameraID, "error", err)
			return
		}
	}
}

// AudioTriggerEvent injects an external audio-activity event (issue #478:
// semantic classifiers run outside the NVR — POST /api/cameras/{id}/adaptive/
// trigger lands here). hold extends how long TIMELAPSE entry stays deferred.
func (b *baseRecorder) AudioTriggerEvent(at time.Time, hold time.Duration) error {
	if b.adaptive == nil {
		return fmt.Errorf("camera %s is not in adaptive recording mode", b.cfg.CameraID)
	}
	b.adaptive.audioLoud(at, hold)
	return nil
}

// PixelTriggerEvent injects a pixel-domain activity confirmation from the
// pixgate CV gate (issue #636): same exit path as the audio trigger (TL
// exit + GOP flush + entry deferral), logged with reason=pixel so field
// data can attribute exits correctly.
func (b *baseRecorder) PixelTriggerEvent(at time.Time, hold time.Duration) error {
	if b.adaptive == nil {
		return fmt.Errorf("camera %s is not in adaptive recording mode", b.cfg.CameraID)
	}
	b.adaptive.audioLoudSrc(at, hold, "pixel")
	return nil
}

// MotionTriggerEvent injects a camera-side ONVIF MotionAlarm confirmation
// (issue #711): same exit path as the audio/pixel triggers, attributed
// reason=onvif_motion so field data can attribute exits correctly.
func (b *baseRecorder) MotionTriggerEvent(at time.Time, hold time.Duration) error {
	if b.adaptive == nil {
		return fmt.Errorf("camera %s is not in adaptive recording mode", b.cfg.CameraID)
	}
	b.adaptive.audioLoudSrc(at, hold, "onvif_motion")
	return nil
}

// ---------------------------------------------------------------------------
// Segment lifecycle (shared)
// ---------------------------------------------------------------------------
