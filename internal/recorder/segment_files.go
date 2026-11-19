package recorder

// Segment file lifecycle for the base recorder: creating the next segment,
// the ambient-audio sidecar, closing the current segment and reacting to a
// storage failure. Split out of base.go.

import (
	"os"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
)

// createNewSegment creates a new MP4 segment via the store, adds the
// codec-specific video track (and audio track if configured), and stores
// the muxer + segment metadata. Returns false on failure.
func (b *baseRecorder) createNewSegment(at time.Time) bool {
	tempPath, finalPath, err := b.store.CreateSegment(b.cfg.CameraID, string(b.driver.segmentFormat()))
	if err != nil {
		b.log.Error("failed to create segment",
			"camera_id", b.cfg.CameraID, "error", err)
		return false
	}
	m := muxer.NewMP4Muxer(tempPath)
	// Segment preallocation (#757): last segment's actual size × headroom,
	// clamped, is the estimate for this one — contiguous extents, no
	// per-append inode metadata transactions. No history yet → skip.
	if est := segmentPreallocEstimate(b.lastSegBytes, b.cfg.Prealloc); est > 0 {
		m.SetPreallocateBytes(est)
	}
	trackID, err := b.driver.addTrack(m, b)
	if err != nil {
		b.log.Error("failed to add video track",
			"camera_id", b.cfg.CameraID, "codec", b.driver.codecLabel(), "error", err)
		os.Remove(tempPath) // clean up empty temp file
		return false
	}
	b.trackID = trackID

	// Add audio track if audio config is available AND the camera keeps audio
	// in recordings (default off — #496 follow-up). Live preview and the audio
	// trigger read the pre-disk path and do not need the track.
	var audioTrackID int
	if a := b.audioSnapshot(); a != nil && len(a.muxerConfig) > 0 && a.codec != "" && b.cfg.AudioInRecordings {
		aID, err := m.AddAudioTrack(a.codec, a.muxerConfig)
		if err != nil {
			b.log.Error("failed to add audio track",
				"camera_id", b.cfg.CameraID, "codec", a.codec, "error", err)
		} else {
			audioTrackID = aID
		}
	}

	// Publish muxer + segStart + audioTrackID together under mu so the audio
	// RTP callback (a different goroutine) observes an aligned triplet. Earlier
	// audioTrackID was assigned outside this lock, so a callback could see a
	// non-zero trackID before muxer was published (torn view) — fixed (#226).
	// segStart anchors to the creating frame's ARRIVAL time (#506).
	b.mu.Lock()
	b.muxer = m
	b.segStart = at
	b.audioTrackID = audioTrackID
	b.ambientFile = b.openAmbientSidecar(finalPath)
	b.mu.Unlock()
	b.curTempPath = tempPath
	b.curFinalPath = finalPath
	b.lastFrameTime = at
	b.frameCount.Store(0)
	return true
}

// openAmbientSidecar opens the raw ambient-audio archive file
// (<final>.g711) when adaptive.ambient_audio + ambient_archive are both on
// (#496 playability option): the merge only carries the atmosphere bed, the
// sidecar keeps the untouched recording for post-production. Called under mu
// from createNewSegment; nil when disabled.
func (b *baseRecorder) openAmbientSidecar(finalPath string) *os.File {
	if b.cfg.Adaptive == nil || !b.cfg.Adaptive.AmbientAudio || !b.cfg.Adaptive.AmbientArchive {
		return nil
	}
	f, err := os.OpenFile(finalPath+".g711", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		b.log.Warn("ambient archive sidecar unavailable", "camera_id", b.cfg.CameraID, "error", err)
		return nil
	}
	return f
}

// writeAmbientArchive appends raw G.711 bytes to the sidecar (audio RTP
// callback goroutine; snapshots the handle under mu like the muxer).
func (b *baseRecorder) writeAmbientArchive(data []byte) {
	if len(data) == 0 {
		return
	}
	b.mu.Lock()
	f := b.ambientFile
	b.mu.Unlock()
	if f == nil {
		return
	}
	if _, err := f.Write(data); err != nil {
		b.log.Warn("ambient archive write failed", "camera_id", b.cfg.CameraID, "error", err)
	}
}

// closeAmbientSidecar closes the sidecar (segment lifecycle paths, under mu).
func (b *baseRecorder) closeAmbientSidecar() {
	if b.ambientFile != nil {
		if err := b.ambientFile.Close(); err != nil {
			b.log.Warn("ambient archive close failed", "camera_id", b.cfg.CameraID, "error", err)
		}
		b.ambientFile = nil
	}
}

