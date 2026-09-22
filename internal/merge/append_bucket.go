package merge

// append_bucket.go — #853 顺序追加桶（B1：预留容量 moov 原地补丁）。
//
// 滚动合并的每折 O(桶大小) 全量重写是 #851 写放大的结构根源；#852 把
// 折卷次数降为 1/N，本模块把单次代价从 O(桶) 降为 O(段)：桶创建时在
// stsz/stco/stts/stss/stsc 每表预留容量槽位，折卷 = mdat 尾部顺序追加
// 新段字节 + 空槽写入表条目 + 计数字段原地补丁，绝不重写已有字节。
//
// 文件布局（v1，纯视频桶——音频段继续走经典 MergeMP4Segments 路径）：
//
//	ftyp                      标准（writeMergeFtyp）
//	free                      私有标记盒：magic + 容量 + 全部补丁偏移（LE）
//	moov (预留,总长固定)      mvhd/trak(tkhd mdhd hdlr minf stbl(stsd stts
//	                          stsc stsz stss stco))，表容量槽位清零
//	mdat (增长)               每折追加一个 chunk
//
// 每折一个 chunk（stco/stsc 各得一槽），mdat 中各折的字节连续追加——
// 解析器按 entry_count 迭代表项、忽略尾部零槽，布局对标准播放器透明
// （兼容矩阵见 issue #853 验收）。
//
// 崩溃一致性（进程崩溃即页缓存序，#781 分层下不做 fsync）：提交点 =
// 最后一次 mdat size 补丁。恢复（OpenAppendBucket 自检）：文件尾长于
// mdat 声明 → 截断到 mdat 末（丢未提交尾部，源段文件仍在，由上层回折）。
//
// RPi 3B 实测基线：镜像 + 补丁元数据常驻约
// 1–2MB/活跃相机；`merge.rolling_append_bucket` 默认 false，Web 设置页
// 可操作（见 rolling 集成层）。

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	mp4 "github.com/abema/go-mp4"
)

// appendBucketMagic 标记盒开头 8 字节（私有命名空间，小端序载荷）。
const appendBucketMagic = "mibeeab1"

// AppendBucket 错误分类：容量耗尽（调用方压紧重写）与非追加桶（回退经
// 典路径）需要不同处置，不混在通用 error 里。
var (
	ErrAppendCapacity      = fmt.Errorf("append bucket: capacity exhausted")
	ErrNotAppendBucket     = fmt.Errorf("append bucket: marker box absent")
	ErrAppendBucketCorrupt = fmt.Errorf("append bucket: corrupt tables")
)

// appendBucketLayout 记录追加桶的全部原地补丁位点，序列化进标记盒，
// 重启后直接恢复，无需重推导。
type appendBucketLayout struct {
	videoSampleCap uint32
	chunkCap       uint32

	mdatSizeOff   int64 // mdat 盒 size 字段（提交点）
	mdatDataStart int64 // mdat 头后首字节

	moovDurOff int64 // mvhd duration（视频 timescale）
	tkhdDurOff int64
	mdhdDurOff int64

	sttsCountOff int64
	sttsSlot0    int64
	stszCountOff int64
	stszSlot0    int64
	stssCountOff int64
	stssSlot0    int64
	stcoCountOff int64
	stcoSlot0    int64
	stscCountOff int64
	stscSlot0    int64
}

// keyframeCapFor：stss 槽位容量（样本容量 1/4 + 余量；全 IDR 流超限即
// 压紧）。创建与恢复两侧共享，保持确定性。
func keyframeCapFor(sampleCap uint32) uint32 {
	return sampleCap/4 + 64
}

// appendTrackState 是视频轨的 RAM 镜像（恢复时从表重建）。
type appendTrackState struct {
	sampleCount uint32
	chunkCount  uint32
	keyframes   uint32
	sttsRuns    uint32
	duration    uint64 // Σ 样本时长（timescale 单位）

	// stts 末 run（可原地扩展 count）。
	lastSttsDelta  uint32
	lastSttsCount  uint32
	lastSttsCntOff int64 // 末 run 的 SampleCount 字段位点

	lastChunkOff  int64
	lastChunkSize int64
}

