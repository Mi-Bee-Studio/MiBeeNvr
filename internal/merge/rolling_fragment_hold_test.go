// rolling_fragment_hold_test.go — #852 碎段攒批一次折卷 TDD 契约。
//
// 背景（#851 实测）：闪断相机每次重连产一个 ~7s 碎段，每段触发一次
// MergeMP4Segments([bucket, frag]) 全桶整读整写；发病期折卷率 36/min vs
// 健康基线 12/min，PSI io some 冲 86%。本套件钉住：
//
//	H1 碎段判定边界：<30s 持有，≥30s 即时折（健康段零影响）；
//	H2 持有期间不折；到龄（最老碎段年龄 ≥ hold）一次折整批；
//	H3 深度上限 8 段 / 字节上限 64MB 到达即冲刷；
//	H4 小时窗翻转冲刷（批不跨窗）；
//	H5 健康段到达顺带冲刷持有批（反正要折一次，碎段搭车）；
//	H6 rolling_fragment_hold_s=0 关闭（回到逐段折卷现状）；
//	H7 真批折：N 个碎段一个桶产物行、墙钟跨度含批内间隙；
//	H8 backfill 扫描不得收走持有窗内的年轻碎段（轨道镜像 #810 transcode grace）。
package merge

import (
	"context"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// fragEnv 启动一个指定 fragment hold（秒）的 rolling 测试环境。
func fragEnv(t *testing.T, holdS *int) (*mergeTestEnv, *event.EventBus, *RollingMergeCoordinator) {
	t.Helper()
	env := newMergeTestEnv(t)
	t.Cleanup(func() { env.close(t) })
	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:       boolPtr(true),
		RollingDebounce:      "50ms",
		RollingWindow:        "1h",
		RollingFragmentHoldS: holdS,
	}
	r := newTestRollingCoordinator(env, cfg, bus)
	require.NoError(t, r.Start(context.Background()))
	t.Cleanup(r.Stop)
	return env, bus, r
}

// publishFrag 落盘一个真实 H264 段 + 入库 + 发布显式 endedAt/FileSize 的
// 完成事件（分类只看事件时长与字节数，文件本体是标准 fixture）。
func publishFrag(t *testing.T, env *mergeTestEnv, bus *event.EventBus, cameraID, recID string, startedAt time.Time, dur time.Duration, fileSize int64) string {
	t.Helper()
	path := createAndInsertSegment(t, env, recID, cameraID, startedAt)
	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
		CameraID:    cameraID,
		FilePath:    path,
		Format:      "h264",
		Encoding:    "h264",
		StartedAt:   startedAt.Format(time.RFC3339Nano),
		EndedAt:     startedAt.Add(dur).Format(time.RFC3339Nano),
		FileSize:    fileSize,
		RecordingID: recID,
	})
	return path
}

// recordingExists 查 DB 里某录像行是否仍在（未被合并消费）。
func recordingExists(t *testing.T, env *mergeTestEnv, cameraID, recID string) bool {
	t.Helper()
	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 500})
	require.NoError(t, err)
	for _, rec := range recs {
		if rec.ID == recID {
			return true
		}
	}
	return false
}

func mergedRowCount(t *testing.T, env *mergeTestEnv, cameraID string) int {
	t.Helper()
	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 500})
	require.NoError(t, err)
	n := 0
	for _, rec := range recs {
		if rec.MergeStatus == model.MergeStatusMerged {
			n++
		}
	}
	return n
}

