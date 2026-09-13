package merge

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// Bucket-retention suite (#764): cameras oscillating between two parameter
// sets (xiaomi HD/SD quality flapping during reconnect storms) flip SPS/PPS
// every reconnect. With a single live bucket, every flip finalized the old
// bucket and created a new one — a micro-merge storm of tiny output files
// and constant metadata churn. Retention keeps the last N buckets keyed by
// parameter set so flipping BACK appends to the existing bucket.

// Variant parameter sets: the PPS stays raw in the compatibility key (no
// semantic parser), so a one-byte PPS difference is enough to make the two
// keys alternate deterministically.
var (
	retentionSPS  = []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	retentionPPSA = []byte{0x68, 0xce, 0x38, 0x80}
	retentionPPSB = []byte{0x68, 0xce, 0x38, 0x81}
)

// createAndInsertSegmentWithParams is createAndInsertSegment with explicit
// SPS/PPS bytes so tests can alternate parameter-set keys.
func createAndInsertSegmentWithParams(t *testing.T, env *mergeTestEnv, recordingID, cameraID string, startedAt time.Time, sps, pps []byte) string {
	t.Helper()

	tempPath, finalPath, err := env.store.CreateSegment(cameraID, "h264")
	require.NoError(t, err)

	segDir := filepath.Dir(tempPath)
	segFile := createTestH264SegmentWithParams(t, segDir, sps, pps)
	data, err := os.ReadFile(segFile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(tempPath, data, 0o644))
	os.Remove(segFile)
	require.NoError(t, env.store.CloseSegment(tempPath, finalPath))

	fi, err := os.Stat(finalPath)
	require.NoError(t, err)

	rec := &model.Recording{
		ID:         recordingID,
		CameraID:   cameraID,
		FilePath:   finalPath,
		Format:     model.FormatH264,
		StartedAt:  startedAt,
		EndedAt:    startedAt.Add(30 * time.Second),
		Duration:   30.0,
		FileSize:   fi.Size(),
		FrameCount: 2,
	}
	require.NoError(t, env.db.InsertRecording(context.Background(), rec))
	return finalPath
}

// waitForRecordingGone polls until the source segment row disappears from the
// DB — the precise per-segment merge-completion signal (rolling replace
// deletes the source row in the same transaction that writes the bucket row).
// Appends only UPDATE the bucket row, so the row count alone cannot signal
// append completion.
func waitForRecordingGone(t *testing.T, env *mergeTestEnv, cameraID, recordingID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		recs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 100})
		require.NoError(t, err)
		found := false
		for _, rec := range recs {
			if rec.ID == recordingID {
				found = true
				break
			}
		}
		if !found {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for recording %s to be merged away", recordingID)
}

// fakeClock is a mutex-guarded controllable clock — the coordinator's merge
// goroutines read r.nowFn concurrently, so the fixture must not mutate shared
// state unsynchronized (test-own races are bugs too, #764 suite).
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// waitForBucketAppend polls until the newest bucket has absorbed n segments
// AND recorded its last append. This is the true completion point of a
// mergeOneSegment run: the source DB row disappears EARLIER (inside
// appendToBucket), but bucket state — including lastAppend, the retention
// clock read — is written after it. TTL tests must not advance the fake clock
// while a straggler merge goroutine can still read it.
func waitForBucketAppend(t *testing.T, r *RollingMergeCoordinator, cameraID string, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b := r.newestBucket(cameraID); b != nil {
			b.mu.Lock()
			done := b.segmentCount == n && !b.lastAppend.IsZero()
			b.mu.Unlock()
			if done {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for bucket append (segments=%d) for camera %s", n, cameraID)
}

// retentionTestEnv wires a coordinator with retention-friendly defaults. nowFn,
// when non-nil, is installed BEFORE Start — the merge goroutines read the
// clock, so a post-Start write would race them.
func retentionTestEnv(t *testing.T, cfg config.MergeConfig, nowFn func() time.Time) (*RollingMergeCoordinator, *mergeTestEnv, *event.EventBus) {
	t.Helper()
	env := newMergeTestEnv(t)
	t.Cleanup(func() { env.close(t) })

	bus := event.NewEventBus(16)
	r := newTestRollingCoordinator(env, cfg, bus)
	r.nowFn = nowFn
	require.NoError(t, r.Start(context.Background()))
	t.Cleanup(r.Stop)
	return r, env, bus
}

// ---------------------------------------------------------------------------
// TestRollingMerge_SPSBucketRetention — alternating keys A→B→A→B keep exactly
// two buckets (one per key): flipping back to a retained key appends instead
// of finalizing + re-creating.
// ---------------------------------------------------------------------------

func TestRollingMerge_SPSBucketRetention(t *testing.T) {
	cfg := config.MergeConfig{
		RollingEnabled:       boolPtr(true),
		RollingDebounce:      "50ms",
		RollingWindow:        "1h",
		RollingBucketRetain:  2,
		RollingBucketIdleTTL: "1h", // long TTL — capacity policy only in this test
	}
	_, env, bus := retentionTestEnv(t, cfg, nil)

	cameraID := "cam-oscillating"
	base := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)

	type step struct {
		recID string
		pps   []byte
		at    time.Time
	}
	steps := []step{
		{"rec-a1", retentionPPSA, base},
		{"rec-b1", retentionPPSB, base.Add(1 * time.Minute)},
		{"rec-a2", retentionPPSA, base.Add(2 * time.Minute)},
		{"rec-b2", retentionPPSB, base.Add(3 * time.Minute)},
	}
	for _, s := range steps {
		path := createAndInsertSegmentWithParams(t, env, s.recID, cameraID, s.at, retentionSPS, s.pps)
		publishSegmentCompleted(t, bus, cameraID, s.recID, path, "h264", s.at)
		waitForRecordingGone(t, env, cameraID, s.recID)
	}

	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, recs, 2, "A→B→A→B must produce exactly 2 buckets (one per param key), not one per flip")

	// Resume (not re-create): each bucket absorbed two segments, so each row
	// carries 4 samples (2 per segment) instead of a fresh bucket's 2.
	totalFrames := 0
	for _, rec := range recs {
		require.Equal(t, 4, rec.FrameCount, "each bucket must have been appended to twice")
		require.Equal(t, model.MergeStatusMerged, rec.MergeStatus)
		totalFrames += rec.FrameCount
	}
	require.Equal(t, 8, totalFrames)
}

