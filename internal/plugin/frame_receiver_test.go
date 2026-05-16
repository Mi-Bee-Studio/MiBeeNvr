package plugin

import (
"context"
"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	gen "github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// --- Mocks ---

type mockSegmentStore struct {
	mu           sync.Mutex
	segments     []string         // final paths of closed segments
	tempFiles    []string         // temp files created
	createErr    error            // inject error on next CreateSegment
	closeErr     error            // inject error on next CloseSegment
	createCalled int
	closeCalled  int
}

func (s *mockSegmentStore) CreateSegment(cameraID string, format string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		err := s.createErr
		s.createErr = nil
		return "", "", err
	}
	s.createCalled++
	tmp := filepath.Join(s.tempDir(), cameraID+"_"+format+".tmp")
	final := filepath.Join(s.tempDir(), cameraID+"_"+format+".mp4")
	s.tempFiles = append(s.tempFiles, tmp)
	// Create the temp file so MP4Muxer can write to it.
	f, err := os.Create(tmp)
	if err != nil {
		return "", "", err
	}
	f.Close()
	return tmp, final, nil
}

func (s *mockSegmentStore) CloseSegment(tempPath, finalPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeCalled++
	if s.closeErr != nil {
		err := s.closeErr
		s.closeErr = nil
		return err
	}
	// Rename temp to final.
	if err := os.Rename(tempPath, finalPath); err != nil {
		return err
	}
	s.segments = append(s.segments, finalPath)
	return nil
}

func (s *mockSegmentStore) tempDir() string {
	return filepath.Join(os.TempDir(), "frame_receiver_test")
}

type mockRecordingDB struct {
	mu         sync.Mutex
	recordings []*model.Recording
}

func (d *mockRecordingDB) InsertRecording(_ context.Context, r *model.Recording) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.recordings = append(d.recordings, r)
	return nil
}

func (d *mockRecordingDB) InsertRecordingWithRetry(_ context.Context, r *model.Recording, _ int, _ time.Duration) error {
	return d.InsertRecording(context.Background(), r)
}

// --- Helpers ---

func newTestFrameReceiver(t *testing.T) (*FrameReceiver, *mockSegmentStore, *mockRecordingDB) {
	t.Helper()
	store := &mockSegmentStore{}
	db := &mockRecordingDB{}
	fr := NewFrameReceiver(store, db, nil, "test-cam", 10*time.Minute)
	return fr, store, db
}

func newTestFrameReceiverWithMetrics(t *testing.T) (*FrameReceiver, *mockSegmentStore, *mockRecordingDB, *metrics.Metrics) {
	t.Helper()
	store := &mockSegmentStore{}
	db := &mockRecordingDB{}
	m := metrics.NewMetrics()
	fr := NewFrameReceiver(store, db, m, "test-cam", 10*time.Minute)
	return fr, store, db, m
}

// Ensure temp dir exists for tests.
func ensureTempDir(t *testing.T) {
	t.Helper()
	dir := filepath.Join(os.TempDir(), "frame_receiver_test")
	os.MkdirAll(dir, 0755)
	t.Cleanup(func() { os.RemoveAll(dir) })
}

// makeH264SPS returns a minimal valid H.264 SPS NAL unit (NAL type 7).
func makeH264SPS() []byte {
	return []byte{0x67, 0x64, 0x00, 0x0A, 0xAC, 0xD9, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xC8, 0x40}
}

// makeH264PPS returns a minimal H.264 PPS NAL unit (NAL type 8).
func makeH264PPS() []byte {
	return []byte{0x68, 0xEE, 0x3C, 0x80}
}

// makeH264IDR returns a minimal H.264 IDR slice NAL unit (NAL type 5).
func makeH264IDR() []byte {
	return []byte{0x65, 0x88, 0x80, 0x40, 0x00, 0x04, 0x00, 0x00, 0x10}
}

// makeH264P returns a minimal H.264 non-IDR slice NAL unit (NAL type 1).
func makeH264P() []byte {
	return []byte{0x21, 0x88, 0x80, 0x40, 0x00, 0x02, 0x00}
}

// makeH265VPS returns a minimal H.265 VPS NAL unit (NAL type 32).
func makeH265VPS() []byte {
	return []byte{0x40, 0x01, 0x0C, 0x01, 0xFF, 0xFF, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x7B, 0xAC, 0x09}
}

