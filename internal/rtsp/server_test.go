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
		c := &gortsplib.Client{
			Scheme: u.Scheme, Host: u.Host, Protocol: &tcp,
			ReadTimeout: time.Second, WriteTimeout: time.Second,
		}
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
	c := &gortsplib.Client{
		Scheme: u.Scheme, Host: u.Host, Protocol: &tcp,
		ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second,
	}
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

// feedIDRBare pushes an IDR AU WITHOUT parameter sets — mirrors recorders
// whose hub AUs carry the codec params only via CodecParams, not per-IDR
// in-band (the case that motivated param-set injection on join).
func (ts *testServer) feedIDRBare() {
	ts.hub.Broadcast(0, [][]byte{{0x26, 0x01, 0x02, 0x03}}, true)
}

// feedP pushes one synthetic non-keyframe AU through the hub.
func (ts *testServer) feedP() {
	ts.hub.Broadcast(0, [][]byte{{0x41, 0x01, 0x02, 0x03}}, false)
}

// firstNALType drains pkts until the first packet and returns the NAL type
// it carries. h264: 7=SPS 8=PPS 5=IDR 1=P, STAP-A (24) aggregates several
// NALs (first sub-NAL decides); h265: types shift by 1 bit, AP is 48.
func firstNALType(t *testing.T, pkts <-chan *rtp.Packet, h265 bool) byte {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case p := <-pkts:
			if len(p.Payload) > 0 {
				if h265 {
					typ := (p.Payload[0] >> 1) & 0x3F
					if typ == 48 && len(p.Payload) >= 4 { // AP: 2 hdr + 2 size + NAL
						return (p.Payload[4] >> 1) & 0x3F
					}
					return typ
				}
				typ := p.Payload[0] & 0x1F
				if typ == 24 && len(p.Payload) >= 4 { // STAP-A: 1 hdr + 2 size + NAL
					return p.Payload[3] & 0x1F
				}
				return typ
			}
		case <-time.After(300 * time.Millisecond):
			select {
			case <-deadline:
				t.Fatal("no RTP packet received within 5s")
			default:
			}
		}
	}
}

// Mid-GOP joins must start at the GOP head (#524 phase 2): the shared-stream
// fan-out used to write mid-GOP P-frames to freshly attached readers, whose
// decoders logged reference-missing warnings until the next IDR. Now each
// reader gets the cached GOP replayed — the first packet a late joiner
// receives belongs to the keyframe AU.
func TestRTSPMidGOPJoinStartsAtKeyframe(t *testing.T) {
	ts := startTestServer(t, Config{})

	// Reader A attaches first and sees the GOP head.
	a := dialClient(t, ts.url)
	descA, _, err := a.Describe(mustURL(t, ts.url))
	require.NoError(t, err)
	_, err = a.Setup(descA.BaseURL, descA.Medias[0], 0, 0)
	require.NoError(t, err)
	pktsA := make(chan *rtp.Packet, 32)
	a.OnPacketRTP(descA.Medias[0], descA.Medias[0].Formats[0], func(pkt *rtp.Packet) {
		select {
		case pktsA <- pkt:
		default:
		}
	})
	_, err = a.Play(nil)
	require.NoError(t, err)

	ts.feedIDR()
	for range 3 {
		ts.feedP()
	}
	// Let the GOP cache settle before the late join.
	time.Sleep(100 * time.Millisecond)

	// Reader B joins mid-GOP: its first packet must be the replayed GOP head
	// (SPS/PPS/IDR), never a bare P-frame.
	b := dialClient(t, ts.url)
	descB, _, err := b.Describe(mustURL(t, ts.url))
	require.NoError(t, err)
	_, err = b.Setup(descB.BaseURL, descB.Medias[0], 0, 0)
	require.NoError(t, err)
	pktsB := make(chan *rtp.Packet, 32)
	b.OnPacketRTP(descB.Medias[0], descB.Medias[0].Formats[0], func(pkt *rtp.Packet) {
		select {
		case pktsB <- pkt:
		default:
		}
	})
	_, err = b.Play(nil)
	require.NoError(t, err)

	// The replay rides the first post-PLAY frame — feed one to trigger it.
	ts.feedP()

	switch n := firstNALType(t, pktsB, false); n {
	case 7, 8, 5: // SPS / PPS / IDR — GOP head AU
	default:
		t.Fatalf("late joiner's first packet NAL type %d — mid-GOP P-frames leaked to a fresh reader", n)
	}

	// Existing reader A keeps receiving live frames after B joined.
	ts.feedP()
	select {
	case <-pktsA:
	case <-time.After(5 * time.Second):
		t.Fatal("reader A stopped receiving after another reader joined")
	}
}

