package metrics

// Coverage for the observability recorder methods (#580) — every one is a
// thin Prometheus gauge/counter/histogram write; calling each with both a
// fresh and repeated label set pins the no-panic contract and the
// nil-metrics tolerance.

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecorderMethodsNoPanic(t *testing.T) {
	m := NewMetrics()

	// Query + SQLite health.
	m.ObserveQueryDuration("ListRecordings", 0.25)
	m.ObserveQueryDuration("ListRecordings", 1.25)
	m.IncSQLiteBusyErrors()
	m.IncStorageWriteErrors()

	// Merge telemetry.
	m.RecordMergeSuccess(2*time.Second, 4096)
	m.RecordMergeFailure("incompatible")
	m.UpdateMergePending("cam-1", 3)
	m.RecordRollingMergeLatency("cam-1", 150*time.Millisecond)
	m.UpdateRollingMergeBucketSegments("cam-1", 12)

	// Playback telemetry.
	m.SetPlaybackLiveLatency("cam-1", "hls", 850.5)
	m.IncPlaybackStall("cam-1", "flv")

	// Segment write + audit paths.
	m.ObserveSegmentWrite("cam-1", 30*time.Millisecond)
	m.IncRecordingAudit("cam-1", "ok")
	m.IncRecordingAudit("cam-1", "missing")
	m.IncRecordingDeepCheck("cam-1", "ok")
	m.IncRecordingDeepCheck("cam-1", "corrupt")
}

func TestNilMetricsRecordersAreNoOps(t *testing.T) {
	var m *Metrics
	requireNoPanic(t, func() {
		m.ObserveQueryDuration("q", 1)
		m.IncSQLiteBusyErrors()
		m.IncStorageWriteErrors()
		m.RecordMergeSuccess(time.Second, 1)
		m.RecordMergeFailure("x")
		m.UpdateMergePending("c", 1)
		m.RecordRollingMergeLatency("c", time.Second)
		m.UpdateRollingMergeBucketSegments("c", 1)
		m.SetPlaybackLiveLatency("c", "hls", 1)
		m.IncPlaybackStall("c", "hls")
		m.ObserveSegmentWrite("c", time.Second)
		m.IncRecordingAudit("c", "ok")
		m.IncRecordingDeepCheck("c", "ok")
	})
}

func requireNoPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("recorder panicked on nil metrics: %v", r)
		}
	}()
	fn()
}

// TestObserveTxn (#759): write transactions land in the per-source counter
// and histogram.
func TestObserveTxn(t *testing.T) {
	m := NewMetrics()

	m.ObserveTxn("recording_insert", 0.01)
	m.ObserveTxn("recording_insert", 0.02)
	m.ObserveTxn("merge_status", 0.005)

	if got := testutil.ToFloat64(m.SQLiteTxnsTotal.WithLabelValues("recording_insert")); got != 2 {
		t.Errorf("recording_insert txns = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.SQLiteTxnsTotal.WithLabelValues("merge_status")); got != 1 {
		t.Errorf("merge_status txns = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.SQLiteTxnsTotal.WithLabelValues("cleanup")); got != 0 {
		t.Errorf("cleanup txns = %v, want 0", got)
	}
	// Histogram receives observations (count = 2 for recording_insert).
	if c := testutil.CollectAndCount(m.SQLiteTxnDurationSeconds); c == 0 {
		t.Error("txn duration histogram has no series")
	}
}