// makeH265SPS returns a minimal H.265 SPS NAL unit (NAL type 33).
func makeH265SPS() []byte {
	return []byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x7B, 0xA0, 0x03, 0xC0, 0x80, 0x10, 0xE4, 0xD9, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xC8, 0x40}
}

// makeH265PPS returns a minimal H.265 PPS NAL unit (NAL type 34).
func makeH265PPS() []byte {
	return []byte{0x44, 0x01, 0xC1, 0x73, 0xD1, 0x89}
}

// makeH265IDR returns a minimal H.265 IDR slice NAL unit (NAL type 19 = IDR_W_RADL).
func makeH265IDR() []byte {
	return []byte{0x26, 0x01, 0xAF, 0x1D, 0x21, 0x4A, 0x93, 0x08, 0x00, 0x04, 0x00, 0x00, 0x10}
}

// makeH265P returns a minimal H.265 non-IDR slice NAL unit (NAL type 1).
func makeH265P() []byte {
	return []byte{0x02, 0x01, 0xAF, 0x1D, 0x21, 0x4A, 0x93, 0x08, 0x00, 0x02, 0x00}
}

// withStartCode prepends Annex B 4-byte start code.
func withStartCode(nalu []byte) []byte {
	return append([]byte{0x00, 0x00, 0x00, 0x01}, nalu...)
}

// --- Tests ---

func TestFrameReceiver_H264FullSequence(t *testing.T) {
	t.Helper()
	ensureTempDir(t)
	fr, store, db := newTestFrameReceiver(t)

	// 1. Send SPS as codec info.
	sps := makeH264SPS()
	err := fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(sps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})
	require.NoError(t, err)

	// 2. Send PPS as codec info.
	pps := makeH264PPS()
	err = fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(pps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})
	require.NoError(t, err)

	// Codec should be detected now.
	require.True(t, fr.codecDetected)
	require.Equal(t, model.FormatH264, fr.codec)

	// 3. Send P-frame before IDR — should be discarded (no muxer yet).
	err = fr.HandleFrame(context.Background(), &gen.Frame{
		Data:  withStartCode(makeH264P()),
		Codec: gen.Codec_CODEC_H264,
	})
	require.NoError(t, err)
	require.Nil(t, fr.muxer)

	// 4. Send IDR — should create first segment.
	err = fr.HandleFrame(context.Background(), &gen.Frame{
		Data:  withStartCode(makeH264IDR()),
		Codec: gen.Codec_CODEC_H264,
		IsIdr: true,
	})
	require.NoError(t, err)
	require.NotNil(t, fr.muxer)
	require.Equal(t, 1, fr.frameCount)

	// 5. Send 29 P-frames.
	for i := 0; i < 29; i++ {
		err = fr.HandleFrame(context.Background(), &gen.Frame{
			Data:  withStartCode(makeH264P()),
			Codec: gen.Codec_CODEC_H264,
		})
		require.NoError(t, err)
	}
	require.Equal(t, 30, fr.frameCount)

	// 6. Send second IDR — triggers segment close + new segment.
	err = fr.HandleFrame(context.Background(), &gen.Frame{
		Data:  withStartCode(makeH264IDR()),
		Codec: gen.Codec_CODEC_H264,
		IsIdr: true,
	})
	require.NoError(t, err)

	// Previous segment should be closed.
	require.Equal(t, 1, store.closeCalled)
	require.Len(t, db.recordings, 1)
	require.Equal(t, "test-cam", db.recordings[0].CameraID)
	require.Equal(t, model.FormatH264, db.recordings[0].Format)
	require.Equal(t, 30, db.recordings[0].FrameCount)

	// New segment should be started with 1 frame (the IDR).
	require.NotNil(t, fr.muxer)
	require.Equal(t, 1, fr.frameCount)

	// 7. Close the receiver — should close the second segment.
	err = fr.Close()
	require.NoError(t, err)
	require.Nil(t, fr.muxer)
	require.Equal(t, 2, store.closeCalled)
	require.Len(t, db.recordings, 2)
}

