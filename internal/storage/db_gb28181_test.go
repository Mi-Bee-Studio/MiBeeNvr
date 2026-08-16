package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newGB28181DB is the shared setup for GB28181 DAO tests: an initialized
// SQLite DB in a temp dir, closed on cleanup.
func newGB28181DB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_gb28181.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestDB_GB28181_DeviceCRUD covers the device lifecycle: upsert (insert +
// replace), list, and mark-offline.
func TestDB_GB28181_DeviceCRUD(t *testing.T) {
	db := newGB28181DB(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 12, 10, 30, 0, 123456789, time.UTC)

	dev := GB28181Device{
		ID:            "34020000001320000001",
		Name:          "Front NVR",
		Manufacturer:  "Hikvision",
		Model:         "DS-7608",
		Status:        "online",
		LastKeepalive: now,
		RegisteredAt:  now,
	}
	require.NoError(t, db.UpsertGB28181Device(ctx, dev))

	devs, err := db.ListGB28181Devices(ctx)
	require.NoError(t, err)
	require.Len(t, devs, 1)
	require.Equal(t, dev.ID, devs[0].ID)
	require.Equal(t, "Front NVR", devs[0].Name)
	require.Equal(t, "Hikvision", devs[0].Manufacturer)
	require.Equal(t, "DS-7608", devs[0].Model)
	require.Equal(t, "online", devs[0].Status)
	require.True(t, devs[0].LastKeepalive.Equal(now), "last_keepalive round-trips as UTC")
	require.True(t, devs[0].RegisteredAt.Equal(now), "registered_at round-trips as UTC")

	// Re-upsert (INSERT OR REPLACE) with changed status — still one row.
	dev.Status = "offline"
	require.NoError(t, db.UpsertGB28181Device(ctx, dev))
	devs, err = db.ListGB28181Devices(ctx)
	require.NoError(t, err)
	require.Len(t, devs, 1)
	require.Equal(t, "offline", devs[0].Status)

	// MarkDeviceOffline flips status back to online→offline.
	dev.Status = "online"
	require.NoError(t, db.UpsertGB28181Device(ctx, dev))
	require.NoError(t, db.MarkDeviceOffline(ctx, dev.ID))
	devs, err = db.ListGB28181Devices(ctx)
	require.NoError(t, err)
	require.Len(t, devs, 1)
	require.Equal(t, "offline", devs[0].Status)

	// MarkDeviceOffline on an unknown ID is a silent no-op.
	require.NoError(t, db.MarkDeviceOffline(ctx, "never-registered"))
}

// TestDB_GB28181_ChannelCRUD covers channel upsert, per-device listing, and
// camera binding.
func TestDB_GB28181_ChannelCRUD(t *testing.T) {
	db := newGB28181DB(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 12, 11, 0, 0, 987654321, time.UTC)

	const devID = "34020000001320000001"
	ch := GB28181Channel{
		ID:           "34020000001320000001",
		DeviceID:     devID,
		Name:         "Front Door",
		Manufacturer: "Hikvision",
		Parental:     0,
		Status:       "idle",
		UpdatedAt:    now,
	}
	require.NoError(t, db.UpsertGB28181Channel(ctx, ch))

	// Second channel on the same device.
	ch2 := ch
	ch2.ID = "34020000001320000002"
	ch2.Name = "Back Yard"
	require.NoError(t, db.UpsertGB28181Channel(ctx, ch2))

	// Listing is scoped per device.
	chs, err := db.ListGB28181Channels(ctx, devID)
	require.NoError(t, err)
	require.Len(t, chs, 2)
	require.Equal(t, "34020000001320000001", chs[0].ID)
	require.Equal(t, "Front Door", chs[0].Name)
	require.Equal(t, 0, chs[0].Parental)
	require.Equal(t, "idle", chs[0].Status)
	require.Equal(t, "", chs[0].CameraID, "unbound channel has empty camera_id")
	require.True(t, chs[0].UpdatedAt.Equal(now), "updated_at round-trips as UTC")

	// A different device sees none of these channels.
	other, err := db.ListGB28181Channels(ctx, "34020000009999999999")
	require.NoError(t, err)
	require.Empty(t, other)

	// Bind a camera to one channel.
	require.NoError(t, db.BindChannelCamera(ctx, ch.ID, "front-door"))
	chs, err = db.ListGB28181Channels(ctx, devID)
	require.NoError(t, err)
	require.Len(t, chs, 2)
	require.Equal(t, "front-door", chs[0].CameraID)
	require.Equal(t, "", chs[1].CameraID, "other channel stays unbound")

	// BindChannelCamera on an unknown channel is a silent no-op.
	require.NoError(t, db.BindChannelCamera(ctx, "never-existed", "cam-x"))

	// Re-upsert (INSERT OR REPLACE) with changed status — still two rows.
	ch.Status = "playing"
	require.NoError(t, db.UpsertGB28181Channel(ctx, ch))
	chs, err = db.ListGB28181Channels(ctx, devID)
	require.NoError(t, err)
	require.Len(t, chs, 2)
	require.Equal(t, "playing", chs[0].Status)
}

// TestDB_GB28181_DeleteDeviceCascades verifies that deleting a device also
// removes its channel rows (explicit cascade — no FK constraints).
func TestDB_GB28181_DeleteDeviceCascades(t *testing.T) {
	db := newGB28181DB(t)
	ctx := context.Background()

	const devID = "34020000001320000001"
	require.NoError(t, db.UpsertGB28181Device(ctx, GB28181Device{ID: devID, Name: "NVR", Status: "online"}))
	require.NoError(t, db.UpsertGB28181Channel(ctx, GB28181Channel{ID: devID + "01", DeviceID: devID, Name: "Ch1"}))
	require.NoError(t, db.UpsertGB28181Channel(ctx, GB28181Channel{ID: devID + "02", DeviceID: devID, Name: "Ch2"}))

	require.NoError(t, db.DeleteGB28181Device(ctx, devID))

	devs, err := db.ListGB28181Devices(ctx)
	require.NoError(t, err)
	require.Empty(t, devs, "device row removed")

	chs, err := db.ListGB28181Channels(ctx, devID)
	require.NoError(t, err)
	require.Empty(t, chs, "channels cascade-deleted with the device")

	// Deleting an unknown device is a silent no-op.
	require.NoError(t, db.DeleteGB28181Device(ctx, "never-registered"))
}

func TestGB28181FingerprintRoundtrip(t *testing.T) {
	db := newGB28181DB(t)
	ctx := context.Background()

	fp, err := db.GetGB28181Fingerprint(ctx, "34020000001310000001")
	require.NoError(t, err)
	require.Nil(t, fp, "no fingerprint yet")

	now := time.Now()
	require.NoError(t, db.UpsertGB28181Fingerprint(ctx, GB28181Fingerprint{
		DeviceID: "34020000001310000001", Serial: "NC00000001", SourceIP: "192.168.63.152", ProbedAt: now,
	}))

	fp, err = db.GetGB28181Fingerprint(ctx, "34020000001310000001")
	require.NoError(t, err)
	require.NotNil(t, fp)
	require.Equal(t, "NC00000001", fp.Serial)
	require.Equal(t, "192.168.63.152", fp.SourceIP)

	// Upsert updates, list sees one row.
	require.NoError(t, db.UpsertGB28181Fingerprint(ctx, GB28181Fingerprint{
		DeviceID: "34020000001310000001", Serial: "NC00000002", SourceIP: "192.168.63.40", ProbedAt: now,
	}))
	fps, err := db.ListGB28181Fingerprints(ctx)
	require.NoError(t, err)
	require.Len(t, fps, 1)
	require.Equal(t, "NC00000002", fps[0].Serial)
}
