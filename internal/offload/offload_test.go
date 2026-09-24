package offload

// offload_test.go — the upload pipeline against a real temp SQLite DB and a
// fake object store. Covers the issue #874 batch-1 acceptance semantics:
// recovery without duplicates or loss, stale re-upload, skip-missing, backlog
// cap, iobudget tenancy, key derivation.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/iobudget"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/objectstore"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStore is an in-memory objectstore.Store. Put calls are recorded so
// tests can assert counts (recovery must not double-upload) and content.
type fakeStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    []string // keys in Put order
	headErr map[string]error
	putErr  error
}

func newFakeStore() *fakeStore { return &fakeStore{objects: map[string][]byte{}} }

func (f *fakeStore) Put(_ context.Context, key string, body io.Reader, _ int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return "", f.putErr
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	f.objects[key] = b
	f.puts = append(f.puts, key)
	return `"fake-etag"`, nil
}

func (f *fakeStore) GetRange(_ context.Context, key string, start, end int64) (io.ReadCloser, objectstore.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[key]
	if !ok {
		return nil, objectstore.ObjectInfo{}, objectstore.ErrObjectNotFound
	}
	if start >= int64(len(b)) {
		return nil, objectstore.ObjectInfo{}, objectstore.ErrRangeNotSatisfiable
	}
	if end < 0 || end >= int64(len(b)) {
		end = int64(len(b)) - 1
	}
	info := objectstore.ObjectInfo{Key: key, Size: int64(len(b)), ETag: `"fake-etag"`}
	return io.NopCloser(bytes.NewReader(b[start : end+1])), info, nil
}

func (f *fakeStore) Head(_ context.Context, key string) (objectstore.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.headErr[key]; ok {
		return objectstore.ObjectInfo{}, err
	}
	b, ok := f.objects[key]
	if !ok {
		return objectstore.ObjectInfo{}, objectstore.ErrObjectNotFound
	}
	return objectstore.ObjectInfo{Key: key, Size: int64(len(b)), ETag: `"fake-etag"`}, nil
}

func (f *fakeStore) putCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.puts)
}

// recordingBudget records every Wait as an iobudget.Limiter.
type recordingBudget struct {
	mu      sync.Mutex
	calls   []recordedWait
	blocked bool
}

type recordedWait struct {
	consumer string
	n        int64
}

func (r *recordingBudget) Wait(_ context.Context, consumer string, n int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.blocked {
		<-make(chan struct{}) // park forever until ctx… tests never cancel
	}
	r.calls = append(r.calls, recordedWait{consumer, n})
	return nil
}

// newManagerEnv builds a temp DB + a recording whose file exists on disk,
// plus a Manager with test-friendly timings.
func newManagerEnv(t *testing.T, opts func(*Options)) (*Manager, *storage.DB, *fakeStore, *recordingBudget) {
	t.Helper()
	db, err := storage.New(filepath.Join(t.TempDir(), "offload.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	t.Cleanup(func() { db.Close() })

	store := newFakeStore()
	budget := &recordingBudget{}
	o := Options{
		Store:        store,
		Budget:       budget,
		Prefix:       "recordings",
		ScanInterval: 50 * time.Millisecond,
		MinAge:       0,
		Workers:      1,
		BacklogLimit: 0,
		UploadPause:  time.Millisecond,
	}
	if opts != nil {
		opts(&o)
	}
	m := NewManager(db, o)
	return m, db, store, budget
}

// seedMergedRecording inserts a rolling-merged row plus its on-disk file.
func seedMergedRecording(t *testing.T, db *storage.DB, id, cameraID string, endedAgo time.Duration, content string) *model.Recording {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, id+".mp4")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	ended := time.Now().UTC().Add(-endedAgo)
	rec := &model.Recording{
		ID: id, CameraID: cameraID, FilePath: path, Format: model.FormatH264,
		StartedAt: ended.Add(-time.Hour), EndedAt: ended, Duration: 3600,
		FileSize: int64(len(content)), MergeStatus: model.MergeStatusMerged,
		MergeTier: "rolling",
	}
	require.NoError(t, db.InsertRecording(context.Background(), rec))
	return rec
}

func waitForStatus(t *testing.T, db *storage.DB, status string, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		counts, err := db.CountOffloadByStatus(context.Background())
		if err != nil {
			return false
		}
		return counts[status] >= n
	}, 15*time.Second, 100*time.Millisecond, "outbox never reached %d×%s", n, status)
}

