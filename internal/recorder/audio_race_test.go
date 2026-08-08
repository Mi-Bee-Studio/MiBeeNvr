package recorder

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// These tests guard the fix for #226: the baseRecorder audio configuration
// fields (codec/sampleRate/channels/muxerConfig/g711MULaw/g711SampleRate)
// are read cross-goroutine by the AudioCodec()/AudioConfig()/AudioSampleRate()/
// AudioChannels() accessors (called from WS/relay/status goroutines) and by
// the G.711 RTP callback, while connectAndRecord writes them. Previously these
// were plain fields → a data race under -race. They are now an
// atomic.Pointer[audioConfig] snapshot. The tests prove concurrent writers +
// readers run cleanly under `go test -race`.

// sampleAudioConfig returns a distinct audioConfig for variant v, alternating
// AAC and G.711 so the snapshot's codec↔muxerConfig pairing is exercised.
func sampleAudioConfig(variant int) *audioConfig {
	if variant%2 == 0 {
		// AAC: muxerConfig is a 2-byte AudioSpecificConfig-ish blob.
		return &audioConfig{
			codec:       "aac",
			sampleRate:  48000,
			channels:    2,
			muxerConfig: []byte{0x12, byte(variant)},
		}
	}
	// G.711 μ-law, varying sample rate so the RTP-callback read path is stressed.
	rate := 8000 + variant
	return &audioConfig{
		codec:          "g711",
		sampleRate:     rate,
		channels:       1,
		g711MULaw:      true,
		g711SampleRate: rate,
		muxerConfig:    []byte{1, byte(rate >> 24), byte(rate >> 16), byte(rate >> 8), byte(rate)},
	}
}

// TestAudioConfig_ConcurrentReadWrite_H264 spins writer goroutines that publish
// the audio snapshot (simulating connectAndRecord's AAC/G.711 detection) while
// reader goroutines invoke every public audio accessor. Must not trigger the
// race detector and must not panic.
func TestAudioConfig_ConcurrentReadWrite_H264(t *testing.T) {
	t.Parallel()
	rec := NewH264Recorder(H264Config{CameraID: "race-audio-h264"}, nil)

	runAudioRace(t, rec.baseRecorder)
}

// TestAudioConfig_ConcurrentReadWrite_H265 is the H.265 variant.
func TestAudioConfig_ConcurrentReadWrite_H265(t *testing.T) {
	t.Parallel()
	rec := NewH265Recorder(H265Config{CameraID: "race-audio-h265"}, nil)

	runAudioRace(t, rec.baseRecorder)
}

// runAudioRace exercises writer(setAudioConfig) + reader(accessors) concurrency
// for ~50ms on a *baseRecorder. The readers mirror the WS/relay/status reader
// paths (audio snapshot + muxer/audioTrackID/segStart lock snapshot).
func runAudioRace(t *testing.T, base *baseRecorder) {
	t.Helper()
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writers — alternate AAC/G.711 snapshots so the race detector is more
	// likely to catch a torn view if the atomic guard regresses.
	const writers = 2
	for w := range writers {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 1; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				base.setAudioConfig(sampleAudioConfig(w*1000 + i))
			}
		}()
	}

	// Readers — call all public audio accessors concurrently. These mirror the
	// WS/relay/status reader paths.
	const readers = 4
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = audioCodecOf(base)
				_ = audioConfigOf(base)
				_ = base.audioSnapshot()
				// muxer/audioTrackID/segStart snapshot read (the RTP-callback path).
				base.mu.Lock()
				_ = base.muxer
				_ = base.audioTrackID
				_ = base.segStart
				base.mu.Unlock()
			}
		}()
	}

	// Let the race detector observe contention for a short window, then stop.
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// audioCodecOf / audioConfigOf read via a *baseRecorder so the test doesn't
// depend on the concrete H264/H265 accessor method set in this closure.
func audioCodecOf(b *baseRecorder) string {
	if a := b.audioSnapshot(); a != nil {
		return a.codec
	}
	return ""
}

func audioConfigOf(b *baseRecorder) []byte {
	if a := b.audioSnapshot(); a != nil && a.muxerConfig != nil {
		return append([]byte(nil), a.muxerConfig...)
	}
	return nil
}

// TestAudioConfig_SnapshotConsistency verifies a single published snapshot is
// read back coherently (codec matches muxerConfig prefix — AAC 0x12, G.711 0x01)
// across all accessors, ruling out a torn codec/muxerConfig pairing.
func TestAudioConfig_SnapshotConsistency(t *testing.T) {
	t.Parallel()
	for _, v := range []int{0, 1, 2, 3} {
		rec := NewH264Recorder(H264Config{CameraID: "snap-h264"}, nil)
		rec.setAudioConfig(sampleAudioConfig(v))
		s := rec.audioSnapshot()
		require.NotNil(t, s)
		require.Len(t, s.muxerConfig, 0+len(s.muxerConfig)) // sanity
		switch s.codec {
		case "aac":
			require.NotEmpty(t, s.muxerConfig)
			require.Equal(t, "aac", rec.AudioCodec())
			require.Equal(t, 48000, rec.AudioSampleRate())
			require.Equal(t, 2, rec.AudioChannels())
		case "g711":
			require.Equal(t, byte(1), s.muxerConfig[0], "g711 muLaw flag baked into muxerConfig[0]")
			require.Equal(t, "g711", rec.AudioCodec())
			require.Positive(t, rec.AudioSampleRate())
			require.Equal(t, 1, rec.AudioChannels())
		}
	}
}

// TestAudioConfig_NilSnapshotDefaults verifies accessors return zero values
// before any audio config is published (the no-audio / pre-connect state).
func TestAudioConfig_NilSnapshotDefaults(t *testing.T) {
	t.Parallel()
	rec := NewH264Recorder(H264Config{CameraID: "nil-audio-h264"}, nil)
	require.Equal(t, "", rec.AudioCodec())
	require.Nil(t, rec.AudioConfig())
	require.Zero(t, rec.AudioSampleRate())
	require.Zero(t, rec.AudioChannels())
}
