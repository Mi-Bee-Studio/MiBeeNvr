package camera

import (
	"context"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"

	"github.com/stretchr/testify/require"
)

// A camera created with motion_source camera:onvif must get its event
// subscription reconciled right away — not wait for the first update or the
// next boot (#922 joint-debug finding).
func TestAddCameraReconcilesMotionSubscription(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cm := NewCameraManager(cfg, nil, nil, "")

	sub := &onvif.MockEventSubscriber{}
	cm.SetEventSubscriberFactory(func(ctx context.Context, cameraID string, cb onvif.EventCallback) (onvif.EventSubscriber, error) {
		return sub, nil
	})

	_, err := cm.AddCamera(context.Background(), config.CameraConfig{
		ID: "cam-new", Name: "新增", Protocol: "onvif", URL: "http://192.0.2.10/onvif/device_service",
		MotionSource: config.MotionSourceCameraONVIF,
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool { return sub.SubscribeCallCount() == 1 },
		5e9, 100e6, "create path must reconcile the motion subscription")
}