func TestManagerUploadsEligibleCandidates(t *testing.T) {
	m, db, store, budget := newManagerEnv(t, nil)
	rec := seedMergedRecording(t, db, "m1", "camA", 2*time.Hour, "segment-bytes")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, m.Start(ctx))
	defer func() { _ = m.Stop() }()

	waitForStatus(t, db, storage.OffloadStatusUploaded, 1)

	// Object key layout: prefix/camera/date/id.ext, content intact.
	key := ObjectKey("recordings", "camA", rec.StartedAt, rec.ID, ".mp4")
	store.mu.Lock()
	got := store.objects[key]
	store.mu.Unlock()
	assert.Equal(t, "segment-bytes", string(got))

	// Upload bytes were billed to the offload tenant.
	budget.mu.Lock()
	defer budget.mu.Unlock()
	require.Len(t, budget.calls, 1)
	assert.Equal(t, iobudget.ConsumerOffload, budget.calls[0].consumer)
	assert.Equal(t, int64(len("segment-bytes")), budget.calls[0].n)
}

func TestManagerRecoversUploadingRows(t *testing.T) {
	m, db, store, _ := newManagerEnv(t, nil)
	rec := seedMergedRecording(t, db, "m2", "camA", 2*time.Hour, "recover-me")
	ctx := context.Background()
	_, err := db.EnqueueOffload(ctx, storage.OffloadItem{
		RecordingID: rec.ID, CameraID: "camA", ObjectKey: ObjectKey("recordings", "camA", rec.StartedAt, rec.ID, ".mp4"),
		LocalPath: rec.FilePath, FileSize: rec.FileSize,
	})
	require.NoError(t, err)
	// Simulate a crash mid-PUT: row stranded in uploading.
	_, err = db.ClaimPendingOffload(ctx, 1)
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	require.NoError(t, m.Start(runCtx))
	defer func() { _ = m.Stop() }()

	waitForStatus(t, db, storage.OffloadStatusUploaded, 1)
	assert.Equal(t, 1, store.putCount(), "recovery re-uploads exactly once (idempotent overwrite)")
}

func TestManagerRequeuesGrownFiles(t *testing.T) {
	m, db, store, _ := newManagerEnv(t, nil)
	rec := seedMergedRecording(t, db, "m3", "camA", 2*time.Hour, "v1")

	ctx := context.Background()
	_, err := db.EnqueueOffload(ctx, storage.OffloadItem{
		RecordingID: rec.ID, CameraID: "camA", ObjectKey: ObjectKey("recordings", "camA", rec.StartedAt, rec.ID, ".mp4"),
		LocalPath: rec.FilePath, FileSize: rec.FileSize,
	})
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(ctx)
	require.NoError(t, m.Start(runCtx))
	waitForStatus(t, db, storage.OffloadStatusUploaded, 1)
	require.Equal(t, 1, store.putCount())

	// A late backfill append grows the file and the recording row.
	require.NoError(t, os.WriteFile(rec.FilePath, []byte("v1-plus-late-append"), 0o644))
	grown, err := db.GetRecording(ctx, rec.ID)
	require.NoError(t, err)
	grown.FileSize = int64(len("v1-plus-late-append"))
	require.NoError(t, db.UpdateRecording(ctx, grown))

	// Next scan re-queues; the worker overwrites the stale object.
	waitForStatus(t, db, storage.OffloadStatusUploaded, 1)
	require.Eventually(t, func() bool { return store.putCount() == 2 }, 15*time.Second, 100*time.Millisecond,
		"grown file must be re-uploaded")
	store.mu.Lock()
	body := store.objects[ObjectKey("recordings", "camA", rec.StartedAt, rec.ID, ".mp4")]
	store.mu.Unlock()
	assert.Equal(t, "v1-plus-late-append", string(body))
	cancel()
	_ = m.Stop()
}

func TestManagerSkipsMissingFiles(t *testing.T) {
	m, db, store, _ := newManagerEnv(t, nil)
	rec := seedMergedRecording(t, db, "m4", "camA", 2*time.Hour, "soon-gone")
	require.NoError(t, os.Remove(rec.FilePath)) // retention won the race

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, m.Start(ctx))
	defer func() { _ = m.Stop() }()

	waitForStatus(t, db, storage.OffloadStatusSkipped, 1)
	assert.Equal(t, 0, store.putCount(), "nothing to upload")
}

