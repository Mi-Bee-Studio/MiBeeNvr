package merge

// repair_tkhd_test.go —— #853 现场回归（2026-09-24）的修复链回归测试：
// 追加桶历史产物 stsd 样本条目/tkhd 宽高 0×0 → RepairZeroDimensions 从
// 文件自身 SPS 原地还原两处，修复后必须过 Chromium 级结构校验。
//
// 回归背景（两轮定位的第一轮教训）：只补 tkhd 不够——Chromium 构建流
// 配置读的是 stsd 样本条目；TestRepairZeroTkhdOnly 明确复刻「tkhd 已修
// 而 stsd 仍为 0」的中间态并钉住修复器必须补齐 stsd。

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func buildTestBucket(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := buildAppendSourceWithSPS(t, dir, "s1.mp4", h264SPS1920Fixture, wallPps,
		[][]byte{wallIDR, wallP, wallP},
		[]time.Duration{33 * time.Millisecond, 33 * time.Millisecond, 33 * time.Millisecond})

	bucketPath := filepath.Join(dir, "bucket.mp4")
	b, err := CreateAppendBucket(bucketPath, src, appendBucketCfg())
	require.NoError(t, err)
	_, err = b.AppendBatch(100*time.Millisecond, 2*time.Second, []AppendSource{{Path: src.FilePath, Samples: src.Samples}})
	require.NoError(t, err)
	require.NoError(t, b.Close())
	require.NoError(t, ValidateMergedMP4(bucketPath))
	return bucketPath
}

func zeroDimsAt(t *testing.T, path string, tkhd, stsd bool) {
	t.Helper()
	vt, err := ProbeVideoTrack(path)
	require.NoError(t, err)
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	require.NoError(t, err)
	defer f.Close()
	if tkhd {
		require.Greater(t, vt.TkhdDimsOffset, int64(0))
		_, err = f.WriteAt([]byte{0, 0, 0, 0, 0, 0, 0, 0}, vt.TkhdDimsOffset)
		require.NoError(t, err)
	}
	if stsd {
		require.Greater(t, vt.SampleEntryDimsOffset, int64(0))
		_, err = f.WriteAt([]byte{0, 0, 0, 0}, vt.SampleEntryDimsOffset)
		require.NoError(t, err)
	}
}

func TestRepairZeroDimensionsBothFields(t *testing.T) {
	bucketPath := buildTestBucket(t)

	// 复刻历史坏产物：两处宽高全部清零。
	zeroDimsAt(t, bucketPath, true, true)

	broken, err := ProbeVideoTrack(bucketPath)
	require.NoError(t, err)
	require.Zero(t, broken.Width)
	require.Zero(t, broken.SampleEntryWidth)

	tk, se, w, h, err := RepairZeroDimensions(bucketPath)
	require.NoError(t, err)
	require.True(t, tk)
	require.True(t, se)
	require.Equal(t, uint16(1920), w)
	require.Equal(t, uint16(1080), h)
	require.NoError(t, ValidateMergedMP4(bucketPath))

	fixed, err := ProbeVideoTrack(bucketPath)
	require.NoError(t, err)
	require.Equal(t, uint16(1920), fixed.SampleEntryWidth)
	require.Equal(t, uint16(1080), fixed.SampleEntryHeight)

	// 幂等：已修复文件再跑一次不再改写。
	tk2, se2, w2, h2, err := RepairZeroDimensions(bucketPath)
	require.NoError(t, err)
	require.False(t, tk2)
	require.False(t, se2)
	require.Equal(t, uint16(1920), w2)
	require.Equal(t, uint16(1080), h2)
}

func TestRepairZeroTkhdOnly(t *testing.T) {
	// 第一轮修复的中间态：tkhd 已补、stsd 样本条目仍为 0——Chromium 照样
	// 拒播。修复器必须只动 stsd 并让校验器转绿。
	bucketPath := buildTestBucket(t)
	zeroDimsAt(t, bucketPath, false, true)

	broken, err := ProbeVideoTrack(bucketPath)
	require.NoError(t, err)
	require.NotZero(t, broken.Width)
	require.Zero(t, broken.SampleEntryWidth)
	require.Error(t, ValidateMergedMP4(bucketPath))

	tk, se, w, h, err := RepairZeroDimensions(bucketPath)
	require.NoError(t, err)
	require.False(t, tk)
	require.True(t, se)
	require.Equal(t, uint16(1920), w)
	require.Equal(t, uint16(1080), h)
	require.NoError(t, ValidateMergedMP4(bucketPath))
}

func TestRepairZeroTkhd_OffsetIsLastEightBytes(t *testing.T) {
	// 钉死两处补丁位点语义：tkhd 宽度 = 盒末 8 字节（16.16）；
	// stsd 样本条目宽度 = 盒内 u16（盒起始 +8 头 +24 固定字段）。
	dir := t.TempDir()
	src := buildAppendSourceWithSPS(t, dir, "s1.mp4", h264SPS1920Fixture, wallPps,
		[][]byte{wallIDR, wallP}, []time.Duration{33 * time.Millisecond, 33 * time.Millisecond})
	bucketPath := filepath.Join(dir, "bucket.mp4")
	b, err := CreateAppendBucket(bucketPath, src, appendBucketCfg())
	require.NoError(t, err)
	require.NoError(t, b.Close())

	vt, err := ProbeVideoTrack(bucketPath)
	require.NoError(t, err)
	f, err := os.Open(bucketPath)
	require.NoError(t, err)
	defer f.Close()
	var got [8]byte
	_, err = f.ReadAt(got[:], vt.TkhdDimsOffset)
	require.NoError(t, err)
	w16 := binary.BigEndian.Uint32(got[0:4])
	h16 := binary.BigEndian.Uint32(got[4:8])
	require.Equal(t, uint32(1920)<<16, w16)
	require.Equal(t, uint32(1080)<<16, h16)

	var se [4]byte
	_, err = f.ReadAt(se[:], vt.SampleEntryDimsOffset)
	require.NoError(t, err)
	require.Equal(t, uint16(1920), binary.BigEndian.Uint16(se[0:2]))
	require.Equal(t, uint16(1080), binary.BigEndian.Uint16(se[2:4]))
}
