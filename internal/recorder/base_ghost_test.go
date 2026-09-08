package recorder

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// ghostCountingDB records every inserted recording row.
type ghostCountingDB struct {
	mu      sync.Mutex
	inserts []*model.Recording
}

func (d *ghostCountingDB) InsertRecording(_ context.Context, r *model.Recording) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.inserts = append(d.inserts, r)
	return nil
}

func (d *ghostCountingDB) InsertRecordingWithRetry(ctx context.Context, r *model.Recording, _ int, _ time.Duration) error {
	return d.InsertRecording(ctx, r)
}

func (d *ghostCountingDB) SetMergeStatus(context.Context, []string, string) error { return nil }

func (d *ghostCountingDB) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.inserts)
}

// Production incident 2026-09-08: the startup temp-cleanup scan deleted an
// in-flight segment's .tmp; CloseSegment failed with "temp path not found"
// but the DB row was still inserted — a permanent 404 entry in the
// recordings list. closeCurrentSegment must NOT insert a row (nor publish a
// SegmentCompleted event) when the final file never materialized.
func TestCloseCurrentSegment_NoGhostRowWhenTempVanished(t *testing.T) {
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)

	db := &ghostCountingDB{}
	b := &baseRecorder{
		cfg:    BaseConfig{CameraID: "cam-ghost", DB: db},
		store:  store,
		driver: H264NALDriver{},
		log:    slog.Default(),
	}

	tempPath, finalPath, err := store.CreateSegment("cam-ghost", "h264")
	require.NoError(t, err)

	b.muxer = muxer.NewMP4Muxer(tempPath)
	trackID, err := b.muxer.AddH264Track(testSPS, testPPS)
	require.NoError(t, err)
	b.trackID = trackID
	require.NoError(t, b.muxer.WriteSample(trackID, []byte{0x65, 0x01, 0x02}, 0, 33*time.Millisecond))

	b.curTempPath = tempPath
	b.curFinalPath = finalPath
	b.segStart = time.Now().Add(-time.Minute)
	b.frameCount = 10

	// Simulate the race: the .tmp vanishes (startup cleanup scan) while the
	// recorder is mid-segment. The muxer's fd stays valid (writes land in the
	// unlinked inode), so muxer.Close() succeeds — only CloseSegment
	// discovers the loss, exactly like the production logs.
	require.NoError(t, os.Remove(tempPath))

	b.closeCurrentSegment()

	require.Zero(t, db.count(), "vanished segment must not produce a DB row (permanent 404 ghost)")
	_, err = os.Stat(finalPath)
	require.True(t, os.IsNotExist(err), "final file must not exist")
}

// Control: a healthy finalize still inserts exactly one row.
func TestCloseCurrentSegment_HealthySegmentInsertsRow(t *testing.T) {
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)

	db := &ghostCountingDB{}
	b := &baseRecorder{
		cfg:    BaseConfig{CameraID: "cam-ok", DB: db},
		store:  store,
		driver: H264NALDriver{},
		log:    slog.Default(),
	}

	tempPath, finalPath, err := store.CreateSegment("cam-ok", "h264")
	require.NoError(t, err)

	b.muxer = muxer.NewMP4Muxer(tempPath)
	trackID, err := b.muxer.AddH264Track(testSPS, testPPS)
	require.NoError(t, err)
	b.trackID = trackID
	require.NoError(t, b.muxer.WriteSample(trackID, []byte{0x65, 0x01, 0x02}, 0, 33*time.Millisecond))

	b.curTempPath = tempPath
	b.curFinalPath = finalPath
	b.segStart = time.Now().Add(-time.Minute)
	b.frameCount = 10

	b.closeCurrentSegment()

	require.Equal(t, 1, db.count(), "healthy segment must insert its row")
	_, err = os.Stat(finalPath)
	require.NoError(t, err)
}
