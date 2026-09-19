package merge

// append_bucket_test.go — #853 顺序追加桶 TDD 契约。
//
//	A1 创建+两批追加：ParseSegment 往返一致（样本数/尺寸/时长/关键帧/
//    字节内容逐项对账）；
//	A2 重开恢复：镜像从表重建，后续追加无缝（RLE 跨折续 run）；
//	A3 崩溃恢复：mdat 之后的未提交尾部被截断；
//	A4 容量耗尽：ErrAppendCapacity（上层压紧回退）；
//	A5 驻留压缩：>gap 样本压到 cadence（两轴分离）；
//	A6 关键帧表：1 起索引、与源一致；
//	A7 时长字段：mvhd/tkhd/mdhd 补丁 = Σ 时长。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// buildAppendSource 落盘一个指定样本形状的 H264 源段并 Parse 出样本表。
func buildAppendSource(t *testing.T, dir, name string, samples [][]byte, durs []time.Duration) *SegmentInfo {
	t.Helper()
	path := createH264SegmentWithDurations(t, dir, name, wallSps, wallPps, samples, durs)
	info, err := ParseSegment(path)
	require.NoError(t, err)
	return info
}

func appendBucketCfg() AppendBucketConfig {
	return AppendBucketConfig{Window: time.Hour, MaxSamples: 4096}
}

// expectedAppendSamples 把源段样本映射为期望表（gap/cadence 压缩同规则）。
func expectedAppendSamples(timescale uint32, cadence, gap time.Duration, srcs []*SegmentInfo) (sizes []uint32, durs []uint32, keys []bool, wall, file uint64) {
	gapTicks := float64(timescale) * gap.Seconds()
	frameTicks := uint32(float64(timescale) * cadence.Seconds())
	for _, s := range srcs {
		for _, e := range s.Samples {
			sizes = append(sizes, e.Size)
			d := e.Duration
			durs = append(durs, d)
			wall += uint64(d)
			keys = append(keys, e.IsKeyFrame)
			_ = gapTicks
			_ = frameTicks
		}
	}
	return
}

func TestAppendBucket_CreateAppendRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cadence, gap := 100*time.Millisecond, 2*time.Second

	src1 := buildAppendSource(t, dir, "s1.mp4",
		[][]byte{wallIDR, wallP, wallP, wallIDR},
		[]time.Duration{33 * time.Millisecond, 33 * time.Millisecond, 33 * time.Millisecond, 33 * time.Millisecond})
	src2 := buildAppendSource(t, dir, "s2.mp4",
		[][]byte{wallIDR, wallP},
		[]time.Duration{33 * time.Millisecond, 33 * time.Millisecond})

	bucketPath := filepath.Join(dir, "bucket.mp4")
	b, err := CreateAppendBucket(bucketPath, src1, appendBucketCfg())
	require.NoError(t, err)

	st1, err := b.AppendBatch(cadence, gap, []AppendSource{{Path: src1.FilePath, Samples: src1.Samples}})
	require.NoError(t, err)
	st2, err := b.AppendBatch(cadence, gap, []AppendSource{{Path: src2.FilePath, Samples: src2.Samples}})
	require.NoError(t, err)
	require.NoError(t, b.Close())

	require.Equal(t, uint32(6), b.SampleCount(), "mirror sample count (post-close read is fine — plain struct field)")
	require.Equal(t, uint32(2), b.ChunkCount())
	require.Equal(t, st1.Bytes+st2.Bytes, b.MDatLen())

	// ParseSegment 往返：追加桶对标准解析器透明。
	parsed, err := ParseSegment(bucketPath)
	require.NoError(t, err)
	require.Equal(t, 6, parsed.SampleCount, "parsed sample count")
	require.True(t, parsed.KeyframesFromStss, "keyframes must come from stss")
	require.Equal(t, src1.Timescale, parsed.Timescale)
	require.Equal(t, src1.Codec, parsed.Codec)
	require.Equal(t, len(wallSps), len(parsed.SPS))

	keyCount := 0
	for _, s := range parsed.Samples {
		if s.IsKeyFrame {
			keyCount++
		}
	}
	require.Equal(t, 3, keyCount, "IDR count across both batches")

	// 字节内容逐项对账：每样本尺寸 + 内容与源一致。
	var srcSamples []SampleEntry
	srcSamples = append(srcSamples, src1.Samples...)
	srcSamples = append(srcSamples, src2.Samples...)
	bucketBytes, err := os.ReadFile(bucketPath)
	require.NoError(t, err)
	for i, s := range parsed.Samples {
		require.Equal(t, srcSamples[i].Size, s.Size, "sample %d size", i)
		require.Equal(t, srcSamples[i].Duration, s.Duration, "sample %d duration (uniform cadence, no compression)", i)
		// 该样本在桶中的字节 == 源文件对应字节。
		srcf, err := os.ReadFile(sourceOf(i, src1, src2))
		require.NoError(t, err)
		got := bucketBytes[s.Offset : s.Offset+int64(s.Size)]
		want := srcf[srcSamples[i].Offset : srcSamples[i].Offset+int64(srcSamples[i].Size)]
		require.Equal(t, string(want), string(got), "sample %d bytes", i)
	}

	// A7：时长字段 = Σ 时长（以解析侧 timescale 折算）。
	totalTicks := uint64(0)
	for _, s := range srcSamples {
		totalTicks += uint64(s.Duration)
	}
	require.Equal(t, totalTicks, uint64(uint32(totalTicks)))
	require.Equal(t, src1.Timescale, parsed.Timescale)
	require.InDelta(t, float64(totalTicks)/float64(parsed.Timescale)*1e9,
		float64(parsed.TotalDuration), float64(totalTicks)/float64(parsed.Timescale)*1e9*0.01+1e6)
}