// AppendBucket 是一个打开中的追加桶。非并发安全——调用方（rolling 的
// per-camera merge lock）已串行化同桶访问。
type AppendBucket struct {
	f         *os.File
	path      string
	timescale uint32
	layout    appendBucketLayout
	mdatLen   int64 // 当前 mdat 载荷字节数
	video     appendTrackState
}

// AppendBucketConfig 控制创建时的容量预估。
type AppendBucketConfig struct {
	// Window 为桶窗口时长（容量预估基准；0 → 默认 1h）。
	Window time.Duration
	// MaxSamples 为单轨样本容量上限（0 → 262144，约 5MB/轨磁盘预留上限）。
	MaxSamples uint32
}

// offsetWriter 包装真 WriteSeeker（bytesWriter）并镜像其位置——moov
// 创建期记录补丁位点。Seek 委托底层：mp4.Writer 的盒簿记（StartBox
// 偏移 / EndBox 尺寸回填）依赖真实的回跳落笔。坐标空间为 moov 相对，
// 位点记录时 +moovStart 折算文件绝对偏移。
type offsetWriter struct {
	w   io.WriteSeeker
	pos int64
}

func (o *offsetWriter) Write(p []byte) (int, error) {
	n, err := o.w.Write(p)
	o.pos += int64(n)
	return n, err
}

func (o *offsetWriter) Seek(offset int64, whence int) (int64, error) {
	res, err := o.w.Seek(offset, whence)
	o.pos = res
	return res, err
}

// --- 标记盒（free）编解码 ---

func appendBucketMarkerSize() int64 {
	// magic(8) + ver(4) + videoSampleCap(4) + chunkCap(4) + 15×u64 补丁位
	return 8 + 4 + 4 + 4 + 15*8
}

func appendBucketMarkerBytes(l *appendBucketLayout) []byte {
	payload := make([]byte, appendBucketMarkerSize())
	copy(payload, appendBucketMagic)
	le := binary.LittleEndian
	off := 8
	le.PutUint32(payload[off:], 1)
	off += 4
	le.PutUint32(payload[off:], l.videoSampleCap)
	off += 4
	le.PutUint32(payload[off:], l.chunkCap)
	off += 4
	for _, v := range []int64{
		l.mdatSizeOff, l.mdatDataStart,
		l.moovDurOff, l.tkhdDurOff, l.mdhdDurOff,
		l.sttsCountOff, l.sttsSlot0,
		l.stszCountOff, l.stszSlot0,
		l.stssCountOff, l.stssSlot0,
		l.stcoCountOff, l.stcoSlot0,
		l.stscCountOff, l.stscSlot0,
	} {
		le.PutUint64(payload[off:], uint64(v))
		off += 8
	}
	var box []byte
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(payload)+8))
	copy(hdr[4:], "free")
	box = append(box, hdr...)
	box = append(box, payload...)
	return box
}

// readAppendBucketMarker 沿顶层盒找到标记 free；ok=false = 非追加桶。
func readAppendBucketMarker(f *os.File) (layout appendBucketLayout, ok bool, err error) {
	var hdr [8]byte
	pos := int64(0)
	for {
		if _, err := f.ReadAt(hdr[:], pos); err != nil {
			if err == io.EOF {
				// 走完所有顶层盒都没有标记 free —— 普通文件，非追加桶。
				return layout, false, nil
			}
			return layout, false, fmt.Errorf("walk top-level boxes: %w", err)
		}
		size := int64(binary.BigEndian.Uint32(hdr[:4]))
		if size < 8 {
			return layout, false, fmt.Errorf("bad box size %d at %d", size, pos)
		}
		if string(hdr[4:8]) == "free" {
			if size < 8+appendBucketMarkerSize() {
				// 经典合并输出的 moov 填充 free 盒——装不下标记载荷，
				// 即非追加桶。
				return layout, false, nil
			}
			break
		}
		pos += size
	}
	payload := make([]byte, appendBucketMarkerSize())
	if _, err := f.ReadAt(payload, pos+8); err != nil {
		return layout, false, fmt.Errorf("read marker: %w", err)
	}
	if string(payload[:8]) != appendBucketMagic {
		return layout, false, nil
	}
	le := binary.LittleEndian
	off := 12
	layout.videoSampleCap = le.Uint32(payload[off:])
	off += 4
	layout.chunkCap = le.Uint32(payload[off:])
	off += 4
	vals := make([]int64, 15)
	for i := range vals {
		vals[i] = int64(le.Uint64(payload[off:]))
		off += 8
	}
	layout.mdatSizeOff, layout.mdatDataStart = vals[0], vals[1]
	layout.moovDurOff, layout.tkhdDurOff, layout.mdhdDurOff = vals[2], vals[3], vals[4]
	layout.sttsCountOff, layout.sttsSlot0 = vals[5], vals[6]
	layout.stszCountOff, layout.stszSlot0 = vals[7], vals[8]
	layout.stssCountOff, layout.stssSlot0 = vals[9], vals[10]
	layout.stcoCountOff, layout.stcoSlot0 = vals[11], vals[12]
	layout.stscCountOff, layout.stscSlot0 = vals[13], vals[14]
	return layout, true, nil
}

