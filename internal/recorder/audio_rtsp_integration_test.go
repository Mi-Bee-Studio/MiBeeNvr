package recorder

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/testutil/audiosrc"
)

// TestAudioRecorder_RTSPIntegration drives the FULL pull path — SDP
// negotiation, codec arming, RTP demux, AU enqueue, MP4 segment write, hub
// broadcast — against the in-repo audio-only RTSP source (internal/testutil/
// audiosrc). One case per codec: G.711 a-law/µ-law (synthesized) and
// AAC/Opus (real pre-encoded fixtures).
//
// This is the harness the #29/#142 incident family asked for: unit fixtures
// never carry wire-real payload shapes, and the A-law-silence
// misclassification bug lived exactly in that gap.
func TestAudioRecorder_RTSPIntegration(t *testing.T) {
	src, err := audiosrc.Start()
	require.NoError(t, err)
	defer src.Close()

	cases := []struct {
		codec     audiosrc.Codec
		wantArmed string
	}{
		{audiosrc.ALaw, "g711"},
		{audiosrc.ULaw, "g711"},
		{audiosrc.AAC, "aac"},
		{audiosrc.Opus, "opus"},
	}
	for _, tc := range cases {
		t.Run(string(tc.codec), func(t *testing.T) {
			store := &audioTestStore{dir: t.TempDir()}
			db := &audioTestDB{}
			hub := streamhub.New()
			r := NewAudioRecorder(AudioConfig{
				CameraID:   "mic-it-" + string(tc.codec),
				RTSPURL:    src.URL(tc.codec),
				SegmentDur: time.Minute, // no rotation inside the test window
				DB:         db,
				Store:      store,
			}, store, nil)
			r.SetHub(hub)

			var frames atomic.Int64
			require.NoError(t, hub.SubscribeAudio("it-"+string(tc.codec), func(_ int64, _ model.AudioCodec, _ []byte) {
				frames.Add(1)
			}))

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			require.NoError(t, r.Start(ctx), "recorder must connect to the source")
			t.Cleanup(func() { _ = r.Stop() })

			// Codec arming (SDP-derived) + live frames on the hub.
			require.Eventually(t, func() bool {
				return r.AudioCodec() == tc.wantArmed && frames.Load() >= 20
			}, 8*time.Second, 100*time.Millisecond,
				"codec must arm to %q and hub audio frames must flow (codec=%q frames=%d)",
				tc.wantArmed, r.AudioCodec(), frames.Load())

			require.NoError(t, r.Stop(), "stop finalizes the open segment")

			store.mu.Lock()
			defer store.mu.Unlock()
			require.GreaterOrEqual(t, store.created, 1, "a segment must have been opened")
			require.GreaterOrEqual(t, store.closed, 1, "the segment must finalize on stop")
		})
	}
}