func TestManagerBacklogCapStopsEnqueue(t *testing.T) {
	// A dead uplink makes the backlog pile up; the cap must stop NEW rows
	// from entering while the stuck one retries. (With a healthy uplink the
	// queue drains and new candidates legitimately keep flowing — the cap
	// bounds accumulation, not total throughput.)
	m, db, store, _ := newManagerEnv(t, func(o *Options) { o.BacklogLimit = 1 })
	seedMergedRecording(t, db, "m5a", "camA", 2*time.Hour, "one")
	seedMergedRecording(t, db, "m5b", "camA", 3*time.Hour, "two")

	store.mu.Lock()
	store.putErr = errors.New("uplink down")
	store.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, m.Start(ctx))
	defer func() { _ = m.Stop() }()

	// The stuck row retries (pending→uploading→pending), so the backlog
	// never drains — the second candidate must never enter the outbox.
	require.Eventually(t, func() bool {
		items, err := db.ClaimPendingOffload(ctx, 1)
		if err != nil || len(items) == 0 {
			return false
		}
		return items[0].Attempts >= 1
	}, 15*time.Second, 100*time.Millisecond, "stuck row should have retried at least once")

	assert.Never(t, func() bool {
		counts, _ := db.CountOffloadByStatus(context.Background())
		total := 0
		for _, n := range counts {
			total += n
		}
		return total > 1
	}, 2*time.Second, 100*time.Millisecond, "backlog cap must stop enqueueing while the uplink is stuck")
}

func TestObjectKeyLayout(t *testing.T) {
	started := time.Date(2026, 9, 22, 8, 30, 0, 0, time.UTC)
	key := ObjectKey("recordings", "front-door", started, "merge-abc", ".mp4")
	assert.Equal(t, "recordings/front-door/2026/09/22/merge-abc.mp4", key)

	// Empty prefix collapses cleanly; camera IDs keep their kebab-case.
	assert.Equal(t, "cam/2026/09/22/merge-abc.mp4", ObjectKey("", "cam", started, "merge-abc", ".mp4"))
}

func TestManagerUploadRetryOnStoreError(t *testing.T) {
	m, db, store, _ := newManagerEnv(t, nil)
	seedMergedRecording(t, db, "m6", "camA", 2*time.Hour, "will-fail-first")

	store.mu.Lock()
	store.putErr = errors.New("simulated connection reset")
	store.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, m.Start(ctx))
	defer func() { _ = m.Stop() }()

	// First attempt fails → row back to pending (attempts=1)...
	require.Eventually(t, func() bool {
		counts, _ := db.CountOffloadByStatus(context.Background())
		return counts[storage.OffloadStatusPending]+counts[storage.OffloadStatusUploading] >= 1
	}, 15*time.Second, 100*time.Millisecond)

	// ...then the store heals and the next scan's worker succeeds.
	store.mu.Lock()
	store.putErr = nil
	store.mu.Unlock()
	waitForStatus(t, db, storage.OffloadStatusUploaded, 1)
}

func TestManagerAutoEvictAfterDays(t *testing.T) {
	m, db, store, _ := newManagerEnv(t, func(o *Options) {
		o.EvictAfterDays = 7 * 24 * time.Hour
		o.EvictInterval = 50 * time.Millisecond
	})
	rec := seedMergedRecording(t, db, "ae-1", "camA", 2*time.Hour, "auto-evict-me")

	// Deterministic clock: upload happens "now"; evict eligibility is judged
	// 8 days later.
	now := time.Now().UTC()
	m.nowFn = func() time.Time { return now.Add(8 * 24 * time.Hour) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, m.Start(ctx))
	defer func() { _ = m.Stop() }()

	// Upload completes (judged at the advanced clock — fine), then the evict
	// loop finds it confirmed 8 days ago > after_days=7 and evicts.
	require.Eventually(t, func() bool {
		counts, _ := db.CountOffloadByStatus(context.Background())
		return counts[storage.OffloadStatusEvicted] == 1
	}, 15*time.Second, 100*time.Millisecond, "auto-evict must fire after the retention window")

	_, statErr := os.Stat(rec.FilePath)
	require.True(t, os.IsNotExist(statErr), "local file must be evicted")
	require.Equal(t, 1, store.putCount(), "object must remain (exactly one upload)")
}

func TestManagerAutoEvictDisabledByDefault(t *testing.T) {
	m, db, store, _ := newManagerEnv(t, func(o *Options) {
		o.EvictInterval = 50 * time.Millisecond // fast loop, after_days = 0
	})
	seedMergedRecording(t, db, "ae-2", "camA", 2*time.Hour, "never-evicted")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, m.Start(ctx))
	defer func() { _ = m.Stop() }()

	waitForStatus(t, db, storage.OffloadStatusUploaded, 1)
	assert.Never(t, func() bool {
		counts, _ := db.CountOffloadByStatus(context.Background())
		return counts[storage.OffloadStatusEvicted] > 0
	}, 2*time.Second, 100*time.Millisecond, "after_days=0 must never auto-evict")
	_ = store
}

