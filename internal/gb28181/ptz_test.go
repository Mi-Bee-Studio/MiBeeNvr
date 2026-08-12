package gb28181

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPTZ_CommandBytes(t *testing.T) {
	// Vectors derived from GB/T 28181-2016 § A.4: [0]=0xA5 start,
	// [1]=0x0F combination, [2]=0x01 address, [3]=direction bits,
	// [4]=pan speed, [5]=tilt speed, [6]=zoom speed,
	// [7]=checksum (b0+...+b6) & 0xFF.
	tests := []struct {
		name      string
		direction string
		speed     byte
		want      []byte
	}{
		// speed 0x20 on the moving axes only; all checksums verified by hand.
		{name: "stop", direction: DirStop, speed: 0x00, want: []byte{0xA5, 0x0F, 0x01, 0x00, 0x00, 0x00, 0x00, 0xB5}},
		{name: "up", direction: DirUp, speed: 0x20, want: []byte{0xA5, 0x0F, 0x01, 0x08, 0x00, 0x20, 0x00, 0xDD}},
		{name: "down", direction: DirDown, speed: 0x20, want: []byte{0xA5, 0x0F, 0x01, 0x04, 0x00, 0x20, 0x00, 0xD9}},
		{name: "left", direction: DirLeft, speed: 0x20, want: []byte{0xA5, 0x0F, 0x01, 0x02, 0x20, 0x00, 0x00, 0xD7}},
		{name: "right", direction: DirRight, speed: 0x20, want: []byte{0xA5, 0x0F, 0x01, 0x01, 0x20, 0x00, 0x00, 0xD6}},
		{name: "up-left", direction: DirUpLeft, speed: 0x20, want: []byte{0xA5, 0x0F, 0x01, 0x0A, 0x20, 0x20, 0x00, 0xFF}},
		{name: "up-right", direction: DirUpRight, speed: 0x20, want: []byte{0xA5, 0x0F, 0x01, 0x09, 0x20, 0x20, 0x00, 0xFE}},
		{name: "down-left", direction: DirDownLeft, speed: 0x20, want: []byte{0xA5, 0x0F, 0x01, 0x06, 0x20, 0x20, 0x00, 0xFB}},
		{name: "down-right", direction: DirDownRight, speed: 0x20, want: []byte{0xA5, 0x0F, 0x01, 0x05, 0x20, 0x20, 0x00, 0xFA}},
		{name: "zoom-in", direction: DirZoomIn, speed: 0x20, want: []byte{0xA5, 0x0F, 0x01, 0x10, 0x00, 0x00, 0x20, 0xE5}},
		{name: "zoom-out", direction: DirZoomOut, speed: 0x20, want: []byte{0xA5, 0x0F, 0x01, 0x20, 0x00, 0x00, 0x20, 0xF5}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := BuildPTZCommand(tt.direction, tt.speed)
			require.NoError(t, err)
			require.Equal(t, tt.want, cmd, "exact 8-byte PTZ command")

			// The checksum must equal (b0+...+b6) & 0xFF.
			var sum byte
			for _, b := range cmd[:7] {
				sum += b
			}
			require.Equal(t, sum, cmd[7], "checksum includes byte0")
		})
	}
}

func TestPTZ_CommandBytes_UnknownDirection(t *testing.T) {
	_, err := BuildPTZCommand("diagonal", 0x20)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown PTZ direction")
}

func TestPTZ_CommandString(t *testing.T) {
	cmd, err := BuildPTZCommand(DirUp, 0x20)
	require.NoError(t, err)
	require.Equal(t, "A5 0F 01 08 00 20 00 DD", ptzCmdString(cmd))
}

// fakeMessageSender records sent device/body pairs for assertions.
type fakeMessageSender struct {
	deviceID string
	body     string
	err      error
}

func (f *fakeMessageSender) SendMessage(deviceID string, body []byte) error {
	f.deviceID = deviceID
	f.body = string(body)
	return f.err
}

// newPTZTestEnv registers one online PTZ-capable device + channel and returns
// the controller with a recording sender.
func newPTZTestEnv(t *testing.T, ptzType int) (*PTZController, *fakeMessageSender, *DeviceManager) {
	t.Helper()
	m := NewDeviceManager(time.Minute)
	dev := &Device{ID: "34020000001310000001", Name: "Front Gate", NetAddr: "192.168.1.50:5060"}
	m.Register(dev)

	ch := &Channel{ID: "34020000001320000001", Name: "Channel 1", Parental: 1, PTZType: ptzType}
	m.RegisterChannel(dev.ID, ch)

	sender := &fakeMessageSender{}
	return NewPTZController(m, sender), sender, m
}

func TestPTZ_SendPTZ_Success(t *testing.T) {
	c, sender, _ := newPTZTestEnv(t, 2)

	err := c.SendPTZ("34020000001320000001", DirUp, 0x20)
	require.NoError(t, err)

	require.Equal(t, "34020000001310000001", sender.deviceID)
	require.Contains(t, sender.body, `CmdType="DeviceControl"`)
	require.Contains(t, sender.body, "<DeviceID>34020000001320000001</DeviceID>")
	require.Contains(t, sender.body, "<PTZCmd>A5 0F 01 08 00 20 00 DD</PTZCmd>")
}

func TestPTZ_SendPTZ_ChannelNotFound(t *testing.T) {
	c, _, _ := newPTZTestEnv(t, 2)
	err := c.SendPTZ("34020000001320000099", DirUp, 0x20)
	require.ErrorIs(t, err, ErrChannelNotFound)
}

func TestPTZ_SendPTZ_DeviceOffline(t *testing.T) {
	c, _, m := newPTZTestEnv(t, 2)
	m.MarkOffline("34020000001310000001")

	err := c.SendPTZ("34020000001320000001", DirUp, 0x20)
	require.ErrorIs(t, err, ErrDeviceOffline)
}

func TestPTZ_SendPTZ_PTZUnsupported(t *testing.T) {
	c, _, _ := newPTZTestEnv(t, 0)
	err := c.SendPTZ("34020000001320000001", DirUp, 0x20)
	require.ErrorIs(t, err, ErrPTZUnsupported)
}

func TestPTZ_SendPTZ_ZoomOnPanTiltOnly(t *testing.T) {
	c, _, _ := newPTZTestEnv(t, 1)

	// Pan/tilt commands are fine on PTZType 1.
	require.NoError(t, c.SendPTZ("34020000001320000001", DirLeft, 0x20))

	err := c.SendPTZ("34020000001320000001", DirZoomIn, 0x20)
	require.ErrorIs(t, err, ErrZoomUnsupported)
}

func TestPTZ_SendPTZ_SenderError(t *testing.T) {
	c, _, _ := newPTZTestEnv(t, 2)

	// Sender errors wrap the transport failure.
	c.sender = errSender{}
	err := c.SendPTZ("34020000001320000001", DirUp, 0x20)
	require.Error(t, err)
	require.Contains(t, err.Error(), "send PTZ")
}

type errSender struct{}

func (errSender) SendMessage(string, []byte) error {
	return &sipWriteError{}
}

type sipWriteError struct{}

func (*sipWriteError) Error() string { return "sip write failed" }
