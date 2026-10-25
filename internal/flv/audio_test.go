package flv

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

// ── writer 层:AAC tag 字节布局(#1066)──

func TestAACSequenceHeaderLayout(t *testing.T) {
	asc := []byte{0x12, 0x10} // AAC-LC 44.1kHz stereo (canonical example)

	tag := aacAudioSequenceHeader(asc, 2)

	require.EqualValues(t, tagTypeAudio, tag[0], "audio tag type 0x08")
	// DataSize = 2 (first byte + AACPacketType) + len(asc) = 4
	dataSize := uint32(tag[1])<<16 | uint32(tag[2])<<8 | uint32(tag[3])
	assert.EqualValues(t, 4, dataSize)
	// Timestamp 0
	assert.Equal(t, byte(0), tag[4])
	assert.Equal(t, byte(0), tag[5])
	assert.Equal(t, byte(0), tag[6])
	assert.Equal(t, byte(0), tag[7])
	// Body: 0xAF (AAC|44k|16bit|stereo) + seq-header type + ASC
	assert.EqualValues(t, 0xAF, tag[11], "stereo first byte")
	assert.EqualValues(t, aacPacketTypeSeqHeader, tag[12])
	assert.Equal(t, asc, tag[13:15])
}

func TestAACSequenceHeaderMono(t *testing.T) {
	tag := aacAudioSequenceHeader([]byte{0x12, 0x08}, 1)
	assert.EqualValues(t, 0xAE, tag[11], "mono first byte: SoundType=0")
}

func TestAACFrameTagLayout(t *testing.T) {
	frame := []byte{0x21, 0x1A, 0x00}

	tag := aacAudioFrameTag(4521, frame, 2)

	require.EqualValues(t, tagTypeAudio, tag[0])
	assert.EqualValues(t, 0xAF, tag[11])
	assert.EqualValues(t, aacPacketTypeRaw, tag[12])
	assert.Equal(t, frame, tag[13:13+len(frame)])
	// Timestamp 4521ms → 0x000011A9 split across 3 bytes + extended
	assert.Equal(t, byte(0x00), tag[4])
	assert.Equal(t, byte(0x11), tag[5])
	assert.Equal(t, byte(0xA9), tag[6])
	assert.Equal(t, byte(0x00), tag[7], "extended timestamp high byte")
}

// ── manager 层:音频腿端到端(#1066)──

// TestAudioEndToEnd drives a real StreamHub: arm AAC, broadcast an AU with a
// wallclock-nanos pts (the RTSP recorder's convention), and assert the viewer
// receives the sequence header + one audio tag interleaved on the wire.
func TestAudioEndToEnd(t *testing.T) {
	mgr := newTestManager(t)
	hub := streamhub.New()
	require.NoError(t, mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, hub))
	mgr.RegisterAudioConfig("cam1", []byte{0x12, 0x10}, 44100, 2)

	ctx, cancel := context.WithCancel(context.Background())
	var buf bytes.Buffer
	w := &capturingResponseWriter{w: &buf}
	r := httptest.NewRequest(http.MethodGet, "/api/cameras/cam1/stream.flv", nil).WithContext(ctx)

	go func() {
		<-ctx.Done()
	}()
	go func() {
		time.Sleep(50 * time.Millisecond)
		now := time.Now().UnixNano()
		hub.BroadcastAudio(now, model.AudioAAC, []byte{0x21, 0x00})
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_ = mgr.ServeFLV("cam1", w, r)

	out := buf.Bytes()
	// FLV header + video seq header + audio seq header + audio frame tag.
	seq := aacAudioSequenceHeader([]byte{0x12, 0x10}, 2)
	require.NotEqual(t, 0, bytes.Index(out, seq[9:]), "audio sequence header must reach the viewer")
	frame := aacAudioFrameTag(0, []byte{0x21, 0x00}, 2)
	// Compare body only (timestamp depends on the anchoring clock).
	assert.True(t, bytes.Contains(out, frame[13:15]), "raw AAC payload must reach the viewer")
	// One audio tag of each kind on the wire: two tagTypeAudio occurrences
	// after the video sequence header.
	assert.EqualValues(t, 2, bytes.Count(out[dataSizeStart(out):], []byte{tagTypeAudio}),
		"exactly one seq header + one frame audio tag")
}

// dataSizeStart returns the offset just past the 9-byte FLV header +
// PreviousTagSize0 so audio-tag counting skips the file header bytes.
func dataSizeStart(out []byte) int {
	return 13
}

func TestAudioNotArmed_DropsFrames(t *testing.T) {
	mgr := newTestManager(t)
	hub := streamhub.New()
	require.NoError(t, mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, hub))
	// No RegisterAudioConfig call.
	mgr.writeAudioFrame("cam1", time.Now().UnixNano(), model.AudioAAC, []byte{0x21})
	mgr.mu.RLock()
	entry := mgr.streams["cam1"]
	mgr.mu.RUnlock()
	require.NotNil(t, entry)
	select {
	case <-entry.audioCh:
		t.Fatal("unarmed stream must not queue audio")
	default:
	}
}

func TestAudioNonAACIgnored(t *testing.T) {
	mgr := newTestManager(t)
	hub := streamhub.New()
	require.NoError(t, mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, hub))
	mgr.RegisterAudioConfig("cam1", []byte{0x12, 0x10}, 44100, 2)

	mgr.writeAudioFrame("cam1", time.Now().UnixNano(), model.AudioG711A, []byte{0x55})
	mgr.mu.RLock()
	entry := mgr.streams["cam1"]
	mgr.mu.RUnlock()
	select {
	case <-entry.audioCh:
		t.Fatal("G.711 must not be queued (classic FLV carries AAC only)")
	default:
	}
}

func TestAudioTsMsAnchorsNanosToVideoTimeline(t *testing.T) {
	mgr := newTestManager(t)
	entry := &streamEntry{}
	const baseMs = int64(1_700_000_000_000) // realistic unix ms (~2023)
	entry.clockBase.Store(baseMs)
	entry.lastVideoMs.Store(5000)

	// Nanos wallclock: base + 5s elapsed → anchored onto the video timeline.
	ts := mgr.audioTsMs(entry, (baseMs+5000)*1_000_000)
	assert.EqualValues(t, 5000, ts)

	// Subsequent frame: +1s wallclock → +1000ms on the anchored timeline.
	ts = mgr.audioTsMs(entry, (baseMs+6000)*1_000_000)
	assert.EqualValues(t, 6000, ts)

	// Tick-family pts (GB28181, 90kHz) maps 1:1 with no anchoring.
	assert.EqualValues(t, 900, mgr.audioTsMs(entry, 81000))
}
