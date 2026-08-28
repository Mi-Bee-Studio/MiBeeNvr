package cascade

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/psmux"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/mickeyzzc/gb28181-go/manscdp"
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

// TestCatalogHiddenCamerasExcluded verifies catalog convergence: cameras with
// CascadeHidden are absent from the aggregated catalog, while their persisted
// channel allocation is kept — re-enabling restores the same channel code.
func TestCatalogHiddenCamerasExcluded(t *testing.T) {
	db := newCascadeTestDB(t)
	svc := New(testCfg(), fakeSource{cams: []CameraInfo{
		{ID: "front", Name: "Front"},
		{ID: "back", Name: "Back"},
	}}, db)

	// First pass: both cameras visible, channels allocated.
	items, err := svc.catalogItems()
	require.NoError(t, err)
	require.Len(t, items, 2)

	// Hide "back": it disappears from the catalog...
	svcHidden := New(testCfg(), fakeSource{cams: []CameraInfo{
		{ID: "front", Name: "Front"},
		{ID: "back", Name: "Back", CascadeHidden: true},
	}}, db)
	itemsHidden, err := svcHidden.catalogItems()
	require.NoError(t, err)
	require.Len(t, itemsHidden, 1)
	require.Equal(t, "34020000001320000001", itemsHidden[0].DeviceID, "front keeps its channel")

	// ...and re-enabling restores the SAME channel (allocation persisted).
	svcAgain := New(testCfg(), fakeSource{cams: []CameraInfo{
		{ID: "front", Name: "Front"},
		{ID: "back", Name: "Back"},
	}}, db)
	itemsAgain, err := svcAgain.catalogItems()
	require.NoError(t, err)
	require.Len(t, itemsAgain, 2)
	require.Equal(t, "34020000001320000002", itemsAgain[1].DeviceID, "back's channel is stable across hide/show")

	// The hidden camera's channel still resolves to its camera — the INVITE
	// gate must refuse it with 404 rather than silently forwarding.
	camID, ok := svcHidden.cameraOfChannel("34020000001320000002")
	require.True(t, ok)
	require.Equal(t, "back", camID)
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
	sd, err := sdpFromInvite([]byte(
		"v=0\r\no=- 0 0 IN IP4 10.0.0.1\r\ns=Play\r\nc=IN IP4 10.0.0.1\r\nt=0 0\r\n" +
			"m=video 30010 RTP/AVP 96\r\na=recvonly\r\na=rtpmap:96 PS/90000\r\ny=0200000031\r\n"))
	require.NoError(t, err)
	require.Equal(t, "10.0.0.1", sd.host)
	require.Equal(t, 30010, sd.port)
	require.Equal(t, "Play", sd.name)
	require.EqualValues(t, 200000031, sd.ssrc, "y= is decimal per GB28181 Annex C")
	require.False(t, sd.hasT, "t=0 0 is not a playback range")

	// Playback form: NTP-era t= seconds normalize to Unix.
	sd, err = sdpFromInvite([]byte(
		"v=0\r\no=- 0 0 IN IP4 10.0.0.2\r\ns=Playback\r\nc=IN IP4 10.0.0.2\r\nt=3970000000 3970000120\r\n" +
			"m=video 30012 RTP/AVP 96\r\na=recvonly\r\na=rtpmap:96 PS/90000\r\ny=1000000042\r\n"))
	require.NoError(t, err)
	require.Equal(t, "Playback", sd.name)
	require.True(t, sd.hasT)
	require.EqualValues(t, 3970000000-2208988800, sd.t0)
	require.EqualValues(t, 3970000120-2208988800, sd.t1)
	require.Equal(t, "3970000000", sd.rawT0)
	require.EqualValues(t, 1000000042, sd.ssrc)

	// Unix-era t= values pass through unchanged.
	sd, err = sdpFromInvite([]byte(
		"v=0\r\no=- 0 0 IN IP4 10.0.0.3\r\ns=Playback\r\nc=IN IP4 10.0.0.3\r\nt=1760000000 1760000060\r\n" +
			"m=video 30014 RTP/AVP 96\r\na=recvonly\r\ny=9\r\n"))
	require.NoError(t, err)
	require.True(t, sd.hasT)
	require.EqualValues(t, 1760000000, sd.t0)
	require.EqualValues(t, 1760000060, sd.t1)

	_, err = sdpFromInvite([]byte("v=0\r\nm=audio 8 RTP/AVP 8\r\n"))
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

// TestLensCmdKind covers FI/auxiliary opcode recognition (GB/T 28181-2022
// § A.3.3/A.3.7): FI lens 0x4X, aux switch 0x8C/0x8D; direction bits (0x00-
// 0x3F) and preset opcodes must NOT classify as lens.
func TestLensCmdKind(t *testing.T) {
	require.Equal(t, "FI lens", lensCmdKind("A50F014400400039"))    // iris open
	require.Equal(t, "FI lens", lensCmdKind("A50F014120000016"))    // focus far
	require.Equal(t, "FI lens", lensCmdKind("A50F01492040005E"))    // combined iris+focus
	require.Equal(t, "aux switch", lensCmdKind("A50F018C01000042")) // wiper on
	require.Equal(t, "aux switch", lensCmdKind("A50F018D01000043")) // wiper off
	require.Equal(t, "", lensCmdKind("A50F0108002000DD"))           // direction (up)
	require.Equal(t, "", lensCmdKind("A50F0100000000B5"))           // stop
	require.Equal(t, "", lensCmdKind("nothex"))
	require.Equal(t, "", lensCmdKind("1234"))
}

// Upper-platform FI/aux commands must not be mis-decoded as PTZ directions
// and pushed at local cameras — they stop at an explicit refusal log.
func TestForwardDeviceControl_LensNotForwarded(t *testing.T) {
	db := newCascadeTestDB(t)
	svc := New(testCfg(), fakeSource{cams: []CameraInfo{{ID: "front", Name: "Front"}}}, db)
	_, err := svc.catalogItems()
	require.NoError(t, err)

	forwarded := 0
	svc.SetPTZForwarder(func(cameraID, direction string, speed byte) error {
		forwarded++
		return nil
	})

	svc.forwardDeviceControl(manscdp.DeviceControl{
		CmdType:  manscdp.CmdDeviceControl,
		DeviceID: "34020000001320000001",
		PTZCmd:   "A50F014400400039", // FI iris-open
	})
	svc.forwardDeviceControl(manscdp.DeviceControl{
		CmdType:  manscdp.CmdDeviceControl,
		DeviceID: "34020000001320000001",
		PTZCmd:   "A50F018C01000042", // aux wiper on
	})
	require.Equal(t, 0, forwarded, "lens/aux commands must not reach the local PTZ bridge")

	// Sanity: plain directions still forward through the same path.
	svc.forwardDeviceControl(manscdp.DeviceControl{
		CmdType:  manscdp.CmdDeviceControl,
		DeviceID: "34020000001320000001",
		PTZCmd:   "A50F0108002000DD",
	})
	require.Equal(t, 1, forwarded)
}

// --- sub-stream forwarding (#512) -------------------------------------------------

type fakeSubAcquirer struct {
	hub     *model.StreamHub
	err     error
	calls   int
	release chan struct{}
}

func (f *fakeSubAcquirer) AcquireSubHub(context.Context, string) (*model.StreamHub, func(), error) {
	f.calls++
	if f.err != nil {
		return nil, nil, f.err
	}
	return f.hub, func() { f.release <- struct{}{} }, nil
}

// hubSource is a fakeSource whose Hub returns a real hub for one camera.
type hubSource struct {
	fakeSource
	hub *model.StreamHub
}

func (f hubSource) Hub(string) *model.StreamHub { return f.hub }

var errNoSubForTest = &testError{"no sub-stream configured"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

// A wanted sub forward subscribes to the SUB hub, records the swap, and drops
// the acquisition reference on close.
func TestMediaSessionSubStreamForward(t *testing.T) {
	mainHub, subHub := model.NewStreamHub(), model.NewStreamHub()
	svc := New(testCfg(), hubSource{fakeSource{cams: []CameraInfo{{ID: "cam-1", SubStream: true}}}, mainHub}, nil)
	acq := &fakeSubAcquirer{hub: subHub, release: make(chan struct{}, 1)}
	svc.SetSubStreamAcquirer(acq)

	ms := &mediaSession{
		svc: svc, callID: "c1", channel: "ch", camera: "cam-1",
		wantSub: true, mux: psmux.New(),
	}
	ms.hub = mainHub
	ms.run(mainHub)

	require.Equal(t, 1, acq.calls, "run must acquire the sub tier for a wanted camera")
	require.Equal(t, 1, subHub.ConsumerCount(), "session must subscribe on the sub hub")
	require.Equal(t, 0, mainHub.ConsumerCount(), "main hub untouched by the sub forward")
	require.Equal(t, subHub, ms.hub, "session must record the swapped hub for close()")

	ms.stop()
	require.Equal(t, 0, subHub.ConsumerCount(), "close must unsubscribe from the sub hub")
	select {
	case <-acq.release:
	default:
		t.Fatal("close must drop the sub-stream reference")
	}
}

// Acquisition failure degrades to a main-stream forward — never a dead session.
func TestMediaSessionSubStreamFallbackToMain(t *testing.T) {
	mainHub := model.NewStreamHub()
	svc := New(testCfg(), hubSource{fakeSource{cams: []CameraInfo{{ID: "cam-1", SubStream: true}}}, mainHub}, nil)
	svc.SetSubStreamAcquirer(&fakeSubAcquirer{err: errNoSubForTest, release: make(chan struct{}, 1)})

	ms := &mediaSession{
		svc: svc, callID: "c1", channel: "ch", camera: "cam-1",
		wantSub: true, mux: psmux.New(),
	}
	ms.hub = mainHub
	ms.run(mainHub)

	require.Equal(t, 1, mainHub.ConsumerCount(), "failed acquisition must fall back to main")
	require.Equal(t, mainHub, ms.hub)
	ms.stop()
	require.Equal(t, 0, mainHub.ConsumerCount())
}

// A BYE racing the acquisition: close() first ⇒ run drops the freshly granted
// reference and never subscribes.
func TestMediaSessionSubStreamCloseDuringAcquire(t *testing.T) {
	mainHub, subHub := model.NewStreamHub(), model.NewStreamHub()
	svc := New(testCfg(), hubSource{fakeSource{cams: []CameraInfo{{ID: "cam-1", SubStream: true}}}, mainHub}, nil)
	acq := &fakeSubAcquirer{hub: subHub, release: make(chan struct{}, 1)}
	svc.SetSubStreamAcquirer(acq)

	ms := &mediaSession{
		svc: svc, callID: "c1", channel: "ch", camera: "cam-1",
		wantSub: true, mux: psmux.New(),
	}
	ms.hub = mainHub
	ms.closed.Store(true) // BYE won the race before run() acquired

	ms.run(mainHub)
	require.Equal(t, 0, acq.calls, "a session closed before the acquire must not pull the sub tier at all")
	require.Equal(t, 0, subHub.ConsumerCount(), "closed session must not subscribe")
	require.Equal(t, 0, mainHub.ConsumerCount(), "closed session must not subscribe to main either")
}
