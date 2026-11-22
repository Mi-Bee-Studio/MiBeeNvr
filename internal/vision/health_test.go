package vision

// Tests for the heartbeat v2 extensions on HealthTracker (#671):
// optional drop reports + runtime metrics, and the in-memory history ring
// that backs GET /api/vision/metrics.

import (
	"sync/atomic"
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
	for i := range maxHistorySamples + 10 {
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

// --- onRecovery transition semantics ---

// TestHealthTracker_OnRecoveryTransitionOnly : onRecovery 必须只在
// unhealthy→healthy 的跃迁沿触发。持续上报 degraded 的消费者旧行为下
// 每个心跳间隔都触发一次补偿回调，补偿入口 IsHealthy() 又立即判不健康
// 退出——30s 一次空转（DB 全量查询 + INFO/WARN 成对刷屏）、积压只涨不消。
func TestHealthTracker_OnRecoveryTransitionOnly(t *testing.T) {
	h := NewHealthTracker(60)

	var fired atomic.Int32
	h.SetOnRecovery(func() { fired.Add(1) })

	// 持续 degraded：任何一拍都不触发。
	for range 5 {
		h.RecordHeartbeat(HeartbeatStatus{Status: "degraded"})
	}
	require.Equal(t, int32(0), fired.Load(), "persistent degraded must never fire recovery")
	require.False(t, h.IsHealthy())

	// degraded→healthy 跃迁沿：恰好触发一次。
	h.RecordHeartbeat(HeartbeatStatus{Status: "healthy"})
	require.Eventually(t, func() bool { return fired.Load() == 1 },
		2*time.Second, 20*time.Millisecond, "degraded→healthy edge must fire once")
	require.True(t, h.IsHealthy())

	// 已健康后的重复心跳保持沉默。
	h.RecordHeartbeat(HeartbeatStatus{Status: "healthy"})
	h.RecordHeartbeat(HeartbeatStatus{Status: "healthy"})
	require.Equal(t, int32(1), fired.Load())
}

// TestHealthTracker_OnRecoveryGenuineOfflineGap : 真离线缺口（超过 timeout
// 无心跳）后的首个健康心跳触发一次补偿——这正是离线补偿的存在意义；
// 恢复后的下一拍不再触发。
func TestHealthTracker_OnRecoveryGenuineOfflineGap(t *testing.T) {
	h := NewHealthTracker(1) // 1s timeout——间隔超过它即视为离线缺口

	var fired atomic.Int32
	h.SetOnRecovery(func() { fired.Add(1) })

	// 首拍：全新 tracker 视为「缺口后恢复」，触发一次（重启后补偿语义）。
	h.RecordHeartbeat(HeartbeatStatus{Status: "healthy"})
	require.Eventually(t, func() bool { return fired.Load() == 1 },
		2*time.Second, 20*time.Millisecond)

	// 制造离线缺口：让 lastSeen 老化超过 timeout，再收健康心跳。
	time.Sleep(1100 * time.Millisecond)
	h.RecordHeartbeat(HeartbeatStatus{Status: "healthy"})
	require.Eventually(t, func() bool { return fired.Load() == 2 },
		2*time.Second, 20*time.Millisecond, "healthy beat after an outage gap must fire once")

	// 恢复后的下一拍：不再触发。
	h.RecordHeartbeat(HeartbeatStatus{Status: "healthy"})
	require.Equal(t, int32(2), fired.Load())
}

// TestHealthTracker_OnRecovery_FirstBeatDegraded : 全新 tracker 的首拍若
// 自身 degraded，不触发补偿——没有「恢复」可言。
func TestHealthTracker_OnRecovery_FirstBeatDegraded(t *testing.T) {
	h := NewHealthTracker(60)

	var fired atomic.Int32
	h.SetOnRecovery(func() { fired.Add(1) })

	h.RecordHeartbeat(HeartbeatStatus{Status: "degraded"})
	require.Equal(t, int32(0), fired.Load())
	require.False(t, h.IsHealthy())
}
