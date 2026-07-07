package recorder

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtplpcm"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtpmjpeg"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// generateTestJPEG creates a valid 16x16 JPEG image for testing.
func generateTestJPEG() []byte {
	img := image.NewYCbCr(image.Rect(0, 0, 16, 16), image.YCbCrSubsampleRatio420)
	for y := range 16 {
		for x := range 16 {
			c := color.YCbCr{Y: 128, Cb: 128, Cr: 128}
			img.Y[img.YOffset(x, y)] = c.Y
			img.Cb[img.COffset(x, y)] = c.Cb
			img.Cr[img.COffset(x, y)] = c.Cr
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 50}); err != nil {
		panic("generateTestJPEG: " + err.Error())
	}
	return buf.Bytes()
}

// generateTestAudio generates test G.711 mu-law audio samples.
func generateTestAudio(sampleCount int) []byte {
	data := make([]byte, sampleCount)
	for i := range data {
		data[i] = byte(i * 7)
	}
	return data
}

// --- MJPEG test RTSP server (video only) ---

type mjpegTestServer struct {
	server  *gortsplib.Server
	stream  *gortsplib.ServerStream
	media   *description.Media
	rtpEnc  *rtpmjpeg.Encoder
	playCh  chan struct{}
	rtspURL string
}

func newMjpegTestServer(t *testing.T) *mjpegTestServer {
	t.Helper()
	port := findPort(t)
	time.Sleep(5 * time.Millisecond)

	forma := &format.MJPEG{}
	desc := &description.Session{
		Medias: []*description.Media{{
			Type:    description.MediaTypeVideo,
			Formats: []format.Format{forma},
		}},
	}

	s := &mjpegTestServer{playCh: make(chan struct{})}
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

func (s *mjpegTestServer) close() {
	s.stream.Close()
	s.server.Close()
}

func (s *mjpegTestServer) sendJPEG(jpeg []byte) {
	pkts, err := s.rtpEnc.Encode(jpeg)
	if err != nil {
		return
	}
	for _, pkt := range pkts {
		s.stream.WritePacketRTP(s.media, pkt)
	}
}

func (s *mjpegTestServer) sendFrames(count int, interval time.Duration) {
	for range count {
		s.sendJPEG(generateTestJPEG())
		if interval > 0 {
			time.Sleep(interval)
		}
	}
}

func (s *mjpegTestServer) waitPlay(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-s.playCh:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for PLAY")
	}
}

