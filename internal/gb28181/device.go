package gb28181

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Device liveness status values (Device.Status).
const (
	DeviceOffline int32 = 0
	DeviceOnline  int32 = 1
)

// DefaultHeartbeatInterval is used by NewDeviceManager when the configured
// interval is missing or non-positive (matches the config default "60s").
const DefaultHeartbeatInterval = 60 * time.Second

// Device represents a registered GB28181 device (e.g., a Hikvision NVR).
// It is created by the SIP REGISTER handler and its channels are populated
// from the device's Catalog response.
type Device struct {
	ID            string
	Name          string
	Manufacturer  string
	Model         string
	NetAddr       string // IP:port of the device's SIP stack
	Status        atomic.Int32
	channels      sync.Map     // channelID -> *Channel
	lastKeepalive atomic.Int64 // UnixNano timestamp of the last keepalive
	Mu            sync.RWMutex // guards Name/Manufacturer/Model/NetAddr
}

// DeviceManager tracks registered GB28181 devices and their channels.
//
// Concurrency: the device map is a sync.Map and per-device state uses
// atomics — reads never block, writes never hold a lock across I/O, and
// there is no per-device goroutine. The single heartbeat checker goroutine
// (started by Start) marks devices offline after 3 missed keepalives.
type DeviceManager struct {
	devices          sync.Map // deviceID -> *Device
	heartbeatTimeout time.Duration
	checkInterval    time.Duration
	stopCh           chan struct{}
	started          bool
	mu               sync.Mutex // protects Start/Stop lifecycle
	wg               sync.WaitGroup
	onOffline        func(deviceID string) // optional callback when a device is marked offline
}

// NewDeviceManager creates a manager whose heartbeat checker marks a device
// offline after 3 * heartbeatInterval without a keepalive, polling every
// heartbeatInterval/2.
func NewDeviceManager(heartbeatInterval time.Duration) *DeviceManager {
	if heartbeatInterval <= 0 {
		heartbeatInterval = DefaultHeartbeatInterval
	}
	return &DeviceManager{
		heartbeatTimeout: 3 * heartbeatInterval,
		checkInterval:    heartbeatInterval / 2,
	}
}

// SetOfflineCallback installs a callback invoked when the heartbeat checker
// marks a device offline. Used by the SIP server to sync offline status to
// the database so the REST API reflects real liveness.
func (m *DeviceManager) SetOfflineCallback(cb func(deviceID string)) {
	m.mu.Lock()
	m.onOffline = cb
	m.mu.Unlock()
}

// Start launches the heartbeat checker goroutine. It is idempotent; the
// goroutine exits on Stop or when ctx is cancelled.
func (m *DeviceManager) Start(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return
	}
	m.started = true
	m.stopCh = make(chan struct{})
	m.wg.Add(1)
	go m.checkLoop(ctx)
}

// Stop stops the heartbeat checker and waits for it to exit. Idempotent.
func (m *DeviceManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started {
		return
	}
	m.started = false
	close(m.stopCh)
	m.wg.Wait()
}

// checkLoop is the single heartbeat goroutine: one ticker for all devices.
func (m *DeviceManager) checkLoop(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(m.checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.checkHeartbeats()
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		}
	}
}

// checkHeartbeats marks every device whose last keepalive predates
// heartbeatTimeout as offline.
func (m *DeviceManager) checkHeartbeats() {
	now := time.Now()
	m.mu.Lock()
	cb := m.onOffline
	m.mu.Unlock()

	m.devices.Range(func(_, value any) bool {
		d := value.(*Device)
		if time.Unix(0, d.lastKeepalive.Load()).Add(m.heartbeatTimeout).Before(now) {
			if d.Status.CompareAndSwap(DeviceOnline, DeviceOffline) {
				slog.Info("gb28181: device went offline (heartbeat timeout)", "device", d.ID)
				if cb != nil {
					cb(d.ID)
				}
			}
		}
		return true
	})
}

// Register records d as online. A re-REGISTER for an existing ID refreshes the
// stored device's liveness in place (periodic SIP REGISTER) so its channel
// catalog survives; the caller's struct is only inserted on first registration.
func (m *DeviceManager) Register(d *Device) {
	if d == nil {
		return
	}
	actual, loaded := m.devices.LoadOrStore(d.ID, d)
	dev := actual.(*Device)
	dev.Status.Store(DeviceOnline)
	dev.lastKeepalive.Store(time.Now().UnixNano())
	if !loaded {
		// First registration: d is the stored entry.
		return
	}
	// Existing device: copy fresh metadata (a device may re-REGISTER from a
	// new address after an IP change) but keep the original channels map.
	dev.Mu.Lock()
	dev.Name = d.Name
	dev.Manufacturer = d.Manufacturer
	dev.Model = d.Model
	dev.NetAddr = d.NetAddr
	dev.Mu.Unlock()
}

// Unregister removes the device and its channels.
func (m *DeviceManager) Unregister(deviceID string) {
	m.devices.Delete(deviceID)
}

// Touch records a keepalive for deviceID and marks it online again. A no-op
// for an unregistered device.
func (m *DeviceManager) Touch(deviceID string) {
	if d, ok := m.devices.Load(deviceID); ok {
		dev := d.(*Device)
		dev.lastKeepalive.Store(time.Now().UnixNano())
		dev.Status.Store(DeviceOnline)
	}
}

// MarkOffline sets deviceID's status to offline. A no-op for an unregistered
// device.
func (m *DeviceManager) MarkOffline(deviceID string) {
	if d, ok := m.devices.Load(deviceID); ok {
		d.(*Device).Status.Store(DeviceOffline)
	}
}

// Channels returns the registered channels of deviceID, sorted by channel ID,
// or nil when the device is unknown.
func (m *DeviceManager) Channels(deviceID string) []*Channel {
	d, ok := m.devices.Load(deviceID)
	if !ok {
		return nil
	}
	out := make([]*Channel, 0, 8)
	d.(*Device).channels.Range(func(_, value any) bool {
		out = append(out, value.(*Channel))
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// FindChannel returns the channel of deviceID, or false when either is
// unknown.
func (m *DeviceManager) FindChannel(deviceID, channelID string) (*Channel, bool) {
	d, ok := m.devices.Load(deviceID)
	if !ok {
		return nil, false
	}
	c, ok := d.(*Device).channels.Load(channelID)
	if !ok {
		return nil, false
	}
	return c.(*Channel), true
}

// Device returns the registered device, or false when unknown.
func (m *DeviceManager) Device(deviceID string) (*Device, bool) {
	d, ok := m.devices.Load(deviceID)
	if !ok {
		return nil, false
	}
	return d.(*Device), true
}

// AllDevices returns all registered devices, sorted by ID.
func (m *DeviceManager) AllDevices() []*Device {
	out := make([]*Device, 0, 16)
	m.devices.Range(func(_, value any) bool {
		out = append(out, value.(*Device))
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// RegisterChannel attaches ch to the device with devID, setting the channel's
// DeviceID and back-reference. A no-op when the device is unregistered (the
// SIP server registers the device before populating its catalog).
func (m *DeviceManager) RegisterChannel(devID string, ch *Channel) {
	d, ok := m.devices.Load(devID)
	if !ok {
		return
	}
	dev := d.(*Device)
	ch.DeviceID = devID
	ch.Device = dev
	dev.channels.Store(ch.ID, ch)
}
