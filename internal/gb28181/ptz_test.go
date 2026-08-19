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
	require.Equal(t, "A50F0108002000DD", ptzCmdString(cmd))
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
	require.Contains(t, sender.body, "<CmdType>DeviceControl</CmdType>")
	require.Contains(t, sender.body, "<DeviceID>34020000001320000001</DeviceID>")
	require.Contains(t, sender.body, "<PTZCmd>A50F0108002000DD</PTZCmd>")
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

// TestBuildPTZPresetCommand verifies the § A.3.4 preset instruction layout:
// byte 3 = 81/82/83 (set/call/delete), byte 5 = 0, byte 6 = preset number.
func TestBuildPTZPresetCommand(t *testing.T) {
	cmd, err := BuildPTZPresetCommand(PresetSet, 5)
	require.NoError(t, err)
	require.Equal(t, []byte{0xA5, 0x0F, 0x01, 0x81, 0x00, 0x00, 0x05, 0x3B}, cmd)

	cmd, err = BuildPTZPresetCommand(PresetCall, 100)
	require.NoError(t, err)
	require.Equal(t, byte(0x82), cmd[3])
	require.Equal(t, byte(100), cmd[6])

	cmd, err = BuildPTZPresetCommand(PresetDelete, 255)
	require.NoError(t, err)
	require.Equal(t, byte(0x83), cmd[3])
	require.Equal(t, byte(0xFF), cmd[6])

	// checksum = sum(bytes 0-6) mod 256
	var sum byte
	for _, b := range cmd[:7] {
		sum += b
	}
	require.Equal(t, sum, cmd[7])

	_, err = BuildPTZPresetCommand(PresetSet, 0)
	require.Error(t, err, "preset 0 is reserved")
	_, err = BuildPTZPresetCommand("bogus", 1)
	require.Error(t, err)
}

// TestBuildPTZCruiseCommand verifies the § A.3.5 cruise instruction layout:
// byte 3 = 84-88, byte 4 = cruise group, byte 6 = value.
func TestBuildPTZCruiseCommand(t *testing.T) {
	cmd, err := BuildPTZCruiseCommand(CruiseAddPoint, 1, 5)
	require.NoError(t, err)
	require.Equal(t, byte(0x84), cmd[3])
	require.Equal(t, byte(1), cmd[4])
	require.Equal(t, byte(5), cmd[6])

	cmd, err = BuildPTZCruiseCommand(CruiseStart, 2, 0)
	require.NoError(t, err)
	require.Equal(t, byte(0x88), cmd[3])
	require.Equal(t, byte(2), cmd[4])

	_, err = BuildPTZCruiseCommand(CruiseStart, 0, 0)
	require.Error(t, err, "cruise group 0 invalid")
	_, err = BuildPTZCruiseCommand("bogus", 1, 0)
	require.Error(t, err)
}

// TestBuildFICommand verifies the FI lens instruction layout against the
// GB/T 28181-2022 § A.3.3 表 A.6/A.7 examples: byte 3 = 0x40|bits (Bit3 iris
// close, Bit2 iris open, Bit1 focus near, Bit0 focus far), byte 5 = focus
// speed, byte 6 = iris speed. Checksums verified by hand.
func TestBuildFICommand(t *testing.T) {
	tests := []struct {
		name   string
		action string
		speed  byte
		want   []byte
	}{
		{name: "iris close", action: FIIrisClose, speed: 0x40, want: []byte{0xA5, 0x0F, 0x01, 0x48, 0x00, 0x40, 0x00, 0x3D}},
		{name: "iris open", action: FIIrisOpen, speed: 0x40, want: []byte{0xA5, 0x0F, 0x01, 0x44, 0x00, 0x40, 0x00, 0x39}},
		{name: "focus near", action: FIFocusNear, speed: 0x20, want: []byte{0xA5, 0x0F, 0x01, 0x42, 0x20, 0x00, 0x00, 0x17}},
		{name: "focus far", action: FIFocusFar, speed: 0x20, want: []byte{0xA5, 0x0F, 0x01, 0x41, 0x20, 0x00, 0x00, 0x16}},
		{name: "stop all lens motion", action: FILensStop, speed: 0x00, want: []byte{0xA5, 0x0F, 0x01, 0x40, 0x00, 0x00, 0x00, 0xF5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := BuildFICommand(tt.action, tt.speed)
			require.NoError(t, err)
			require.Equal(t, tt.want, cmd)

			var sum byte
			for _, b := range cmd[:7] {
				sum += b
			}
			require.Equal(t, sum, cmd[7], "checksum includes byte0")
		})
	}

	_, err := BuildFICommand("bogus", 0x20)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown FI lens action")
}

// TestBuildAuxSwitchCommand verifies the auxiliary switch instruction layout
// (GB/T 28181-2022 § A.3.7 表 A.11): byte 3 = 0x8C on / 0x8D off, byte 4 =
// switch number (1 = wiper per the table's note). Checksums verified by hand.
func TestBuildAuxSwitchCommand(t *testing.T) {
	cmd, err := BuildAuxSwitchCommand(AuxSwitchWiper, true)
	require.NoError(t, err)
	require.Equal(t, []byte{0xA5, 0x0F, 0x01, 0x8C, 0x01, 0x00, 0x00, 0x42}, cmd)

	cmd, err = BuildAuxSwitchCommand(AuxSwitchWiper, false)
	require.NoError(t, err)
	require.Equal(t, []byte{0xA5, 0x0F, 0x01, 0x8D, 0x01, 0x00, 0x00, 0x43}, cmd)

	cmd, err = BuildAuxSwitchCommand(AuxSwitchLight, true)
	require.NoError(t, err)
	require.Equal(t, []byte{0xA5, 0x0F, 0x01, 0x8C, 0x02, 0x00, 0x00, 0x43}, cmd)

	_, err = BuildAuxSwitchCommand(0, true)
	require.Error(t, err, "switch number 0 invalid")
}

func TestPTZ_SendFI_Success(t *testing.T) {
	c, sender, _ := newPTZTestEnv(t, 2)

	err := c.SendFI("34020000001320000001", FIIrisOpen, 0x40)
	require.NoError(t, err)
	require.Equal(t, "34020000001310000001", sender.deviceID)
	require.Contains(t, sender.body, "<PTZCmd>A50F014400400039</PTZCmd>")
}

func TestPTZ_SendFI_PTZUnsupported(t *testing.T) {
	c, _, _ := newPTZTestEnv(t, 0)
	err := c.SendFI("34020000001320000001", FIFocusNear, 0x20)
	require.ErrorIs(t, err, ErrPTZUnsupported)
}

// Aux switches are device-level (wiper/light) — a fixed camera without a
// pan/tilt head (PTZType 0) must still be controllable.
func TestPTZ_SendAuxSwitch_NoPTZTypeGate(t *testing.T) {
	c, sender, _ := newPTZTestEnv(t, 0)

	err := c.SendAuxSwitch("34020000001320000001", AuxSwitchWiper, true)
	require.NoError(t, err)
	require.Contains(t, sender.body, "<PTZCmd>A50F018C01000042</PTZCmd>")
}

func TestPTZ_SendAuxSwitch_InvalidSwitch(t *testing.T) {
	c, _, _ := newPTZTestEnv(t, 2)
	err := c.SendAuxSwitch("34020000001320000001", 0, true)
	require.Error(t, err)
}
