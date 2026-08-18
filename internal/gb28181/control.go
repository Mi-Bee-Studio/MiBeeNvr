package gb28181

// Non-PTZ DeviceControl instructions (GB/T 28181-2016 § 9.3.2, #379):
// remote device-side record start/stop, arm/disarm, alarm reset, reboot,
// and home-position reset. Sent as Control messages via the PTZController's
// channel-resolution + SIP MESSAGE plumbing.

import (
	"fmt"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/manscdp"
)

// Valid RecordCmd values.
const (
	RecordCmdStart = "Record"
	RecordCmdStop  = "StopRecord"
)

// Valid GuardCmd values.
const (
	GuardCmdSet   = "SetGuard"
	GuardCmdReset = "ResetGuard"
)

// SendDeviceControl resolves channelID across registered devices and sends a
// non-PTZ DeviceControl. element is the XML child carrying the command
// ("RecordCmd"/"GuardCmd"/"AlarmCmd"/"TeleBoot"/"HomePosition"); value its
// text content ("" for flag-style elements).
func (c *PTZController) SendDeviceControl(channelID, element, value string) error {
	var ch *Channel
	for _, d := range c.devices.AllDevices() {
		if found, ok := c.devices.FindChannel(d.ID, channelID); ok {
			ch = found
			break
		}
	}
	if ch == nil {
		return ErrChannelNotFound
	}
	if d, ok := c.devices.Device(ch.DeviceID); !ok || d.Status.Load() != DeviceOnline {
		return ErrDeviceOffline
	}

	dc := manscdp.DeviceControl{
		CmdType:  manscdp.CmdDeviceControl,
		SN:       int(c.seq.Add(1)),
		DeviceID: ch.ID,
	}
	switch element {
	case "RecordCmd":
		dc.RecordCmd = value
	case "GuardCmd":
		dc.GuardCmd = value
	case "AlarmCmd":
		dc.AlarmCmd = value
	case "TeleBoot":
		dc.TeleBoot = value
	case "HomePosition":
		dc.HomePosition = value
	default:
		return fmt.Errorf("gb28181: unsupported DeviceControl element %q", element)
	}
	body, err := manscdp.Encode(dc)
	if err != nil {
		return fmt.Errorf("gb28181: encode DeviceControl: %w", err)
	}
	if err := c.sender.SendMessage(ch.DeviceID, body); err != nil {
		return fmt.Errorf("gb28181: send DeviceControl to %s: %w", ch.DeviceID, err)
	}
	return nil
}