// --- 创建 ---

// estimateSampleCap 按首段样本率 × 窗口 × 1.3 安全系数预估容量。
func estimateSampleCap(first *SegmentInfo, window time.Duration, maxSamples uint32) uint32 {
	if window <= 0 {
		window = time.Hour
	}
	perSec := float64(first.SampleCount) / first.TotalDuration.Seconds()
	if perSec <= 0 || math.IsInf(perSec, 0) || math.IsNaN(perSec) {
		perSec = 25
	}
	cap32 := uint32(perSec * window.Seconds() * 1.3)
	if cap32 < 1024 {
		cap32 = 1024
	}
	if cap32 > maxSamples {
		cap32 = maxSamples
	}
	return cap32
}

// CreateAppendBucket 以 info 的编码参数创建一个空的追加桶文件。
func CreateAppendBucket(path string, info *SegmentInfo, cfg AppendBucketConfig) (*AppendBucket, error) {
	maxSamples := cfg.MaxSamples
	if maxSamples == 0 {
		maxSamples = 262144
	}
	sampleCap := estimateSampleCap(info, cfg.Window, maxSamples)
	chunkCap := uint32(cfg.Window / (15 * time.Second))
	if chunkCap < 64 {
		chunkCap = 64
	}
	if chunkCap > 4096 {
		chunkCap = 4096
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create append bucket: %w", err)
	}
	b := &AppendBucket{
		f:         f,
		path:      path,
		timescale: info.Timescale,
		layout: appendBucketLayout{
			videoSampleCap: sampleCap,
			chunkCap:       chunkCap,
		},
	}

	ftypSize, err := writeMergeFtyp(f, info.Codec)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("append bucket ftyp: %w", err)
	}

	// moov 先写进内存缓冲（记录补丁位点），再整体落盘——moov 为 MB 级，
	// 与既有单遍合并的 moov 测算同档，只在桶创建时发生一次。
	// 标记盒 = 8 字节盒头 + appendBucketMarkerSize() 载荷。
	moovStart := ftypSize + 8 + appendBucketMarkerSize()
	// rec.pos 从 moovStart 起步：mp4.Writer 的盒簿记（StartBox 偏移/
	// EndBox 尺寸回填）用它做绝对定位，不能从 0 起。
	buf := &bytesWriter{}
	rec := &offsetWriter{w: buf}
	if err := writeReservedMoov(rec, info, moovStart, &b.layout); err != nil {
		f.Close()
		return nil, err
	}
	moovBytes := buf.data

	if _, err := f.Write(appendBucketMarkerBytes(&appendBucketLayout{
		videoSampleCap: sampleCap, chunkCap: chunkCap,
	})); err != nil {
		f.Close()
		return nil, fmt.Errorf("append bucket marker: %w", err)
	}
	if _, err := f.Write(moovBytes); err != nil {
		f.Close()
		return nil, fmt.Errorf("append bucket moov: %w", err)
	}

	b.layout.mdatDataStart = moovStart + int64(len(moovBytes)) + 8
	b.layout.mdatSizeOff = b.layout.mdatDataStart - 8
	var mdatHdr [8]byte
	binary.BigEndian.PutUint32(mdatHdr[:4], 8)
	copy(mdatHdr[4:], "mdat")
	if _, err := f.Write(mdatHdr[:]); err != nil {
		f.Close()
		return nil, fmt.Errorf("append bucket mdat header: %w", err)
	}
	// 标记盒含 mdat 位点，创建时两段式：先写占位（caps 已真），mdat
	// 确定后整盒重写（定长）。
	if _, err := f.WriteAt(appendBucketMarkerBytes(&b.layout), ftypSize); err != nil {
		f.Close()
		return nil, fmt.Errorf("append bucket marker rewrite: %w", err)
	}
	return b, nil
}

