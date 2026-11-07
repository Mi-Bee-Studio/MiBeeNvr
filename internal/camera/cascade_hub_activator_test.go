package camera

// Cascade main-stream activation tests (#451, Slice 2): the HubActivator
// adapter turns an upper platform's INVITE for a not-currently-recording GB
// child camera into an on-demand local INVITE through the substream
// machinery, keyed apart from the camera's sub entry. The B-plan (#1061)
// extends activation to rtsp/onvif sources; undialable protocols and
// unknown IDs refuse cleanly (the cascade answers the legacy 500).

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

type activatorPuller struct {
	mu       sync.Mutex
	invited  []string
	released []string
	onAU     func(au [][]byte, ptsTicks int64, isIDR bool)
}

func (p *activatorPuller) EnsureSubChannelRegistered(deviceID, channelID string) error {
	return nil
}

func (p *activatorPuller) InviteSubChannel(
	deviceID, channelID string, onAU func(au [][]byte, ptsTicks int64, isIDR bool),
) (func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.invited = append(p.invited, deviceID+"/"+channelID)
	p.onAU = onAU
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.released = append(p.released, deviceID+"/"+channelID)
	}, nil
}

func (p *activatorPuller) feed(au [][]byte, pts int64, idr bool) {
	p.mu.Lock()
	cb := p.onAU
	p.mu.Unlock()
	if cb != nil {
		cb(au, pts, idr)
	}
}

func activatorConfig() *config.Config {
	cfg := testConfig()
	// Retire on-demand pulls fast so the release assertions fit the test budget.
	cfg.Server.SubStream.IdleTimeoutS = 1
	cfg.Cameras = append(cfg.Cameras, config.CameraConfig{
		ID: "cam-gb", Name: "GB child", Protocol: "gb28181",
		GB28181: config.GB28181ChannelConfig{
			DeviceID:  "34020000002000000001",
			ChannelID: "34020000001320000001",
		},
	})
	cfg.Cameras = append(cfg.Cameras, config.CameraConfig{
		ID: "cam-rtsp", Name: "Local", Protocol: "rtsp",
		URL: "rtsp://127.0.0.1:8554/x",
	})
	return cfg
}

// The GB camera's MAIN channel is INVITE'd on demand; the hub carries the
// frames; release drops the reference and retires the pull after idle.
func TestCascadeHubActivatorActivatesMainChannel(t *testing.T) {
	mgr := NewCameraManager(activatorConfig(), nil, nil, "")
	puller := &activatorPuller{}
	mgr.SubStreams().SetGBPuller(puller)

	act := NewCascadeHubActivator(mgr)
	type res struct {
		hub  interface{ ConsumerCount() int }
		err  error
		stop func()
	}
	out := make(chan res, 1)
	go func() {
		hub, release, err := act.EnsureHubActive(context.Background(), "cam-gb")
		out <- res{hub: hub, err: err, stop: release}
	}()

	require.Eventually(t, func() bool {
		puller.mu.Lock()
		defer puller.mu.Unlock()
		return len(puller.invited) == 1
	}, 3*time.Second, 10*time.Millisecond, "the MAIN channel must be INVITE'd")
	puller.mu.Lock()
	invited := append([]string(nil), puller.invited...)
	puller.mu.Unlock()
	require.Equal(t, []string{"34020000002000000001/34020000001320000001"}, invited,
		"activation INVITEs the camera's main channel code")

	// Feed parameter sets so the pull turns ready and the acquire completes.
	puller.feed([][]byte{{0x67, 0x64, 0x00, 0x1e}, {0x68, 0xee, 0x3c, 0x80}, {0x65, 0x01}}, 3600, true)
	r := <-out
	require.NoError(t, r.err)
	require.NotNil(t, r.hub, "activation must return a hub carrying the frames")
	require.NotNil(t, r.stop)

	r.stop()
	require.Eventually(t, func() bool {
		puller.mu.Lock()
		defer puller.mu.Unlock()
		return len(puller.released) == 1
	}, 3*time.Second, 20*time.Millisecond, "release must retire the on-demand pull")
}

