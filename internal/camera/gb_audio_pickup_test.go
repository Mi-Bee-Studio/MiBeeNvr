package camera

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/mickeyzzc/gb28181-go/platform"
)

// GB/T 28181 20-digit IDs: digits 11-13 are the Table E.1 type code.
// 136 = voice-input device (network pickup); 132 = IRC (ordinary camera).
const (
	gbPickupChannelID  = "34020000001360000001"
	gbCameraChannelID  = "34020000001320000001"
	gbNonStandardIDLow = "short-id"
)

// TestEnsureGB28181CameraAudioOnlyEnrollment: a voice-input channel (type
// code 136) enrolls as encoding=audio — the standalone-audio pipeline —
// while ordinary camera channels keep the default (empty → h264) encoding.
func TestEnsureGB28181CameraAudioOnlyEnrollment(t *testing.T) {
	cfg := testConfig()
	cfg.Cameras = nil
	cfg.GB28181.AllowSameIPEnroll = true // skip cross-protocol probes
	mgr, _ := newManagerWithBus(t, cfg)

	require.NoError(t, mgr.EnsureGB28181Camera("34020000002000000001", gbPickupChannelID, "前门拾音器", ""))
	cam := mgr.GetCameraConfig("gb-" + gbPickupChannelID)
	require.NotNil(t, cam)
	assert.Equal(t, string(model.FormatAudio), cam.Encoding,
		"type-136 channels must enroll as audio cameras")
	assert.Equal(t, string(model.ProtoGB28181), cam.Protocol)

	require.NoError(t, mgr.EnsureGB28181Camera("34020000002000000001", gbCameraChannelID, "门口枪机", ""))
	cam2 := mgr.GetCameraConfig("gb-" + gbCameraChannelID)
	require.NotNil(t, cam2)
	assert.Empty(t, cam2.Encoding, "ordinary camera channels keep the default encoding")

	// Non-standard short IDs never classify as audio (library returns 0).
	require.NoError(t, mgr.EnsureGB28181Camera("34020000002000000001", gbNonStandardIDLow, "legacy", ""))
	cam3 := mgr.GetCameraConfig("gb-" + gbNonStandardIDLow)
	require.NotNil(t, cam3)
	assert.Empty(t, cam3.Encoding)

	// Idempotent: re-ensure keeps the audio encoding.
	require.NoError(t, mgr.EnsureGB28181Camera("34020000002000000001", gbPickupChannelID, "前门拾音器", ""))
	assert.Equal(t, string(model.FormatAudio), mgr.GetCameraConfig("gb-"+gbPickupChannelID).Encoding)
}

// TestBuildGB28181RecorderAudioPickup: the gb28181+audio combination builds
// a push-mode AudioRecorder (the standalone-audio pipeline), not the passive
// video recorder that would wait forever for video AUs.
func TestBuildGB28181RecorderAudioPickup(t *testing.T) {
	cfg := testConfig()
	cfg.Cameras = nil
	mgr, _ := newManagerWithBus(t, cfg)

	rec := mgr.buildGB28181Recorder(config.CameraConfig{
		ID: "gb-pickup", Protocol: string(model.ProtoGB28181), Encoding: string(model.FormatAudio),
	}, 60*1e9)
	_, isAudio := rec.(*recorder.AudioRecorder)
	assert.True(t, isAudio, "pickup recorder must be the standalone AudioRecorder")
}

// TestGB28181AudioWriterPickupBridge: the audio writer for an enrolled
// pickup arms the push format from the first frame's law (A-law for the
// negotiated PT 8) and feeds AUs into the audio pipeline.
func TestGB28181AudioWriterPickupBridge(t *testing.T) {
	cfg := testConfig()
	cfg.Cameras = nil
	mgr, _ := newManagerWithBus(t, cfg)

	ctx := context.Background()
	_, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID: "gb-pickup", Name: "pickup", Protocol: string(model.ProtoGB28181), Encoding: string(model.FormatAudio),
		GB28181: config.GB28181ChannelConfig{DeviceID: "34020000002000000001", ChannelID: gbPickupChannelID},
	})
	require.NoError(t, err)
	require.NotNil(t, mgr.GetAudioPushRecorder("gb-pickup"), "started pickup must hold an audio recorder")
	assert.Nil(t, mgr.GetGB28181Recorder("gb-pickup"), "no video recorder for an audio pickup")

	w := mgr.GB28181AudioWriter("gb-pickup")
	require.NotNil(t, w, "audio writer must bind for a voice-input channel")
	assert.Nil(t, mgr.GB28181NALUWriter("gb-pickup"), "no NALU writer for audio-only")

	w("g711a", []byte{0xd5, 0x42}, nil, 90000, 160)
	w("g711a", []byte{0xd5, 0x42}, nil, 108000, 160)
	ar := mgr.GetAudioPushRecorder("gb-pickup")
	assert.Equal(t, "g711", ar.AudioCodec(), "armed as g711")
	assert.Equal(t, 8000, ar.AudioSampleRate())
}