// writeReservedMoov 写出预留容量的 moov（位点记录进 layout；各盒绝对
// 偏移 = rec.pos + base）。
func writeReservedMoov(rec *offsetWriter, info *SegmentInfo, base int64, l *appendBucketLayout) error {
	w := mp4.NewWriter(rec)
	tr := &mergeTrack{
		isH265:    info.Codec == "h265",
		sps:       info.SPS,
		pps:       info.PPS,
		vps:       info.VPS,
		timescale: info.Timescale,
		// tkhd 宽高未知写 0，播放器从码流推导（同经典路径）。
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("moov")}); err != nil {
		return err
	}
	mvhdStart := base + rec.pos
	if err := writeMergeMvhd(w, tr, false); err != nil {
		return err
	}
	// mvhd v0: hdr8 + vf4 + creation4 + modification4 + timescale4 → duration
	l.moovDurOff = mvhdStart + 24

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("trak")}); err != nil {
		return err
	}
	tkhdStart := base + rec.pos
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("tkhd")})
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Tkhd{
		TrackID: 1,
		Matrix: [9]int32{
			0x00010000, 0, 0,
			0, 0x00010000, 0,
			0, 0, 0x40000000,
		},
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi
	// tkhd v0: hdr8 + ver/flags4 + creation4 + modification4 + trackID4 + reserved4 → duration
	l.tkhdDurOff = tkhdStart + 28

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mdia")}); err != nil {
		return err
	}
	mdhdStart := base + rec.pos
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mdhd")})
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Mdhd{
		Timescale: info.Timescale,
		Language:  [3]byte{0x15, 0xC0, 0x00}, // 'und' packed
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi2
	l.mdhdDurOff = mdhdStart + 24 // 同 mvhd 布局（duration 在 timescale 之后）

	bi3, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hdlr")})
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Hdlr{
		HandlerType: [4]byte{'v', 'i', 'd', 'e'},
		Name:        "VideoHandler\x00",
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi3

	if err := writeReservedMinf(rec, w, tr, base, l); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // mdia
		return err
	}
	if _, err := w.EndBox(); err != nil { // trak
		return err
	}
	if _, err := w.EndBox(); err != nil { // moov
		return err
	}
	return nil
}