// closeCurrentSegment finalizes the current segment via the shared
// FinalizeSegment skeleton (close → rename → insert row → event → metrics;
// issue #1012 block 1). The codec-agnostic bits come from the driver
// (segmentFormat for the recording metadata) and the recorder's own locking
// discipline lives in the hooks (b.mu around muxer/audioTrackID/ambient).
func (b *baseRecorder) closeCurrentSegment() {
	if b.muxer == nil {
		return
	}
	FinalizeSegment(SegmentFinalizeParams{
		Log:          b.log,
		CameraID:     b.cfg.CameraID,
		Store:        b.store,
		DB:           b.cfg.DB,
		EventBus:     b.cfg.EventBus,
		Muxer:        b.muxer,
		TempPath:     b.curTempPath,
		FinalPath:    b.curFinalPath,
		SegStart:     b.segStart,
		FrameCount:   b.frameCount.Load(),
		Format:       b.driver.segmentFormat(),
		LastSegBytes: &b.lastSegBytes,
		ResetRings: func() {
			// The retained GOP/audio rings' `written` markings refer to the
			// segment being closed; a later timelapse-exit flush into a FRESH
			// segment must write those frames instead of skipping them
			// (issue #498: mid-GOP frames existed only in the closed file, so
			// the skip produced missing refs).
			if b.adaptive != nil {
				b.adaptive.clearWritten()
			}
			if b.audioTrig != nil {
				b.audioTrig.ClearWritten()
			}
		},
		BeforeMuxerClose: func() {
			// ambientFile is mu-guarded (the audio RTP callback reads it
			// under mu) — close it under the lock before the muxer (#1001).
			b.mu.Lock()
			b.closeAmbientSidecar()
			b.mu.Unlock()
		},
		OnCloseError: func() {
			b.mu.Lock()
			b.muxer = nil
			b.audioTrackID = 0
			b.mu.Unlock()
			b.curTempPath = ""
			b.curFinalPath = ""
			b.frameCount.Store(0)
		},
		OnMetrics: func(fileSize int64, wall time.Duration) {
			b.recordSegmentCreated()
			if fileSize > 0 {
				b.recordBytes(fileSize)
			}
			// Segment finalize wall time (muxer close + fsync/rename + DB
			// insert): sustained growth is the SD-card/USB degradation
			// early-warning (#469).
			if b.mtrics != nil {
				b.mtrics.ObserveSegmentWrite(b.cfg.CameraID, wall)
			}
		},
		OnFinished: func() {
			b.mu.Lock()
			b.muxer = nil
			b.audioTrackID = 0
			b.closeAmbientSidecar()
			b.mu.Unlock()
			b.curTempPath = ""
			b.curFinalPath = ""
			b.frameCount.Store(0)
		},
	})
}

// handleStorageFailure closes the current segment cleanly without storage I/O
// when storage health check fails. The stream is kept alive; recording resumes
// when storage recovers.
func (b *baseRecorder) handleStorageFailure() {
	if b.muxer != nil {
		// The discarded segment's frames are on nowhere on disk — same
		// written-flag reset as a clean rotation (issue #498).
		if b.adaptive != nil {
			b.adaptive.clearWritten()
		}
		if b.audioTrig != nil {
			b.audioTrig.ClearWritten()
		}
		// ambientFile is mu-guarded (the audio RTP callback reads it under
		// mu) — close it under the lock, same as closeCurrentSegment (#1001).
		b.mu.Lock()
		b.closeAmbientSidecar()
		b.mu.Unlock()
		b.muxer.Close()
		os.Remove(b.curTempPath)
		os.Remove(b.curFinalPath + ".g711")
		b.mu.Lock()
		b.muxer = nil
		b.curTempPath = ""
		b.curFinalPath = ""
		b.audioTrackID = 0
		b.frameCount.Store(0)
		b.mu.Unlock()
	}
	if logNow, ok := shouldLogHealth(b.lastHealthLogAt); ok {
		b.lastHealthLogAt = logNow
		b.log.Warn("storage health failed, skipping recording (stream kept alive)",
			"camera_id", b.cfg.CameraID)
	}
}

// ---------------------------------------------------------------------------
// Metrics helpers (shared)
// ---------------------------------------------------------------------------
