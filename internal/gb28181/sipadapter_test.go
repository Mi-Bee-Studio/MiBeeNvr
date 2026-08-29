package gb28181

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mickeyzzc/gb28181-go/manscdp"
	"github.com/mickeyzzc/gb28181-go/platform"
	gbsip "github.com/mickeyzzc/gb28181-go/platform/sip"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

func TestSIPConfigMapsEveryField(t *testing.T) {
	subOn := true
	in := config.GB28181ServerConfig{
		Enabled:                 true,
		SIPListen:               ":5060",
		ServerID:                "34020000002000000001",
		Realm:                   "3402000000",
		Password:                "secret",
		PortRange:               "30000-30050",
		AllowedDeviceIDs:        []string{"dev1"},
		AllowSameIPEnroll:       true,
		HeartbeatInterval:       "30s",
		CatalogInterval:         "15m",
		SubChannelProbe:         "on",
		SubChannelProbeOffset:   1,
		TCPMode:                 true,
		TCPFraming:              "0x24",
		MediaTransport:          "udp",
		SIPTransport:            "tcp",
		SubscribeCatalog:        &subOn,
		SubscribeAlarm:          &subOn,
		SubscribeMobilePosition: true,
		SubscribeExpires:        "1800s",
		AlarmLinkage:            &config.GB28181AlarmLinkageConfig{Enabled: true, Duration: "45s"},
	}
	out := SIPConfig(in)
	require.Equal(t, in.Enabled, out.Enabled)
	require.Equal(t, in.SIPListen, out.SIPListen)
	require.Equal(t, in.ServerID, out.ServerID)
	require.Equal(t, in.Realm, out.Realm)
	require.Equal(t, in.Password, out.Password)
	require.Equal(t, in.PortRange, out.PortRange)
	require.Equal(t, in.AllowedDeviceIDs, out.AllowedDeviceIDs)
	require.True(t, out.AllowSameIPEnroll)
	require.Equal(t, in.HeartbeatInterval, out.HeartbeatInterval)
	require.Equal(t, in.CatalogInterval, out.CatalogInterval)
	require.Equal(t, in.SubChannelProbe, out.SubChannelProbe)
	require.Equal(t, in.SubChannelProbeOffset, out.SubChannelProbeOffset)
	require.True(t, out.TCPMode)
	require.Equal(t, in.TCPFraming, out.TCPFraming)
	require.Equal(t, in.MediaTransport, out.MediaTransport)
	require.Equal(t, in.SIPTransport, out.SIPTransport)
	require.Equal(t, in.SubscribeCatalog, out.SubscribeCatalog)
	require.Equal(t, in.SubscribeAlarm, out.SubscribeAlarm)
	require.True(t, out.SubscribeMobilePosition)
	require.Equal(t, in.SubscribeExpires, out.SubscribeExpires)
	require.NotNil(t, out.AlarmLinkage)
	require.True(t, out.AlarmLinkage.Enabled)
	require.Equal(t, "45s", out.AlarmLinkage.Duration)
	require.Equal(t, 45*time.Second, out.AlarmLinkage.AlarmLinkageDuration())

	// nil linkage stays nil (duration resolves to its default via the lib method).
	require.Nil(t, SIPConfig(config.GB28181ServerConfig{}).AlarmLinkage)
}

func TestDeviceStoreRoundTrip(t *testing.T) {
	db, err := storage.New(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	t.Cleanup(func() { _ = db.Close() })
	store := NewDeviceStore(db)
	require.NotNil(t, store)
	ctx := context.Background()

	dev := gbsip.GB28181Device{
		ID: "dev1", Name: "Cam", Manufacturer: "Hikvision", Model: "DS-2CD",
		Status: "online", LastKeepalive: time.Now(), RegisteredAt: time.Now(),
	}
	require.NoError(t, store.UpsertGB28181Device(ctx, dev))
	got, err := store.GetGB28181Device(ctx, "dev1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, dev.ID, got.ID)
	require.Equal(t, dev.Manufacturer, got.Manufacturer)
	require.Equal(t, dev.Status, got.Status)

	ch := gbsip.GB28181Channel{
		ID: "chan1", DeviceID: "dev1", Name: "ch1", Parental: 1,
		Status: "idle", UpdatedAt: time.Now(),
	}
	require.NoError(t, store.UpsertGB28181Channel(ctx, ch))
	require.NoError(t, store.BindChannelCamera(ctx, "chan1", "cam-1"))

	chans, err := store.ListGB28181Channels(ctx, "dev1")
	require.NoError(t, err)
	require.Len(t, chans, 1)
	require.Equal(t, "cam-1", chans[0].CameraID)

	devs, err := store.ListGB28181Devices(ctx)
	require.NoError(t, err)
	require.Len(t, devs, 1)

	require.NoError(t, store.MarkDeviceOffline(ctx, "dev1"))
	got2, err := store.GetGB28181Device(ctx, "dev1")
	require.NoError(t, err)
	require.Equal(t, "offline", got2.Status)

	require.NoError(t, store.DeleteGB28181Channel(ctx, "chan1"))
	require.NoError(t, store.DeleteGB28181Device(ctx, "dev1"))
	gone, err := store.GetGB28181Device(ctx, "dev1")
	require.NoError(t, err)
	require.Nil(t, gone)
}

func TestEventBridgeForwardsAlarms(t *testing.T) {
	nvr := event.NewEventBus(64)
	lib := NewEventBridge(nvr)
	require.NotNil(t, lib)

	ch := make(chan event.Event, 8)
	require.NoError(t, nvr.Subscribe(event.TopicGB28181Alarm, ch, 8))

	lib.Publish(context.Background(), gbsip.TopicGB28181Alarm, gbsip.GB28181AlarmEvent{
		DeviceID:      "dev1",
		ChannelID:     "chan1",
		AlarmPriority: "2",
		AlarmMethod:   "2",
		ReceivedAt:    time.Now(),
	})

	select {
	case ev := <-ch:
		require.Equal(t, event.TopicGB28181Alarm, ev.Topic)
		alarm, ok := ev.Data.(event.GB28181AlarmEvent)
		require.True(t, ok)
		require.Equal(t, "dev1", alarm.DeviceID)
		require.Equal(t, "chan1", alarm.ChannelID)
		require.Equal(t, "2", alarm.AlarmPriority)
	case <-time.After(5 * time.Second):
		t.Fatal("alarm event was not forwarded to the NVR bus")
	}
}

// deviceMediaShape mirrors api.GB28181DeviceMedia's method set (declared
// inline to avoid importing the api package from here). ServerAdapter must
// satisfy it — the embedded library server provides everything except
// GB28181Alarms, which the adapter re-types to the NVR event payload.
type deviceMediaShape interface {
	QueryChannelRecords(deviceID, channelID string, start, end time.Time) ([]manscdp.RecordItem, error)
	StartPlayback(deviceID, channelID string, start, end time.Time) error
	StartDownload(deviceID, channelID string, start, end time.Time) error
	StopPlayback(channelID string) error
	PlaybackStatusFor(channelID string) (platform.PlaybackInfo, bool)
	PlaybackControl(channelID, action string, scale, position float64) error
	StartTalk(cameraID, deviceID, channelID string) error
	StopTalk(channelID string) error
	WriteTalkAudio(channelID string, alaw []byte)
	TalkStatusFor(cameraID string) platform.TalkStatus
	GB28181Alarms(deviceID string) []event.GB28181AlarmEvent
	GB28181Positions(deviceID string) []platform.GBPosition
}

var _ deviceMediaShape = ServerAdapter{}
