package relay

// RTMP publish loopback tests (#567): a scripted fake RTMP server exercises
// the custom handshake + FMLE publish sequence (dialRTMPPublish) and the full
// engine path (connectRTMP → hub frames → RTMP video messages on the wire).
// Hermetic: everything on 127.0.0.1, no real platform involved.

import (
	"context"
	"crypto/rand"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/bluenviron/gortmplib/pkg/amf0"
	"github.com/bluenviron/gortmplib/pkg/bytecounter"
	"github.com/bluenviron/gortmplib/pkg/message"
	"github.com/stretchr/testify/require"
)

var (
	rlSPS = []byte{
		0x67, 0x64, 0x00, 0x1f, 0xac, 0xd9, 0x40, 0x50, 0x05, 0xbb, 0x01,
		0x10, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00,
		0x00, 0x03, 0x00, 0x7b, 0xac, 0x09,
	}
	rlPPS = []byte{0x68, 0xee, 0x3c, 0x80}
	rlIDR = []byte{0x65, 0x88, 0x84, 0x00, 0x40, 0xff, 0xfe, 0xf8, 0xc0}
)

// fakeRTMPServer is a minimal scripted RTMP publish receiver: plain
// handshake, _result answers for connect/createStream, NetStream.Publish.Start
// for publish, and counting of subsequent video messages.
type fakeRTMPServer struct {
	listener    net.Listener
	published   chan string // stream key from the publish command
	videoMsgs   atomic.Int64
	metaData    atomic.Bool
	gotSetChunk atomic.Bool
}

func startFakeRTMPServer(t *testing.T) *fakeRTMPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &fakeRTMPServer{listener: ln, published: make(chan string, 1)}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeRTMPServer) addr() string { return s.listener.Addr().String() }

func (s *fakeRTMPServer) serve() {
	conn, err := s.listener.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	// Plain handshake: C0+C1 in, S0+S1+S2 out (S1 without digest), C2 in.
	head := make([]byte, 1+1536)
	if _, err := io.ReadFull(conn, head); err != nil {
		return
	}
	s1 := make([]byte, 1536)
	_, _ = rand.Read(s1)
	resp := make([]byte, 0, 1+1536+1536)
	resp = append(resp, 0x03)
	resp = append(resp, s1...)
	resp = append(resp, head[1:]...) // S2 = echo of C1
	if _, err := conn.Write(resp); err != nil {
		return
	}
	if _, err := io.ReadFull(conn, make([]byte, 1536)); err != nil { // C2
		return
	}

	bc := bytecounter.NewReadWriter(conn)
	mrw := message.NewReadWriter(bc, bc, false)
	for {
		msg, err := mrw.Read()
		if err != nil {
			return
		}
		switch m := msg.(type) {
		case *message.SetChunkSize:
			s.gotSetChunk.Store(true)
		case *message.DataAMF0:
			s.metaData.Store(true)
		case *message.Video:
			s.videoMsgs.Add(1)
		case *message.CommandAMF0:
			switch m.Name {
			case "connect":
				_ = mrw.Write(&message.SetWindowAckSize{Value: 2500000})
				_ = mrw.Write(&message.CommandAMF0{
					ChunkStreamID: 3,
					Name:          "_result",
					CommandID:     1,
					Arguments: []any{
						amf0.Object{{Key: "fmsVer", Value: "FMS/3,5,3,824"}},
						amf0.Object{{Key: "level", Value: "status"}},
					},
				})
			case "createStream":
				_ = mrw.Write(&message.CommandAMF0{
					ChunkStreamID: 3,
					Name:          "_result",
					CommandID:     4,
					Arguments:     []any{nil, float64(1)},
				})
			case "publish":
				key := ""
				if len(m.Arguments) > 1 {
					if ks, ok := m.Arguments[1].(string); ok {
						key = ks
					}
				}
				select {
				case s.published <- key:
				default:
				}
				_ = mrw.Write(&message.CommandAMF0{
					ChunkStreamID:   5,
					MessageStreamID: 0x1000000,
					Name:            "onStatus",
					CommandID:       0,
					Arguments: []any{nil, amf0.Object{
						{Key: "level", Value: "status"},
						{Key: "code", Value: "NetStream.Publish.Start"},
					}},
				})
			}
		}
	}
}

func TestDialRTMPPublishLoopback(t *testing.T) {
	srv := startFakeRTMPServer(t)

	conn, cleanup, err := dialRTMPPublish(context.Background(),
		"rtmp://"+srv.addr()+"/live/stream-key", 640, 360, 25)
	require.NoError(t, err)
	defer cleanup()

	select {
	case key := <-srv.published:
		require.Equal(t, "stream-key", key)
	case <-time.After(5 * time.Second):
		t.Fatal("fake server never saw the publish command")
	}
	require.True(t, srv.gotSetChunk.Load(), "client must send SetChunkSize=4096 before connect")
	require.Greater(t, conn.BytesSent(), uint64(0))
	// Note: do NOT call conn.Read() here — dialRTMPPublish already runs its
	// own background reader; a second reader would race on the bufio state.
	cleanup()
}

// The full engine path: connectRTMP subscribes the hub and forwards hub AUs
// as RTMP video messages the fake server can count.
func TestConnectRTMPFullFlow(t *testing.T) {
	srv := startFakeRTMPServer(t)
	hub := newHubForRelay()

	target := NewPushTarget("cam1", PushTargetConfig{
		ID: "rtmp-e2e", Protocol: "rtmp",
		URL:             "rtmp://" + srv.addr() + "/live/key",
		TranscodePolicy: "off",
	}, hub, func() ([]byte, []byte, bool) {
		return append([]byte(nil), rlSPS...), append([]byte(nil), rlPPS...), true
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- target.connectAndStream(ctx) }()

	select {
	case key := <-srv.published:
		require.Equal(t, "key", key)
	case <-time.After(5 * time.Second):
		t.Fatal("publish never reached the server")
	}

	// Push frames until the (asynchronously subscribed) target forwards one.
	au := [][]byte{append([]byte(nil), rlSPS...), append([]byte(nil), rlPPS...), append([]byte(nil), rlIDR...)}
	require.Eventually(t, func() bool {
		hub.Broadcast(time.Now().UnixNano()/1e6, au, true)
		return srv.videoMsgs.Load() >= 1 && srv.metaData.Load()
	}, 5*time.Second, 50*time.Millisecond, "hub AUs must arrive as RTMP video messages")

	require.Eventually(t, func() bool {
		return target.Status().Status == StatusStreaming
	}, 5*time.Second, 50*time.Millisecond, "target must report streaming")

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("connectAndStream did not return after cancel")
	}
}

func newHubForRelay() *streamhub.StreamHub {
	return streamhub.New()
}
