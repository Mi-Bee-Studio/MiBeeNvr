package xiaomi

// Device-control surface of the Xiaomi recorder: motor/PTZ commands, device
// info, two-way audio (session start/stop, speaker codec, writing audio back to
// the camera) and the test seams. Split out of recorder.go.

// MotorControl sends a PTZ motor control command to the Xiaomi camera.
// direction: "left", "right", "up", "down"; speed: 1-100.
func (r *XiaomiRecorder) MotorControl(direction string, speed int) error {
	r.missMu.Lock()
	client := r.missClient
	r.missMu.Unlock()
	if client == nil {
		return ErrNotConnected
	}
	return client.MotorControl(direction, speed)
}

// GetDeviceInfo fetches device information from the Xiaomi camera.
// Returns a map with keys like "firmware_version", "hardware_version", etc.
func (r *XiaomiRecorder) GetDeviceInfo() (map[string]interface{}, error) {
	r.missMu.Lock()
	client := r.missClient
	r.missMu.Unlock()
	if client == nil {
		return nil, ErrNotConnected
	}
	return client.GetDeviceInfo()
}

// StartTwoWayAudio starts two-way audio on the Xiaomi camera.
// Returns an error if the recorder is not connected or the camera
// uses CS2 transport (two-way audio requires TUTK).
func (r *XiaomiRecorder) StartTwoWayAudio() error {
	r.missMu.Lock()
	client := r.missClient
	r.missMu.Unlock()
	if client == nil {
		return ErrNotConnected
	}
	return client.StartSpeaker()
}

// StopTwoWayAudio stops two-way audio playback.
func (r *XiaomiRecorder) StopTwoWayAudio() error {
	r.missMu.Lock()
	client := r.missClient
	r.missMu.Unlock()
	if client == nil {
		return ErrNotConnected
	}
	return client.wirePacket(missCmdSpeakerStop, nil)
}

// SpeakerCodec returns the audio codec ID for two-way audio speaker output.
// Returns 0 if the camera model does not support two-way audio.
func (r *XiaomiRecorder) SpeakerCodec() uint32 {
	r.missMu.Lock()
	client := r.missClient
	r.missMu.Unlock()
	if client == nil {
		return 0
	}
	return client.SpeakerCodec()
}

// WriteAudioToCamera sends an audio payload to the Xiaomi camera
// for two-way audio playback.
// codecID is the MISS codec ID for the audio format (e.g. missCodecPCMU).
func (r *XiaomiRecorder) WriteAudioToCamera(codecID uint32, payload []byte) error {
	r.missMu.Lock()
	client := r.missClient
	r.missMu.Unlock()
	if client == nil {
		return ErrNotConnected
	}
	return client.WriteAudio(codecID, payload)
}

// SetMISSClientForTest sets the MISS client for testing purposes only.
func (r *XiaomiRecorder) SetMISSClientForTest(c *MISSClient) {
	r.missMu.Lock()
	defer r.missMu.Unlock()
	r.missClient = c
}

// NewTestMISSClient creates a MISSClient for testing with a dummy 32-byte key.
func NewTestMISSClient(conn MISSConn) *MISSClient {
	return &MISSClient{Conn: conn, key: make([]byte, 32)}
}
