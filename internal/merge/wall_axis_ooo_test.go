// wall_axis_ooo_test.go — 乱序到达场景（#698）。
//
// CI 实证（run 33848470012）：TestWallAxis_WindowRolloverTwoBuckets 失败行
// "duration 120.00 must equal wall span -1.00" —— 产物行 ended_at 早于
// started_at。根因是同相机两段的合并顺序可与时间顺序颠倒（锁获取顺序不
// 保证派发顺序）。本文件用确定性的直接调用复现两条路径：
//
//	O1 桶 append 乱序   — 晚段先入桶、早段后 append，行必须仍自洽；
//	O2 批量乱序         — 同一 dispatch 内 [晚, 早] 排列，行必须仍自洽。
package merge

import (
	"context"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// oooSeg 落盘一个源段并入库（不走事件总线，避免 debounce 派发的不确定性），
// 返回可直接交给 mergeSegments 的 pendingSegmentInfo。
func oooSeg(t *testing.T, env *mergeTestEnv, cameraID, recID string, startedAt time.Time, samples []wallSample) pendingSegmentInfo {
	t.Helper()
	nalus := make([][]byte, len(samples))
	durs := make([]time.Duration, len(samples))
	var wall time.Duration
	for i, s := range samples {
		if s.key {
			nalus[i] = wallIDR
		} else {
			nalus[i] = wallP
		}
		durs[i] = s.d
		wall += s.d
	}
	path := createH264SegmentWithDurations(t, t.TempDir(), recID+".mp4", wallSps, wallPps, nalus, durs)
	seg := pendingSegmentInfo{
		recordingID: recID,
		filePath:    path,
		format:      "h264",
		cameraID:    cameraID,
		startedAt:   startedAt,
		endedAt:     startedAt.Add(wall),
		fileSize:    0, // batch 路径不用它做磁盘准入（live 路径），置零无害
	}
	require.NoError(t, env.db.InsertRecording(context.Background(), &model.Recording{
		ID:         recID,
		CameraID:   cameraID,
		FilePath:   path,
		Format:     model.FormatH264,
		StartedAt:  startedAt,
		EndedAt:    seg.endedAt,
		Duration:   wall.Seconds(),
		FrameCount: len(samples),
	}))
	return seg
}

// requireRowSelfConsistent 断言一条 merged 行的最小自洽不变量（#698 的
// 核心诉求）：started_at ≤ ended_at，且 duration 与墙钟跨度一致（±2s）。
func requireRowSelfConsistent(t *testing.T, env *mergeTestEnv, cameraID string) model.Recording {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var rows []model.Recording
	for time.Now().Before(deadline) {
		all, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 100})
		require.NoError(t, err)
		rows = nil
		for _, rr := range all {
			if rr.MergeStatus == model.MergeStatusMerged {
				rows = append(rows, rr)
			}
		}
		if len(rows) == 1 && len(all) == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.Len(t, rows, 1, "expected exactly one merged row")
	full, err := env.db.GetRecording(context.Background(), rows[0].ID)
	require.NoError(t, err)
	require.False(t, full.EndedAt.Before(full.StartedAt),
		"row %s: ended_at %v is before started_at %v (inverted row)", full.ID, full.EndedAt, full.StartedAt)
	span := full.EndedAt.Sub(full.StartedAt).Seconds()
	require.InDelta(t, span, full.Duration, 2.0,
		"row %s: duration %.2f must equal wall span %.2f", full.ID, full.Duration, span)
	return *full
}

// O1 桶 append 乱序：晚段（base+61s）先 createBucket，早段（base）后
// appendToBucket。CI 失败签名（duration 120 / span −1）即此路径错位的
// 行端点。修复后行必须覆盖 [base, base+121s]，duration ≥ 120。
func TestMergeOutOfOrder_AppendSwapped(t *testing.T) {
	env, _, r := wallEnv(t)
	cam := "cam-ooo1"
	base := time.Now().UTC().Truncate(time.Hour).Add(10 * time.Minute)

	late := oooSeg(t, env, cam, "ooo-late", base.Add(61*time.Second), sparseSeg(2))
	early := oooSeg(t, env, cam, "ooo-early", base, sparseSeg(2))

	r.mergeSegments(context.Background(), cam, []pendingSegmentInfo{late})
	r.mergeSegments(context.Background(), cam, []pendingSegmentInfo{early})

	full := requireRowSelfConsistent(t, env, cam)
	// 时间范围必须覆盖两段：started 不晚于早段 start，ended 不早于晚段 end。
	require.False(t, full.StartedAt.After(early.startedAt), "started_at must cover the earlier segment")
	require.False(t, full.EndedAt.Before(late.endedAt), "ended_at must cover the later segment")
}

// O2 批量乱序：同一 dispatch 携带 [晚, 早]。mergeAudioRun 直接取
// recs[0].StartedAt / recs[last].EndedAt，乱序输入即产生倒挂行。
func TestMergeOutOfOrder_BatchUnsorted(t *testing.T) {
	env, _, r := wallEnv(t)
	cam := "cam-ooo2"
	base := time.Now().UTC().Truncate(time.Hour).Add(10 * time.Minute)

	late := oooSeg(t, env, cam, "ooo-b-late", base.Add(61*time.Second), sparseSeg(2))
	early := oooSeg(t, env, cam, "ooo-b-early", base, sparseSeg(2))

	r.mergeSegments(context.Background(), cam, []pendingSegmentInfo{late, early})

	full := requireRowSelfConsistent(t, env, cam)
	require.False(t, full.StartedAt.After(early.startedAt), "started_at must cover the earlier segment")
	require.False(t, full.EndedAt.Before(late.endedAt), "ended_at must cover the later segment")
}
