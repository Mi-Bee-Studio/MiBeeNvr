package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// ---------------------------------------------------------------------------
// BenchmarkListRecordings — baseline for covering index optimization (Task 10).
//
// Query pattern benchmarked:
//   SELECT id, camera_id, file_path, format, started_at, ended_at, duration,
//          file_size, frame_count, merged, archived
//   FROM recordings
//   WHERE camera_id = ?
//     AND started_at >= ?
//     AND started_at <= ?
//     AND archived = 0
//   ORDER BY started_at DESC;
//
// Current indexes:
//   idx_recordings_camera   (camera_id)
//   idx_recordings_time     (started_at)
//   idx_recordings_merged   (merged)       — duplicate (lines 142 & 167 of db.go)
//   idx_recordings_archived (archived)
//
// These are separate single-column indexes. SQLite can use at most one
// per query (plus optionally one for ORDER BY), so the current query
// either does a full table scan or uses one index + manual filter for
// the other WHERE clauses.
//
// Recommended covering index columns:
//   (camera_id, started_at, archived) INCLUDE (file_path, format, ended_at,
//    duration, file_size, frame_count, merged, id)
//
// Why:
//   - camera_id:   equality filter (WHERE)
//   - started_at:  range filter + ORDER BY (index-sorted, avoids sort op)
//   - archived:    equality filter (archived=0 is the default)
//   - INCLUDE:     all SELECT columns so SQLite answers entirely from the
//                  index without touching the table.
//
// Note: SQLite (as of 3.45) does NOT support INCLUDE columns. A true
// covering index requires ALL referenced columns in the key. The pragmatic
// alternative is a compound index on (camera_id, started_at, archived) which
// narrows rows efficiently; the table lookup for SELECT columns then hits
// only the filtered rowset. If a full covering index is desired, include
// all 11 columns in the index key directly.
// ---------------------------------------------------------------------------

func BenchmarkListRecordings(b *testing.B) {
	dir := b.TempDir()
	dbPath := filepath.Join(dir, "bench_list.db")
	db, err := New(dbPath)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	if err := db.Init(ctx); err != nil {
		b.Fatal(err)
	}

	// Seed with realistic data: 5 cameras, ~60 days, ~48 recordings/day each
	// Total: 5 * 60 * 48 = 14,400 recordings
	const (
		numCameras       = 5
		numDays          = 60
		recsPerDayPerCam = 48 // every 30 minutes
	)

	now := time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC)
	cameraIDs := make([]string, numCameras)
	for i := range numCameras {
		cameraIDs[i] = fmt.Sprintf("cam-%d", i+1)
	}

	seq := 0
	for _, camID := range cameraIDs {
		for day := range numDays {
			baseTime := now.Add(-time.Duration(numDays-day) * 24 * time.Hour)
			for slot := range recsPerDayPerCam {
				seq++
				startedAt := baseTime.Add(time.Duration(slot) * 30 * time.Minute)
				endedAt := startedAt.Add(30 * time.Minute)
				rec := &model.Recording{
					ID:          fmt.Sprintf("rec-%06d", seq),
					CameraID:    camID,
					FilePath:    fmt.Sprintf("/recordings/%s/%s/seg-%06d.mp4", camID, startedAt.Format("2006-01-02"), seq),
					Format:      model.FormatH264,
					StartedAt:   startedAt,
					EndedAt:     endedAt,
					Duration:    30.0,
					FileSize:    int64(30+seq%100) * 1024 * 1024,
					FrameCount:  30 * 30, // 30fps * 30min
					MergeStatus: model.MergeStatusPending,
				}
				if seq%10 == 0 {
					rec.MergeStatus = model.MergeStatusMerged
				}
				if err := db.InsertRecording(ctx, rec); err != nil {
					b.Fatal(err)
				}
			}
		}
	}
	b.Logf("seeded %d recordings across %d cameras, %d days", seq, numCameras, numDays)

	// Pick a camera in the middle to avoid edge effects
	targetCam := cameraIDs[2] // cam-3
	// Reference time: 30 days ago — middle of data range
	refTime := now.Add(-30 * 24 * time.Hour)

	// Sub-benchmarks for different time ranges
	ranges := []struct {
		name string
		dur  time.Duration
	}{
		{"1day", 24 * time.Hour},
		{"7days", 7 * 24 * time.Hour},
		{"30days", 30 * 24 * time.Hour},
	}

	for _, tr := range ranges {
		b.Run(tr.name, func(b *testing.B) {
			startTime := refTime
			endTime := refTime.Add(tr.dur)
			filter := model.RecordingFilter{
				CameraID:  targetCam,
				StartTime: startTime,
				EndTime:   endTime,
			}
			b.ResetTimer()
			for i := range b.N {
				recs, err := db.ListRecordings(ctx, filter)
				if err != nil {
					b.Fatal(err)
				}
				// Sanity check: we expect some results (not zero)
				if i == 0 {
					perCam := int(tr.dur.Hours() / 0.5)
					b.Logf("%s: got %d recordings (of ~%d expected per camera)",
						tr.name, len(recs), perCam)
				}
			}
		})
	}
}
