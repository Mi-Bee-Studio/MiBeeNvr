// wall_axis_test.go — 合并播放时间轴正确性的场景矩阵(#650 TDD 套件)。
//
// 不变量(每条 merged 行必须全部满足,与相机行为无关):
//
//	I1. duration == 墙钟跨度(started_at..ended_at),与 TL 压缩无关;
//	I2. timeline_map ≥2 点、首点 [0,0]、双轴单调、末点 wall == duration;
//	I3. 末点 file == 产物文件真实时长(映射反映现实,播放定位才准)。
//
// 场景矩阵(每场景一个测试,覆盖 2026-08/09 现网踩过的全部形态及类比):
//
//	S1 纯延时小时           — 全 30s 驻留,重压缩;
//	S2 段内模式切换         — 驻留+全速突发共存于同一源段;
//	S3 智能编码慢相机       — "全速"段样本 ~2.1s(阈值下,不得误压缩)与稀疏段交错;
//	S4 日夜帧率切换         — 0.05s/0.1s 样本交替 append;
//	S5 离线补偿重放旧段     — 事件时间在过去,行时间必须用事件真值(不得用 now);
//	S6 窗口翻转双桶         — 跨小时窗 → 两个独立产物,各自正确;
//	S7 批量派发路径         — 同一 debounce 窗口 2+ 段 → batch 产物同样守不变量;
//	S8 极端稀疏             — 单段 60×60s 驻留(1h 墙钟)重压缩。
package merge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// wallSps/wallPps/wallIDR/wallP — 与 timelapse_compress_test 相同的已知良好 fixture。
var (
	wallSps = []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	wallPps = []byte{0x68, 0xce, 0x38, 0x80}
	wallIDR = []byte{0x65, 0x88, 0x80, 0x40}
	wallP   = []byte{0x41, 0x10, 0x00, 0x0c}
)

// wallEnv 启动一个 rolling 协调器测试环境。
func wallEnv(t *testing.T) (*mergeTestEnv, *event.EventBus, *RollingMergeCoordinator) {
	t.Helper()
	env := newMergeTestEnv(t)
	t.Cleanup(func() { env.close(t) })
	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:  boolPtr(true),
		RollingDebounce: "50ms",
		RollingWindow:   "1h",
	}
	r := newTestRollingCoordinator(env, cfg, bus)
	require.NoError(t, r.Start(context.Background()))
	t.Cleanup(r.Stop)

	orig := TimelapseFrameDur
	TimelapseFrameDur = 100 * time.Millisecond
	t.Cleanup(func() { TimelapseFrameDur = orig })
	return env, bus, r
}

// wallSample 描述一个源样本:关键帧与否 + 时长。
type wallSample struct {
	key bool
	d   time.Duration
}

// publishWallSeg 落盘一个指定样本形状的源段,入库并发布事件(startedAt 起墙钟 Σd)。
// 返回 (行ID, 墙钟跨度秒)。
func publishWallSeg(t *testing.T, env *mergeTestEnv, bus *event.EventBus, cameraID, recID string, startedAt time.Time, samples []wallSample) (string, float64) {
	t.Helper()
	dir := t.TempDir()
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
	path := createH264SegmentWithDurations(t, dir, recID+".mp4", wallSps, wallPps, nalus, durs)
	fi, err := os.Stat(path)
	require.NoError(t, err)
	wallSec := wall.Seconds()
	rec := &model.Recording{
		ID:         recID,
		CameraID:   cameraID,
		FilePath:   path,
		Format:     model.FormatH264,
		StartedAt:  startedAt,
		EndedAt:    startedAt.Add(wall),
		Duration:   wallSec,
		FileSize:   fi.Size(),
		FrameCount: len(samples),
	}
	require.NoError(t, env.db.InsertRecording(context.Background(), rec))
	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
		CameraID:    cameraID,
		FilePath:    path,
		Format:      "h264",
		Encoding:    "h264",
		StartedAt:   startedAt.Format(time.RFC3339Nano),
		EndedAt:     rec.EndedAt.Format(time.RFC3339Nano),
		FileSize:    fi.Size(),
		RecordingID: recID,
	})
	return recID, wallSec
}

