package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// seedBenchDB opens a fresh DB, runs Init, and returns it ready for writes.
// Uses a per-benchmark temp dir so each run starts from an empty DB.
func seedBenchDB(b *testing.B) (*DB, context.Context) {
	b.Helper()
	dir := b.TempDir()
	dbPath := filepath.Join(dir, "bench_write.db")
	db, err := New(dbPath)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	if err := db.Init(ctx); err != nil {
		b.Fatal(err)
	}
	return db, ctx
}

// makeRecording builds a representative recording for insertion.
func makeRecording(seq int, camID string, startedAt time.Time) *model.Recording {
	return &model.Recording{
		ID:         fmt.Sprintf("rec-%06d", seq),
		CameraID:   camID,
		FilePath:   fmt.Sprintf("/recordings/%s/%s/seg-%06d.mp4", camID, startedAt.Format("2006-01-02"), seq),
		Format:     model.FormatH264,
		StartedAt:  startedAt,
		EndedAt:    startedAt.Add(30 * time.Second),
		Duration:   30.0,
		FileSize:   int64(5+seq%50) * 1024 * 1024,
		FrameCount: 30 * 30,
	}
}

// ---------------------------------------------------------------------------
// BenchmarkInsertRecording — the recording hot-path write rate.
//
// Each closed segment triggers one InsertRecording (db_recording.go). At scale
// (many cameras, short segments) this is the dominant DB write. The benchmark
// measures single-row INSERT throughput including all index maintenance, which
// is sensitive to the number of indexes on the recordings table (each extra
// index adds write amplification). Use this to detect regressions when adding
// indexes or to confirm the redundant-index DROP (db.go Init) improves write rate.
// ---------------------------------------------------------------------------

func BenchmarkInsertRecording(b *testing.B) {
	db, ctx := seedBenchDB(b)
	defer db.Close()

	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	b.ResetTimer()
	for i := range b.N {
		rec := makeRecording(i+1, "cam-1", now.Add(time.Duration(i)*30*time.Second))
		if err := db.InsertRecording(ctx, rec); err != nil {
			b.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------------------
// BenchmarkSetMergeStatus — measures the A2 batching improvement.
//
// Compares the per-row UPDATE loop (old) against the batched WHERE id IN (...)
// (new). Each sub-benchmark seeds N recordings then flips their merge_status.
// The improvement scales with N: old did N ExecContext round-trips, new does
// ceil(N/500).
// ---------------------------------------------------------------------------

func BenchmarkSetMergeStatus(b *testing.B) {
	sizes := []int{50, 200, 1000}
	for _, n := range sizes {
		b.Run(fmt.Sprintf("batch=%d", n), func(b *testing.B) {
			for range b.N {
				b.StopTimer()
				db, ctx := seedBenchDB(b)
				ids := make([]string, n)
				now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
				for j := range n {
					ids[j] = fmt.Sprintf("rec-%06d", j+1)
					if err := db.InsertRecording(ctx, makeRecording(j+1, "cam-1", now.Add(time.Duration(j)*30*time.Second))); err != nil {
						b.Fatal(err)
					}
				}
				b.StartTimer()

				if err := db.SetMergeStatus(ctx, ids, model.MergeStatusMerging); err != nil {
					b.Fatal(err)
				}

				b.StopTimer()
				db.Close()
				b.StartTimer()
			}
		})
	}
}

// ---------------------------------------------------------------------------
// BenchmarkUpdateMergeProgressBatch — measures the A3 batching improvement.
//
// Simulates a timelapse progress tick updating N segments at once. Old path
// called UpdateMergeProgress once per segment (N statements); new path uses
// UpdateMergeProgressBatch (ceil(N/500) statements in one tx).
// ---------------------------------------------------------------------------

func BenchmarkUpdateMergeProgressBatch(b *testing.B) {
	const n = 200
	for range b.N {
		b.StopTimer()
		db, ctx := seedBenchDB(b)
		ids := make([]string, n)
		now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
		for j := range n {
			ids[j] = fmt.Sprintf("rec-%06d", j+1)
			if err := db.InsertRecording(ctx, makeRecording(j+1, "cam-1", now.Add(time.Duration(j)*30*time.Second))); err != nil {
				b.Fatal(err)
			}
		}
		b.StartTimer()

		if err := db.UpdateMergeProgressBatch(ctx, ids, 42); err != nil {
			b.Fatal(err)
		}

		b.StopTimer()
		db.Close()
		b.StartTimer()
	}
}

// ---------------------------------------------------------------------------
// BenchmarkCountRecordingsWithFilter — measures the count-query cost that
// accompanies every ListRecordings page request. At 50万-100万 rows this
// becomes the dominant cost (full filtered-set COUNT). Use to evaluate the
// C3 double-query elimination / count-caching work.
// ---------------------------------------------------------------------------

func BenchmarkCountRecordingsWithFilter(b *testing.B) {
	dir := b.TempDir()
	dbPath := filepath.Join(dir, "bench_count.db")
	db, err := New(dbPath)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.Init(ctx); err != nil {
		b.Fatal(err)
	}

	// Seed a larger volume: 5 cameras × 60 days × 48/day = 14,400 rows
	const numCameras, numDays, recsPerDay = 5, 60, 48
	now := time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC)
	seq := 0
	for c := range numCameras {
		camID := fmt.Sprintf("cam-%d", c+1)
		for d := range numDays {
			base := now.Add(-time.Duration(numDays-d) * 24 * time.Hour)
			for s := range recsPerDay {
				seq++
				if err := db.InsertRecording(ctx, makeRecording(seq, camID, base.Add(time.Duration(s)*30*time.Minute))); err != nil {
					b.Fatal(err)
				}
			}
		}
	}
	b.Logf("seeded %d recordings", seq)

	filter := model.RecordingFilter{
		CameraID:  "cam-3",
		StartTime: now.Add(-30 * 24 * time.Hour),
		EndTime:   now.Add(-20 * 24 * time.Hour),
	}
	b.ResetTimer()
	for range b.N {
		if _, err := db.CountRecordingsWithFilter(ctx, filter); err != nil {
			b.Fatal(err)
		}
	}
}