// With no replayable GOP (reader attaches before any keyframe arrived), the
// reader stays silent until the next IDR instead of starting mid-GOP.
func TestRTSPJoinBeforeFirstIDRWaitsForKeyframe(t *testing.T) {
	ts := startTestServer(t, Config{})
	// Provider becomes ready without any frame on the hub yet.
	c := dialClient(t, ts.url)
	desc, _, err := c.Describe(mustURL(t, ts.url))
	require.NoError(t, err)
	_, err = c.Setup(desc.BaseURL, desc.Medias[0], 0, 0)
	require.NoError(t, err)
	pkts := make(chan *rtp.Packet, 32)
	c.OnPacketRTP(desc.Medias[0], desc.Medias[0].Formats[0], func(pkt *rtp.Packet) {
		select {
		case pkts <- pkt:
		default:
		}
	})
	_, err = c.Play(nil)
	require.NoError(t, err)

	ts.feedP()
	select {
	case p := <-pkts:
		t.Fatalf("received a packet before any keyframe (NAL %d)", p.Payload[0]&0x1F)
	case <-time.After(300 * time.Millisecond):
	}

	ts.feedIDR()
	if n := firstNALType(t, pkts, false); n != 7 && n != 8 && n != 5 {
		t.Fatalf("first packet after IDR is NAL %d, want SPS/PPS/IDR", n)
	}
}

// Late joiners must receive the parameter sets IN-BAND before any picture
// data even when the hub's IDR AUs don't carry them (recorder-dependent) —
// the SDP sprop is not reliably consumed by every puller.
func TestRTSPJoinInjectsParamSets(t *testing.T) {
	ts := startTestServer(t, Config{})
	ts.codec = model.FormatH265
	ts.sps = tSPS265
	ts.pps = tPPS265

	a := dialClient(t, ts.url)
	descA, _, err := a.Describe(mustURL(t, ts.url))
	require.NoError(t, err)
	_, err = a.Setup(descA.BaseURL, descA.Medias[0], 0, 0)
	require.NoError(t, err)
	pktsA := make(chan *rtp.Packet, 32)
	a.OnPacketRTP(descA.Medias[0], descA.Medias[0].Formats[0], func(pkt *rtp.Packet) {
		select {
		case pktsA <- pkt:
		default:
		}
	})
	_, err = a.Play(nil)
	require.NoError(t, err)

	// GOP whose head carries no in-band params.
	ts.hub.Broadcast(0, [][]byte{tVPS, tSPS265, tPPS265, {0x26, 0x01, 0x02}}, true) // first-ever AU seeds the encoder
	for range 3 {
		ts.feedIDRBare()
		ts.feedP()
	}
	time.Sleep(100 * time.Millisecond)

	b := dialClient(t, ts.url)
	descB, _, err := b.Describe(mustURL(t, ts.url))
	require.NoError(t, err)
	_, err = b.Setup(descB.BaseURL, descB.Medias[0], 0, 0)
	require.NoError(t, err)
	pktsB := make(chan *rtp.Packet, 64)
	b.OnPacketRTP(descB.Medias[0], descB.Medias[0].Formats[0], func(pkt *rtp.Packet) {
		select {
		case pktsB <- pkt:
		default:
		}
	})
	_, err = b.Play(nil)
	require.NoError(t, err)
	ts.feedIDRBare() // trigger the deferred replay

	switch n := firstNALType(t, pktsB, true); n {
	case 32, 33, 34, 48: // VPS/SPS/PPS or an aggregation carrying them
	default:
		t.Fatalf("late joiner's first packet NAL type %d — parameter sets not injected before picture data", n)
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