// segWallOf 求一段样本的墙钟跨度。
func segWallOf(samples []wallSample) time.Duration {
	var d time.Duration
	for _, s := range samples {
		d += s.d
	}
	return d
}

// sparseSeg 是纯 TL 段: n 个 30s IDR 驻留。
func sparseSeg(n int) []wallSample {
	s := make([]wallSample, n)
	for i := range s {
		s[i] = wallSample{key: true, d: 30 * time.Second}
	}
	return s
}

// requireWallAxisProduct 对一条 merged 行断言全部不变量(I1..I3),返回产物解析信息。
func requireWallAxisProduct(t *testing.T, env *mergeTestEnv, cameraID string, wantRowCount int) []*SegmentInfo {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var recs []model.Recording
	for time.Now().Before(deadline) {
		all, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 100})
		require.NoError(t, err)
		recs = nil
		for _, rr := range all {
			if rr.MergeStatus == model.MergeStatusMerged {
				recs = append(recs, rr)
			}
		}
		if len(recs) == wantRowCount && len(all) == wantRowCount {
			break // 源行已被消费,只剩 merged 行
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.Len(t, recs, wantRowCount, "expected %d merged rows for %s", wantRowCount, cameraID)

	var out []*SegmentInfo
	for _, listed := range recs {
		full, err := env.db.GetRecording(context.Background(), listed.ID)
		require.NoError(t, err)
		wallSpan := full.EndedAt.Sub(full.StartedAt).Seconds()
		// I1: 行时长 = 墙钟跨度。
		require.InDelta(t, wallSpan, full.Duration, 2.0,
			"row %s: duration %.2f must equal wall span %.2f", full.ID, full.Duration, wallSpan)

		// I2: map 形态与单调性,末点 wall == duration。
		var pairs [][2]float64
		require.NoError(t, json.Unmarshal([]byte(full.TimelineMap), &pairs),
			"row %s: timeline_map must be valid JSON, got %q", full.ID, full.TimelineMap)
		require.GreaterOrEqual(t, len(pairs), 2, "row %s: map needs ≥2 points", full.ID)
		require.InDelta(t, 0, pairs[0][0], 1e-9)
		require.InDelta(t, 0, pairs[0][1], 1e-9)
		for i := 1; i < len(pairs); i++ {
			require.GreaterOrEqual(t, pairs[i][0], pairs[i-1][0], "wall axis must not shrink: %+v", pairs)
			require.GreaterOrEqual(t, pairs[i][1], pairs[i-1][1], "file axis must not shrink: %+v", pairs)
		}
		require.InDelta(t, full.Duration, pairs[len(pairs)-1][0], 2.0,
			"row %s: map wall endpoint must match duration: %+v vs %.2f", full.ID, pairs, full.Duration)

		// I3: 末点 file == 产物文件真实时长。
		prodPath := full.FilePath
		if !filepath.IsAbs(prodPath) {
			prodPath = filepath.Join(env.store.RootDir(), prodPath)
		}
		info, err := ParseSegment(prodPath)
		require.NoError(t, err, "row %s: product must parse", full.ID)
		require.InDelta(t, info.TotalDuration.Seconds(), pairs[len(pairs)-1][1], 0.75,
			"row %s: map file endpoint %.2f must match product file duration %.2f",
			full.ID, pairs[len(pairs)-1][1], info.TotalDuration.Seconds())
		out = append(out, info)
	}
	return out
}