// writeReservedMinf：静态盒复用 mp4 盒编组；stbl 五表全部预留槽位。
func writeReservedMinf(rec *offsetWriter, w *mp4.Writer, tr *mergeTrack, base int64, l *appendBucketLayout) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("minf")}); err != nil {
		return err
	}
	// vmhd
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("vmhd")})
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Vmhd{Graphicsmode: 0}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi
	// dinf > dref > url
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("dinf")}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("dref")}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Dref{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("url ")}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Url{Location: ""}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // url
		return err
	}
	if _, err := w.EndBox(); err != nil { // dref
		return err
	}
	if _, err := w.EndBox(); err != nil { // dinf
		return err
	}
	// stbl
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stbl")}); err != nil {
		return err
	}
	// stsd 子树在隔离缓冲里用经典写器完整渲染后作为字节块拼入——
	// mp4.Writer 的盒簿记只覆盖容器（moov/trak/mdia/minf/stbl），与裸写
	// 的表盒不共享位置状态，杜绝交错失同步。
	stsdBuf := &bytesWriter{}
	stsdW := mp4.NewWriter(stsdBuf)
	if _, err := stsdW.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsd")}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(stsdW, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	if tr.isH265 {
		if err := writeMergeH265SampleEntry(stsdW, tr); err != nil {
			return err
		}
	} else {
		if err := writeMergeH264SampleEntry(stsdW, tr); err != nil {
			return err
		}
	}
	if _, err := stsdW.EndBox(); err != nil {
		return err
	}
	if _, err := rec.Write(stsdBuf.data); err != nil {
		return err
	}

	writeU32 := func(v uint32) error {
		var b4 [4]byte
		binary.BigEndian.PutUint32(b4[:], v)
		_, err := rec.Write(b4[:])
		return err
	}
	// reservedTable 写一个 [size][name][ver/flags][extra?][entry_count]
	// [slots…] 盒，返回 count 字段与首槽的绝对偏移。stsz 比siblings多
	// 一个 sample_size 字段（extra=1）。
	reservedTable := func(name string, slots uint32, slotBytes int32, extraFields int) (countOff, slot0 int64, err error) {
		total := uint64(16 + uint64(extraFields)*4 + uint64(slots)*uint64(slotBytes))
		if total > math.MaxUint32 {
			return 0, 0, fmt.Errorf("reserved %s too large", name)
		}
		var hdr [8]byte
		binary.BigEndian.PutUint32(hdr[:4], uint32(total))
		copy(hdr[4:], name)
		if _, err := rec.Write(hdr[:]); err != nil {
			return 0, 0, err
		}
		boxStart := base + rec.pos - 8
		if err := writeU32(0); err != nil { // version/flags
			return 0, 0, err
		}
		for range extraFields {
			if err := writeU32(0); err != nil { // stsz: sample_size=0（变长）
				return 0, 0, err
			}
		}
		if err := writeU32(0); err != nil { // entry_count
			return 0, 0, err
		}
		slot0 = boxStart + int64(16+extraFields*4)
		zeros := make([]byte, 256*int(slotBytes))
		remain := int64(slots) * int64(slotBytes)
		for remain > 0 {
			n := int64(len(zeros))
			if n > remain {
				n = remain
			}
			if _, err := rec.Write(zeros[:n]); err != nil {
				return 0, 0, err
			}
			remain -= n
		}
		return slot0 - 4, slot0, nil
	}

	if l.sttsCountOff, l.sttsSlot0, err = reservedTable("stts", l.videoSampleCap, 8, 0); err != nil {
		return err
	}
	if l.stscCountOff, l.stscSlot0, err = reservedTable("stsc", l.chunkCap, 12, 0); err != nil {
		return err
	}
	if l.stszCountOff, l.stszSlot0, err = reservedTable("stsz", l.videoSampleCap, 4, 1); err != nil {
		return err
	}
	if l.stssCountOff, l.stssSlot0, err = reservedTable("stss", keyframeCapFor(l.videoSampleCap), 4, 0); err != nil {
		return err
	}
	if l.stcoCountOff, l.stcoSlot0, err = reservedTable("stco", l.chunkCap, 4, 0); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // stbl
		return err
	}
	if _, err := w.EndBox(); err != nil { // minf
		return err
	}
	return nil
}

// --- 打开（恢复） ---