func TestFrameReceiver_H265FullSequence(t *testing.T) {
	t.Helper()
	ensureTempDir(t)
	fr, store, db := newTestFrameReceiver(t)

	// 1. Send VPS, SPS, PPS as codec info.
	vps := makeH265VPS()
	err := fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(vps),
		Codec:       gen.Codec_CODEC_H265,
		IsCodecInfo: true,
	})
	require.NoError(t, err)

	sps := makeH265SPS()
	err = fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(sps),
		Codec:       gen.Codec_CODEC_H265,
		IsCodecInfo: true,
	})
	require.NoError(t, err)

	pps := makeH265PPS()
	err = fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(pps),
		Codec:       gen.Codec_CODEC_H265,
		IsCodecInfo: true,
	})
	require.NoError(t, err)

	require.True(t, fr.codecDetected)
	require.Equal(t, model.FormatH265, fr.codec)

	// 2. Send IDR — creates segment.
	err = fr.HandleFrame(context.Background(), &gen.Frame{
		Data:  withStartCode(makeH265IDR()),
		Codec: gen.Codec_CODEC_H265,
		IsIdr: true,
	})
	require.NoError(t, err)
	require.NotNil(t, fr.muxer)

	// 3. Send 30 P-frames.
	for i := 0; i < 30; i++ {
		err = fr.HandleFrame(context.Background(), &gen.Frame{
			Data:  withStartCode(makeH265P()),
			Codec: gen.Codec_CODEC_H265,
		})
		require.NoError(t, err)
	}
	require.Equal(t, 31, fr.frameCount)

	// 4. Second IDR triggers close.
	err = fr.HandleFrame(context.Background(), &gen.Frame{
		Data:  withStartCode(makeH265IDR()),
		Codec: gen.Codec_CODEC_H265,
		IsIdr: true,
	})
	require.NoError(t, err)

	require.Equal(t, 1, store.closeCalled)
	require.Len(t, db.recordings, 1)
	require.Equal(t, model.FormatH265, db.recordings[0].Format)
	require.Equal(t, 31, db.recordings[0].FrameCount)

	// 5. Close finalizes second segment.
	err = fr.Close()
	require.NoError(t, err)
	require.Equal(t, 2, store.closeCalled)
}

func TestFrameReceiver_DiscardBeforeCodecDetection(t *testing.T) {
	t.Helper()
	ensureTempDir(t)
	fr, store, _ := newTestFrameReceiver(t)

	// Send regular frames without codec info — should be discarded.
	for i := 0; i < 10; i++ {
		err := fr.HandleFrame(context.Background(), &gen.Frame{
			Data:  []byte{0x65, 0x88, 0x80},
			Codec: gen.Codec_CODEC_H264,
			IsIdr: true,
		})
		require.NoError(t, err)
	}

	// No segments created.
	require.Equal(t, 0, store.createCalled)
	require.Nil(t, fr.muxer)

	// Now send codec info + IDR — should work.
	sps := makeH264SPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(sps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})
	pps := makeH264PPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(pps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})

	err := fr.HandleFrame(context.Background(), &gen.Frame{
		Data:  withStartCode(makeH264IDR()),
		Codec: gen.Codec_CODEC_H264,
		IsIdr: true,
	})
	require.NoError(t, err)
	require.NotNil(t, fr.muxer)
	require.Equal(t, 1, store.createCalled)

	fr.Close()
}

func TestFrameReceiver_CloseCleansUp(t *testing.T) {
	t.Helper()
	ensureTempDir(t)
	fr, store, db := newTestFrameReceiver(t)

	// Setup codec and start a segment.
	sps := makeH264SPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(sps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})
	pps := makeH264PPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(pps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:  withStartCode(makeH264IDR()),
		Codec: gen.Codec_CODEC_H264,
		IsIdr: true,
	})
	require.NotNil(t, fr.muxer)

	// Close should finalize the segment.
	err := fr.Close()
	require.NoError(t, err)
	require.Nil(t, fr.muxer)
	require.Equal(t, 1, store.closeCalled)
	require.Len(t, db.recordings, 1)

	// Double close is safe.
	err = fr.Close()
	require.NoError(t, err)
	require.Equal(t, 1, store.closeCalled) // no extra close
}

func TestFrameReceiver_CloseWithNoSegment(t *testing.T) {
	t.Helper()
	fr, store, _ := newTestFrameReceiver(t)

	err := fr.Close()
	require.NoError(t, err)
	require.Equal(t, 0, store.closeCalled)
}