func (s *mjpegTestServer) OnDescribe(_ *gortsplib.ServerHandlerOnDescribeCtx) (
	*base.Response, *gortsplib.ServerStream, error,
) {
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

func (s *mjpegTestServer) OnSetup(_ *gortsplib.ServerHandlerOnSetupCtx) (
	*base.Response, *gortsplib.ServerStream, error,
) {
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

func (s *mjpegTestServer) OnPlay(_ *gortsplib.ServerHandlerOnPlayCtx) (
	*base.Response, error,
) {
	select {
	case <-s.playCh:
	default:
		close(s.playCh)
	}
	return &base.Response{StatusCode: base.StatusOK}, nil
}

// --- MJPEG test RTSP server with G.711 audio ---

type mjpegTestServerWithAudio struct {
	*mjpegTestServer
	audioMedia *description.Media
	audioForma *format.G711
	audioEnc   *rtplpcm.Encoder
}

func newMjpegTestServerWithAudio(t *testing.T) *mjpegTestServerWithAudio {
	t.Helper()
	port := findPort(t)
	time.Sleep(5 * time.Millisecond)

	videoForma := &format.MJPEG{}
	audioForma := &format.G711{
		PayloadTyp:   0,
		MULaw:        true,
		SampleRate:   8000,
		ChannelCount: 1,
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

	s := &mjpegTestServer{playCh: make(chan struct{})}
	s.media = desc.Medias[0]

	enc, err := videoForma.CreateEncoder()
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

	audioEnc, err := audioForma.CreateEncoder()
	require.NoError(t, err)

	return &mjpegTestServerWithAudio{
		mjpegTestServer: s,
		audioMedia:      desc.Medias[1],
		audioForma:      audioForma,
		audioEnc:        audioEnc,
	}
}

func (s *mjpegTestServerWithAudio) sendAudioFrame(data []byte) {
	pkts, err := s.audioEnc.Encode(data)
	if err != nil {
		return
	}
	for _, pkt := range pkts {
		s.stream.WritePacketRTP(s.audioMedia, pkt)
	}
}

// --- MJPEG test helpers ---

func countJPGFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	count := 0
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".jpg" {
			count++
		}
	}
	return count
}

func countSegmentDirs(t *testing.T, m *storage.Manager, cameraID string) int {
	t.Helper()
	files, err := m.ListFiles(cameraID)
	require.NoError(t, err)
	return len(files)
}

// --- Tests ---

func TestMJPEGRecorder_RecordsFrames(t *testing.T) {
	srv := newMjpegTestServer(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewMJPEGRecorder(MJPEGConfig{
		CameraID:   "cam-mjpeg-test",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 5 * time.Minute,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	require.Equal(t, model.StatusRecording, rec.Status())

	srv.waitPlay(t, 5*time.Second)
	time.Sleep(500 * time.Millisecond)

	srv.sendFrames(5, 30*time.Millisecond)
	time.Sleep(300 * time.Millisecond)

	require.NoError(t, rec.Stop())
	require.Equal(t, model.StatusStopped, rec.Status())

	files, err := mgr.ListFiles("cam-mjpeg-test")
	require.NoError(t, err)
	require.NotEmpty(t, files, "expected at least one recorded segment")

	// Check that the segment directory contains .jpg files
	for _, f := range files {
		info, err := os.Stat(f)
		require.NoError(t, err)
		require.True(t, info.IsDir(), "MJPEG segment should be a directory")
		n := countJPGFiles(t, f)
		require.Greater(t, n, 0, "segment %s should contain .jpg files", f)
	}
}

func TestMJPEGRecorder_StartStop(t *testing.T) {
	srv := newMjpegTestServer(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewMJPEGRecorder(MJPEGConfig{
		CameraID:   "cam-mjpeg-lifecycle",
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

func TestMJPEGRecorder_SegmentDuration(t *testing.T) {
	srv := newMjpegTestServer(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewMJPEGRecorder(MJPEGConfig{
		CameraID:   "cam-mjpeg-seg",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 150 * time.Millisecond,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))

	srv.waitPlay(t, 5*time.Second)
	time.Sleep(50 * time.Millisecond)

	srv.sendFrames(20, 50*time.Millisecond)
	time.Sleep(300 * time.Millisecond)

	require.NoError(t, rec.Stop())

	n := countSegmentDirs(t, mgr, "cam-mjpeg-seg")
	require.GreaterOrEqual(t, n, 2, "expected at least 2 segments from duration rotation, got %d", n)
}

func TestMJPEGRecorder_FrameSampling(t *testing.T) {
	srv := newMjpegTestServer(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewMJPEGRecorder(MJPEGConfig{
		CameraID:       "cam-mjpeg-sample",
		RTSPURL:        srv.rtspURL,
		SegmentDur:     5 * time.Minute,
		SampleInterval: 3,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))

	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	srv.sendFrames(9, 30*time.Millisecond)
	time.Sleep(500 * time.Millisecond)

	require.NoError(t, rec.Stop())

	// With SampleInterval=3, sending 9 frames should save exactly 3
	files, err := mgr.ListFiles("cam-mjpeg-sample")
	require.NoError(t, err)
	require.Len(t, files, 1, "expected exactly 1 segment")

	totalJPGs := 0
	for _, f := range files {
		totalJPGs += countJPGFiles(t, f)
	}
	require.Equal(t, 3, totalJPGs, "expected exactly 3 saved frames with SampleInterval=3 from 9 sent")
}

func TestMJPEGRecorder_GracefulShutdown(t *testing.T) {
	srv := newMjpegTestServer(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewMJPEGRecorder(MJPEGConfig{
		CameraID:   "cam-mjpeg-grace",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 5 * time.Minute,
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

func TestMJPEGRecorder_StatusTransitions(t *testing.T) {
	srv := newMjpegTestServer(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewMJPEGRecorder(MJPEGConfig{
		CameraID:   "cam-mjpeg-status",
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

// --- Audio Tests ---

func TestMJPEGRecorderWithAudio(t *testing.T) {
	srv := newMjpegTestServerWithAudio(t)
	defer srv.close()

	mgr := newTestManager(t)
	hub := model.NewStreamHub()

	rec := NewMJPEGRecorder(MJPEGConfig{
		CameraID:     "cam-mjpeg-audio",
		RTSPURL:      srv.rtspURL,
		SegmentDur:   5 * time.Minute,
		AudioEnabled: true,
	}, mgr)
	rec.Hub = hub

	collector := newAudioCollector(hub, "test-audio-mjpeg")
	defer collector.close(hub, "test-audio-mjpeg")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	// Send video frames.
	srv.sendFrames(5, 30*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	// Send audio frames.
	for range 5 {
		srv.sendAudioFrame(generateTestAudio(160))
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, rec.Stop())

	// Verify audio frames were broadcast.
	collector.waitFrames(t, 1, 3*time.Second)
	frames := collector.count()
	require.GreaterOrEqual(t, frames, 1, "expected at least 1 audio frame via BroadcastAudio")

	// Verify all frames are G.711.
	collector.mu.Lock()
	for _, f := range collector.frames {
		require.Equal(t, model.AudioG711, f.Codec, "audio codec should be G.711")
	}
	collector.mu.Unlock()

	// Verify AVI file was created.
	files, err := mgr.ListFiles("cam-mjpeg-audio")
	require.NoError(t, err)
	require.NotEmpty(t, files, "expected at least one segment")

	hasAVI := false
	for _, f := range files {
		if filepath.Ext(f) == ".avi" {
			hasAVI = true
			info, err := os.Stat(f)
			require.NoError(t, err)
			require.False(t, info.IsDir(), "AVI segment should be a file, not a directory")
			require.Greater(t, info.Size(), int64(0), "AVI file should have content")

			// Verify AVI RIFF header.
			data, err := os.ReadFile(f)
			require.NoError(t, err)
			require.True(t, len(data) >= 12, "AVI file should have header")
			require.Equal(t, "RIFF", string(data[:4]), "AVI file should start with RIFF")
		}
	}
	require.True(t, hasAVI, "expected at least one .avi file")
}

func TestMJPEGRecorderNoAudio(t *testing.T) {
	// Regression test: AudioEnabled=true but no audio in SDP → JPEG directory path.
	srv := newMjpegTestServer(t)
	defer srv.close()

	mgr := newTestManager(t)
	hub := model.NewStreamHub()

	rec := NewMJPEGRecorder(MJPEGConfig{
		CameraID:     "cam-noaudio",
		RTSPURL:      srv.rtspURL,
		SegmentDur:   5 * time.Minute,
		AudioEnabled: true,
	}, mgr)
	rec.Hub = hub

	collector := newAudioCollector(hub, "test-noaudio-mjpeg")
	defer collector.close(hub, "test-noaudio-mjpeg")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	srv.sendFrames(5, 30*time.Millisecond)
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, rec.Stop())
	require.Equal(t, model.StatusStopped, rec.Status())

	// No audio frames should have been broadcast.
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, 0, collector.count(), "no audio frames should be broadcast when no audio in SDP")

	// Video should still be recorded as JPEG directory (backward compat).
	files, err := mgr.ListFiles("cam-noaudio")
	require.NoError(t, err)
	require.NotEmpty(t, files, "expected video recording even with no audio in SDP")

	// Verify all segments are directories with .jpg files.
	for _, f := range files {
		info, err := os.Stat(f)
		require.NoError(t, err)
		require.True(t, info.IsDir(), "MJPEG segment should be a directory when no audio")
		n := countJPGFiles(t, f)
		require.Greater(t, n, 0, "segment %s should contain .jpg files", f)
	}
}

func TestMJPEGRecorderAudioDrop(t *testing.T) {
	// Test: audio present then stops → recorder continues, no panic, AVI produced.
	srv := newMjpegTestServerWithAudio(t)
	defer srv.close()

	mgr := newTestManager(t)

	rec := NewMJPEGRecorder(MJPEGConfig{
		CameraID:     "cam-audiodrop",
		RTSPURL:      srv.rtspURL,
		SegmentDur:   5 * time.Minute,
		AudioEnabled: true,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	// Send video + audio frames.
	srv.sendFrames(3, 30*time.Millisecond)
	for range 3 {
		srv.sendAudioFrame(generateTestAudio(160))
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)

	// Stop sending audio, continue video.
	srv.sendFrames(5, 30*time.Millisecond)
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, rec.Stop())

	// Verify AVI file was produced without panic.
	files, err := mgr.ListFiles("cam-audiodrop")
	require.NoError(t, err)
	require.NotEmpty(t, files, "expected at least one segment")

	hasAVI := false
	for _, f := range files {
		if filepath.Ext(f) == ".avi" {
			hasAVI = true
			info, err := os.Stat(f)
			require.NoError(t, err)
			require.False(t, info.IsDir(), "AVI segment should be a file")
			require.Greater(t, info.Size(), int64(0), "AVI file should have content")
		}
	}
	require.True(t, hasAVI, "expected at least one .avi file")
}