// H1+H5: 29s 碎段被持有不折；30s（边界，非碎段）健康段即时折卷并把持有
// 碎段带上——一次折卷、同一桶。
func TestFragmentHold_HoldsFragmentFoldsHealthyImmediately(t *testing.T) {
	env, bus, r := fragEnv(t, intPtr(2)) // hold 2s

	cam := "cam-h1"
	base := mergeTestNow()

	// 29s → fragment: held, not folded while inside the hold window.
	publishFrag(t, env, bus, cam, "h1-frag", base, 29*time.Second, 1<<20)
	require.Never(t, func() bool { return !recordingExists(t, env, cam, "h1-frag") },
		600*time.Millisecond, 100*time.Millisecond,
		"a <30s segment must stay held (un-merged) inside the hold window")
	require.Nil(t, r.newestBucket(cam), "no bucket may exist while the only segment is held")

	// Exactly 30s → healthy: folds via debounce, dragging the held fragment
	// into the SAME fold (one rewrite covers both). Same hour window (+10s).
	publishFrag(t, env, bus, cam, "h1-ok", base.Add(10*time.Second), 30*time.Second, 1<<20)
	waitForBucketStable(t, r, cam, 2, 5*time.Second)
	require.False(t, recordingExists(t, env, cam, "h1-frag"), "held fragment folds with the healthy dispatch")
	require.False(t, recordingExists(t, env, cam, "h1-ok"))
}

// H2+H7: 到龄冲刷——最老碎段年龄 ≥ hold 时一次性折整批。三个 7s 碎段间隔
// 1s 排布（批内间隙必须在墙钟轴可见）：产物是【单个】桶行，墙钟跨度
// last.end−first.start = 23s。
func TestFragmentHold_TimerFlushFoldsBatchIntoOneBucket(t *testing.T) {
	env, bus, r := fragEnv(t, intPtr(1)) // hold 1s — 但深度未到 8，字节未到 64MB，
	// 只能由到龄定时器冲刷（冲刷后批内 3 段一次折卷）。

	cam := "cam-h2"
	base := mergeTestNow()
	for i := range 3 {
		publishFrag(t, env, bus, cam, "h2-"+string(rune('0'+i)),
			base.Add(time.Duration(i)*8*time.Second), 7*time.Second, 1<<20)
	}
	// 持有期内（最老碎段 <1s）不得折卷。
	require.Never(t, func() bool { return mergedRowCount(t, env, cam) > 0 },
		400*time.Millisecond, 50*time.Millisecond,
		"fragments must stay un-merged until the oldest reaches the hold age")

	waitForBucketStable(t, r, cam, 3, 5*time.Second)
	require.Equal(t, 1, mergedRowCount(t, env, cam),
		"the flushed batch must fold into ONE bucket product row (true batch fold)")
	require.False(t, recordingExists(t, env, cam, "h2-0"))
	require.False(t, recordingExists(t, env, cam, "h2-1"))
	require.False(t, recordingExists(t, env, cam, "h2-2"))

	// 墙钟跨度: first.start=base, last.end=base+16s+7s → 23s（含 1s 批内间隙×2）。
	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cam, Limit: 10})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.InDelta(t, 23.0, recs[0].Duration, 1.5,
		"wall span must include inter-fragment gaps, got %v", recs[0].Duration)
}

// H3a: 深度上限——第 8 个碎段到达即冲刷（远早于到龄）。
func TestFragmentHold_DepthCapFlushes(t *testing.T) {
	env, bus, r := fragEnv(t, intPtr(30)) // hold 30s — 只有深度能触发

	cam := "cam-h3a"
	base := mergeTestNow()
	for i := range fragmentHoldMaxCount {
		publishFrag(t, env, bus, cam, "h3a-"+string(rune('0'+i)),
			base.Add(time.Duration(i)*8*time.Second), 7*time.Second, 1<<20)
	}
	waitForBucketStable(t, r, cam, fragmentHoldMaxCount, 5*time.Second)
	require.Equal(t, 1, mergedRowCount(t, env, cam), "depth-cap flush folds the batch once")
}

