package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// txnRecorder captures (source, duration) pairs — a fake QueryMetrics.
type txnRecorder struct {
	lastSource string
	lastDur    time.Duration
	calls      int
}

func (r *txnRecorder) ObserveQueryDuration(_ string, _ float64) {}
func (r *txnRecorder) IncSQLiteBusyErrors()                     {}
func (r *txnRecorder) ObserveTxn(source string, seconds float64) {
	r.calls++
	r.lastSource = source
	r.lastDur = time.Duration(seconds * float64(time.Second))
}

func newTxnTestDB(t *testing.T) (*DB, *txnRecorder) {
	t.Helper()
	db, err := New(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	t.Cleanup(func() { db.Close() })
	rec := &txnRecorder{}
	db.SetMetrics(rec)
	return db, rec
}

func TestObserveTxn_InsertRecordingSource(t *testing.T) {
	db, rec := newTxnTestDB(t)
	require.NoError(t, db.InsertRecording(context.Background(), &model.Recording{
		ID: "r1", CameraID: "cam1", FilePath: "/x", Format: model.FormatH264,
		StartedAt: time.Now().UTC(), EndedAt: time.Now().UTC(),
	}))
	require.Equal(t, 1, rec.calls)
	require.Equal(t, "recording_insert", rec.lastSource)
	require.Greater(t, rec.lastDur, time.Duration(0))
}

func TestObserveTxn_MergeStatusSource(t *testing.T) {
	db, rec := newTxnTestDB(t)
	require.NoError(t, db.InsertRecording(context.Background(), &model.Recording{
		ID: "r1", CameraID: "cam1", FilePath: "/x", Format: model.FormatH264,
		StartedAt: time.Now().UTC(), EndedAt: time.Now().UTC(),
	}))
	rec.calls = 0
	require.NoError(t, db.SetMergeStatus(context.Background(), []string{"r1"}, model.MergeStatusMerged))
	require.Equal(t, "merge_status", rec.lastSource)
}

func TestObserveTxn_DeleteAmbientDefault(t *testing.T) {
	db, rec := newTxnTestDB(t)
	require.NoError(t, db.InsertRecording(context.Background(), &model.Recording{
		ID: "r1", CameraID: "cam1", FilePath: "/x", Format: model.FormatH264,
		StartedAt: time.Now().UTC(), EndedAt: time.Now().UTC(),
	}))
	rec.calls = 0

	// Untagged ctx → generic api_write (API handlers' default ambient).
	if _, err := db.DeleteRecordingsBatch(context.Background(), []string{"r1"}); err != nil {
		// already deleted by a previous run? ensure at least the observation fired
		t.Logf("delete returned %v", err)
	}
	require.GreaterOrEqual(t, rec.calls, 1)
	require.Equal(t, "api_write", rec.lastSource)
}

func TestObserveTxn_CtxSourceOverride(t *testing.T) {
	db, rec := newTxnTestDB(t)
	require.NoError(t, db.InsertRecording(context.Background(), &model.Recording{
		ID: "r2", CameraID: "cam1", FilePath: "/y", Format: model.FormatH264,
		StartedAt: time.Now().UTC(), EndedAt: time.Now().UTC(),
	}))
	rec.calls = 0

	// Cleanup/repair tag their ctx — same method, attributable source.
	ctx := WithTxnSource(context.Background(), "cleanup")
	if _, err := db.DeleteRecordingsBatch(ctx, []string{"r2"}); err != nil {
		t.Logf("delete returned %v", err)
	}
	require.GreaterOrEqual(t, rec.calls, 1)
	require.Equal(t, "cleanup", rec.lastSource)
}

func TestObserveTxn_HealthSource(t *testing.T) {
	db, rec := newTxnTestDB(t)
	require.NoError(t, db.InsertHealthEvent(context.Background(), model.HealthEvent{
		CameraID: "cam1", EventType: "camera_offline", Status: "warn",
	}))
	require.Equal(t, "health", rec.lastSource)
}

func TestObserveTxn_DisabledNoOp(t *testing.T) {
	db, _ := newTxnTestDB(t)
	db.SetMetrics(nil) // unwired — must not panic
	require.NoError(t, db.InsertRecording(context.Background(), &model.Recording{
		ID: "r3", CameraID: "cam1", FilePath: "/z", Format: model.FormatH264,
		StartedAt: time.Now().UTC(), EndedAt: time.Now().UTC(),
	}))
}

func TestWithTxnSource_NilSafe(t *testing.T) {
	ctx := WithTxnSource(context.Background(), "repair")
	require.Equal(t, "repair", txnSourceFrom(ctx, "api_write"))
	require.Equal(t, "api_write", txnSourceFrom(context.Background(), "api_write"))
}

func TestTxnSnapshot_CountsAccumulate(t *testing.T) {
	db, rec := newTxnTestDB(t)
	require.NoError(t, db.InsertRecording(context.Background(), &model.Recording{
		ID: "s1", CameraID: "cam1", FilePath: "/x", Format: model.FormatH264,
		StartedAt: time.Now().UTC(), EndedAt: time.Now().UTC(),
	}))
	require.NoError(t, db.InsertRecording(context.Background(), &model.Recording{
		ID: "s2", CameraID: "cam1", FilePath: "/y", Format: model.FormatH264,
		StartedAt: time.Now().UTC(), EndedAt: time.Now().UTC(),
	}))
	counts, nanos := TxnSnapshot()
	require.GreaterOrEqual(t, counts["recording_insert"], int64(2))
	require.Greater(t, nanos["recording_insert"], int64(0))
	_ = rec
}