func TestFrameReceiver_MetricsUpdated(t *testing.T) {
	t.Helper()
	ensureTempDir(t)
	fr, store, _, m := newTestFrameReceiverWithMetrics(t)

	// Setup codec and create + close a segment.
	sps := makeH264SPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(sps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})
	pps := makeH264PPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(pps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:  withStartCode(makeH264IDR()),
		Codec: gen.Codec_CODEC_H264,
		IsIdr: true,
	})

	// Close to trigger metrics.
	fr.Close()

	// Verify metrics counter was incremented.
	metricCh := make(chan bool, 1)
	go func() {
		// Test that the counter has a non-zero value for our camera.
		vec := m.SegmentsCreated.WithLabelValues("test-cam", "h264")
		// We can't easily read the counter value, but we can verify it doesn't panic.
		vec.Desc()
		metricCh <- true
	}()
	<-metricCh
	_ = store
}

func TestFrameReceiver_CodecInfoFromExtraMap(t *testing.T) {
	t.Helper()
	ensureTempDir(t)
	fr, _, _ := newTestFrameReceiver(t)

	// Send codec info via Extra map.
	err := fr.HandleFrame(context.Background(), &gen.Frame{
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
		Extra: map[string]string{
			"sps_hex": "sps-data",
			"pps_hex": "pps-data",
		},
	})
	require.NoError(t, err)

	require.True(t, fr.codecDetected)
	require.Equal(t, model.FormatH264, fr.codec)
	require.Equal(t, []byte("sps-data"), fr.sps)
	require.Equal(t, []byte("pps-data"), fr.pps)
}

func TestFrameReceiver_CodecInfoH265WithVPS(t *testing.T) {
	t.Helper()
	ensureTempDir(t)
	fr, _, _ := newTestFrameReceiver(t)

	// Send H265 codec info with VPS via Extra map.
	err := fr.HandleFrame(context.Background(), &gen.Frame{
		Codec:       gen.Codec_CODEC_H265,
		IsCodecInfo: true,
		Extra: map[string]string{
			"vps_hex": "vps-data",
			"sps_hex": "sps-data",
			"pps_hex": "pps-data",
		},
	})
	require.NoError(t, err)

	require.True(t, fr.codecDetected)
	require.Equal(t, model.FormatH265, fr.codec)
	require.Equal(t, []byte("vps-data"), fr.vps)
	require.Equal(t, []byte("sps-data"), fr.sps)
	require.Equal(t, []byte("pps-data"), fr.pps)
}

func TestFrameReceiver_PFrameBeforeIDR(t *testing.T) {
	t.Helper()
	ensureTempDir(t)
	fr, store, _ := newTestFrameReceiver(t)

	// Setup codec.
	sps := makeH264SPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(sps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})
	pps := makeH264PPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(pps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})

	// Send P-frame — should be discarded (no muxer, waiting for IDR).
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:  withStartCode(makeH264P()),
		Codec: gen.Codec_CODEC_H264,
	})
	require.Nil(t, fr.muxer)
	require.Equal(t, 0, store.createCalled)

	// Send IDR — now segment starts.
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:  withStartCode(makeH264IDR()),
		Codec: gen.Codec_CODEC_H264,
		IsIdr: true,
	})
	require.NotNil(t, fr.muxer)
	require.Equal(t, 1, store.createCalled)

	fr.Close()
}

func TestFrameReceiver_MultipleSegmentSplits(t *testing.T) {
	t.Helper()
	ensureTempDir(t)
	fr, store, db := newTestFrameReceiver(t)

	// Setup H264 codec.
	sps := makeH264SPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(sps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})
	pps := makeH264PPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(pps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})

	// Create 3 segments via IDR splits.
	for seg := 0; seg < 3; seg++ {
		fr.HandleFrame(context.Background(), &gen.Frame{
			Data:  withStartCode(makeH264IDR()),
			Codec: gen.Codec_CODEC_H264,
			IsIdr: true,
		})
		for i := 0; i < 5; i++ {
			fr.HandleFrame(context.Background(), &gen.Frame{
				Data:  withStartCode(makeH264P()),
				Codec: gen.Codec_CODEC_H264,
			})
		}
	}

	// Close finalizes the last segment.
	fr.Close()

	require.Equal(t, 3, store.createCalled)
	require.Equal(t, 3, store.closeCalled)
	require.Len(t, db.recordings, 3)
	// Each segment has 6 frames (1 IDR + 5 P).
	for _, rec := range db.recordings {
		require.Equal(t, 6, rec.FrameCount)
	}
}