// bucketStores fakes per-bucket stores for routing tests.
type bucketStores struct {
	mu    sync.Map // bucket → *fakeStore
	def   *fakeStore
	newFn func(bucket string) (objectstore.Store, error)
}

func newBucketStores() *bucketStores {
	bs := &bucketStores{def: newFakeStore()}
	bs.newFn = func(bucket string) (objectstore.Store, error) {
		v, _ := bs.mu.LoadOrStore(bucket, newFakeStore())
		return v.(*fakeStore), nil
	}
	return bs
}

func (bs *bucketStores) store(bucket string) *fakeStore {
	if bucket == "" {
		return bs.def
	}
	v, ok := bs.mu.Load(bucket)
	if !ok {
		return nil
	}
	return v.(*fakeStore)
}

func TestManagerRoutesCameraToOverrideBucket(t *testing.T) {
	bs := newBucketStores()
	m, db, _, _ := newManagerEnv(t, func(o *Options) {
		o.Store = bs.def
		o.NewBucketStore = bs.newFn
		o.CameraRoutes = map[string]CameraRoute{
			"camVIP": {Bucket: "vip-bucket", Prefix: "yard"},
		}
	})
	recVIP := seedMergedRecording(t, db, "vip-1", "camVIP", 2*time.Hour, "vip-bytes")
	recStd := seedMergedRecording(t, db, "std-1", "camStd", 2*time.Hour, "std-bytes")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, m.Start(ctx))
	defer func() { _ = m.Stop() }()

	require.Eventually(t, func() bool {
		counts, _ := db.CountOffloadByStatus(ctx)
		return counts[storage.OffloadStatusUploaded] == 2
	}, 15*time.Second, 100*time.Millisecond)

	// VIP camera's object went to the override bucket under the override prefix.
	vip := bs.store("vip-bucket")
	require.NotNil(t, vip)
	key := ObjectKey("yard", "camVIP", recVIP.StartedAt, recVIP.ID, ".mp4")
	vip.mu.Lock()
	_, inVip := vip.objects[key]
	vip.mu.Unlock()
	assert.True(t, inVip, "override camera must upload to its routed bucket+prefix")

	// Default camera stayed on the default bucket with the default prefix.
	// Derive the expected key from the recording's own StartedAt (like the
	// uploader does) — anchoring it to time.Now() makes the test fail whenever
	// seeding crossed a UTC midnight (StartedAt = now-3h lands on yesterday).
	stdKey := ObjectKey("recordings", "camStd", recStd.StartedAt, "std-1", ".mp4")
	bs.def.mu.Lock()
	_, stdFound := bs.def.objects[stdKey]
	bs.def.mu.Unlock()
	assert.True(t, stdFound, "non-overridden camera uses the default bucket+prefix")
}

func TestRunEvictBucketAware(t *testing.T) {
	bs := newBucketStores()
	dir := t.TempDir()
	db, err := storage.New(filepath.Join(dir, "ev.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	defer db.Close()
	ctx := context.Background()

	// One row in the default bucket, one routed to "vip".
	for i, bucket := range []string{"", "vip"} {
		rec := seedMergedRecording(t, db, "ev-b"+string(rune('a'+i)), "cam", 2*time.Hour, "content-"+bucket)
		key := ObjectKey("recordings", "cam", rec.StartedAt, rec.ID, ".mp4")
		_, err := db.EnqueueOffload(ctx, storage.OffloadItem{
			RecordingID: rec.ID, CameraID: "cam", ObjectKey: key, Bucket: bucket,
			LocalPath: rec.FilePath, FileSize: rec.FileSize,
		})
		require.NoError(t, err)
		items, err := db.ClaimPendingOffload(ctx, 1)
		require.NoError(t, err)
		require.Len(t, items, 1)
		st := bs.def
		if bucket != "" {
			v, _ := bs.mu.LoadOrStore(bucket, newFakeStore())
			st = v.(*fakeStore)
		}
		_, err = st.Put(ctx, key, strings.NewReader("content-"+bucket), rec.FileSize)
		require.NoError(t, err)
		require.NoError(t, db.MarkOffloadUploaded(ctx, items[0].ID, `"e"`, rec.FileSize))
	}

	storeFor := func(bucket string) (objectstore.Store, error) {
		if bucket == "" {
			return bs.def, nil
		}
		return bs.newFn(bucket)
	}
	sum, err := RunEvict(ctx, db, EvictOptions{
		StoreFor:        storeFor,
		ConfirmedBefore: time.Now().UTC().Add(time.Minute),
		Execute:         true,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, sum.Evicted, "both buckets' items evict through their own stores")
}