// ---------------------------------------------------------------------------
// S1 纯延时小时:6 个稀疏段(各 2×30s 驻留)滚动合并 → 墙钟完整、文件重压缩。
// ---------------------------------------------------------------------------
func TestWallAxis_PureTimelapseHour(t *testing.T) {
	env, bus, r := wallEnv(t)
	cam := "cam-s1"
	base := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)
	for i := 0; i < 6; i++ {
		publishWallSeg(t, env, bus, cam, fmt.Sprintf("s1-%d", i), base.Add(time.Duration(i)*61*time.Second), sparseSeg(2))
		waitForBucketStable(t, r, cam, i+1, 5*time.Second)
	}
	infos := requireWallAxisProduct(t, env, cam, 1)
	// 文件被重压缩: 12 帧按 0.1s 节拍 ≈1.2s ≪ 墙钟 ~366s。
	require.Less(t, infos[0].TotalDuration.Seconds(), 5.0,
		"pure-TL product must be heavily compressed, file=%.2fs", infos[0].TotalDuration.Seconds())
}

// ---------------------------------------------------------------------------
// S2 段内模式切换:同一源段内 30s 驻留与全速突发共存(常驻延时退出口径)。
// 压缩只作用于 >2s 驻留;突发帧保留真实节拍;墙钟不丢。
// ---------------------------------------------------------------------------
func TestWallAxis_ModeSwitchInsideSegment(t *testing.T) {
	env, bus, r := wallEnv(t)
	cam := "cam-s2"
	base := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)
	mixed := func() []wallSample {
		return []wallSample{
			{key: true, d: 30 * time.Second},
			{key: true, d: 30 * time.Second},
			{key: false, d: 33 * time.Millisecond}, {key: false, d: 33 * time.Millisecond},
			{key: false, d: 33 * time.Millisecond}, {key: false, d: 33 * time.Millisecond},
			{key: true, d: 30 * time.Second},
		}
	}
	for i := 0; i < 3; i++ {
		publishWallSeg(t, env, bus, cam, fmt.Sprintf("s2-%d", i), base.Add(time.Duration(i)*91*time.Second), mixed())
		waitForBucketStable(t, r, cam, i+1, 5*time.Second)
	}
	infos := requireWallAxisProduct(t, env, cam, 1)
	// 3×(60s 驻留→0.2s) + 3×4 突发帧 33ms: 文件 ≈ 0.9s ≪ 墙钟 ~273s。
	require.Less(t, infos[0].TotalDuration.Seconds(), 5.0)
}

// ---------------------------------------------------------------------------
// S3 智能编码慢相机:相机静态抽帧到 ~2.1s/帧(阈值之下)。其"全速"段不得被
// 误压缩(样本时长原样保留);交错的稀疏 TL 段正常压缩;墙钟全程保真。
// ---------------------------------------------------------------------------
func TestWallAxis_SmartCodecSlowCamera(t *testing.T) {
	env, bus, r := wallEnv(t)
	cam := "cam-s3"
	base := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)

	slow := func() []wallSample {
		s := make([]wallSample, 30) // 30 × 2.09s ≈ 62.7s 墙钟
		s[0] = wallSample{key: true, d: 2090 * time.Millisecond}
		for i := 1; i < 30; i++ {
			s[i] = wallSample{key: false, d: 2090 * time.Millisecond}
		}
		return s
	}
	// slow(62.7s) → sparse(60s) → slow → sparse: 每段间隔 2s 防同窗派发。
	at := base
	for i := 0; i < 4; i++ {
		var samples []wallSample
		if i%2 == 0 {
			samples = slow()
		} else {
			samples = sparseSeg(2)
		}
		publishWallSeg(t, env, bus, cam, fmt.Sprintf("s3-%d", i), at, samples)
		at = at.Add(segWallOf(samples) + 2*time.Second)
		waitForBucketStable(t, r, cam, i+1, 5*time.Second)
	}
	infos := requireWallAxisProduct(t, env, cam, 1)
	// slow 部分不压缩: 文件时长 ≈ 2×62.7 + 2×0.2 ≈ 126s(若误压缩会 ≪)。
	require.InDelta(t, 126.0, infos[0].TotalDuration.Seconds(), 6.0,
		"slow-camera NORMAL samples must keep real durations, file=%.2f", infos[0].TotalDuration.Seconds())
}