// H3b: 字节上限——累计 FileSize ≥64MB 即冲刷（文件本体小，事件字节计数为准）。
func TestFragmentHold_ByteCapFlushes(t *testing.T) {
	env, bus, r := fragEnv(t, intPtr(30))

	cam := "cam-h3b"
	base := mergeTestNow()
	for i := range 4 {
		publishFrag(t, env, bus, cam, "h3b-"+string(rune('0'+i)),
			base.Add(time.Duration(i)*8*time.Second), 7*time.Second, 20<<20) // 4×20MB = 80MB ≥ 64MB
	}
	waitForBucketStable(t, r, cam, 4, 5*time.Second)
	require.Equal(t, 1, mergedRowCount(t, env, cam), "byte-cap flush folds the batch once")
}

// H4: 小时窗翻转——持有碎段都在窗 H，新碎段落窗 H+1 → 立即冲刷；派发按
// 窗拆 run，两个窗各一个桶产物（批不跨窗）。
func TestFragmentHold_WindowRolloverFlushes(t *testing.T) {
	env, bus, r := fragEnv(t, intPtr(30))
	_ = r // 断言走产物行计数（两个窗 → 两个桶行），无需桶内状态

	cam := "cam-h4"
	base := mergeTestNow() // HH:50
	publishFrag(t, env, bus, cam, "h4-a", base, 7*time.Second, 1<<20)
	publishFrag(t, env, bus, cam, "h4-b", base.Add(8*time.Second), 7*time.Second, 1<<20)
	// 下一个小时窗的碎段到达 → 冲刷整队。
	publishFrag(t, env, bus, cam, "h4-c", base.Add(15*time.Minute), 7*time.Second, 1<<20)

	require.Eventually(t, func() bool { return mergedRowCount(t, env, cam) >= 2 },
		5*time.Second, 50*time.Millisecond, "window rollover must flush and split per-window products")
	require.False(t, recordingExists(t, env, cam, "h4-a"))
	require.False(t, recordingExists(t, env, cam, "h4-b"))
	require.False(t, recordingExists(t, env, cam, "h4-c"))
}

// H6: rolling_fragment_hold_s=0 → 持有关闭，碎段照旧随 debounce 即时折卷。
func TestFragmentHold_DisabledFoldsImmediately(t *testing.T) {
	env, bus, r := fragEnv(t, intPtr(0))

	cam := "cam-h6"
	base := mergeTestNow()
	publishFrag(t, env, bus, cam, "h6-0", base, 7*time.Second, 1<<20)
	waitForBucketStable(t, r, cam, 1, 5*time.Second)
	require.False(t, recordingExists(t, env, cam, "h6-0"))
}

// H8: backfill 扫描的持有轨道——年轻碎段（ended < hold 前）不被周期扫描
// 收走折卷；过窗后照常由 backfill 批折。
func TestFragmentHold_BackfillDefersYoungFragments(t *testing.T) {
	env, bus, r := fragEnv(t, intPtr(300)) // 默认 300s 持有
	_ = bus

	cam := "cam-h8"
	now := time.Now().UTC()
	// 两个"老"碎段（1h 前，同窗 30s 间隔）——backfill 可正常批折。
	env.insertMergeableRecording(t, "h8-old-0", cam, now.Add(-2*time.Hour), now.Add(-2*time.Hour).Add(7*time.Second))
	env.insertMergeableRecording(t, "h8-old-1", cam, now.Add(-2*time.Hour).Add(30*time.Second), now.Add(-2*time.Hour).Add(37*time.Second))
	// 一个"年轻"碎段（3s 前结束）——在持有轨道内，扫描必须跳过。
	env.insertMergeableRecording(t, "h8-young", cam, now.Add(-10*time.Second), now.Add(-3*time.Second))

	_, err := r.BackfillCamera(context.Background(), cam, false)
	require.NoError(t, err)

	require.True(t, recordingExists(t, env, cam, "h8-young"),
		"the periodic sweep must NOT fold a fragment still inside its hold window")
	require.False(t, recordingExists(t, env, cam, "h8-old-0"), "old fragments fold via the sweep")
	require.False(t, recordingExists(t, env, cam, "h8-old-1"))
}

