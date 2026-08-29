package camera

// PTZ-forward dispatch and misc guard-path tests (#568). All deterministic:
// nil-manager / unknown-camera / missing-controller branches, GB28181 send
// hooking, pointer helpers, and the cascade source adapter.

import (
	"context"
	"errors"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

func TestForwardPTZ_Guards(t *testing.T) {
	ctx := context.Background()

	require.Error(t, ForwardPTZ(ctx, nil, nil, "cam", "up", 10), "nil manager")

	mgr, _, _, _ := newTestManager(t)
	require.Error(t, ForwardPTZ(ctx, mgr, nil, "no-such", "up", 10), "unknown camera")
	require.Error(t, ForwardPTZ(ctx, mgr, nil, "cam-h264", "up", 10),
		"rtsp cameras have no PTZ support")
}

func TestForwardPTZ_GB28181(t *testing.T) {
	cfg := testConfig()
	cfg.Cameras = append(cfg.Cameras, config.CameraConfig{
		ID: "cam-gb", Name: "GB", Protocol: "gb28181", Encoding: "h264",
		GB28181: config.GB28181ChannelConfig{ChannelID: "34020000001320000011"},
	})
	mgr, _, _, _ := newTestManagerWithCfg(t, cfg)
	ctx := context.Background()

	// Nil controller → explicit error.
	require.Error(t, ForwardPTZ(ctx, mgr, nil, "cam-gb", "up", 10))

	sent := ""
	err := ForwardPTZ(ctx, mgr, func(channelID, direction string, speed byte) error {
		sent = channelID + "/" + direction
		return nil
	}, "cam-gb", "up", 10)
	require.NoError(t, err)
	require.Equal(t, "34020000001320000011/up", sent)

	// Controller errors propagate.
	boom := errors.New("boom")
	require.ErrorIs(t, ForwardPTZ(ctx, mgr, func(string, string, byte) error { return boom },
		"cam-gb", "up", 10), boom)

	// A GB camera without a channel binding refuses cleanly.
	cfg2 := testConfig()
	cfg2.Cameras = append(cfg2.Cameras, config.CameraConfig{
		ID: "cam-gb-nobind", Name: "GB", Protocol: "gb28181", Encoding: "h264",
	})
	mgr2, _, _, _ := newTestManagerWithCfg(t, cfg2)
	require.Error(t, ForwardPTZ(ctx, mgr2, func(string, string, byte) error { return nil },
		"cam-gb-nobind", "up", 10))
}

func TestPtrHelpers(t *testing.T) {
	s := "x"
	require.Equal(t, "x", strPtrOrEmpty(&s))
	require.Equal(t, "", strPtrOrEmpty(nil))
	i := 7
	require.Equal(t, 7, intPtrOrZero(&i))
	require.Equal(t, 0, intPtrOrZero(nil))
}

func TestCascadeSourceHub(t *testing.T) {
	mgr, _, db, _ := newTestManager(t)
	src := NewCascadeSource(mgr, db)
	require.Nil(t, src.Hub("no-such"))
	mgr.GetOrCreateHub("cam-h264")
	h1 := src.Hub("cam-h264")
	require.NotNil(t, h1)
	// The cascade gets a process-lifetime FrameHub mirror (not the raw NVR
	// stream hub); repeated calls return the SAME mirror via the bridge cache.
	require.Same(t, h1, src.Hub("cam-h264"))
}

func TestSetHealthManager(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	mgr.SetHealthManager(nil) // must not panic; health wiring is optional
}

func TestStartCamera_Unknown(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	err := mgr.StartCamera(context.Background(), "no-such")
	require.Error(t, err)
	var notFound *model.CameraNotFoundError
	require.ErrorAs(t, err, &notFound)
}