func sourceOf(i int, s1, s2 *SegmentInfo) string {
	if i < len(s1.Samples) {
		return s1.FilePath
	}
	return s2.FilePath
}

func TestAppendBucket_ResumeAfterClose(t *testing.T) {
	dir := t.TempDir()
	cadence, gap := 100*time.Millisecond, 2*time.Second
	src1 := buildAppendSource(t, dir, "r1.mp4",
		[][]byte{wallIDR, wallP}, []time.Duration{40 * time.Millisecond, 40 * time.Millisecond})
	src2 := buildAppendSource(t, dir, "r2.mp4",
		[][]byte{wallIDR, wallP, wallP}, []time.Duration{40 * time.Millisecond, 40 * time.Millisecond, 40 * time.Millisecond})

	bucketPath := filepath.Join(dir, "bucket.mp4")
	b, err := CreateAppendBucket(bucketPath, src1, appendBucketCfg())
	require.NoError(t, err)
	_, err = b.AppendBatch(cadence, gap, []AppendSource{{Path: src1.FilePath, Samples: src1.Samples}})
	require.NoError(t, err)
	require.NoError(t, b.Close())

	// 重开：镜像从表重建。
	b2, err := OpenAppendBucket(bucketPath)
	require.NoError(t, err)
	require.Equal(t, uint32(2), b2.SampleCount())
	require.Equal(t, uint32(1), b2.ChunkCount())
	// RLE：重开后同 delta 样本续接末 run（总 run 数不因重开增长）。
	beforeRuns := readTableCount(t, bucketPath, "stts")
	_, err = b2.AppendBatch(cadence, gap, []AppendSource{{Path: src2.FilePath, Samples: src2.Samples}})
	require.NoError(t, err)
	afterRuns := readTableCount(t, bucketPath, "stts")
	require.Equal(t, beforeRuns, afterRuns, "uniform cadence across appends must extend ONE stts run")
	require.Equal(t, uint32(5), b2.SampleCount())
	require.Equal(t, uint32(2), b2.ChunkCount())
	require.NoError(t, b2.Close())

	parsed, err := ParseSegment(bucketPath)
	require.NoError(t, err)
	require.Equal(t, 5, parsed.SampleCount)
	// 时长字段在重开后仍被正确补丁（以解析侧 timescale 折算）。
	total := uint64(0)
	for _, s := range append(append([]SampleEntry{}, src1.Samples...), src2.Samples...) {
		total += uint64(s.Duration)
	}
	require.Equal(t, src1.Timescale, parsed.Timescale)
	require.InDelta(t, float64(total)/float64(parsed.Timescale)*1e9,
		float64(parsed.TotalDuration), float64(total)/float64(parsed.Timescale)*1e9*0.01+1e6)
}