// The activation entry coexists with the camera's own sub-stream entry: the
// keyed main pull never clobbers a resolver-based sub pull.
func TestCascadeHubActivatorKeyIsolatedFromSubEntries(t *testing.T) {
	mgr := NewCameraManager(activatorConfig(), nil, nil, "")
	puller := &activatorPuller{}
	mgr.SubStreams().SetGBPuller(puller)
	act := NewCascadeHubActivator(mgr)

	require.Equal(t, "cascade-main:cam-gb", CascadeMainKey("cam-gb"))

	done := make(chan error, 1)
	go func() {
		_, release, err := act.EnsureHubActive(context.Background(), "cam-gb")
		if err == nil {
			release()
		}
		done <- err
	}()
	require.Eventually(t, func() bool {
		puller.mu.Lock()
		defer puller.mu.Unlock()
		return puller.onAU != nil
	}, 3*time.Second, 10*time.Millisecond)
	puller.feed([][]byte{{0x67, 0x64, 0x00, 0x1e}, {0x68, 0xee, 0x3c, 0x80}}, 3600, true)
	require.NoError(t, <-done)

	// The keyed entry must not appear under the bare camera ID.
	require.Nil(t, mgr.SubStreams().Hub("cam-gb"),
		"the activation entry lives under its prefixed key, not the camera ID")
}

// --- B-plan (#1061): non-GB sources activate on demand ---

// rtspFake is a minimal H.264 RTSP camera for the activation tests (same
// pattern as the substream package's test source, trimmed to one media).
type rtspFake struct {
	gs    *gortsplib.Server
	media *description.Media
	enc   interface {
		Encode(au [][]byte) ([]*rtp.Packet, error)
	}
	idr, pSl, sps, pps []byte
	stop               chan struct{}
	done               chan struct{}
	url                string
	once               sync.Once
	streamReady        chan struct{}
	stream             *gortsplib.ServerStream
}