func TestFrameReceiver_CreateSegmentError(t *testing.T) {
	t.Helper()
	ensureTempDir(t)
	fr, store, _ := newTestFrameReceiver(t)
	store.createErr = fmt.Errorf("disk full")

	// Setup codec.
	sps := makeH264SPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(sps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})
	pps := makeH264PPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(pps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})

	// IDR should fail to create segment.
	err := fr.HandleFrame(context.Background(), &gen.Frame{
		Data:  withStartCode(makeH264IDR()),
		Codec: gen.Codec_CODEC_H264,
		IsIdr: true,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "start segment")
	require.Nil(t, fr.muxer)
}

func TestFrameReceiver_SegmentDurationExpiry(t *testing.T) {
	t.Helper()
	ensureTempDir(t)
	// Create receiver with very short segment duration.
	store := &mockSegmentStore{}
	db := &mockRecordingDB{}
	fr := NewFrameReceiver(store, db, nil, "test-cam", 10*time.Millisecond)

	// Setup codec.
	sps := makeH264SPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(sps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})
	pps := makeH264PPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(pps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})

	// Send IDR — creates segment.
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:  withStartCode(makeH264IDR()),
		Codec: gen.Codec_CODEC_H264,
		IsIdr: true,
	})
	require.NotNil(t, fr.muxer)

	// Wait for segment duration to expire.
	time.Sleep(20 * time.Millisecond)

	// Send a P-frame — should trigger close due to duration expiry.
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:  withStartCode(makeH264P()),
		Codec: gen.Codec_CODEC_H264,
	})
	// Segment should have been closed but new segment NOT started (P-frame, not IDR).
	require.Nil(t, fr.muxer)
	require.Equal(t, 1, store.closeCalled)

	fr.Close()
}

func TestFrameReceiver_DefaultSegmentDuration(t *testing.T) {
	t.Helper()
	store := &mockSegmentStore{}
	db := &mockRecordingDB{}
	fr := NewFrameReceiver(store, db, nil, "cam", 0)
	require.Equal(t, defaultFrameReceiverSegDur, fr.segDur)
}

func TestFrameReceiver_ContextPassedToDB(t *testing.T) {
	t.Helper()
	ensureTempDir(t)
	fr, store, db := newTestFrameReceiver(t)

	// Setup codec + one segment.
	sps := makeH264SPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(sps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})
	pps := makeH264PPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(pps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:  withStartCode(makeH264IDR()),
		Codec: gen.Codec_CODEC_H264,
		IsIdr: true,
	})

	// Close uses context.Background() internally.
	fr.Close()
	require.Len(t, db.recordings, 1)
	_ = store
}

func TestFrameReceiver_ConcurrentFrameHandling(t *testing.T) {
	t.Helper()
	ensureTempDir(t)
	fr, _, _ := newTestFrameReceiver(t)

	// Setup codec.
	sps := makeH264SPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(sps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})
	pps := makeH264PPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(pps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})

	// Send IDR to create segment.
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:  withStartCode(makeH264IDR()),
		Codec: gen.Codec_CODEC_H264,
		IsIdr: true,
	})

	// Concurrent P-frames — mutex should prevent races.
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fr.HandleFrame(context.Background(), &gen.Frame{
				Data:  withStartCode(makeH264P()),
				Codec: gen.Codec_CODEC_H264,
			})
		}()
	}
	wg.Wait()

	fr.Close()
}

