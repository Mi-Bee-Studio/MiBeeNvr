package cascade

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

type fakeSource struct {
	cams []CameraInfo
}

func (f fakeSource) Cameras() []CameraInfo       { return f.cams }
func (f fakeSource) Hub(string) *model.StreamHub { return nil }

func newCascadeTestDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.New(filepath.Join(t.TempDir(), "cascade.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func testCfg() config.GB28181CascadeConfig {
	return config.GB28181CascadeConfig{
		Enabled:       true,
		ServerDomain:  "34020000002000000001",
		ServerAddr:    "127.0.0.1:5060",
		LocalDeviceID: "34020000001320000099",
	}
}

func TestCatalogItemsAllocatesAndPersists(t *testing.T) {
	db := newCascadeTestDB(t)
	svc := New(testCfg(), fakeSource{cams: []CameraInfo{
		{ID: "front", Name: "Front"},
		{ID: "back", Name: "Back"},
	}}, db)

	items, err := svc.catalogItems()
	require.NoError(t, err)
	require.Len(t, items, 2)
	// <LocalDeviceID[:10]>132 + 7-digit serial, allocation order.
	require.Equal(t, "34020000001320000001", items[0].DeviceID)
	require.Equal(t, "34020000001320000002", items[1].DeviceID)
	require.Equal(t, "ON", items[0].Status)
	require.Zero(t, items[0].Parental)

	// Allocation is stable across "restarts" (fresh service, same DB) and
	// camera order changes.
	svc2 := New(testCfg(), fakeSource{cams: []CameraInfo{
		{ID: "back", Name: "Back"},
		{ID: "front", Name: "Front"},
	}}, db)
	items2, err := svc2.catalogItems()
	require.NoError(t, err)
	require.Equal(t, "34020000001320000002", items2[0].DeviceID, "back keeps its channel")
	require.Equal(t, "34020000001320000001", items2[1].DeviceID, "front keeps its channel")

	// New camera gets the next serial, never a reuse.
	svc3 := New(testCfg(), fakeSource{cams: []CameraInfo{
		{ID: "front", Name: "Front"}, {ID: "back", Name: "Back"}, {ID: "gate", Name: "Gate"},
	}}, db)
	items3, err := svc3.catalogItems()
	require.NoError(t, err)
	require.Equal(t, "34020000001320000003", items3[2].DeviceID)
}

func TestCameraOfChannel(t *testing.T) {
	db := newCascadeTestDB(t)
	svc := New(testCfg(), fakeSource{cams: []CameraInfo{{ID: "front", Name: "Front"}}}, db)
	_, err := svc.catalogItems()
	require.NoError(t, err)

	cam, ok := svc.cameraOfChannel("34020000001320000001")
	require.True(t, ok)
	require.Equal(t, "front", cam)

	_, ok = svc.cameraOfChannel("34020000001999999999")
	require.False(t, ok)
}

func TestSDPFromInvite(t *testing.T) {
	host, port, ssrc, err := sdpFromInvite([]byte(
		"v=0\r\no=- 0 0 IN IP4 10.0.0.1\r\ns=Play\r\nc=IN IP4 10.0.0.1\r\nt=0 0\r\n" +
			"m=video 30010 RTP/AVP 96\r\na=recvonly\r\na=rtpmap:96 PS/90000\r\ny=0200000031\r\n"))
	require.NoError(t, err)
	require.Equal(t, "10.0.0.1", host)
	require.Equal(t, 30010, port)
	require.EqualValues(t, 200000031, ssrc, "y= is decimal per GB28181 Annex C")

	_, _, _, err = sdpFromInvite([]byte("v=0\r\nm=audio 8 RTP/AVP 8\r\n"))
	require.Error(t, err, "no c=/m=video must fail")
}

func TestSniffCodecAndIDR(t *testing.T) {
	require.Equal(t, "h264", sniffCodec([]byte{0x67, 0x42})) // SPS
	require.Equal(t, "h264", sniffCodec([]byte{0x65, 0x88})) // IDR
	require.Equal(t, "h264", sniffCodec([]byte{0x41, 0x9A})) // non-IDR
	require.Equal(t, "h265", sniffCodec([]byte{0x40, 0x01})) // H.265 VPS lead
	require.Equal(t, "h265", sniffCodec([]byte{0x42, 0x01})) // H.265 SPS lead
	require.Equal(t, "h264", sniffCodec([]byte{0x41, 0x9A})) // ambiguous → h264

	require.True(t, auIsIDR([][]byte{{0x67, 0x42}, {0x65, 0x88}}, "h264"), "H.264 SPS+IDR")
	require.True(t, auIsIDR([][]byte{{0x40, 0x01}}, "h265"), "H.265 VPS")
	require.False(t, auIsIDR([][]byte{{0x41, 0x9A}}, "h264"), "P-frame only")
}

// TestDecodePTZCmd covers the hex → direction/speed decode used to bridge
// upper-platform DeviceControl commands onto local cameras.
func TestDecodePTZCmd(t *testing.T) {
	dir, speed, err := decodePTZCmd("A50F010800000067")
	require.NoError(t, err)
	require.Equal(t, "up", dir)
	require.Equal(t, byte(0), speed)

	dir, speed, err = decodePTZCmd("A50F010132000069")
	require.NoError(t, err)
	require.Equal(t, "right", dir)
	require.Equal(t, byte(0x32), speed)

	dir, _, err = decodePTZCmd("A50F010A20000079")
	require.NoError(t, err)
	require.Equal(t, "up-left", dir)

	dir, _, err = decodePTZCmd("A50F0110000000C5")
	require.NoError(t, err)
	require.Equal(t, "zoom-in", dir)

	dir, _, err = decodePTZCmd("A50F0120000000E5")
	require.NoError(t, err)
	require.Equal(t, "zoom-out", dir)

	dir, _, err = decodePTZCmd("A50F0100000000B5")
	require.NoError(t, err)
	require.Equal(t, "stop", dir)

	_, _, err = decodePTZCmd("nothex")
	require.Error(t, err)
	_, _, err = decodePTZCmd("1234")
	require.Error(t, err)
}
