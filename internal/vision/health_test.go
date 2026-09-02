package vision

// Tests for the heartbeat v2 extensions on HealthTracker (#671):
// optional drop reports + runtime metrics, and the in-memory history ring
// that backs GET /api/vision/metrics.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHealthTracker_MetricsHistoryRing(t *testing.T) {
	h := NewHealthTracker(60)

	// Old-format heartbeat (no metrics) still yields a sample with zeroed
	// metrics fields — the ring must not skip legacy consumers.
	h.RecordHeartbeat(HeartbeatStatus{Status: "healthy", QueueDepth: 1, ProcessedCount: 5})

	// v2 heartbeat with metrics.
	h.RecordHeartbeat(HeartbeatStatus{
		Status:         "healthy",
		QueueDepth:     3,
		ProcessedCount: 7,
		Metrics: &VisionMetrics{
			QueueCapacity:    64,
			DecodeWorkers:    2,
			WorkersBusy:      1,
			ReceivedTotal:    100,
			DroppedTotal:     10,
			DroppedQueueFull: 9,
			DroppedTTL:       1,
			EventsEmitted:    42,
			SegMsP50:         16000,
			SegMsP90:         76000,
			MemAvailableMB:   620,
			Load1:            2.5,
		},
	})

	samples := h.MetricsHistory(time.Time{})
	require.Len(t, samples, 2)
	require.Equal(t, 1, samples[0].QueueDepth)
	require.Equal(t, int64(5), samples[0].ProcessedCount)
	require.Equal(t, 0, samples[0].DecodeWorkers, "legacy heartbeat has no worker info")
	require.Equal(t, 3, samples[1].QueueDepth)
	require.Equal(t, 2, samples[1].DecodeWorkers)
	require.Equal(t, 1, samples[1].WorkersBusy)
	require.Equal(t, int64(10), samples[1].DroppedTotal)
	require.Equal(t, int64(42), samples[1].EventsEmitted)
	require.False(t, samples[0].TS.After(samples[1].TS), "samples are in arrival order")

	// since-filter drops older samples.
	since := samples[1].TS
	require.Len(t, h.MetricsHistory(since), 1)

	// Ring cap: only the newest maxHistorySamples survive.
	for i := 0; i < maxHistorySamples+10; i++ {
		h.RecordHeartbeat(HeartbeatStatus{Status: "healthy", ProcessedCount: i})
	}
	all := h.MetricsHistory(time.Time{})
	require.Len(t, all, maxHistorySamples)
	require.Equal(t, int64(maxHistorySamples+9), all[len(all)-1].ProcessedCount,
		"newest sample kept after overflow")
}

func TestHealthTracker_MarkedDropCounter(t *testing.T) {
	h := NewHealthTracker(60)
	require.Zero(t, h.MarkedDropTotal())
	h.NoteMarkedDrops(3)
	h.NoteMarkedDrops(2)
	require.Equal(t, int64(5), h.MarkedDropTotal())
}

func TestHealthTracker_DropsCarriedInSnapshot(t *testing.T) {
	h := NewHealthTracker(60)
	drops := &VisionDrops{Seq: 7, Ranges: []VisionDropRange{{
		CameraID: "cam-1", Reason: "queue_full", Count: 3,
		From: "2026-09-02T04:00:01Z", To: "2026-09-02T04:31:20Z",
	}}}
	h.RecordHeartbeat(HeartbeatStatus{Status: "healthy", Drops: drops})

	_, _, status := h.Snapshot()
	require.NotNil(t, status.Drops)
	require.Equal(t, int64(7), status.Drops.Seq)
	require.Len(t, status.Drops.Ranges, 1)
	require.Equal(t, "queue_full", status.Drops.Ranges[0].Reason)
}