func (s *rtspFake) OnConnOpen(*gortsplib.ServerHandlerOnConnOpenCtx) {}
func (s *rtspFake) ensureStream() {
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

func (s *rtspFake) OnDescribe(*gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	s.ensureStream()
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

func (s *rtspFake) OnSetup(*gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	s.ensureStream()
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

func (s *rtspFake) OnPlay(*gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	return &base.Response{StatusCode: base.StatusOK}, nil
}

func (s *rtspFake) writeLoop() {
	defer close(s.done)
	<-s.streamReady
	ts := uint32(1600)
	idr := true
	for {
		select {
		case <-s.stop:
			return
		default:
		}
		au := [][]byte{s.pSl}
		if idr {
			au = [][]byte{s.sps, s.pps, s.idr}
		}
		pkts, err := s.enc.Encode(au)
		if err == nil {
			for _, p := range pkts {
				p.Timestamp = ts
				_ = s.stream.WritePacketRTP(s.media, p)
			}
		}
		ts += 3600
		idr = !idr
		time.Sleep(20 * time.Millisecond)
	}
}

func startRTSPFake(t *testing.T) *rtspFake {
	t.Helper()
	f := &format.H264{PayloadTyp: 96, SPS: []byte{0x67, 0x64, 0x00, 0x1e}, PPS: []byte{0x68, 0xee, 0x3c, 0x80}, PacketizationMode: 1}
	enc, err := f.CreateEncoder()
	require.NoError(t, err)
	s := &rtspFake{
		enc:         enc,
		idr:         []byte{0x65, 0x01},
		pSl:         []byte{0x41, 0x02},
		sps:         []byte{0x67, 0x64, 0x00, 0x1e},
		pps:         []byte{0x68, 0xee, 0x3c, 0x80},
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
		streamReady: make(chan struct{}),
	}
	s.media = &description.Media{Type: description.MediaTypeVideo, Control: "trackID=0", Formats: []format.Format{f}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s.url = "rtsp://" + ln.Addr().String() + "/cam"
	s.gs = &gortsplib.Server{Handler: s, RTSPAddress: "rtsp://" + ln.Addr().String()}
	s.gs.Listen = func(_, _ string) (net.Listener, error) { return ln, nil }
	go func() { _ = s.gs.Start() }()
	go s.writeLoop()
	t.Cleanup(func() {
		close(s.stop)
		<-s.done
		s.gs.Close()
	})
	return s
}

// An upper platform's INVITE for a not-recording rtsp camera dials the
// camera's main URL on demand through the substream machinery; BYE (release)
// retires the pull after the idle timeout (#1061 acceptance).
func TestCascadeHubActivatorActivatesRTSPMain(t *testing.T) {
	fake := startRTSPFake(t)
	cfg := activatorConfig()
	for i := range cfg.Cameras {
		if cfg.Cameras[i].ID == "cam-rtsp" {
			cfg.Cameras[i].URL = fake.url
		}
	}
	mgr := NewCameraManager(cfg, nil, nil, "")
	act := NewCascadeHubActivator(mgr)

	type res struct {
		hub  interface{ ConsumerCount() int }
		err  error
		stop func()
	}
	out := make(chan res, 1)
	go func() {
		hub, release, err := act.EnsureHubActive(context.Background(), "cam-rtsp")
		out <- res{hub: hub, err: err, stop: release}
	}()

	var r res
	require.Eventually(t, func() bool {
		select {
		case r = <-out:
			return true
		default:
			return false
		}
	}, 8*time.Second, 20*time.Millisecond, "the rtsp main pull must become ready")
	require.NoError(t, r.err)
	require.NotNil(t, r.hub, "activation must return a hub carrying the camera frames")
	require.NotNil(t, r.stop)

	// The keyed entry retires after release + idle timeout.
	r.stop()
	require.Eventually(t, func() bool {
		return mgr.SubStreams().Hub(CascadeMainKey("cam-rtsp")) == nil
	}, 4*time.Second, 50*time.Millisecond, "release must retire the on-demand rtsp pull")
}

// An unreachable camera fails the activation within the caller's deadline —
// the cascade answers a bounded 500 and never establishes a medialess dialog
// (#1061 acceptance: 拉流失败限时回 500).
func TestCascadeHubActivatorRTSPFailureIsBounded(t *testing.T) {
	cfg := activatorConfig()
	for i := range cfg.Cameras {
		if cfg.Cameras[i].ID == "cam-rtsp" {
			cfg.Cameras[i].URL = "rtsp://127.0.0.1:1/dead" // port 1: nothing listens
		}
	}
	mgr := NewCameraManager(cfg, nil, nil, "")
	act := NewCascadeHubActivator(mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, release, err := act.EnsureHubActive(ctx, "cam-rtsp")
	require.Error(t, err)
	require.Nil(t, release)
	require.Less(t, time.Since(start), 3*time.Second, "failure must honor the caller deadline")
}

// An onvif camera without a main profile token and without a direct rtsp URL
// refuses with a clear error instead of dialing garbage.
func TestCascadeHubActivatorONVIFWithoutTargetRefuses(t *testing.T) {
	cfg := activatorConfig()
	cfg.Cameras = append(cfg.Cameras, config.CameraConfig{
		ID: "cam-onvif-notoken", Name: "ONVIF no token", Protocol: "onvif",
		ONVIFEndpoint: "http://127.0.0.1:1/onvif",
	})
	mgr := NewCameraManager(cfg, nil, nil, "")
	act := NewCascadeHubActivator(mgr)

	_, release, err := act.EnsureHubActive(context.Background(), "cam-onvif-notoken")
	require.Error(t, err)
	require.Nil(t, release)
}

// Protocols without a dialable main stream (push ingest, xiaomi) still refuse
// — their hubs live with their recorders and the cascade answers 500.
func TestCascadeHubActivatorRefusesUndialableProtocols(t *testing.T) {
	cfg := activatorConfig()
	cfg.Cameras = append(cfg.Cameras, config.CameraConfig{
		ID: "cam-xiaomi", Name: "Xiaomi", Protocol: "xiaomi",
	})
	mgr := NewCameraManager(cfg, nil, nil, "")
	act := NewCascadeHubActivator(mgr)

	_, release, err := act.EnsureHubActive(context.Background(), "cam-xiaomi")
	require.Error(t, err)
	require.Nil(t, release)
}
