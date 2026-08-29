package gb28181

import (
	"context"

	gbsip "github.com/mickeyzzc/gb28181-go/platform/sip"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// This file is the host-side assembly layer over gb28181-go's platform/sip
// server (batch 3 of the library migration): config mapping, the DeviceStore
// seam over storage.DB, the alarm-event bus bridge, and the small server
// adapter that re-types GB28181Alarms to the NVR event type. The SIP protocol
// logic itself lives in the library.

// SIPConfig maps the NVR YAML config onto the library's sip.Config. The
// fields are one-to-one (the library struct is a documented superset — TLS
// fields stay zero until the NVR config grows them).
func SIPConfig(cfg config.GB28181ServerConfig) gbsip.Config {
	out := gbsip.Config{
		Enabled:                 cfg.Enabled,
		SIPListen:               cfg.SIPListen,
		ServerID:                cfg.ServerID,
		Realm:                   cfg.Realm,
		Password:                cfg.Password,
		PortRange:               cfg.PortRange,
		AllowedDeviceIDs:        cfg.AllowedDeviceIDs,
		AllowSameIPEnroll:       cfg.AllowSameIPEnroll,
		HeartbeatInterval:       cfg.HeartbeatInterval,
		CatalogInterval:         cfg.CatalogInterval,
		SubChannelProbe:         cfg.SubChannelProbe,
		SubChannelProbeOffset:   cfg.SubChannelProbeOffset,
		TCPMode:                 cfg.TCPMode,
		TCPFraming:              cfg.TCPFraming,
		MediaTransport:          cfg.MediaTransport,
		SIPTransport:            cfg.SIPTransport,
		SubscribeCatalog:        cfg.SubscribeCatalog,
		SubscribeAlarm:          cfg.SubscribeAlarm,
		SubscribeMobilePosition: cfg.SubscribeMobilePosition,
		SubscribeExpires:        cfg.SubscribeExpires,
	}
	if cfg.AlarmLinkage != nil {
		out.AlarmLinkage = &gbsip.AlarmLinkageConfig{
			Enabled:  cfg.AlarmLinkage.Enabled,
			Duration: cfg.AlarmLinkage.Duration,
		}
	}
	return out
}

// NewDeviceStore adapts storage.DB to the library's sip.DeviceStore seam.
// The row structs are field-identical (the library types were extracted from
// these), so each method is a plain conversion + delegation.
func NewDeviceStore(db *storage.DB) gbsip.DeviceStore {
	if db == nil {
		return nil
	}
	return &deviceStoreAdapter{db: db}
}

type deviceStoreAdapter struct {
	db *storage.DB
}

func convDeviceIn(d gbsip.GB28181Device) storage.GB28181Device {
	return storage.GB28181Device{
		ID:            d.ID,
		Name:          d.Name,
		Manufacturer:  d.Manufacturer,
		Model:         d.Model,
		Status:        d.Status,
		LastKeepalive: d.LastKeepalive,
		RegisteredAt:  d.RegisteredAt,
	}
}

func convDeviceOut(d storage.GB28181Device) gbsip.GB28181Device {
	return gbsip.GB28181Device{
		ID:            d.ID,
		Name:          d.Name,
		Manufacturer:  d.Manufacturer,
		Model:         d.Model,
		Status:        d.Status,
		LastKeepalive: d.LastKeepalive,
		RegisteredAt:  d.RegisteredAt,
	}
}

func convChannelIn(c gbsip.GB28181Channel) storage.GB28181Channel {
	return storage.GB28181Channel{
		ID:           c.ID,
		DeviceID:     c.DeviceID,
		Name:         c.Name,
		Manufacturer: c.Manufacturer,
		Parental:     c.Parental,
		Status:       c.Status,
		CameraID:     c.CameraID,
		UpdatedAt:    c.UpdatedAt,
	}
}

func convChannelOut(c storage.GB28181Channel) gbsip.GB28181Channel {
	return gbsip.GB28181Channel{
		ID:           c.ID,
		DeviceID:     c.DeviceID,
		Name:         c.Name,
		Manufacturer: c.Manufacturer,
		Parental:     c.Parental,
		Status:       c.Status,
		CameraID:     c.CameraID,
		UpdatedAt:    c.UpdatedAt,
	}
}

func (a *deviceStoreAdapter) UpsertGB28181Device(ctx context.Context, device gbsip.GB28181Device) error {
	return a.db.UpsertGB28181Device(ctx, convDeviceIn(device))
}

func (a *deviceStoreAdapter) UpsertGB28181Channel(ctx context.Context, channel gbsip.GB28181Channel) error {
	return a.db.UpsertGB28181Channel(ctx, convChannelIn(channel))
}

func (a *deviceStoreAdapter) ListGB28181Devices(ctx context.Context) ([]gbsip.GB28181Device, error) {
	rows, err := a.db.ListGB28181Devices(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]gbsip.GB28181Device, len(rows))
	for i, r := range rows {
		out[i] = convDeviceOut(r)
	}
	return out, nil
}