// ---------------------------------------------------------------------------
// S4 日夜帧率切换:0.05s(20fps 白天) 与 0.1s(10fps 夜间) 样本交替 append。
// 全部在阈值之下 → 不压缩、近恒等映射;墙钟保真。
// ---------------------------------------------------------------------------
func TestWallAxis_DayNightCadenceSwitch(t *testing.T) {
	env, bus, r := wallEnv(t)
	cam := "cam-s4"
	base := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)
	at := base
	for i := 0; i < 4; i++ {
		frameDur := 50 * time.Millisecond
		if i%2 == 1 {
			frameDur = 100 * time.Millisecond // 夜间 10fps
		}
		s := make([]wallSample, 40) // 40 帧 ≈ 2-4s 段
		s[0] = wallSample{key: true, d: frameDur}
		for j := 1; j < 40; j++ {
			s[j] = wallSample{key: false, d: frameDur}
		}
		publishWallSeg(t, env, bus, cam, fmt.Sprintf("s4-%d", i), at, s)
		at = at.Add(40 * frameDur).Add(2 * time.Second)
		waitForBucketStable(t, r, cam, i+1, 5*time.Second)
	}
	infos := requireWallAxisProduct(t, env, cam, 1)
	// 近恒等: 4 段内容 2+4+2+4 = 12s 全保留(无 >阈值 驻留,不压缩;段间 2s 间隙在墙钟轴,不在文件轴)。
	require.InDelta(t, 12.0, infos[0].TotalDuration.Seconds(), 1.5)
}

// ---------------------------------------------------------------------------
// S5 离线补偿重放旧段:事件 started/ended 都在过去。行时间必须采用事件真值
// (2026-09-01 实网 bug: endedAt=time.Now() 把旧段行膨胀出数小时幻影墙钟)。
// ---------------------------------------------------------------------------
func TestWallAxis_ReplayedOldSegment(t *testing.T) {
	env, bus, _ := wallEnv(t)
	cam := "cam-s5"
	started := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	publishWallSeg(t, env, bus, cam, "s5-old", started, sparseSeg(2))

	deadline := time.Now().Add(10 * time.Second)
	var full *model.Recording
	for time.Now().Before(deadline) {
		recs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cam, Limit: 10})
		require.NoError(t, err)
		if len(recs) == 1 {
			full, err = env.db.GetRecording(context.Background(), recs[0].ID)
			require.NoError(t, err)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.NotNil(t, full, "merged row must appear")
	require.InDelta(t, 60.0, full.Duration, 2.0, "wall span = 60s, not now-started")
	require.WithinDuration(t, started.Add(60*time.Second), full.EndedAt, 2*time.Second,
		"ended_at must come from the event, not time.Now(): %v", full.EndedAt)
}

// ---------------------------------------------------------------------------
// S6 窗口翻转:同一相机跨小时窗 → 两个独立桶/产物,各自守不变量。
// ---------------------------------------------------------------------------
func TestWallAxis_WindowRolloverTwoBuckets(t *testing.T) {
	env, bus, _ := wallEnv(t)
	cam := "cam-s6"
	h0 := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)
	h1 := h0.Add(time.Hour)
	publishWallSeg(t, env, bus, cam, "s6-a", h0, sparseSeg(2))
	time.Sleep(300 * time.Millisecond)
	publishWallSeg(t, env, bus, cam, "s6-b", h0.Add(61*time.Second), sparseSeg(2))
	time.Sleep(300 * time.Millisecond)
	publishWallSeg(t, env, bus, cam, "s6-c", h1, sparseSeg(2))
	time.Sleep(300 * time.Millisecond)
	publishWallSeg(t, env, bus, cam, "s6-d", h1.Add(61*time.Second), sparseSeg(2))
	infos := requireWallAxisProduct(t, env, cam, 2)
	for _, info := range infos {
		require.Less(t, info.TotalDuration.Seconds(), 5.0, "both buckets compressed")
	}
}

