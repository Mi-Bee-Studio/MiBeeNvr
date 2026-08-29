package gb28181

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	gbcascade "github.com/mickeyzzc/gb28181-go/platform/cascade"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

func TestCascadeConfigMapsFields(t *testing.T) {
	in := config.GB28181CascadeConfig{
		Enabled:           true,
		ServerDomain:      "34020000002000000001",
		ServerAddr:        "10.0.0.1:5060",
		LocalDeviceID:     "34020000001320000001",
		Realm:             "3402000000",
		Password:          "secret",
		SIPListen:         ":5061",
		HeartbeatInterval: "30s",
		RegisterExpires:   3600,
		Upstreams: []config.GB28181CascadeUpstream{{
			ServerDomain: "upper2", ServerAddr: "10.0.0.2:5060",
			LocalDeviceID: "dev-at-2", Realm: "r2", Password: "p2",
			HeartbeatInterval: "45s", RegisterExpires: 1800,
		}},
	}
	out := CascadeConfig(in)
	require.Equal(t, in.Enabled, out.Enabled)
	require.Equal(t, in.ServerDomain, out.ServerDomain)
	require.Equal(t, in.ServerAddr, out.ServerAddr)
	require.Equal(t, in.LocalDeviceID, out.LocalDeviceID)
	require.Equal(t, in.Realm, out.Realm)
	require.Equal(t, in.Password, out.Password)
	require.Equal(t, in.SIPListen, out.SIPListen)
	require.Equal(t, in.HeartbeatInterval, out.HeartbeatInterval)
	require.Equal(t, in.RegisterExpires, out.RegisterExpires)
	require.Len(t, out.Upstreams, 1)
	u := out.Upstreams[0]
	require.Equal(t, "upper2", u.ServerDomain)
	require.Equal(t, "10.0.0.2:5060", u.ServerAddr)
	require.Equal(t, "dev-at-2", u.LocalDeviceID)
	require.Equal(t, "45s", u.HeartbeatInterval)
	require.Equal(t, 1800, u.RegisterExpires)
}

// TestCascadeStoreRoundTrip covers the real-SQLite seam path the library's
// own tests only exercise with the in-memory fake (batch-4 review note).
func TestCascadeStoreRoundTrip(t *testing.T) {
	db, err := storage.New(filepath.Join(t.TempDir(), "cascade.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	t.Cleanup(func() { _ = db.Close() })
	store := NewCascadeStore(db)
	ctx := context.Background()

	ch := gbcascade.CascadeChannel{
		CameraID: "cam-1", GBChannelID: "34020000001320000002", Name: "front", UpdatedAt: time.Now(),
	}
	require.NoError(t, store.UpsertCascadeChannel(ctx, ch))
	got, err := store.ListCascadeChannels(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, ch.CameraID, got[0].CameraID)
	require.Equal(t, ch.GBChannelID, got[0].GBChannelID)

	// Recordings index: seed one row via storage, read it back through the seam.
	start := time.Now().UTC().Add(-time.Hour)
	end := time.Now().UTC()
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "rec-1", CameraID: "cam-1", FilePath: "/tmp/x.mp4", Format: model.FormatH264,
		StartedAt: start, EndedAt: end, Duration: end.Sub(start).Seconds(),
	}))
	recs, err := store.ListRecordings(ctx, gbcascade.RecordingFilter{
		CameraID: "cam-1", StartTime: start.Add(-time.Minute), EndTime: end.Add(time.Minute),
		Limit: 100, SortBy: "started_at", SortOrder: "asc",
	})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "rec-1", recs[0].ID)
	require.Equal(t, gbcascade.FormatH264, recs[0].Format)
	require.Equal(t, "/tmp/x.mp4", recs[0].FilePath)
}

// TestSegmentParserWrapsMerge parses a real fMP4 segment written by the NVR
// muxer through the library seam (type-for-type wrapper verification).
func TestSegmentParserWrapsMerge(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "seg.mp4")
	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	m := muxer.NewMP4Muxer(path)
	trackID, err := m.AddH264Track(sps, pps)
	require.NoError(t, err)
	require.NoError(t, m.WriteSample(trackID, []byte{0x65, 0x88, 0x80, 0x40}, 0, 33*time.Millisecond))
	require.NoError(t, m.WriteSample(trackID, []byte{0x41, 0x10, 0x00, 0x0c}, 33*time.Millisecond, 33*time.Millisecond))
	require.NoError(t, m.Close())

	info, err := SegmentParser()(path)
	require.NoError(t, err)
	require.NotNil(t, info)
	require.Equal(t, "h264", info.Codec)
	require.Equal(t, sps, info.SPS)
	require.Equal(t, pps, info.PPS)
	require.NotEmpty(t, info.Samples)
	require.True(t, info.Samples[0].IsKeyFrame)

	_, err = SegmentParser()(filepath.Join(dir, "missing.mp4"))
	require.Error(t, err)
}

func TestBridgeHubForwardsVideoAndAudio(t *testing.T) {
	nvr := streamhub.New()
	lib := BridgeHub(nvr)
	require.NotNil(t, lib)
	require.Same(t, lib, BridgeHub(nvr), "bridge must be cached per hub")

	gotVideo := make(chan [2]any, 4)
	require.NoError(t, lib.Subscribe("consumer-1", func(pts int64, au [][]byte) {
		gotVideo <- [2]any{pts, au}
	}))
	gotAudio := make(chan [3]any, 4)
	require.NoError(t, lib.SubscribeAudio("consumer-1", func(pts int64, codec string, data []byte) {
		gotAudio <- [3]any{pts, codec, data}
	}))

	nvr.Broadcast(1000, [][]byte{{0x65, 0x01}}, true)
	select {
	case v := <-gotVideo:
		require.EqualValues(t, 1000, v[0])
		require.Equal(t, [][]byte{{0x65, 0x01}}, v[1])
	case <-time.After(5 * time.Second):
		t.Fatal("video frame was not forwarded through the bridge")
	}

	nvr.BroadcastAudio(2000, model.AudioG711A, []byte{0x55})
	select {
	case a := <-gotAudio:
		require.EqualValues(t, 2000, a[0])
		require.Equal(t, "g711a", a[1])
		require.Equal(t, []byte{0x55}, a[2])
	case <-time.After(5 * time.Second):
		t.Fatal("audio frame was not forwarded through the bridge")
	}
}

func TestBridgeSubHubDetaches(t *testing.T) {
	nvr := streamhub.New()
	lib, detach := BridgeSubHub(nvr, "sub-bridge-test")
	require.NotNil(t, lib)

	got := make(chan int64, 4)
	require.NoError(t, lib.Subscribe("consumer", func(pts int64, _ [][]byte) { got <- pts }))
	nvr.Broadcast(1, [][]byte{{0x01}}, true)
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("expected frame before detach")
	}
	detach()
	// After detach the forwarding consumer is gone from the NVR hub; the lib
	// hub keeps its own subscriber but nothing feeds it anymore.
	nvr.Broadcast(2, [][]byte{{0x02}}, true)
	select {
	case <-got:
		t.Fatal("frame must not arrive after detach")
	case <-time.After(300 * time.Millisecond):
	}
}
