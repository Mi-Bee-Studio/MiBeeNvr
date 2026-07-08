// SPDX-License-Identifier: MIT
//
// Two-way audio lifecycle tests for XiaomiRecorder.
// Verifies that StartTwoWayAudio, StopTwoWayAudio, SpeakerCodec,
// and WriteAudioToCamera handle nil/active missClient correctly.

package xiaomi

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTwoWayAudioNotConnected(t *testing.T) {
	t.Helper()
	// When missClient is nil (not connected), all two-way audio methods
	// must return an error / zero codec.
	r := &XiaomiRecorder{}

	// StartTwoWayAudio should fail with nil client.
	err := r.StartTwoWayAudio()
	require.Error(t, err)
	require.Contains(t, err.Error(), "not connected")

	// StopTwoWayAudio should fail with nil client.
	err = r.StopTwoWayAudio()
	require.Error(t, err)
	require.Contains(t, err.Error(), "not connected")

	// SpeakerCodec should return 0 with nil client.
	codec := r.SpeakerCodec()
	require.Zero(t, codec)

	// WriteAudioToCamera should fail with nil client.
	err = r.WriteAudioToCamera(1024, []byte{0x00, 0x00})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not connected")
}

func TestTwoWayAudioWithMockMISS(t *testing.T) {
	t.Helper()
	// When a MISSClient is connected, two-way audio methods delegate to it.
	r := &XiaomiRecorder{}
	client, mock := newTestMISSClient()
	client.model = "isa.camera.hlc6" // model with PCM speaker codec
	mock.protocol = "tutk" // non-CS2, gate passes for StartSpeaker
	r.missMu.Lock()
	r.missClient = client
	r.missMu.Unlock()

	// Configure StartSpeaker mock response.
	respData := []byte(`{"result":"success"}`)
	encrypted, err := Encode(respData, client.key)
	require.NoError(t, err)
	mock.readCmdResp = struct {
		cmd  uint32
		data []byte
	}{cmd: missCmdEncoded, data: encrypted}

	// StartTwoWayAudio should succeed.
	err = r.StartTwoWayAudio()
	require.NoError(t, err)

	// Verify the wirePacket was sent.
	cmd, data, ok := mock.lastWrittenCmd()
	require.True(t, ok)
	require.Equal(t, uint32(missCmdEncoded), cmd)
	decoded, err := Decode(data, client.key)
	require.NoError(t, err)
	innerCmd := binary.BigEndian.Uint32(decoded)
	require.Equal(t, uint32(missCmdSpeakerStartReq), innerCmd)

	// SpeakerCodec should return the client's codec.
	codec := r.SpeakerCodec()
	require.NotZero(t, codec)

	// WriteAudioToCamera should succeed.
	err = r.WriteAudioToCamera(1024, []byte{0x00, 0x01, 0x02, 0x03})
	require.NoError(t, err)

	// StopTwoWayAudio should succeed.
	err = r.StopTwoWayAudio()
	require.NoError(t, err)
}

func TestTwoWayAudioWithMockMISS_CS2(t *testing.T) {
	t.Helper()
	// When transport is CS2, StartTwoWayAudio should fail
	// (two-way audio requires TUTK on some camera models).
	r := &XiaomiRecorder{}
	client, mock := newTestMISSClient()
	_ = mock // default protocol is "cs2+udp"
	r.missMu.Lock()
	r.missClient = client
	r.missMu.Unlock()

	// StartSpeaker should fail on CS2.
	err := r.StartTwoWayAudio()
	require.Error(t, err)
	require.Contains(t, err.Error(), "two-way audio requires TUTK")
}