// ---------------------------------------------------------------------------
// S7 批量派发路径:同一 debounce 窗口内 2+ 段 → batch 合并产物同样守不变量
// (2026-09-01 发现的第二条产出行路径,与滚动 append 并存)。
// ---------------------------------------------------------------------------
func TestWallAxis_BatchDispatchPath(t *testing.T) {
	env, bus, _ := wallEnv(t)
	cam := "cam-s7"
	base := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)
	// 无间隔连发 → 同一 debounce 窗口 → dispatch 聚齐 2+ 段走 batch。
	publishWallSeg(t, env, bus, cam, "s7-a", base, sparseSeg(2))
	publishWallSeg(t, env, bus, cam, "s7-b", base.Add(61*time.Second), sparseSeg(2))
	publishWallSeg(t, env, bus, cam, "s7-c", base.Add(122*time.Second), sparseSeg(2))
	infos := requireWallAxisProduct(t, env, cam, 1)
	require.Less(t, infos[0].TotalDuration.Seconds(), 5.0, "batch product compressed")
}

// ---------------------------------------------------------------------------
// S8 极端稀疏:单段 60×60s 驻留(1 小时墙钟、61 帧) → 文件 ~6s,不变量成立。
// ---------------------------------------------------------------------------
func TestWallAxis_LongSparseExtreme(t *testing.T) {
	env, bus, _ := wallEnv(t)
	cam := "cam-s8"
	base := time.Now().UTC().Truncate(time.Hour).Add(2 * time.Minute)
	publishWallSeg(t, env, bus, cam, "s8-long", base, sparseSeg(60))
	infos := requireWallAxisProduct(t, env, cam, 1)
	require.Less(t, infos[0].TotalDuration.Seconds(), 10.0,
		"60 dwell frames at 0.1s cadence ≈6s, got %.2f", infos[0].TotalDuration.Seconds())
}

// ---------------------------------------------------------------------------
// 单元级:stats 映射反映现实 — FileDurationSec == 产物解析时长,
// WallDurationSec == 输入原始时长和(不因压缩缩水)。
// ---------------------------------------------------------------------------
func TestMergeStats_MapMatchesProductFile(t *testing.T) {
	dir := t.TempDir()
	orig := TimelapseFrameDur
	TimelapseFrameDur = 100 * time.Millisecond
	t.Cleanup(func() { TimelapseFrameDur = orig })

	sparse := createH264SegmentWithDurations(t, dir, "u-sparse.mp4", wallSps, wallPps,
		[][]byte{wallIDR, wallIDR, wallIDR}, []time.Duration{30 * time.Second, 30 * time.Second, 30 * time.Second})
	normal := createH264SegmentWithDurations(t, dir, "u-normal.mp4", wallSps, wallPps,
		[][]byte{wallIDR, wallP, wallP}, []time.Duration{33 * time.Millisecond, 33 * time.Millisecond, 33 * time.Millisecond})
	si, err := ParseSegment(sparse)
	require.NoError(t, err)
	ni, err := ParseSegment(normal)
	require.NoError(t, err)

	outPath := filepath.Join(dir, "u-out.mp4")
	stats, err := MergeMP4Segments(context.Background(), []*SegmentInfo{si, ni}, outPath)
	require.NoError(t, err)

	wantWall := si.TotalDuration.Seconds() + ni.TotalDuration.Seconds()
	require.InDelta(t, wantWall, stats.WallDurationSec(), 0.1,
		"stats wall = sum of ORIGINAL input durations")
	out, err := ParseSegment(outPath)
	require.NoError(t, err)
	require.InDelta(t, out.TotalDuration.Seconds(), stats.FileDurationSec(), 0.5,
		"stats file axis must equal the product's real duration")
}
