package recorder

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtpmpeg4audio"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

var (
	testSPS  = []byte{0x67, 0x42, 0x00, 0x0a, 0xf8, 0x41, 0xa2}
	testPPS  = []byte{0x68, 0xce, 0x38, 0x80}
	testIDR  = []byte{0x65, 0x88, 0x84, 0x00, 0x10}
	testSPS2 = []byte{0x67, 0x42, 0x00, 0x14, 0xf8, 0x41, 0xa2}
)

type testRTSPServer struct {
	server  *gortsplib.Server
	stream  *gortsplib.ServerStream
	media   *description.Media
	rtpEnc  *rtph264.Encoder
	playCh  chan struct{}
	rtspURL string
}

func findPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func newTestRTSPServer(t *testing.T) *testRTSPServer {
	t.Helper()
	port := findPort(t)
	time.Sleep(5 * time.Millisecond)

	forma := &format.H264{
		PayloadTyp:        96,
		PacketizationMode: 1,
		SPS:               testSPS,
		PPS:               testPPS,
	}
	desc := &description.Session{
		Medias: []*description.Media{{
			Type:    description.MediaTypeVideo,
			Formats: []format.Format{forma},
		}},
	}

	s := &testRTSPServer{playCh: make(chan struct{})}
	s.media = desc.Medias[0]

	enc, err := forma.CreateEncoder()
	require.NoError(t, err)
	s.rtpEnc = enc

	s.server = &gortsplib.Server{
		Handler:     s,
		RTSPAddress: fmt.Sprintf("127.0.0.1:%d", port),
	}
	require.NoError(t, s.server.Start())

	s.stream = &gortsplib.ServerStream{Server: s.server, Desc: desc}
	require.NoError(t, s.stream.Initialize())

	s.rtspURL = fmt.Sprintf("rtsp://127.0.0.1:%d/test", port)
	return s
}

func (s *testRTSPServer) close() {
	s.stream.Close()
	s.server.Close()
}

func (s *testRTSPServer) sendAU(au [][]byte) {
	pkts, err := s.rtpEnc.Encode(au)
	if err != nil {
		return
	}
	for _, pkt := range pkts {
		s.stream.WritePacketRTP(s.media, pkt)
	}
}

func (s *testRTSPServer) sendFrames(count int, interval time.Duration) {
	for range count {
		s.sendAU([][]byte{testSPS, testPPS, testIDR})
		if interval > 0 {
			time.Sleep(interval)
		}
	}
}

func (s *testRTSPServer) waitPlay(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-s.playCh:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for PLAY")
	}
}

