// Package recorder: segment_finalize.go — shared MP4 segment finalize skeleton.
//
// Every MP4-writing recorder used to carry its own copy of the
// close→rename→insert-row→event→metrics sequence (baseRecorder, XiaomiRecorder,
// GB28181Recorder, IngestRecorder — plus MJPEG/HTTP-JPEG variants for the
// insert sub-step). Those copies drifted apart repeatedly (#1003 was the last
// field incident); this file is the single skeleton they all delegate to,
// with per-recorder differences expressed as hooks.
//
// Ownership map (issue #1012 block 1):
//
//	FinalizeSegment          — baseRecorder.closeCurrentSegment,
//	                           XiaomiRecorder.closeCurrentSegment,
//	                           GB28181Recorder.closeCurrentSegmentLocked,
//	                           IngestRecorder.closeCurrentSegmentLocked
//	insertSegmentRow         — the skeleton + MJPEGRecorder and
//		HTTPJPEGRecorder (dir-form/AVI recorders that keep their own close
//		sequence but share the row shape)
//	publishSegmentCompleted  — the skeleton + MJPEGRecorder (HTTP-JPEG keeps
//	                           its own publish: its Encoding field is always
//	                           mjpeg while Format varies with AVI mode)

package recorder

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// segmentStoreCloser is the minimal storage surface the finalize skeleton
// needs. Satisfied by recorder.SegmentStore and xiaomi.SegmentStore.
type segmentStoreCloser interface {
	CloseSegment(tempPath, finalPath string) error
}

// segmentRowInserter is the minimal DB surface the finalize skeleton needs.
// Satisfied by recorder.RecordingDB and xiaomi.RecordingDB.
type segmentRowInserter interface {
	InsertRecordingWithRetry(ctx context.Context, r *model.Recording, maxRetries int, backoff time.Duration) error
}

// SegmentFinalizeParams carries the per-segment state and behavioral hooks for
// FinalizeSegment. The skeleton owns the shared sequence; hooks carry the
// per-recorder differences (locking discipline is hook-owned on purpose:
// baseRecorder guards its resets with b.mu while the GB28181/Ingest variants
// run under the caller-held r.mu).
type SegmentFinalizeParams struct {
	Log      *slog.Logger
	CameraID string

	Store    segmentStoreCloser
	DB       segmentRowInserter // nil = skip the recording row
	EventBus *event.EventBus    // nil = row inserted but event not published (warns)

	Muxer      io.Closer
	TempPath   string
	FinalPath  string
	SegStart   time.Time
	FrameCount int64
	Format     model.Format

	// LastSegBytes, when non-nil, receives the final file size to feed the
	// next segment's preallocation estimate (#757).
	LastSegBytes *int64

	// ResetRings runs first: retained-GOP / audio-ring written-flag resets
	// whose markings refer to the segment being closed, so a later
	// timelapse-exit flush into a FRESH segment writes those frames instead
	// of skipping them (issue #498). Optional.
	ResetRings func()

	// BeforeMuxerClose runs after ResetRings, still before Muxer.Close — the
	// mu-guarded ambient-sidecar close on baseRecorder. The finalize
	// wall-time anchor is captured between the two hooks, matching the
	// original baseRecorder ordering. Optional.
	BeforeMuxerClose func()

	// OnCloseError resets recorder-local state after a failed muxer close;
	// the skeleton removes the temp file, calls this, and stops — no rename,
	// row, event, or metrics follow a failed close. Optional.
	OnCloseError func()

	// OnMetrics runs after a successful finalize when frames were written
	// and the final file exists; fileSize is the final size and wall the
	// total finalize duration. The #469 wall-time observation is opt-in
	// here (baseRecorder only). Optional.
	OnMetrics func(fileSize int64, wall time.Duration)

	// OnFinished resets recorder-local segment state after a successful
	// finalize. Runs last. Optional.
	OnFinished func()
}