// ---------------------------------------------------------------------------
// TestRollingMerge_SPSBucketRetentionIdleTTL — a retained bucket idle beyond
// the TTL is finalized; the next segment with the same key starts a new
// bucket instead of resuming across the gap.
// ---------------------------------------------------------------------------

func TestRollingMerge_SPSBucketRetentionIdleTTL(t *testing.T) {
	cfg := config.MergeConfig{
		RollingEnabled:       boolPtr(true),
		RollingDebounce:      "50ms",
		RollingWindow:        "1h",
		RollingBucketRetain:  2,
		RollingBucketIdleTTL: "10m",
	}
	// Controllable clock: TTL decisions read r.nowFn, so the test advances
	// time deterministically instead of sleeping.
	clk := &fakeClock{t: time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)}
	r, env, bus := retentionTestEnv(t, cfg, clk.Now)

	cameraID := "cam-idle"
	base := clk.Now()

	path := createAndInsertSegmentWithParams(t, env, "rec-a1", cameraID, base, retentionSPS, retentionPPSA)
	publishSegmentCompleted(t, bus, cameraID, "rec-a1", path, "h264", base)
	waitForBucketAppend(t, r, cameraID, 1)

	// Advance past the 10m TTL, staying inside the same window. Only after
	// the merge goroutine fully finished (lastAppend observed) — a straggler
	// would otherwise read the advanced clock and stamp a fresh lastAppend,
	// silently un-expiring the bucket.
	clk.Advance(11 * time.Minute)
	at := base.Add(11 * time.Minute)

	path = createAndInsertSegmentWithParams(t, env, "rec-a2", cameraID, at, retentionSPS, retentionPPSA)
	publishSegmentCompleted(t, bus, cameraID, "rec-a2", path, "h264", at)
	waitForRecordingGone(t, env, cameraID, "rec-a2")

	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, recs, 2, "an idle-expired bucket must NOT be resumed — a new bucket starts")
	require.Equal(t, 2, recs[0].FrameCount)
	require.Equal(t, 2, recs[1].FrameCount)
}

// ---------------------------------------------------------------------------
// TestRollingMerge_SPSBucketRetentionLRUCapacity — three distinct keys with
// retain=2: the least-recently-used bucket is evicted on admission, the set
// never exceeds the retention cap, and an evicted key re-creates its bucket.
// ---------------------------------------------------------------------------

func TestRollingMerge_SPSBucketRetentionLRUCapacity(t *testing.T) {
	cfg := config.MergeConfig{
		RollingEnabled:       boolPtr(true),
		RollingDebounce:      "50ms",
		RollingWindow:        "1h",
		RollingBucketRetain:  2,
		RollingBucketIdleTTL: "1h",
	}
	r, env, bus := retentionTestEnv(t, cfg, nil)

	cameraID := "cam-lru"
	base := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)

	// Third key: second PPS variant again is not enough — use a distinct SPS
	// level byte so all three keys differ.
	spsC := append([]byte{}, retentionSPS...)
	spsC[3] = 0x0b

	type step struct {
		recID string
		sps   []byte
		pps   []byte
	}
	steps := []step{
		{"rec-a1", retentionSPS, retentionPPSA},
		{"rec-b1", retentionSPS, retentionPPSB},
		{"rec-c1", spsC, retentionPPSA},
		{"rec-a2", retentionSPS, retentionPPSA}, // A was evicted at C's admission → new bucket
	}
	for i, s := range steps {
		at := base.Add(time.Duration(i) * time.Minute)
		path := createAndInsertSegmentWithParams(t, env, s.recID, cameraID, at, s.sps, s.pps)
		publishSegmentCompleted(t, bus, cameraID, s.recID, path, "h264", at)
		waitForRecordingGone(t, env, cameraID, s.recID)
	}

	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, recs, 4, "C evicts LRU-A, then A re-creates a bucket (evicting LRU-B) — 4 buckets total")

	// The retained set never exceeds the cap.
	setAny, ok := r.buckets.Load(cameraID)
	require.True(t, ok)
	set := setAny.(*cameraBucketSet)
	set.mu.Lock()
	size := len(set.buckets)
	set.mu.Unlock()
	require.LessOrEqual(t, size, 2, "retained bucket set must be capped at rolling_bucket_retain")
}