// TestGB28181LifecycleForPickup: INVITE/BYE map onto the push-audio
// lifecycle (segment boundary on BYE) instead of the video recorder states.
func TestGB28181LifecycleForPickup(t *testing.T) {
	cfg := testConfig()
	cfg.Cameras = nil
	mgr, _ := newManagerWithBus(t, cfg)

	_, err := mgr.AddCamera(context.Background(), config.CameraConfig{
		ID: "gb-pickup2", Name: "pickup", Protocol: string(model.ProtoGB28181), Encoding: string(model.FormatAudio),
		GB28181: config.GB28181ChannelConfig{DeviceID: "34020000002000000001", ChannelID: gbPickupChannelID},
	})
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		mgr.OnGB28181Invite("gb-pickup2")
		mgr.OnGB28181Bye("gb-pickup2")
	})
}

// TestValidEncodingsGB28181Audio: the protocol/encoding matrix accepts
// gb28181+audio (enrollment + manual creation both validate).
func TestValidEncodingsGB28181Audio(t *testing.T) {
	assert.Contains(t,
		model.ValidEncodingsForProtocol[string(model.ProtoGB28181)],
		string(model.FormatAudio))
	assert.True(t, platform.IsAudioInputChannel(gbPickupChannelID))
	assert.False(t, platform.IsAudioInputChannel(gbCameraChannelID))
}

// TestGB28181AudioWriterSurvivesRecorderRecreate — regression for the
// 2026-10-22 22:07 field incident (internal tracker #29 forensics): a
// camera re-created mid-session swaps the manager's recorder under the same
// ID, and a writer pinned to the first recorder instance dropped every AU
// while the session kept reporting "playing" — segments stopped for an hour
// until a process restart. The per-call-resolution writer must follow the
// swap: frames land in the NEW recorder, armed fresh, and the replaced
// instance receives nothing.
func TestGB28181AudioWriterSurvivesRecorderRecreate(t *testing.T) {
	cfg := testConfig()
	cfg.Cameras = nil
	cfg.GB28181.AllowSameIPEnroll = true
	mgr, _ := newManagerWithBus(t, cfg)

	camID := "gb-" + gbPickupChannelID
	require.NoError(t, mgr.EnsureGB28181Camera("34020000002000000001", gbPickupChannelID, "前门拾音器", ""))

	recA := recorder.NewAudioRecorder(recorder.AudioConfig{CameraID: camID}, nil, nil)
	hubA := streamhub.New()
	recA.SetHub(hubA)
	mgr.SetTestRecorder(camID, recA)

	w := mgr.GB28181AudioWriter(camID)
	require.NotNil(t, w)

	// The re-creation: a FRESH recorder replaces the old instance under the
	// same camera ID — exactly the 22:07 catalog re-insert shape.
	recB := recorder.NewAudioRecorder(recorder.AudioConfig{CameraID: camID}, nil, nil)
	hubB := streamhub.New()
	recB.SetHub(hubB)
	mgr.SetTestRecorder(camID, recB)

	var framesA, framesB atomic.Int64
	require.NoError(t, hubA.SubscribeAudio("old", func(int64, model.AudioCodec, []byte) { framesA.Add(1) }))
	require.NoError(t, hubB.SubscribeAudio("new", func(int64, model.AudioCodec, []byte) { framesB.Add(1) }))

	for range 10 {
		w("g711a", make([]byte, 160), nil, 0, 160)
	}

	// Hub consumers drain asynchronously — wait for delivery, then assert
	// the replaced instance stayed silent.
	require.Eventually(t, func() bool { return framesB.Load() == 10 }, 2*time.Second, 10*time.Millisecond,
		"frames must land in the NEW recorder")
	assert.Zero(t, framesA.Load(), "the replaced recorder must receive nothing")
	assert.Equal(t, "g711", recB.AudioCodec(), "the new recorder must be armed fresh")
}

// TestGB28181CameraConfigByChannelID: the stall-watchdog re-invite resolves
// cameras by scanning configs — manually created cameras whose ID doesn't
// follow the auto-enroll "gb-<channelID>" convention must resolve too, and
// non-GB cameras sharing an ID shape must not.
func TestGB28181CameraConfigByChannelID(t *testing.T) {
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{
			ID:       "doorbell-manual", // manual creation: NOT gb-<channel>
			Protocol: string(model.ProtoGB28181),
			GB28181:  config.GB28181ChannelConfig{DeviceID: "34020000002000000001", ChannelID: gbCameraChannelID},
		},
		{
			ID:       gbCameraChannelID, // an RTSP camera whose ID collides with the channel string
			Protocol: string(model.ProtoRTSP),
		},
	}
	mgr, _ := newManagerWithBus(t, cfg)

	got, ok := mgr.GB28181CameraConfigByChannelID(gbCameraChannelID)
	require.True(t, ok, "manual GB camera must resolve by channel alone")
	assert.Equal(t, "doorbell-manual", got.ID)
	assert.Equal(t, "34020000002000000001", got.GB28181.DeviceID)

	_, ok = mgr.GB28181CameraConfigByChannelID(gbPickupChannelID)
	assert.False(t, ok, "unknown channel must not resolve")
}
