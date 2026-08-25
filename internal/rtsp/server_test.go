// SPDX-License-Identifier: MIT
//
// Tests for the RTSP output server (#522): DESCRIBE/SETUP/PLAY round-trips
// against a real gortsplib client, auth, stream (re)build semantics on
// recorder restart / parameter change, and request-path parsing.

package rtsp

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/format"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// Minimal-but-plausible parameter sets (contents are opaque to the server;
// SDP only base64-carries them).
var (
	tSPS = []byte{0x67, 0x64, 0x00, 0x0c, 0xac, 0x3b, 0x50, 0xb0, 0x4b, 0x42, 0x00, 0x00, 0x03, 0x00, 0x02, 0x00, 0x00, 0x03, 0x00, 0x3d, 0x08}
	tPPS = []byte{0x68, 0xee, 0x3c, 0x80}
	// H.265 parameter sets (NAL types 32/33/34) — the client's SDP parser
	// fully parses SPS/PPS, so these must be structurally valid (borrowed from
	// the hls manager tests' real camera capture).
	tVPS    = []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x78, 0x95, 0x98, 0x09}
	tSPS265 = []byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x78, 0xa0, 0x03, 0xc0, 0x80, 0x10, 0xe5, 0x96, 0x66, 0x69, 0x24, 0xca, 0xe0, 0x10, 0x00, 0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x01, 0xe0, 0x80}
	tPPS265 = []byte{0x44, 0x01, 0xc1, 0x72, 0xb4, 0x62, 0x40}
)

type testServer struct {
	*Server
	t       *testing.T
	url     string
	hub     *model.StreamHub
	codec   model.Format
	sps     []byte
	pps     []byte
	known   atomic.Bool
	stopped chan struct{}
}

// startTestServer boots an RTSP server on an ephemeral port with an injectable
// provider whose responses the test can mutate.
func startTestServer(t *testing.T, cfg Config) *testServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()

	ts := &testServer{
		t:       t,
		hub:     model.NewStreamHub(),
		codec:   model.FormatH264,
		sps:     tSPS,
		pps:     tPPS,
		stopped: make(chan struct{}),
	}
	ts.known.Store(true)
	cfg.Addr = addr
	// Hand the PRE-BOUND listener to gortsplib so the socket is accepting
	// before Start runs — clients may dial immediately.
	cfg.ListenFn = func(_, _ string) (net.Listener, error) { return ln, nil }
	ts.Server = NewServer(cfg, func(cameraID string) (StreamInfo, bool) {
		if cameraID != "cam-1" || !ts.known.Load() {
			return StreamInfo{}, false
		}
		return StreamInfo{Codec: ts.codec, SPS: ts.sps, PPS: ts.pps, VPS: tVPS, Hub: ts.hub}, true
	})
	go func() {
		_ = ts.Server.Start(context.Background())
		close(ts.stopped)
	}()
	t.Cleanup(func() {
		_ = ts.Server.Stop()
		select {
		case <-ts.stopped:
		case <-time.After(3 * time.Second):
			t.Error("rtsp server did not stop")
		}
	})
	ts.url = "rtsp://" + addr + "/cam-1"
	ts.waitReady()
	return ts
}