// OpenAppendBucket 打开既有追加桶并重建镜像；文件尾长于 mdat 声明时
// 截断之——崩溃恢复语义：丢未提交尾部，源段仍在由上层回折。
func OpenAppendBucket(path string) (*AppendBucket, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open append bucket: %w", err)
	}
	layout, ok, err := readAppendBucketMarker(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	if !ok {
		f.Close()
		return nil, ErrNotAppendBucket
	}
	b := &AppendBucket{f: f, path: path, layout: layout}

	var sizeB [4]byte
	if _, err := f.ReadAt(sizeB[:], layout.mdatSizeOff); err != nil {
		f.Close()
		return nil, fmt.Errorf("read mdat size: %w", err)
	}
	b.mdatLen = int64(binary.BigEndian.Uint32(sizeB[:])) - 8

	b.video.sampleCount = readU32(f, layout.stszCountOff)
	b.video.chunkCount = readU32(f, layout.stcoCountOff)
	b.video.sttsRuns = readU32(f, layout.sttsCountOff)
	b.video.keyframes = readU32(f, layout.stssCountOff)
	if b.video.sampleCount > layout.videoSampleCap || b.video.chunkCount > layout.chunkCap ||
		b.video.sttsRuns > layout.videoSampleCap || b.video.keyframes > keyframeCapFor(layout.videoSampleCap) {
		f.Close()
		return nil, ErrAppendBucketCorrupt
	}

	// timescale 紧邻 duration 字段之前。
	b.timescale = readU32(f, layout.mdhdDurOff-4)

	if b.video.chunkCount > 0 {
		lastChunk := b.video.chunkCount - 1
		b.video.lastChunkOff = int64(readU32(f, layout.stcoSlot0+int64(lastChunk)*4))
		entryOff := layout.stscSlot0 + int64(lastChunk)*12
		nSamples := readU32(f, entryOff+4)
		chunkSampleBase := b.video.sampleCount - nSamples
		var chunkBytes int64
		for i := range nSamples {
			chunkBytes += int64(readU32(f, layout.stszSlot0+int64(chunkSampleBase+i)*4))
		}
		b.video.lastChunkSize = chunkBytes
	}
	for i := range b.video.sttsRuns {
		c := readU32(f, layout.sttsSlot0+int64(i)*8)
		d := readU32(f, layout.sttsSlot0+int64(i)*8+4)
		b.video.duration += uint64(c) * uint64(d)
		if i == b.video.sttsRuns-1 {
			b.video.lastSttsDelta = d
			b.video.lastSttsCount = c
			b.video.lastSttsCntOff = layout.sttsSlot0 + int64(i)*8
		}
	}

	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	mdatEnd := layout.mdatDataStart + b.mdatLen
	if fi.Size() > mdatEnd {
		rollingLogger.Info("append bucket: truncating uncommitted tail",
			"path", path, "bytes", fi.Size()-mdatEnd)
		if err := f.Truncate(mdatEnd); err != nil {
			f.Close()
			return nil, fmt.Errorf("truncate uncommitted tail: %w", err)
		}
	}
	return b, nil
}

func readU32(f *os.File, off int64) uint32 {
	var b [4]byte
	if _, err := f.ReadAt(b[:], off); err != nil {
		// 布局位点由标记盒给出，读失败即文件被截断——按损坏处理由上层
		// 回退经典路径；此处 panic 保持与 mustRead 语义一致（调用点无
		// error 通道）。
		panic(fmt.Sprintf("append bucket: read u32 at %d: %v", off, err))
	}
	return binary.BigEndian.Uint32(b[:])
}

// --- 追加 ---

// AppendSource 是一次追加的一个来源段（Sample.Offset 相对来源文件）。
type AppendSource struct {
	Path    string
	Samples []SampleEntry
}

// AppendStats 报告本次追加的两轴贡献（上层墙钟记账复用经典口径）。
type AppendStats struct {
	WallTicks uint64 // 原始样本时长和（timescale 单位）
	FileTicks uint64 // 压缩后样本时长和
	Bytes     int64
}

// atWriter 把顺序 Write 透传为文件 WriteAt（base 随写推进）——复用
// copySampleData 的流式拷贝。
type atWriter struct {
	f    *os.File
	base int64
}

func (a *atWriter) Write(p []byte) (int, error) {
	n, err := a.f.WriteAt(p, a.base)
	a.base += int64(n)
	return n, err
}