func TestAppendBucket_CrashTailTruncated(t *testing.T) {
	dir := t.TempDir()
	cadence, gap := 100*time.Millisecond, 2*time.Second
	src1 := buildAppendSource(t, dir, "c1.mp4",
		[][]byte{wallIDR, wallP}, []time.Duration{40 * time.Millisecond, 40 * time.Millisecond})

	bucketPath := filepath.Join(dir, "bucket.mp4")
	b, err := CreateAppendBucket(bucketPath, src1, appendBucketCfg())
	require.NoError(t, err)
	_, err = b.AppendBatch(cadence, gap, []AppendSource{{Path: src1.FilePath, Samples: src1.Samples}})
	require.NoError(t, err)
	committed := b.MDatLen()
	require.NoError(t, b.Close())

	// 模拟崩溃残留：mdat 之后追加垃圾字节（表未提交这些样本）。
	f, err := os.OpenFile(bucketPath, os.O_APPEND|os.O_RDWR, 0o644)
	require.NoError(t, err)
	_, err = f.Write(make([]byte, 4096))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	b2, err := OpenAppendBucket(bucketPath)
	require.NoError(t, err)
	require.Equal(t, committed, b2.MDatLen(), "uncommitted tail must be truncated to the mdat declaration")
	fi, err := os.Stat(bucketPath)
	require.NoError(t, err)
	require.Equal(t, b2.layout.mdatDataStart+committed, fi.Size())

	// 截断后的桶仍可解析且可继续追加。
	parsed, err := ParseSegment(bucketPath)
	require.NoError(t, err)
	require.Equal(t, 2, parsed.SampleCount)
	src2 := buildAppendSource(t, dir, "c2.mp4",
		[][]byte{wallIDR}, []time.Duration{40 * time.Millisecond})
	_, err = b2.AppendBatch(cadence, gap, []AppendSource{{Path: src2.FilePath, Samples: src2.Samples}})
	require.NoError(t, err)
	require.NoError(t, b2.Close())
	parsed, err = ParseSegment(bucketPath)
	require.NoError(t, err)
	require.Equal(t, 3, parsed.SampleCount)
}

func TestAppendBucket_CapacityExhausted(t *testing.T) {
	dir := t.TempDir()
	cadence, gap := 100*time.Millisecond, 2*time.Second
	src1 := buildAppendSource(t, dir, "p1.mp4",
		[][]byte{wallIDR, wallP, wallP, wallP, wallP, wallP, wallP, wallP},
		[]time.Duration{40 * time.Millisecond, 40, 40, 40, 40, 40, 40, 40 * time.Millisecond})

	bucketPath := filepath.Join(dir, "bucket.mp4")
	cfg := AppendBucketConfig{Window: time.Hour, MaxSamples: 8} // 首批即满
	b, err := CreateAppendBucket(bucketPath, src1, cfg)
	require.NoError(t, err)
	require.LessOrEqual(t, b.SampleCap(), uint32(8))
	_, err = b.AppendBatch(cadence, gap, []AppendSource{{Path: src1.FilePath, Samples: src1.Samples}})
	require.NoError(t, err)
	require.Equal(t, uint32(8), b.SampleCount())

	_, err = b.AppendBatch(cadence, gap, []AppendSource{{Path: src1.FilePath, Samples: src1.Samples[:1]}})
	require.ErrorIs(t, err, ErrAppendCapacity)
	require.NoError(t, b.Close())

	// 容量拒绝不得污染桶：仍只含首批。
	parsed, err := ParseSegment(bucketPath)
	require.NoError(t, err)
	require.Equal(t, 8, parsed.SampleCount)
}

func TestAppendBucket_DwellCompression(t *testing.T) {
	dir := t.TempDir()
	cadence, gap := 100*time.Millisecond, 2*time.Second
	// 两个 30s 驻留样本（TL 形态）+ 一个正常帧。
	src := buildAppendSource(t, dir, "d1.mp4",
		[][]byte{wallIDR, wallIDR, wallP},
		[]time.Duration{30 * time.Second, 30 * time.Second, 33 * time.Millisecond})

	bucketPath := filepath.Join(dir, "bucket.mp4")
	b, err := CreateAppendBucket(bucketPath, src, appendBucketCfg())
	require.NoError(t, err)
	st, err := b.AppendBatch(cadence, gap, []AppendSource{{Path: src.FilePath, Samples: src.Samples}})
	require.NoError(t, err)
	require.NoError(t, b.Close())

	require.Greater(t, st.WallTicks, st.FileTicks*10, "dwell-heavy input must compress (wall ≫ file)")
	frameTicks := uint32(float64(src.Timescale) * cadence.Seconds())
	require.Equal(t, 2*uint64(frameTicks)+uint64(src.Samples[2].Duration), st.FileTicks)

	parsed, err := ParseSegment(bucketPath)
	require.NoError(t, err)
	require.Equal(t, 3, parsed.SampleCount)
	require.Equal(t, src.Timescale, parsed.Timescale)
	require.InDelta(t, float64(st.FileTicks)/float64(parsed.Timescale)*1e9,
		float64(parsed.TotalDuration), float64(st.FileTicks)/float64(parsed.Timescale)*1e9*0.01+1e6)
}