// FinalizeSegment finalizes the current MP4 segment: closes the muxer,
// atomically renames temp→final, inserts the recording row into the DB,
// publishes a SegmentCompleted event, and reports metrics via OnMetrics.
//
// Callers gate on "a segment is open" (typically muxer != nil) before calling;
// the skeleton assumes Muxer is non-nil.
func FinalizeSegment(p SegmentFinalizeParams) {
	if p.ResetRings != nil {
		p.ResetRings()
	}
	finishStart := time.Now()
	if p.BeforeMuxerClose != nil {
		p.BeforeMuxerClose()
	}
	if err := p.Muxer.Close(); err != nil {
		p.Log.Error("failed to close muxer",
			"camera_id", p.CameraID, "error", err)
		if p.TempPath != "" {
			os.Remove(p.TempPath)
		}
		if p.OnCloseError != nil {
			p.OnCloseError()
		}
		return
	}

	// Atomic rename: temp → final
	if p.TempPath != "" && p.FinalPath != "" {
		if err := p.Store.CloseSegment(p.TempPath, p.FinalPath); err != nil {
			p.Log.Error("failed to close segment",
				"camera_id", p.CameraID, "error", err)
		}
	}

	// Segment-loss gate: when the final file never materialized (vanished
	// temp — e.g. the startup temp-cleanup scan racing segment creation,
	// 2026-09-08 incident), any DB row would be a permanent 404 entry.
	// Skip the insert, event, and metrics instead.
	var fileSize int64
	var recordingID string
	finalExists := false
	if p.FinalPath != "" {
		if info, err := os.Stat(p.FinalPath); err == nil {
			finalExists = true
			fileSize = info.Size()
			// Feed the next segment's preallocation estimate (#757).
			if p.LastSegBytes != nil {
				*p.LastSegBytes = fileSize
			}
		} else {
			p.Log.Warn("segment file missing after finalize — recording row skipped",
				"camera_id", p.CameraID, "path", p.FinalPath)
		}
	}
	if p.DB != nil && finalExists {
		recordingID = insertSegmentRow(p.Log, p.DB, p.CameraID, p.FinalPath,
			p.Format, p.SegStart, int(p.FrameCount), fileSize)
	}

	publishSegmentCompleted(p.Log, p.EventBus, p.CameraID, p.FinalPath,
		p.Format, p.SegStart, fileSize, recordingID)

	// Segment counters/bytes only when the segment carried frames and the
	// final file exists. The wall-time observation (#469: sustained finalize
	// growth is the SD-card/USB degradation early-warning) is hook-owned.
	if p.FrameCount > 0 && finalExists && p.OnMetrics != nil {
		p.OnMetrics(fileSize, time.Since(finishStart))
	}

	if p.OnFinished != nil {
		p.OnFinished()
	}
}

// insertSegmentRow builds the recording row for a finalized segment and
// inserts it with the shared retry policy, returning the row ID. The row ID
// is derived from the insert-time wall clock, so it stays unique per row even
// when the insert itself fails.
func insertSegmentRow(
	log *slog.Logger,
	db segmentRowInserter,
	cameraID, finalPath string,
	format model.Format,
	segStart time.Time,
	frameCount int,
	fileSize int64,
) string {
	now := time.Now()
	duration := now.Sub(segStart).Seconds()
	rec := &model.Recording{
		ID:         strconv.FormatInt(now.UnixNano(), 10),
		CameraID:   cameraID,
		FilePath:   finalPath,
		Format:     format,
		StartedAt:  segStart,
		EndedAt:    now,
		Duration:   duration,
		FrameCount: frameCount,
		FileSize:   fileSize,
	}
	if err := db.InsertRecordingWithRetry(context.Background(), rec, dbInsertRetries, dbInsertBackoff); err != nil {
		log.Error("failed to insert recording",
			"camera_id", cameraID, "error", err)
	}
	return rec.ID
}

// publishSegmentCompleted publishes the SegmentCompleted event for a
// recording row. No-op when recordingID is empty (no row was inserted); a nil
// EventBus logs a warning instead of failing silently — production always
// wires the bus, so reaching this branch means a wiring bug.
func publishSegmentCompleted(
	log *slog.Logger,
	bus *event.EventBus,
	cameraID, filePath string,
	format model.Format,
	segStart time.Time,
	fileSize int64,
	recordingID string,
) {
	if recordingID == "" {
		return
	}
	if bus == nil {
		log.Warn("SegmentCompleted NOT published — EventBus is nil",
			"camera_id", cameraID, "recording_id", recordingID)
		return
	}
	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
		CameraID:    cameraID,
		FilePath:    filePath,
		Format:      string(format),
		Encoding:    string(format),
		StartedAt:   segStart.Format(time.RFC3339Nano),
		EndedAt:     time.Now().Format(time.RFC3339Nano),
		FileSize:    fileSize,
		RecordingID: recordingID,
	})
}
