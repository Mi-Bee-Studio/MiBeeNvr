package gb28181

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// waitForDeviceStatus polls until deviceID reaches the wanted status or the
// deadline passes. Used instead of a fixed sleep so the heartbeat tests are
// timing-deterministic.
func waitForDeviceStatus(t *testing.T, m *DeviceManager, deviceID string, want int32) {
	t.Helper()
	require.Eventually(t, func() bool {
		d, ok := m.Device(deviceID)
		return ok && d.Status.Load() == want
	}, time.Second, 10*time.Millisecond)
}

func TestDevice_RegisterUnregister(t *testing.T) {
	m := NewDeviceManager(time.Minute)

	dev := &Device{ID: "34020000001310000001", Name: "Front Gate", NetAddr: "192.168.1.50:5060"}
	m.Register(dev)

	got, ok := m.Device(dev.ID)
	require.True(t, ok, "device should be registered")
	require.Equal(t, dev.ID, got.ID)
	require.Equal(t, DeviceOnline, got.Status.Load(), "registering marks the device online")

	m.Unregister(dev.ID)
	_, ok = m.Device(dev.ID)
	require.False(t, ok, "device should be gone after unregister")
	require.Empty(t, m.AllDevices())
}

func TestDevice_HeartbeatTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 100ms interval → 300ms timeout, 50ms check interval.
	m := NewDeviceManager(100 * time.Millisecond)
	m.Start(ctx)
	defer m.Stop()

	m.Register(&Device{ID: "34020000001310000001", Name: "Cam 1"})

	// No keepalives sent: the checker must mark the device offline within
	// ~3 missed intervals.
	waitForDeviceStatus(t, m, "34020000001310000001", DeviceOffline)

	// A keepalive restores the device online.
	m.Touch("34020000001310000001")
	waitForDeviceStatus(t, m, "34020000001310000001", DeviceOnline)
}

func TestDevice_ConcurrentRegister(t *testing.T) {
	m := NewDeviceManager(time.Minute)

	const id = "34020000001310000002"
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.Register(&Device{ID: id, Name: "Concurrent", NetAddr: "192.168.1.10:5060"})
		}()
	}
	wg.Wait()

	devs := m.AllDevices()
	require.Len(t, devs, 1, "exactly one device entry must survive concurrent registration")
	require.Equal(t, id, devs[0].ID)
	require.Equal(t, DeviceOnline, devs[0].Status.Load())
}

func TestDevice_MarkOfflineUnregistered(t *testing.T) {
	m := NewDeviceManager(time.Minute)

	// All operations on unknown IDs must be no-ops, never a panic.
	require.NotPanics(t, func() {
		m.MarkOffline("34020000001310000099")
		m.Touch("34020000001310000099")
		m.Unregister("34020000001310000099")
	})
	_, ok := m.Device("34020000001310000099")
	require.False(t, ok)
	require.Nil(t, m.Channels("34020000001310000099"))
	_, ok = m.FindChannel("34020000001310000099", "any")
	require.False(t, ok)
}

func TestDevice_RegisterChannel(t *testing.T) {
	m := NewDeviceManager(time.Minute)

	dev := &Device{ID: "34020000001310000001", Name: "Front Gate"}
	m.Register(dev)

	ch := &Channel{ID: "34020000001320000001", Name: "Channel 1", Parental: 1}
	m.RegisterChannel(dev.ID, ch)

	got, ok := m.FindChannel(dev.ID, ch.ID)
	require.True(t, ok, "channel should be found after register")
	require.Equal(t, dev.ID, got.DeviceID, "channel DeviceID must be set by RegisterChannel")
	require.Same(t, dev, got.Device, "channel back-reference must point at the device")
	require.Equal(t, ChannelIdle, got.Status.Load())

	gotChannels := m.Channels(dev.ID)
	require.Len(t, gotChannels, 1)
	require.Same(t, ch, gotChannels[0])

	// RegisterChannel on an unregistered device is a no-op, not a panic.
	require.NotPanics(t, func() {
		m.RegisterChannel("34020000001310000099", &Channel{ID: "x"})
	})
}