func TestAppendBucket_KeyframeTableOneIndexed(t *testing.T) {
	dir := t.TempDir()
	cadence, gap := 100*time.Millisecond, 2*time.Second
	// P 开头样本经 AlignToKeyframe 已在上游丢弃；这里直接给 IDR 开头形态。
	src := buildAppendSource(t, dir, "k1.mp4",
		[][]byte{wallIDR, wallP, wallP, wallIDR, wallP},
		[]time.Duration{33 * time.Millisecond, 33, 33, 33, 33 * time.Millisecond})

	bucketPath := filepath.Join(dir, "bucket.mp4")
	b, err := CreateAppendBucket(bucketPath, src, appendBucketCfg())
	require.NoError(t, err)
	_, err = b.AppendBatch(cadence, gap, []AppendSource{{Path: src.FilePath, Samples: src.Samples}})
	require.NoError(t, err)
	require.NoError(t, b.Close())

	parsed, err := ParseSegment(bucketPath)
	require.NoError(t, err)
	require.True(t, parsed.KeyframesFromStss)
	var gotFlags []bool
	for _, ps := range parsed.Samples {
		gotFlags = append(gotFlags, ps.IsKeyFrame)
	}
	var srcFlags []bool
	for _, ss := range src.Samples {
		srcFlags = append(srcFlags, ss.IsKeyFrame)
	}
	t.Logf("DBG src=%v parsed=%v", srcFlags, gotFlags)
	for i, srcs := range src.Samples {
		require.Equal(t, srcs.IsKeyFrame, parsed.Samples[i].IsKeyFrame, "sample %d keyframe", i)
	}
}

// readTableCount 解析桶文件读某表 entry_count（验证 RLE/计数补丁）。
func readTableCount(t *testing.T, path, table string) uint32 {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	var count uint32
	var found bool
	// 轻量盒扫描：找表盒读 +12 偏移的 count。
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	for i := 0; i+8 <= len(data); i++ {
		if string(data[i+4:i+8]) == table {
			size := int(uint32(data[i])<<24 | uint32(data[i+1])<<16 | uint32(data[i+2])<<8 | uint32(data[i+3]))
			if size >= 16 && i+16 <= len(data) {
				count = uint32(data[i+12])<<24 | uint32(data[i+13])<<16 | uint32(data[i+14])<<8 | uint32(data[i+15])
				found = true
			}
		}
	}
	require.True(t, found, "table %s not found", table)
	return count
}

// TestAppendBucket_NotAppendBucketDetected：非追加桶文件（经典合并产物）
// 打开时报 ErrNotAppendBucket——集成层据此回退经典路径。
func TestAppendBucket_NotAppendBucketDetected(t *testing.T) {
	dir := t.TempDir()
	src := buildAppendSource(t, dir, "classic.mp4",
		[][]byte{wallIDR, wallP}, []time.Duration{33 * time.Millisecond, 33 * time.Millisecond})
	_, err := OpenAppendBucket(src.FilePath)
	require.ErrorIs(t, err, ErrNotAppendBucket)
}

// TestAppendBucket_EmptyAppendNoop：空批返回零贡献且不改文件。
func TestAppendBucket_EmptyAppendNoop(t *testing.T) {
	dir := t.TempDir()
	src := buildAppendSource(t, dir, "e1.mp4",
		[][]byte{wallIDR}, []time.Duration{33 * time.Millisecond})
	bucketPath := filepath.Join(dir, "bucket.mp4")
	b, err := CreateAppendBucket(bucketPath, src, appendBucketCfg())
	require.NoError(t, err)
	st, err := b.AppendBatch(100*time.Millisecond, 2*time.Second, nil)
	require.NoError(t, err)
	require.Equal(t, AppendStats{}, st)
	require.Equal(t, uint32(0), b.SampleCount())
	require.NoError(t, b.Close())
	// 空桶 0 样本：解析器按“无样本”拒绝（真实流程里桶创建后立即有首折）。
	_, err = ParseSegment(bucketPath)
	require.ErrorContains(t, err, "no samples")
}

var _ = context.Background
