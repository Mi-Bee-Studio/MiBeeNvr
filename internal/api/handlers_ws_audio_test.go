package api

import (
	"context"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/wsstream"
	"github.com/stretchr/testify/require"
)

// Tests for the AAC config forwarding added by #131: setupAudioForWS must
// pull AudioConfig() (the AudioSpecificConfig) and hand it to SetAudioInfo so
// the WebSocket AudioCodecInfo packet carries it to the browser-side decoder.
//
// These tests cover the early-return paths (no audio / non-audio recorder),
// which return before touching the manager and therefore do not spawn the
// writeLoop goroutine. The AAC-config forwarding through SetAudioInfo is
// exercised end-to-end at the wire-format level in
// wsstream/protocol_audio_test.go — the manager's writeLoop has a pre-existing
// unlocked read on entry.audioCh (see the NOTE at manager.go:166) that is out
// of scope for #131, so these tests avoid launching it by not calling
// RegisterStream (the early-return paths never reach SetAudioInfo).

// mockAudioRecorder implements both model.Recorder and the unexported
// audioInfoProvider interface so setupAudioForWS can read its audio params.
type mockAudioRecorder struct {
	codec    string
	rate     int
	channels int
	config   []byte
}

func (m *mockAudioRecorder) Start(context.Context) error { return nil }
func (m *mockAudioRecorder) Stop() error                 { return nil }
func (m *mockAudioRecorder) Status() model.RecorderStatus {
	return model.RecorderStatus("recording")
}
func (m *mockAudioRecorder) AudioCodec() string   { return m.codec }
func (m *mockAudioRecorder) AudioSampleRate() int { return m.rate }
func (m *mockAudioRecorder) AudioChannels() int   { return m.channels }
func (m *mockAudioRecorder) AudioConfig() []byte  { return m.config }

// newAudioTestHandler builds a Handler wired to a real wsstream.Manager so
// setupAudioForWS has somewhere to push the audio info.
func newAudioTestHandler(t *testing.T) (*Handler, *wsstream.Manager) {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	mgr := wsstream.NewManager()
	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)
	h.SetWSManager(mgr)
	return h, mgr
}

func TestSetupAudioForWS_NoAudioNoOp(t *testing.T) {
	t.Parallel()
	h, _ := newAudioTestHandler(t)

	// Empty codec string → setupAudioForWS returns before touching the manager.
	rec := &mockAudioRecorder{codec: "", rate: 0, channels: 0}
	require.NotPanics(t, func() { setupAudioForWS(h, "cam-silent", rec) })
}

func TestSetupAudioForWS_NonAudioRecorderNoOp(t *testing.T) {
	t.Parallel()
	h, _ := newAudioTestHandler(t)

	// A recorder that does NOT implement audioInfoProvider: setupAudioForWS
	// unwraps delegates, finds no provider, and returns without error.
	rec := silentRecorder{}
	require.NotPanics(t, func() { setupAudioForWS(h, "cam-noaudio", rec) })
}

// silentRecorder implements model.Recorder but NOT audioInfoProvider.
type silentRecorder struct{}

func (silentRecorder) Start(context.Context) error { return nil }
func (silentRecorder) Stop() error                 { return nil }
func (silentRecorder) Status() model.RecorderStatus {
	return model.RecorderStatus("idle")
}

