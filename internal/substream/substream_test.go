// SPDX-License-Identifier: MIT
//
// Tests for the on-demand sub-stream puller (#513): Acquire readiness from
// SDP parameter sets, frame fan-out through the hub, reference counting with
// idle recycling, permanent-failure handling, and monotonic timestamps
// across reconnects. The fake camera is a real gortsplib server (same pattern
// as the RTSP output server tests).

package substream

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// Parameter-set vectors borrowed from the RTSP output server tests (real
// camera captures — structurally valid SPS/PPS the SDP parser accepts).
var (
	tSPS = []byte{0x67, 0x64, 0x00, 0x0c, 0xac, 0x3b, 0x50, 0xb0, 0x4b, 0x42, 0x00, 0x00, 0x03, 0x00, 0x02, 0x00, 0x00, 0x03, 0x00, 0x3d, 0x08}
	tPPS = []byte{0x68, 0xee, 0x3c, 0x80}

	tVPS    = []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x78, 0x95, 0x98, 0x09}
	tSPS265 = []byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x78, 0xa0, 0x03, 0xc0, 0x80, 0x10, 0xe5, 0x96, 0x66, 0x69, 0x24, 0xca, 0xe0, 0x10, 0x00, 0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x01, 0xe0, 0x80}
	tPPS265 = []byte{0x44, 0x01, 0xc1, 0x72, 0xb4, 0x62, 0x40}

	// Slice NALs: contents are opaque to the puller (nalutil only inspects
	// the type bits).
	h264IDR = []byte{0x65, 0x88, 0x84, 0x21, 0xa0}
	h264P   = []byte{0x41, 0x9a, 0x22, 0x10}
	h265IDR = []byte{0x26, 0x01, 0xaf, 0x08, 0x52}
	h265P   = []byte{0x02, 0x3a, 0x71, 0xd4}
)

// rtpEncoder is the subset of the per-codec encoders the source writer needs.
type rtpEncoder interface {
	Encode(au [][]byte) ([]*rtp.Packet, error)
}

// testSource is a gortsplib server playing one video track in a loop.
type testSource struct {
	gs       *gortsplib.Server
	media    *description.Media
	enc      rtpEncoder
	idr, pSl []byte
	sps, pps []byte
	stop     chan struct{}
	done     chan struct{}
	url      string
	lastConn atomic.Pointer[gortsplib.ServerConn]

	// streamReady gates the writer until the ServerStream exists. The stream
	// is created lazily on the first DESCRIBE — ServerStream.Initialize
	// requires the server to be started, and the server runs in a goroutine.
	streamReady chan struct{}
	stream      *gortsplib.ServerStream
	once        sync.Once
}

func (s *testSource) OnConnOpen(ctx *gortsplib.ServerHandlerOnConnOpenCtx) {
	s.lastConn.Store(ctx.Conn)
}

// ensureStream builds the ServerStream on the first request.
func (s *testSource) ensureStream() {
	s.once.Do(func() {
		s.stream = &gortsplib.ServerStream{
			Server: s.gs,
			Desc:   &description.Session{Medias: []*description.Media{s.media}},
		}
		if err := s.stream.Initialize(); err != nil {
			panic(err)
		}
		close(s.streamReady)
	})
}