// appendBucketEnv 启动一个开着追加桶开关的 rolling 测试环境（音频无、
// 段为 30s+ 健康形态，直接走经典 debounce 折卷路径）。
func appendBucketEnv(t *testing.T) (*mergeTestEnv, *event.EventBus, *RollingMergeCoordinator) {
	t.Helper()
	env := newMergeTestEnv(t)
	t.Cleanup(func() { env.close(t) })
	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:      boolPtr(true),
		RollingDebounce:     "50ms",
		RollingWindow:       "1h",
		RollingAppendBucket: true,
	}
	r := newTestRollingCoordinator(env, cfg, bus)
	require.NoError(t, r.Start(context.Background()))
	t.Cleanup(r.Stop)
	return env, bus, r
}

// #853 集成：开关开启后纯视频健康段折卷走追加桶——桶文件带标记盒可重开，
// 多折只涨 mdat（原地补丁），行/时间轴不变量保持；关闭开关（默认）零影响
// 由既有全套件覆盖。
func TestRollingAppendBucket_EndToEnd(t *testing.T) {
	env, bus, r := appendBucketEnv(t)

	cam := "cam-ab"
	base := mergeTestNow()

	pub := func(i int) string {
		recID := "ab-" + string(rune('0'+i))
		startedAt := base.Add(time.Duration(i) * 61 * time.Second)
		return publishFrag(t, env, bus, cam, recID, startedAt, 45*time.Second, 1<<20)
	}

	pub(0)
	waitForBucketStable(t, r, cam, 1, 5*time.Second)
	pub(1)
	waitForBucketStable(t, r, cam, 2, 5*time.Second)

	// 桶文件是追加桶：可重开、镜像含 2 样本、文件不再全量重写（第二次折
	// 卷前后 inode 不变——以内容长度增长 < 全量重写的常识校验由单元测试
	// 覆盖，这里验证可重开 + 行不变量）。
	bi := r.newestBucket(cam)
	require.NotNil(t, bi)
	bi.mu.Lock()
	path := bi.mergedFilePath
	bi.mu.Unlock()
	ab, err := OpenAppendBucket(path)
	require.NoError(t, err, "the bucket file must carry the append-bucket marker")
	require.Equal(t, uint32(4), ab.SampleCount(), "2 segments × 2 samples")
	require.Equal(t, uint32(2), ab.ChunkCount(), "one chunk per fold")
	require.NoError(t, ab.Close())

	// 产物解析 + 行不变量。
	parsed, err := ParseSegment(path)
	require.NoError(t, err)
	require.Equal(t, 4, parsed.SampleCount)
	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cam, Limit: 10})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, model.MergeStatusMerged, recs[0].MergeStatus)
	// 墙钟 = 2 段 45s + 16s 间隙 = 106s。
	require.InDelta(t, 106.0, recs[0].Duration, 2.0, "wall span must cover both segments' span, got %v", recs[0].Duration)
	require.False(t, recordingExists(t, env, cam, "ab-0"))
	require.False(t, recordingExists(t, env, cam, "ab-1"))
}

// 开关关闭（默认）时桶文件必须是经典格式（无标记盒）。
func TestRollingAppendBucket_OffByDefault(t *testing.T) {
	env, bus, r := fragEnv(t, intPtr(0)) // hold off, append off

	cam := "cam-aboff"
	base := mergeTestNow()
	publishFrag(t, env, bus, cam, "aboff-0", base, 45*time.Second, 1<<20)
	waitForBucketStable(t, r, cam, 1, 5*time.Second)

	bi := r.newestBucket(cam)
	require.NotNil(t, bi)
	bi.mu.Lock()
	path := bi.mergedFilePath
	bi.mu.Unlock()
	_, err := OpenAppendBucket(path)
	require.ErrorIs(t, err, ErrNotAppendBucket, "default-off must keep the classic bucket format")
}