func (a *deviceStoreAdapter) ListGB28181Channels(ctx context.Context, deviceID string) ([]gbsip.GB28181Channel, error) {
	rows, err := a.db.ListGB28181Channels(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	out := make([]gbsip.GB28181Channel, len(rows))
	for i, r := range rows {
		out[i] = convChannelOut(r)
	}
	return out, nil
}

func (a *deviceStoreAdapter) MarkDeviceOffline(ctx context.Context, id string) error {
	return a.db.MarkDeviceOffline(ctx, id)
}

func (a *deviceStoreAdapter) BindChannelCamera(ctx context.Context, channelID, cameraID string) error {
	return a.db.BindChannelCamera(ctx, channelID, cameraID)
}

func (a *deviceStoreAdapter) DeleteGB28181Device(ctx context.Context, id string) error {
	return a.db.DeleteGB28181Device(ctx, id)
}

func (a *deviceStoreAdapter) GetGB28181Device(ctx context.Context, id string) (*gbsip.GB28181Device, error) {
	d, err := a.db.GetGB28181Device(ctx, id)
	if err != nil {
		return nil, err
	}
	if d == nil {
		//nolint:nilnil // TODO(#604): nil,nil = "not found" is storage.DB's existing contract (api handlers depend on it); the adapter forwards it verbatim.
		return nil, nil
	}
	out := convDeviceOut(*d)
	return &out, nil
}

func (a *deviceStoreAdapter) DeleteGB28181Channel(ctx context.Context, channelID string) error {
	return a.db.DeleteGB28181Channel(ctx, channelID)
}

// NewEventBridge creates a library EventBus and forwards every alarm event to
// the NVR event bus (same topic, structurally identical payload). The
// forwarder goroutine lives for the process lifetime — same lifetime as the
// NVR bus it feeds.
func NewEventBridge(nvr *event.EventBus) *gbsip.EventBus {
	lib := gbsip.NewEventBus(64)
	if nvr == nil {
		return lib
	}
	ch := make(chan gbsip.Event, 64)
	_ = lib.Subscribe(gbsip.TopicGB28181Alarm, ch, 64)
	go func() {
		for ev := range ch {
			if alarm, ok := ev.Data.(gbsip.GB28181AlarmEvent); ok {
				nvr.Publish(context.Background(), event.TopicGB28181Alarm, event.GB28181AlarmEvent{
					CameraID:         alarm.CameraID,
					DeviceID:         alarm.DeviceID,
					ChannelID:        alarm.ChannelID,
					AlarmPriority:    alarm.AlarmPriority,
					AlarmMethod:      alarm.AlarmMethod,
					AlarmType:        alarm.AlarmType,
					AlarmTime:        alarm.AlarmTime,
					AlarmDescription: alarm.AlarmDescription,
					ReceivedAt:       alarm.ReceivedAt,
				})
			}
		}
	}()
	return lib
}

// ServerAdapter re-types the one DeviceMedia method whose payload type is
// NVR-owned (GB28181Alarms returns event.GB28181AlarmEvent for the REST ring
// and SSE consumers); everything else is promoted from the embedded library
// server unchanged.
type ServerAdapter struct {
	*gbsip.Server
}

// GB28181Alarms returns the device's recent alarms, latest first.
func (a ServerAdapter) GB28181Alarms(deviceID string) []event.GB28181AlarmEvent {
	lib := a.Server.GB28181Alarms(deviceID)
	out := make([]event.GB28181AlarmEvent, len(lib))
	for i, e := range lib {
		out[i] = event.GB28181AlarmEvent{
			CameraID:         e.CameraID,
			DeviceID:         e.DeviceID,
			ChannelID:        e.ChannelID,
			AlarmPriority:    e.AlarmPriority,
			AlarmMethod:      e.AlarmMethod,
			AlarmType:        e.AlarmType,
			AlarmTime:        e.AlarmTime,
			AlarmDescription: e.AlarmDescription,
			ReceivedAt:       e.ReceivedAt,
		}
	}
	return out
}
