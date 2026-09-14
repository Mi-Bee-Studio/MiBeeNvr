package camera

// Cascade main-stream activation tests (#451, Slice 2): the HubActivator
// adapter turns an upper platform's INVITE for a not-currently-recording GB
// child camera into an on-demand local INVITE through the substream
// machinery, keyed apart from the camera's sub entry. Non-GB cameras and
// unknown IDs refuse cleanly (the cascade answers the legacy 500).

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
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

// Non-GB cameras and unknown IDs refuse — their hubs live with their
// recorders; the cascade answers the legacy 500.
func TestCascadeHubActivatorRefusesNonGB(t *testing.T) {
	mgr := NewCameraManager(activatorConfig(), nil, nil, "")
	mgr.SubStreams().SetGBPuller(&activatorPuller{})
	act := NewCascadeHubActivator(mgr)

	_, release, err := act.EnsureHubActive(context.Background(), "cam-rtsp")
	require.Error(t, err)
	require.Nil(t, release)

	_, release, err = act.EnsureHubActive(context.Background(), "no-such-camera")
	require.Error(t, err)
	require.Nil(t, release)
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