func (s *testRTSPServer) OnDescribe(_ *gortsplib.ServerHandlerOnDescribeCtx) (
	*base.Response, *gortsplib.ServerStream, error,
) {
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

func (s *testRTSPServer) OnSetup(_ *gortsplib.ServerHandlerOnSetupCtx) (
	*base.Response, *gortsplib.ServerStream, error,
) {
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

func (s *testRTSPServer) OnPlay(_ *gortsplib.ServerHandlerOnPlayCtx) (
	*base.Response, error,
) {
	select {
	case <-s.playCh:
	default:
		close(s.playCh)
	}
	return &base.Response{StatusCode: base.StatusOK}, nil
}

func newTestManager(t *testing.T) *storage.Manager {
	t.Helper()
	m, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	return m
}

func countFinalFiles(t *testing.T, m *storage.Manager, cameraID string) int {
	t.Helper()
	files, err := m.ListFiles(cameraID)
	require.NoError(t, err)
	return len(files)
}

func fileIsMP4(t *testing.T, path string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	if len(data) < 8 {
		return false
	}
	// MP4 files start with ftyp box: [4-byte size]["ftyp"]
	return bytes.Equal(data[4:8], []byte("ftyp"))
}

// --- Tests ---

func TestH264Recorder_RecordsFrames(t *testing.T) {
	srv := newTestRTSPServer(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewH264Recorder(H264Config{
		CameraID:   "cam-test",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 5 * time.Minute,
		RingBufCap: 100,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	require.Equal(t, model.StatusRecording, rec.Status())

	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	srv.sendFrames(5, 30*time.Millisecond)
	time.Sleep(300 * time.Millisecond)

	require.NoError(t, rec.Stop())
	require.Equal(t, model.StatusStopped, rec.Status())

	files, err := mgr.ListFiles("cam-test")
	require.NoError(t, err)
	require.NotEmpty(t, files, "expected at least one recorded file")

	for _, f := range files {
		require.True(t, fileIsMP4(t, f), "file %s should be valid MP4", f)
	}
}

func TestH264Recorder_StartStop(t *testing.T) {
	srv := newTestRTSPServer(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewH264Recorder(H264Config{
		CameraID:   "cam-lifecycle",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 5 * time.Minute,
	}, mgr)

	require.Equal(t, model.StatusStopped, rec.Status())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	require.Equal(t, model.StatusRecording, rec.Status())

	require.Error(t, rec.Start(ctx))

	require.NoError(t, rec.Stop())
	require.Equal(t, model.StatusStopped, rec.Status())

	require.NoError(t, rec.Stop())
}

func TestH264Recorder_SegmentDuration(t *testing.T) {
	srv := newTestRTSPServer(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewH264Recorder(H264Config{
		CameraID:   "cam-seg",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 150 * time.Millisecond,
		RingBufCap: 200,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))

	srv.waitPlay(t, 5*time.Second)
	time.Sleep(50 * time.Millisecond)

	srv.sendFrames(20, 50*time.Millisecond)
	time.Sleep(300 * time.Millisecond)

	require.NoError(t, rec.Stop())

	n := countFinalFiles(t, mgr, "cam-seg")
	require.GreaterOrEqual(t, n, 2, "expected at least 2 segments from duration rotation, got %d", n)
}

func TestH264Recorder_SPSChangeNewSegment(t *testing.T) {
	srv := newTestRTSPServer(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewH264Recorder(H264Config{
		CameraID:   "cam-sps",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 5 * time.Minute,
		RingBufCap: 200,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))

	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	srv.sendFrames(3, 30*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	for range 3 {
		srv.sendAU([][]byte{testSPS2, testPPS, testIDR})
		time.Sleep(30 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)

	require.NoError(t, rec.Stop())

	n := countFinalFiles(t, mgr, "cam-sps")
	require.GreaterOrEqual(t, n, 2, "SPS change should produce at least 2 segments, got %d", n)
}

func TestH264Recorder_GracefulShutdown(t *testing.T) {
	srv := newTestRTSPServer(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewH264Recorder(H264Config{
		CameraID:   "cam-grace",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 5 * time.Minute,
		RingBufCap: 100,
	}, mgr)

	ctx, cancel := context.WithCancel(context.Background())

	require.NoError(t, rec.Start(ctx))

	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	srv.sendFrames(3, 20*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	cancel()

	done := make(chan struct{})
	go func() {
		rec.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not return within timeout after context cancellation")
	}

	require.Equal(t, model.StatusStopped, rec.Status())
}

func TestH264Recorder_Reconnect(t *testing.T) {
	port := findPort(t)
	time.Sleep(5 * time.Millisecond)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	rtspURL := fmt.Sprintf("rtsp://127.0.0.1:%d/test", port)

	mgr := newTestManager(t)
	rec := NewH264Recorder(H264Config{
		CameraID:   "cam-reconn",
		RTSPURL:    rtspURL,
		SegmentDur: 5 * time.Minute,
		RingBufCap: 100,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))

	time.Sleep(200 * time.Millisecond)

	forma := &format.H264{
		PayloadTyp:        96,
		PacketizationMode: 1,
		SPS:               testSPS,
		PPS:               testPPS,
	}
	desc := &description.Session{
		Medias: []*description.Media{{
			Type:    description.MediaTypeVideo,
			Formats: []format.Format{forma},
		}},
	}

	playCh := make(chan struct{})
	h := &reconnHandler{playCh: playCh}

	srv := &gortsplib.Server{Handler: h, RTSPAddress: addr}
	require.NoError(t, srv.Start())

	stream := &gortsplib.ServerStream{Server: srv, Desc: desc}
	require.NoError(t, stream.Initialize())
	h.setStream(stream)

	defer func() {
		stream.Close()
		srv.Close()
	}()

	select {
	case <-playCh:
	case <-time.After(8 * time.Second):
		t.Fatal("recorder did not reconnect within timeout")
	}

	require.Equal(t, model.StatusRecording, rec.Status())

	enc, err := forma.CreateEncoder()
	require.NoError(t, err)
	for range 3 {
		pkts, _ := enc.Encode([][]byte{testSPS, testPPS, testIDR})
		for _, pkt := range pkts {
			stream.WritePacketRTP(desc.Medias[0], pkt)
		}
		time.Sleep(30 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, rec.Stop())

	n := countFinalFiles(t, mgr, "cam-reconn")
	require.NotEmpty(t, n, "expected at least one file after reconnect")
}

func TestH264Recorder_StatusTransitions(t *testing.T) {
	srv := newTestRTSPServer(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewH264Recorder(H264Config{
		CameraID:   "cam-status",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 5 * time.Minute,
	}, mgr)

	require.Equal(t, model.StatusStopped, rec.Status())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	require.Equal(t, model.StatusRecording, rec.Status())

	srv.waitPlay(t, 5*time.Second)
	time.Sleep(50 * time.Millisecond)

	require.NoError(t, rec.Stop())
	require.Equal(t, model.StatusStopped, rec.Status())
}

func TestH264Recorder_RingBufferDrop(t *testing.T) {
	srv := newTestRTSPServer(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewH264Recorder(H264Config{
		CameraID:   "cam-ring",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 5 * time.Minute,
		RingBufCap: 5,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))

	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	srv.sendFrames(50, 0)
	time.Sleep(500 * time.Millisecond)

	require.NoError(t, rec.Stop())
	require.Equal(t, model.StatusStopped, rec.Status())

	n := countFinalFiles(t, mgr, "cam-ring")
	require.NotEmpty(t, n, "expected at least one file even with ring buffer drops")
}

type reconnHandler struct {
	mu     sync.RWMutex
	stream *gortsplib.ServerStream
	playCh chan struct{}
	once   sync.Once
}

func (h *reconnHandler) setStream(s *gortsplib.ServerStream) {
	h.mu.Lock()
	h.stream = s
	h.mu.Unlock()
}

func (h *reconnHandler) getStream() *gortsplib.ServerStream {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.stream
}

func (h *reconnHandler) OnDescribe(_ *gortsplib.ServerHandlerOnDescribeCtx) (
	*base.Response, *gortsplib.ServerStream, error,
) {
	return &base.Response{StatusCode: base.StatusOK}, h.getStream(), nil
}

func (h *reconnHandler) OnSetup(_ *gortsplib.ServerHandlerOnSetupCtx) (
	*base.Response, *gortsplib.ServerStream, error,
) {
	return &base.Response{StatusCode: base.StatusOK}, h.getStream(), nil
}

func (h *reconnHandler) OnPlay(_ *gortsplib.ServerHandlerOnPlayCtx) (
	*base.Response, error,
) {
	h.once.Do(func() { close(h.playCh) })
	return &base.Response{StatusCode: base.StatusOK}, nil
}

// --- Audio Test Helpers ---

// testRTSPServerWithAudio wraps testRTSPServer with an additional AAC audio media.
type testRTSPServerWithAudio struct {
	*testRTSPServer
	audioMedia *description.Media
	audioForma *format.MPEG4Audio
	audioEnc   *rtpmpeg4audio.Encoder
}

func newTestRTSPServerWithAudio(t *testing.T) *testRTSPServerWithAudio {
	t.Helper()
	port := findPort(t)
	time.Sleep(5 * time.Millisecond)

	videoForma := &format.H264{
		PayloadTyp:        96,
		PacketizationMode: 1,
		SPS:               testSPS,
		PPS:               testPPS,
	}
	audioForma := &format.MPEG4Audio{
		PayloadTyp: 97,
		Config: &mpeg4audio.AudioSpecificConfig{
			Type:          mpeg4audio.ObjectTypeAACLC,
			SampleRate:    48000,
			ChannelConfig: 2,
			ChannelCount:  2,
		},
		SizeLength:       13,
		IndexLength:      3,
		IndexDeltaLength: 3,
	}
	desc := &description.Session{
		Medias: []*description.Media{
			{
				Type:    description.MediaTypeVideo,
				Formats: []format.Format{videoForma},
			},
			{
				Type:    description.MediaTypeAudio,
				Formats: []format.Format{audioForma},
			},
		},
	}

	s := &testRTSPServer{playCh: make(chan struct{})}
	s.media = desc.Medias[0]

	videoEnc, err := videoForma.CreateEncoder()
	require.NoError(t, err)
	s.rtpEnc = videoEnc

	s.server = &gortsplib.Server{
		Handler:     s,
		RTSPAddress: fmt.Sprintf("127.0.0.1:%d", port),
	}
	require.NoError(t, s.server.Start())

	s.stream = &gortsplib.ServerStream{Server: s.server, Desc: desc}
	require.NoError(t, s.stream.Initialize())

	s.rtspURL = fmt.Sprintf("rtsp://127.0.0.1:%d/test", port)

	audioEnc, err := audioForma.CreateEncoder()
	require.NoError(t, err)

	return &testRTSPServerWithAudio{
		testRTSPServer: s,
		audioMedia:     desc.Medias[1],
		audioForma:     audioForma,
		audioEnc:       audioEnc,
	}
}

func (s *testRTSPServerWithAudio) sendAudioFrame(data []byte) {
	pkts, err := s.audioEnc.Encode([][]byte{data})
	if err != nil {
		return
	}
	for _, pkt := range pkts {
		s.stream.WritePacketRTP(s.audioMedia, pkt)
	}
}

// audioCollector is a test helper that collects audio frames from StreamHub.
type audioCollector struct {
	mu     sync.Mutex
	frames []model.AudioFrame
	done   chan struct{}
}

func newAudioCollector(hub *model.StreamHub, id string) *audioCollector {
	c := &audioCollector{done: make(chan struct{})}
	hub.SubscribeAudio(id, func(pts int64, codec model.AudioCodec, data []byte) {
		c.mu.Lock()
		c.frames = append(c.frames, model.AudioFrame{PTS: pts, Codec: codec, Data: data})
		c.mu.Unlock()
	})
	return c
}

func (c *audioCollector) waitFrames(t *testing.T, minFrames int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		n := len(c.frames)
		c.mu.Unlock()
		if n >= minFrames {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.mu.Lock()
	n := len(c.frames)
	c.mu.Unlock()
	t.Fatalf("timed out waiting for %d audio frames, got %d", minFrames, n)
}

func (c *audioCollector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.frames)
}

func (c *audioCollector) close(hub *model.StreamHub, id string) {
	hub.UnsubscribeAudio(id)
}

// --- Audio Tests ---

func TestH264Recorder_AudioBroadcast(t *testing.T) {
	srv := newTestRTSPServerWithAudio(t)
	defer srv.close()

	mgr := newTestManager(t)
	hub := model.NewStreamHub()

	rec := NewH264Recorder(H264Config{
		CameraID:     "cam-audio",
		RTSPURL:      srv.rtspURL,
		SegmentDur:   5 * time.Minute,
		RingBufCap:   100,
		AudioEnabled: true,
		AudioInRecordings: true,
	}, mgr)
	rec.Hub = hub

	collector := newAudioCollector(hub, "test-audio")
	defer collector.close(hub, "test-audio")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	// Send video frames to start segment.
	srv.sendFrames(3, 20*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	// Send audio frames.
	for range 5 {
		srv.sendAudioFrame([]byte{0x01, 0x02, 0x03, 0x04})
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, rec.Stop())

	collector.waitFrames(t, 1, 2*time.Second)
	frames := collector.count()
	require.GreaterOrEqual(t, frames, 1, "expected at least 1 audio frame via BroadcastAudio")

	// Verify all frames are AAC.
	collector.mu.Lock()
	defer collector.mu.Unlock()
	for _, f := range collector.frames {
		require.Equal(t, model.AudioAAC, f.Codec, "audio codec should be AAC")
	}
}

func TestH264Recorder_AudioDisabled_StillRecordsVideo(t *testing.T) {
	srv := newTestRTSPServerWithAudio(t)
	defer srv.close()

	mgr := newTestManager(t)
	hub := model.NewStreamHub()

	rec := NewH264Recorder(H264Config{
		CameraID:     "cam-noaudio",
		RTSPURL:      srv.rtspURL,
		SegmentDur:   5 * time.Minute,
		RingBufCap:   100,
		AudioEnabled: false,
	}, mgr)
	rec.Hub = hub

	collector := newAudioCollector(hub, "test-noaudio")
	defer collector.close(hub, "test-noaudio")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	srv.sendFrames(5, 30*time.Millisecond)
	time.Sleep(200 * time.Millisecond)

	// Send audio — should be ignored.
	for range 3 {
		srv.sendAudioFrame([]byte{0xAA, 0xBB})
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, rec.Stop())
	require.Equal(t, model.StatusStopped, rec.Status())

	// Video should still be recorded.
	files, err := mgr.ListFiles("cam-noaudio")
	require.NoError(t, err)
	require.NotEmpty(t, files, "expected video recording even with audio disabled")

	// No audio frames should have been broadcast.
	require.Equal(t, 0, collector.count(), "no audio frames should be broadcast when AudioEnabled=false")
}

func TestH264Recorder_AudioEnabled_NoAudioInStream(t *testing.T) {
	// Server provides video only (no AAC media).
	srv := newTestRTSPServer(t)
	defer srv.close()

	mgr := newTestManager(t)

	rec := NewH264Recorder(H264Config{
		CameraID:     "cam-videoonly",
		RTSPURL:      srv.rtspURL,
		SegmentDur:   5 * time.Minute,
		RingBufCap:   100,
		AudioEnabled: true,
		AudioInRecordings: true,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	srv.sendFrames(5, 30*time.Millisecond)
	time.Sleep(300 * time.Millisecond)

	require.NoError(t, rec.Stop())
	require.Equal(t, model.StatusStopped, rec.Status())

	files, err := mgr.ListFiles("cam-videoonly")
	require.NoError(t, err)
	require.NotEmpty(t, files, "expected video recording even when no audio in stream")
}