// AppendBatch 把一批来源段的样本顺序追加为一个新 chunk，并原地补丁
// 全部表条目。帧距压缩与 MergeMP4Segments 同规则（>gap 驻留 → cadence）。
// 提交点 = 末尾的 mdat size 补丁。
func (b *AppendBucket) AppendBatch(cadence, gap time.Duration, srcs []AppendSource) (AppendStats, error) {
	var st AppendStats
	l := &b.layout
	if len(srcs) == 0 {
		return st, nil
	}

	// 1) 计划：压缩时长、容量检查（先算后写，任何不足都走 ErrAppendCapacity）。
	type planned struct {
		src     *os.File
		samples []SampleEntry // Duration 已压缩
	}
	plans := make([]planned, 0, len(srcs))
	gapTicks := float64(b.timescale) * gap.Seconds()
	frameTicks := uint32(float64(b.timescale) * cadence.Seconds())
	newSamples := uint32(0)
	newKeyframes := uint32(0)
	var newRuns uint32
	prevDelta := uint32(math.MaxUint32)
	havePrev := b.video.sttsRuns > 0
	if havePrev {
		prevDelta = b.video.lastSttsDelta
	}
	var openErr error
	defer func() {
		for _, p := range plans {
			p.src.Close()
		}
	}()
	for i := range srcs {
		if openErr != nil {
			break
		}
		var f *os.File
		f, openErr = os.Open(srcs[i].Path)
		if openErr != nil {
			break
		}
		samples := make([]SampleEntry, len(srcs[i].Samples))
		copy(samples, srcs[i].Samples)
		for j := range samples {
			d := samples[j].Duration
			if b.timescale > 0 && frameTicks > 0 && float64(d) > gapTicks {
				d = frameTicks
			}
			samples[j].Duration = d
			st.WallTicks += uint64(srcs[i].Samples[j].Duration)
			st.FileTicks += uint64(d)
			st.Bytes += int64(samples[j].Size)
			if d != prevDelta {
				newRuns++
				prevDelta = d
			}
			if samples[j].IsKeyFrame {
				newKeyframes++
			}
		}
		newSamples += uint32(len(samples))
		plans = append(plans, planned{src: f, samples: samples})
	}
	if openErr != nil {
		return st, fmt.Errorf("append bucket: open source: %w", openErr)
	}

	if b.video.sampleCount+newSamples > l.videoSampleCap ||
		b.video.chunkCount+1 > l.chunkCap ||
		b.video.keyframes+newKeyframes > keyframeCapFor(l.videoSampleCap) ||
		b.video.sttsRuns+newRuns > l.videoSampleCap {
		return st, ErrAppendCapacity
	}
	chunkOff := l.mdatDataStart + b.mdatLen
	if chunkOff+st.Bytes > math.MaxUint32 {
		return st, ErrAppendCapacity // stco 32 位（桶另有 3GiB 上限，防御）
	}

	// 2) 顺序追加样本字节 + 随写补丁表槽。
	buf := make([]byte, mergeBufferSize())
	sampleIdx := b.video.sampleCount
	cur := chunkOff
	for _, p := range plans {
		for j := range p.samples {
			s := p.samples[j]
			aw := &atWriter{f: b.f, base: cur}
			if _, err := copySampleData(context.Background(), p.src, aw, s.Offset, int64(s.Size), buf); err != nil {
				return st, fmt.Errorf("append bucket: copy sample: %w", err)
			}
			cur += int64(s.Size)

			var b4 [4]byte
			binary.BigEndian.PutUint32(b4[:], s.Size)
			if _, err := b.f.WriteAt(b4[:], l.stszSlot0+int64(sampleIdx)*4); err != nil {
				return st, fmt.Errorf("append bucket: stsz slot: %w", err)
			}
			if s.IsKeyFrame {
				// stss 条目值 = 1 起样本序号；槽位序 = 关键帧计数。
				binary.BigEndian.PutUint32(b4[:], sampleIdx+1)
				if _, err := b.f.WriteAt(b4[:], l.stssSlot0+int64(b.video.keyframes)*4); err != nil {
					return st, fmt.Errorf("append bucket: stss slot: %w", err)
				}
				b.video.keyframes++
			}
			if havePrev && s.Duration == prevDelta {
				binary.BigEndian.PutUint32(b4[:], b.video.lastSttsCount+1)
				if _, err := b.f.WriteAt(b4[:], b.video.lastSttsCntOff); err != nil {
					return st, fmt.Errorf("append bucket: stts run extend: %w", err)
				}
				b.video.lastSttsCount++
			} else {
				runIdx := b.video.sttsRuns
				binary.BigEndian.PutUint32(b4[:], 1)
				if _, err := b.f.WriteAt(b4[:], l.sttsSlot0+int64(runIdx)*8); err != nil {
					return st, fmt.Errorf("append bucket: stts slot: %w", err)
				}
				binary.BigEndian.PutUint32(b4[:], s.Duration)
				if _, err := b.f.WriteAt(b4[:], l.sttsSlot0+int64(runIdx)*8+4); err != nil {
					return st, fmt.Errorf("append bucket: stts slot: %w", err)
				}
				b.video.lastSttsCntOff = l.sttsSlot0 + int64(runIdx)*8
				b.video.lastSttsDelta = s.Duration
				b.video.lastSttsCount = 1
				b.video.sttsRuns++
				prevDelta = s.Duration
				havePrev = true
			}
			b.video.duration += uint64(s.Duration)
			sampleIdx++
		}
	}

	// 3) 新 chunk 条目（stco 槽 + stsc 槽）。
	var b4 [4]byte
	chunkIdx := b.video.chunkCount
	binary.BigEndian.PutUint32(b4[:], uint32(chunkOff))
	if _, err := b.f.WriteAt(b4[:], l.stcoSlot0+int64(chunkIdx)*4); err != nil {
		return st, fmt.Errorf("append bucket: stco slot: %w", err)
	}
	stscOff := l.stscSlot0 + int64(chunkIdx)*12
	binary.BigEndian.PutUint32(b4[:], chunkIdx+1) // first_chunk（1 起）
	if _, err := b.f.WriteAt(b4[:], stscOff); err != nil {
		return st, fmt.Errorf("append bucket: stsc slot: %w", err)
	}
	binary.BigEndian.PutUint32(b4[:], newSamples)
	if _, err := b.f.WriteAt(b4[:], stscOff+4); err != nil {
		return st, fmt.Errorf("append bucket: stsc slot: %w", err)
	}
	binary.BigEndian.PutUint32(b4[:], 1) // sample_description_index
	if _, err := b.f.WriteAt(b4[:], stscOff+8); err != nil {
		return st, fmt.Errorf("append bucket: stsc slot: %w", err)
	}

	// 4) 计数与时长补丁。
	patchU32 := func(off int64, v uint32) error {
		binary.BigEndian.PutUint32(b4[:], v)
		_, err := b.f.WriteAt(b4[:], off)
		return err
	}
	dur := b.video.duration
	if dur > math.MaxUint32 {
		dur = math.MaxUint32
	}
	for _, p := range []struct {
		off int64
		v   uint32
	}{
		{l.stszCountOff, sampleIdx},
		{l.stssCountOff, b.video.keyframes},
		{l.sttsCountOff, b.video.sttsRuns},
		{l.stcoCountOff, chunkIdx + 1},
		{l.stscCountOff, chunkIdx + 1},
		{l.moovDurOff, uint32(dur)},
		{l.tkhdDurOff, uint32(dur)},
		{l.mdhdDurOff, uint32(dur)},
	} {
		if err := patchU32(p.off, p.v); err != nil {
			return st, fmt.Errorf("append bucket: patch counts: %w", err)
		}
	}

	// 5) 提交点：mdat size（此前的一切中途崩溃都呈现为「尾部未提交」，
	// 由 OpenAppendBucket 截断恢复）。
	b.mdatLen += st.Bytes
	if err := patchU32(l.mdatSizeOff, uint32(b.mdatLen+8)); err != nil {
		return st, fmt.Errorf("append bucket: patch mdat size: %w", err)
	}

	b.video.sampleCount = sampleIdx
	b.video.chunkCount = chunkIdx + 1
	b.video.lastChunkOff = chunkOff
	b.video.lastChunkSize = st.Bytes
	return st, nil
}

// Close closes the underlying file.
func (b *AppendBucket) Close() error {
	if b == nil || b.f == nil {
		return nil
	}
	err := b.f.Close()
	b.f = nil
	return err
}

// SampleCount/ChunkCount/MDatLen/SampleCap 暴露镜像给集成层与测试。
func (b *AppendBucket) SampleCount() uint32 { return b.video.sampleCount }
func (b *AppendBucket) ChunkCount() uint32  { return b.video.chunkCount }
func (b *AppendBucket) MDatLen() int64      { return b.mdatLen }
func (b *AppendBucket) SampleCap() uint32   { return b.layout.videoSampleCap }
func (b *AppendBucket) Path() string        { return b.path }