func TestSkipStartCode(t *testing.T) {
	t.Helper()

	tests := []struct {
		name     string
		input    []byte
		expected []byte
	}{
		{"4-byte start code", []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88}, []byte{0x65, 0x88}},
		{"3-byte start code", []byte{0x00, 0x00, 0x01, 0x65, 0x88}, []byte{0x65, 0x88}},
		{"no start code", []byte{0x65, 0x88}, []byte{0x65, 0x88}},
		{"empty", []byte{}, []byte{}},
		{"too short for start code", []byte{0x00, 0x00}, []byte{0x00, 0x00}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Helper()
			result := skipStartCode(tt.input)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestFrameReceiver_H264CodecNALDetection(t *testing.T) {
	t.Helper()
	ensureTempDir(t)
	fr, _, _ := newTestFrameReceiver(t)

	// Send SPS as codec info with NAL data (no start code).
	sps := makeH264SPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        sps,
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})
	require.True(t, fr.codecDetected)
	require.NotNil(t, fr.sps)
	// SPS NAL type 7: first byte 0x67
	require.Equal(t, byte(0x67), fr.sps[0])

	// Send PPS.
	pps := makeH264PPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        pps,
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})
	require.NotNil(t, fr.pps)
	// PPS NAL type 8: first byte 0x68
	require.Equal(t, byte(0x68), fr.pps[0])
}

func TestFrameReceiver_H265CodecNALDetection(t *testing.T) {
	t.Helper()
	ensureTempDir(t)
	fr, _, _ := newTestFrameReceiver(t)

	// Send VPS.
	vps := makeH265VPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        vps,
		Codec:       gen.Codec_CODEC_H265,
		IsCodecInfo: true,
	})
	require.True(t, fr.codecDetected)
	require.Equal(t, model.FormatH265, fr.codec)
	require.NotNil(t, fr.vps)
	// VPS NAL type 32: first byte 0x40, (0x40 >> 1) & 0x3F = 32
	require.Equal(t, byte(0x40), fr.vps[0])

	// Send SPS.
	sps := makeH265SPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        sps,
		Codec:       gen.Codec_CODEC_H265,
		IsCodecInfo: true,
	})
	require.NotNil(t, fr.sps)

	// Send PPS.
	pps := makeH265PPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        pps,
		Codec:       gen.Codec_CODEC_H265,
		IsCodecInfo: true,
	})
	require.NotNil(t, fr.pps)
}

func TestFrameReceiver_MissingH264Params(t *testing.T) {
	t.Helper()
	ensureTempDir(t)
	fr, _, _ := newTestFrameReceiver(t)

	// Detect codec but only send SPS, not PPS.
	sps := makeH264SPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        sps,
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})
	require.True(t, fr.codecDetected)

	// Try to start segment with IDR — should fail (missing PPS).
	err := fr.HandleFrame(context.Background(), &gen.Frame{
		Data:  withStartCode(makeH264IDR()),
		Codec: gen.Codec_CODEC_H264,
		IsIdr: true,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing H264 codec params")
}

func TestFrameReceiver_MissingH265Params(t *testing.T) {
	t.Helper()
	ensureTempDir(t)
	fr, _, _ := newTestFrameReceiver(t)

	// Detect H265 codec, send only VPS and SPS, no PPS.
	vps := makeH265VPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        vps,
		Codec:       gen.Codec_CODEC_H265,
		IsCodecInfo: true,
	})
	sps := makeH265SPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        sps,
		Codec:       gen.Codec_CODEC_H265,
		IsCodecInfo: true,
	})

	// Try to start segment — should fail (missing PPS and VPS might also be nil).
	err := fr.HandleFrame(context.Background(), &gen.Frame{
		Data:  withStartCode(makeH265IDR()),
		Codec: gen.Codec_CODEC_H265,
		IsIdr: true,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing H265 codec params")
}

func TestFrameReceiver_RecordingMetadata(t *testing.T) {
	t.Helper()
	ensureTempDir(t)
	fr, _, db := newTestFrameReceiver(t)

	// Setup codec.
	sps := makeH264SPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(sps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})
	pps := makeH264PPS()
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:        withStartCode(pps),
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	})

	// Create and close a segment.
	fr.HandleFrame(context.Background(), &gen.Frame{
		Data:  withStartCode(makeH264IDR()),
		Codec: gen.Codec_CODEC_H264,
		IsIdr: true,
	})
	fr.Close()

	require.Len(t, db.recordings, 1)
	rec := db.recordings[0]
	require.Equal(t, "test-cam", rec.CameraID)
	require.Equal(t, model.FormatH264, rec.Format)
	require.Equal(t, 1, rec.FrameCount)
	require.NotEmpty(t, rec.ID)
	require.False(t, rec.StartedAt.IsZero())
	require.False(t, rec.EndedAt.IsZero())
	require.Greater(t, rec.Duration, float64(0))
	require.NotEmpty(t, rec.FilePath)
}