func (s *testSource) OnDescribe(*gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	s.ensureStream()
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

func (s *testSource) OnSetup(*gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	s.ensureStream()
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

func (s *testSource) OnPlay(*gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	return &base.Response{StatusCode: base.StatusOK}, nil
}

// writeLoop alternates IDR (with in-band parameter sets) and P frames at
// ~40ms cadence — a plausible low-res sub-stream.
func (s *testSource) writeLoop() {
	defer close(s.done)
	select {
	case <-s.streamReady:
	case <-s.stop:
		return
	}
	ts := uint32(1600)
	idr := true
	for {
		select {
		case <-s.stop:
			return
		default:
		}
		var au [][]byte
		if idr {
			au = [][]byte{s.sps, s.pps, s.idr}
		} else {
			au = [][]byte{s.pSl}
		}
		pkts, err := s.enc.Encode(au)
		if err == nil {
			for _, p := range pkts {
				p.Timestamp = ts
				_ = s.stream.WritePacketRTP(s.media, p)
			}
		}
		ts += 3600 // 40ms @ 90kHz
		idr = !idr
		time.Sleep(20 * time.Millisecond)
	}
}

// newTestSource starts an H.264 or H.265 source server on a random local port.
func newTestSource(t *testing.T, codec model.Format) (*testSource, string) {
	t.Helper()

	s := &testSource{
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
		streamReady: make(chan struct{}),
	}
	switch codec {
	case model.FormatH265:
		f := &format.H265{PayloadTyp: 96, VPS: tVPS, SPS: tSPS265, PPS: tPPS265}
		enc, err := f.CreateEncoder()
		require.NoError(t, err)
		s.enc, s.idr, s.pSl, s.sps, s.pps = enc, h265IDR, h265P, tSPS265, tPPS265
		s.media = &description.Media{
			Type:    description.MediaTypeVideo,
			Control: "trackID=0",
			Formats: []format.Format{f},
		}
	default:
		f := &format.H264{PayloadTyp: 96, SPS: tSPS, PPS: tPPS, PacketizationMode: 1}
		enc, err := f.CreateEncoder()
		require.NoError(t, err)
		s.enc, s.idr, s.pSl, s.sps, s.pps = enc, h264IDR, h264P, tSPS, tPPS
		s.media = &description.Media{
			Type:    description.MediaTypeVideo,
			Control: "trackID=0",
			Formats: []format.Format{f},
		}
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s.url = "rtsp://" + ln.Addr().String() + "/cam"

	s.gs = &gortsplib.Server{
		Handler:     s,
		RTSPAddress: "rtsp://" + ln.Addr().String(),
	}
	s.gs.Listen = func(_, _ string) (net.Listener, error) { return ln, nil }
	// Note: no t.Logf from this goroutine — it can outlive the test, and
	// touching *testing.T after tRunner returns is a data race.
	go func() { _ = s.gs.StartAndWait() }()
	go s.writeLoop()
	t.Cleanup(func() {
		close(s.stop)
		select {
		case <-s.streamReady:
			s.stream.Close()
		default:
		}
		<-s.done
		s.gs.Close()
	})
	return s, s.url
}

// newMJPEGSource serves a video track that carries no H.264/H.265 format —
// the puller must classify it as a permanent failure (errNoVideo).
func newMJPEGSource(t *testing.T) string {
	t.Helper()
	h := &deadHandler{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	h.gs = &gortsplib.Server{
		Handler:     h,
		RTSPAddress: "rtsp://" + ln.Addr().String(),
	}
	h.gs.Listen = func(_, _ string) (net.Listener, error) { return ln, nil }
	go func() { _ = h.gs.StartAndWait() }()
	t.Cleanup(func() {
		h.mu.Lock()
		stream := h.stream
		h.mu.Unlock()
		if stream != nil {
			stream.Close()
		}
		h.gs.Close()
	})
	return "rtsp://" + ln.Addr().String() + "/cam"
}

// deadHandler answers DESCRIBE/SETUP/PLAY with a valid but codec-less session.
type deadHandler struct {
	gs     *gortsplib.Server
	mu     sync.Mutex
	stream *gortsplib.ServerStream
}

func (h *deadHandler) ensureStream() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stream == nil {
		h.stream = &gortsplib.ServerStream{
			Server: h.gs,
			Desc: &description.Session{Medias: []*description.Media{{
				Type:    description.MediaTypeVideo,
				Control: "trackID=0",
				Formats: []format.Format{&format.MJPEG{}},
			}}},
		}
		if err := h.stream.Initialize(); err != nil {
			panic(err)
		}
	}
}

func (h *deadHandler) OnDescribe(*gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	h.ensureStream()
	return &base.Response{StatusCode: base.StatusOK}, h.stream, nil
}

func (h *deadHandler) OnSetup(*gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	h.ensureStream()
	return &base.Response{StatusCode: base.StatusOK}, h.stream, nil
}

func (h *deadHandler) OnPlay(*gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	return &base.Response{StatusCode: base.StatusOK}, nil
}

// newTestManager builds a manager resolving every camera to the given URL.
func newTestManager(t *testing.T, url string, mut func(*Config)) *Manager {
	t.Helper()
	cfg := Config{
		Resolver:          func(context.Context, string) (Target, bool, error) { return Target{URL: url}, true, nil },
		IdleTimeout:       150 * time.Millisecond,
		ReadyTimeout:      3 * time.Second,
		DialTimeout:       2 * time.Second,
		FrameStallTimeout: 10 * time.Second,
	}
	if mut != nil {
		mut(&cfg)
	}
	m := NewManager(cfg)
	t.Cleanup(m.Stop)
	return m
}

// recvFrame subscribes to the hub and returns the first frame matching pred
// (timeout 5s).
func recvFrame(t *testing.T, src *Source, pred func(model.FrameMsg) bool) model.FrameMsg {
	t.Helper()
	frames := make(chan model.FrameMsg, 64)
	require.NoError(t, src.Hub().SubscribeMsg("test", func(m model.FrameMsg) {
		select {
		case frames <- m:
		default:
		}
	}))
	t.Cleanup(func() { src.Hub().Unsubscribe("test") })
	deadline := time.After(5 * time.Second)
	for {
		select {
		case m := <-frames:
			if pred(m) {
				return m
			}
		case <-deadline:
			t.Fatal("timed out waiting for frame")
			return model.FrameMsg{}
		}
	}
}

func TestAcquireH264ReadyAndFrames(t *testing.T) {
	src0, url := newTestSource(t, model.FormatH264)
	_ = src0
	m := newTestManager(t, url, nil)

	src, err := m.Acquire(context.Background(), "cam-1")
	require.NoError(t, err)
	require.NotNil(t, src)

	codec, sps, pps, vps := src.CodecParams()
	require.Equal(t, model.FormatH264, codec)
	require.Equal(t, tSPS, sps)
	require.Equal(t, tPPS, pps)
	require.Nil(t, vps)

	// IDR reaches hub subscribers (either live or via the hub's IDR replay).
	mf := recvFrame(t, src, func(m model.FrameMsg) bool { return m.IsKeyframe })
	require.True(t, len(mf.AU) >= 3, "IDR AU should carry SPS/PPS + slice")
	require.Eventually(t, func() bool { return src.State() == StateLive }, 3*time.Second, 50*time.Millisecond)

	m.Release("cam-1")
}

func TestAcquireH265ReadyAndFrames(t *testing.T) {
	_, url := newTestSource(t, model.FormatH265)
	m := newTestManager(t, url, nil)

	src, err := m.Acquire(context.Background(), "cam-1")
	require.NoError(t, err)

	codec, sps, pps, vps := src.CodecParams()
	require.Equal(t, model.FormatH265, codec)
	require.Equal(t, tSPS265, sps)
	require.Equal(t, tPPS265, pps)
	require.Equal(t, tVPS, vps)

	mf := recvFrame(t, src, func(m model.FrameMsg) bool { return m.IsKeyframe })
	require.NotEmpty(t, mf.AU)

	m.Release("cam-1")
}

func TestAcquireSharesSourceAcrossConsumers(t *testing.T) {
	_, url := newTestSource(t, model.FormatH264)
	m := newTestManager(t, url, nil)

	a, err := m.Acquire(context.Background(), "cam-1")
	require.NoError(t, err)
	b, err := m.Acquire(context.Background(), "cam-1")
	require.NoError(t, err)
	require.Same(t, a, b)
	require.Same(t, a.Hub(), b.Hub())

	// One release keeps the pull alive; the second recycles.
	m.Release("cam-1")
	time.Sleep(400 * time.Millisecond) // > 3× idle timeout
	require.NotEmpty(t, m.Snapshot())  // still there

	recycled := make(chan *model.StreamHub, 1)
	m.SetOnRecycle(func(id string, hub *model.StreamHub) {
		if id == "cam-1" {
			recycled <- hub
		}
	})
	m.Release("cam-1")
	select {
	case hub := <-recycled:
		require.Same(t, a.Hub(), hub)
	case <-time.After(2 * time.Second):
		t.Fatal("recycle did not fire after last release")
	}

	// Next Acquire starts a fresh generation (different hub pointer).
	c, err := m.Acquire(context.Background(), "cam-1")
	require.NoError(t, err)
	require.NotSame(t, a.Hub(), c.Hub())
	m.Release("cam-1")
}

func TestAcquireNoSubStream(t *testing.T) {
	m := NewManager(Config{
		Resolver: func(context.Context, string) (Target, bool, error) { return Target{}, false, nil },
	})
	t.Cleanup(m.Stop)
	_, err := m.Acquire(context.Background(), "cam-1")
	require.ErrorIs(t, err, ErrNoSubStream)
}

func TestAcquireDialFailTimesOut(t *testing.T) {
	// Bind then close: a port that refuses/errs immediately.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	ln.Close()

	m := newTestManager(t, "rtsp://"+addr+"/cam", func(c *Config) {
		c.ReadyTimeout = 500 * time.Millisecond
	})
	start := time.Now()
	_, err = m.Acquire(context.Background(), "cam-1")
	require.Error(t, err)
	require.Less(t, time.Since(start), 3*time.Second, "Acquire must respect the ready timeout")
	m.Release("cam-1")
}

func TestAcquireNoVideoTrackFailsPermanently(t *testing.T) {
	url := newMJPEGSource(t)
	m := newTestManager(t, url, nil)

	_, err := m.Acquire(context.Background(), "cam-1")
	require.Error(t, err)
}

func TestTimestampsMonotonicAcrossReconnect(t *testing.T) {
	src0, url := newTestSource(t, model.FormatH264)
	m := newTestManager(t, url, nil)

	src, err := m.Acquire(context.Background(), "cam-1")
	require.NoError(t, err)

	frames := make(chan model.FrameMsg, 256)
	require.NoError(t, src.Hub().SubscribeMsg("test", func(mf model.FrameMsg) {
		select {
		case frames <- mf:
		default:
		}
	}))
	t.Cleanup(func() { src.Hub().Unsubscribe("test") })

	lastPTS := int64(0)
	count := 0
	deadline := time.After(2 * time.Second)
	for count < 10 {
		select {
		case mf := <-frames:
			require.Greater(t, mf.PTS, lastPTS, "PTS must be strictly increasing within a session")
			lastPTS = mf.PTS
			count++
		case <-deadline:
			t.Fatalf("only %d frames before disconnect", count)
		}
	}

	// Sever the server side of the connection; the puller must reconnect and
	// CONTINUE the timeline (rebased +1s, never backwards).
	conn := src0.lastConn.Load()
	require.NotNil(t, conn, "source should have seen a connection")
	conn.Close()

	deadline = time.After(8 * time.Second)
	post := 0
	for post < 5 {
		select {
		case mf := <-frames:
			require.Greater(t, mf.PTS, lastPTS, "PTS must continue monotonically after reconnect")
			lastPTS = mf.PTS
			post++
		case <-deadline:
			t.Fatalf("no frames after reconnect (got %d)", post)
		}
	}

	m.Release("cam-1")
}

func TestInjectCredentialsAndRedact(t *testing.T) {
	in := injectCredentials("rtsp://192.168.1.10:554/sub", "admin", "secret")
	require.Equal(t, "rtsp://admin:secret@192.168.1.10:554/sub", in)
	// Existing userinfo preserved.
	require.Equal(t, "rtsp://a:b@host/x", injectCredentials("rtsp://a:b@host/x", "admin", "secret"))
	require.Equal(t, "rtsp://192.168.1.10:554/sub", redactURL(in))
}

// Status/Hub are the flow view's per-camera accessors (#513 observability):
// nil before the first acquire, live entry with refs afterwards, nil again
// once the idle recycle reclaims the source.
func TestStatusAndHubPerCamera(t *testing.T) {
	_, url := newTestSource(t, model.FormatH264)
	m := newTestManager(t, url, nil)

	require.Nil(t, m.Status("cam-1"), "no entry before first acquire")
	require.Nil(t, m.Hub("cam-1"))

	src, err := m.Acquire(context.Background(), "cam-1")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st := m.Status("cam-1")
		return st != nil && st.State == StateLive
	}, 3*time.Second, 50*time.Millisecond)

	st := m.Status("cam-1")
	require.Equal(t, "cam-1", st.CameraID)
	require.Equal(t, model.FormatH264, st.Codec)
	require.Equal(t, 1, st.Refs)
	require.NotNil(t, m.Hub("cam-1"))
	require.Equal(t, src.Hub(), m.Hub("cam-1"))

	_, err = m.Acquire(context.Background(), "cam-1")
	require.NoError(t, err)
	require.Equal(t, 2, m.Status("cam-1").Refs)

	m.Release("cam-1")
	m.Release("cam-1")
	// Idle recycle (150ms fixture timeout) removes the entry — the flow view
	// must stop rendering the sub branch instead of showing a zombie.
	require.Eventually(t, func() bool { return m.Status("cam-1") == nil }, 3*time.Second, 50*time.Millisecond)
	require.Nil(t, m.Hub("cam-1"))
}