// waitReady blocks until gortsplib's Start fully completed — a successful RTSP
// OPTIONS round-trip proves the accept loop is live and the internal state
// (sessions map) exists, so direct streamFor calls in tests don't race startup.
func (ts *testServer) waitReady() {
	u := mustURL(ts.t, "rtsp://"+ts.hostPart()+"/")
	tcp := gortsplib.ProtocolTCP
	deadline := time.Now().Add(3 * time.Second)
	for {
		c := &gortsplib.Client{Scheme: u.Scheme, Host: u.Host, Protocol: &tcp,
			ReadTimeout: time.Second, WriteTimeout: time.Second}
		if err := c.Start(); err == nil {
			_, err := c.Options(u)
			c.Close()
			if err == nil {
				return
			}
		}
		if time.Now().After(deadline) {
			ts.t.Fatalf("rtsp server never became ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func dialClient(t *testing.T, rawURL string) *gortsplib.Client {
	t.Helper()
	u, err := base.ParseURL(rawURL)
	require.NoError(t, err)
	tcp := gortsplib.ProtocolTCP
	c := &gortsplib.Client{Scheme: u.Scheme, Host: u.Host, Protocol: &tcp,
		ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second}
	require.NoError(t, c.Start())
	t.Cleanup(c.Close)
	return c
}

// feedIDR pushes one synthetic IDR AU through the hub.
func (ts *testServer) feedIDR() {
	ts.hub.Broadcast(0, [][]byte{tSPS, tPPS, {0x65, 0x01, 0x02, 0x03}}, true)
}

func TestRTSPServeAndPullH264(t *testing.T) {
	ts := startTestServer(t, Config{})
	c := dialClient(t, ts.url)

	desc, _, err := c.Describe(mustURL(t, ts.url))
	require.NoError(t, err)
	require.Len(t, desc.Medias, 1)
	require.Len(t, desc.Medias[0].Formats, 1)
	require.Equal(t, tSPS, desc.Medias[0].Formats[0].(*format.H264).SPS,
		"SDP must carry the camera's current SPS")

	_, err = c.Setup(desc.BaseURL, desc.Medias[0], 0, 0)
	require.NoError(t, err)

	pkts := make(chan *rtp.Packet, 16)
	c.OnPacketRTP(desc.Medias[0], desc.Medias[0].Formats[0], func(pkt *rtp.Packet) {
		select {
		case pkts <- pkt:
		default:
		}
	})
	_, err = c.Play(nil)
	require.NoError(t, err)

	deadline := time.After(5 * time.Second)
	for {
		ts.feedIDR()
		select {
		case p := <-pkts:
			// First AU's RTP timestamp is legitimately 0 (it anchors the
			// clock); SSRC proves real packets from our encoder.
			require.NotZero(t, p.SSRC)
			return
		case <-time.After(200 * time.Millisecond):
			select {
			case <-deadline:
				t.Fatal("no RTP packet received within 5s")
			default:
			}
		}
	}
}

func TestRTSPServeH265(t *testing.T) {
	ts := startTestServer(t, Config{})
	ts.codec = model.FormatH265
	ts.sps = tSPS265
	ts.pps = tPPS265
	c := dialClient(t, ts.url)

	desc, _, err := c.Describe(mustURL(t, ts.url))
	require.NoError(t, err)
	require.Len(t, desc.Medias, 1)
	require.Equal(t, tSPS265, desc.Medias[0].Formats[0].(*format.H265).SPS)

	_, err = c.Setup(desc.BaseURL, desc.Medias[0], 0, 0)
	require.NoError(t, err)
	pkts := make(chan *rtp.Packet, 16)
	c.OnPacketRTP(desc.Medias[0], desc.Medias[0].Formats[0], func(pkt *rtp.Packet) {
		select {
		case pkts <- pkt:
		default:
		}
	})
	_, err = c.Play(nil)
	require.NoError(t, err)

	ts.hub.Broadcast(0, [][]byte{tVPS, tSPS265, tPPS265, {0x26, 0x01, 0x02}}, true)
	select {
	case <-pkts:
	case <-time.After(5 * time.Second):
		t.Fatal("no H265 RTP packet received")
	}
}

func TestRTSPUnknownCamera(t *testing.T) {
	ts := startTestServer(t, Config{})
	ts.known.Store(false)
	c := dialClient(t, ts.url)
	_, _, err := c.Describe(mustURL(t, ts.url))
	require.Error(t, err, "DESCRIBE for unknown camera must fail")
}

func TestRTSPAuth(t *testing.T) {
	ts := startTestServer(t, Config{Username: "user", Password: "pass"})

	// No credentials → rejected.
	c := dialClient(t, ts.url)
	_, _, err := c.Describe(mustURL(t, ts.url))
	require.Error(t, err, "DESCRIBE without credentials must be rejected")

	// Correct credentials in the URL → accepted.
	c2 := dialClient(t, "rtsp://user:pass@"+ts.hostPart()+"/cam-1")
	_, _, err = c2.Describe(mustURL(t, "rtsp://user:pass@"+ts.hostPart()+"/cam-1"))
	require.NoError(t, err)
}

func (ts *testServer) hostPart() string {
	return mustURL(ts.t, ts.url).Host
}

func mustURL(t *testing.T, raw string) *base.URL {
	t.Helper()
	u, err := base.ParseURL(raw)
	require.NoError(t, err)
	return u
}

func TestRTSPStreamRebuildOnParamAndHubChange(t *testing.T) {
	ts := startTestServer(t, Config{})
	s := ts.Server

	cs1 := s.streamFor("cam-1")
	require.NotNil(t, cs1)
	require.Same(t, cs1, s.streamFor("cam-1"), "unchanged state must reuse the stream")

	// Parameter-set change (e.g. camera resolution switch) → same stream, hot
	// SDP reload — readers are NOT disconnected.
	newSPS := []byte{0x67, 0x64, 0x00, 0x0c, 0xff}
	ts.sps = newSPS
	cs2 := s.streamFor("cam-1")
	require.Same(t, cs1, cs2)
	require.False(t, cs2.closed.Load())
	require.Equal(t, newSPS, cs2.sps)
	require.Equal(t, newSPS, cs2.media.Formats[0].(*format.H264).SPS, "SDP format must carry the new SPS")

	// Recorder restart → new hub → full rebuild even with identical params.
	ts.hub = model.NewStreamHub()
	cs3 := s.streamFor("cam-1")
	require.NotNil(t, cs3)
	require.NotSame(t, cs2, cs3)
	require.True(t, cs2.closed.Load(), "old stream must be torn down")
}

func TestRTSPNotReady(t *testing.T) {
	ts := startTestServer(t, Config{})
	ts.sps = nil // camera warming up
	require.Nil(t, ts.Server.streamFor("cam-1"))

	// MJPEG cameras are not servable.
	ts.sps = tSPS
	ts.codec = model.FormatMJPEG
	require.Nil(t, ts.Server.streamFor("cam-1"))
}

func TestCameraIDFromPath(t *testing.T) {
	require.Equal(t, "cam-1", cameraIDFromPath("/cam-1"))
	require.Equal(t, "cam-1", cameraIDFromPath("cam-1/"))
	require.Equal(t, "cam-1", cameraIDFromPath("/cam-1/trackID=0"))
	require.Equal(t, "cam-1", cameraIDFromPath("/cam-1/streamid=0"))
	require.Equal(t, "cam/a", cameraIDFromPath("/cam/a"))
	require.Equal(t, "", cameraIDFromPath("/"))
}

func TestURLFor(t *testing.T) {
	require.Equal(t, "rtsp://192.168.1.5:8554/cam-9", URLFor("192.168.1.5:9090", 8554, "cam-9"))
	require.Equal(t, "rtsp://nas.local:8554/cam-9", URLFor("nas.local", 8554, "cam-9"))
	require.Equal(t, "rtsp://[::1]:8554/cam-9", URLFor("[::1]:9090", 8554, "cam-9"))
}
