package gb28181

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// PTZ direction identifiers accepted by SendPTZ and the PTZ API.
const (
	DirUp        = "up"
	DirDown      = "down"
	DirLeft      = "left"
	DirRight     = "right"
	DirUpLeft    = "up-left"
	DirUpRight   = "up-right"
	DirDownLeft  = "down-left"
	DirDownRight = "down-right"
	DirZoomIn    = "zoom-in"
	DirZoomOut   = "zoom-out"
	DirStop      = "stop"
)

// ptzCommandCodes maps PTZ direction identifiers to the GB/T 28181-2016
// § A.4 instruction/direction bits (byte3 of the PTZ command). Diagonal
// directions combine the two axis bits (up-left = up|left, etc.); 0x00
// means stop.
var ptzCommandCodes = map[string]byte{
	DirStop:      0x00,
	DirUp:        0x08,
	DirDown:      0x04,
	DirLeft:      0x02,
	DirRight:     0x01,
	DirUpLeft:    0x08 | 0x02,
	DirUpRight:   0x08 | 0x01,
	DirDownLeft:  0x04 | 0x02,
	DirDownRight: 0x04 | 0x01,
	DirZoomIn:    0x10,
	DirZoomOut:   0x20,
}

// BuildPTZCommand builds the 8-byte GB/T 28181-2016 § A.4 PTZ command:
// [0]=0xA5 start, [1]=0x0F combination, [2]=0x01 address, [3]=direction bits,
// [4]=pan speed, [5]=tilt speed, [6]=zoom speed, [7]=checksum (sum of bytes
// 0-6, mod 256, byte0 INCLUDED). Only the moving axes' speed bytes are set
// (pan for left/right, tilt for up/down, both for diagonals, zoom for
// zoom-in/out). Stop is a separate command with byte3=0x00 and all speed
// bytes 0.
func BuildPTZCommand(direction string, speed byte) ([]byte, error) {
	bits, ok := ptzCommandCodes[direction]
	if !ok {
		return nil, fmt.Errorf("gb28181: unknown PTZ direction %q", direction)
	}
	cmd := [8]byte{0xA5, 0x0F, 0x01, bits, 0x00, 0x00, 0x00, 0x00}
	switch direction {
	case DirUp, DirDown:
		cmd[5] = speed // tilt
	case DirLeft, DirRight:
		cmd[4] = speed // pan
	case DirUpLeft, DirUpRight, DirDownLeft, DirDownRight:
		cmd[4] = speed // pan
		cmd[5] = speed // tilt
	case DirZoomIn, DirZoomOut:
		cmd[6] = speed // zoom
	case DirStop:
		// all speed bytes stay 0
	}
	var sum byte
	for _, b := range cmd[:7] {
		sum += b
	}
	cmd[7] = sum
	return cmd[:], nil
}

// MessageSender sends a SIP MESSAGE body to a GB28181 device. Implemented by
// the sip.Server; declared here so the controller does not import sip (which
// would be an import cycle: sip already imports this package).
type MessageSender interface {
	SendMessage(deviceID string, body []byte) error
}

// Sentinel errors returned by SendPTZ so callers (the HTTP handler) can map
// them to status codes.
var (
	// ErrChannelNotFound means no registered channel has the requested ID.
	ErrChannelNotFound = fmt.Errorf("gb28181: channel not found")
	// ErrDeviceOffline means the channel's device is not currently registered/online.
	ErrDeviceOffline = fmt.Errorf("gb28181: device offline")
	// ErrPTZUnsupported means the channel reports PTZType 0 (no PTZ).
	ErrPTZUnsupported = fmt.Errorf("gb28181: channel does not support PTZ")
	// ErrZoomUnsupported means a pan/tilt-only channel (PTZType 1) got a zoom command.
	ErrZoomUnsupported = fmt.Errorf("gb28181: channel does not support zoom")
)

// PTZController sends GB/T 28181 PTZ control commands to registered channels
// over the SIP MESSAGE transport. It validates capability (PTZType) and
// device liveness before sending.
type PTZController struct {
	devices *DeviceManager
	sender  MessageSender
	seq     atomic.Int64 // MANSCDP SN sequence
}

// NewPTZController creates a controller sending through sender.
func NewPTZController(devices *DeviceManager, sender MessageSender) *PTZController {
	return &PTZController{devices: devices, sender: sender}
}

// SendPTZ sends a PTZ command for channelID. The channel is located across all
// registered devices. Errors cover unknown channel, offline device, missing
// PTZ capability, pan/tilt-only devices rejecting zoom, and send failures.
func (c *PTZController) SendPTZ(channelID, direction string, speed byte) error {
	var ch *Channel
	var dev *Device
	for _, d := range c.devices.AllDevices() {
		if found, ok := c.devices.FindChannel(d.ID, channelID); ok {
			ch = found
			dev = d
			break
		}
	}
	if ch == nil {
		return ErrChannelNotFound
	}
	if dev.Status.Load() != DeviceOnline {
		return ErrDeviceOffline
	}
	if ch.PTZType == 0 {
		return ErrPTZUnsupported
	}
	if (direction == DirZoomIn || direction == DirZoomOut) && ch.PTZType == 1 {
		return ErrZoomUnsupported
	}

	cmd, err := BuildPTZCommand(direction, speed)
	if err != nil {
		return err
	}
	sn := c.seq.Add(1)
	// GB/T 28181-2016: DeviceControl uses child elements (not attributes) for
	// CmdType/SN — see CatalogController.RequestCatalog for the same pattern.
	body := []byte(fmt.Sprintf(`<?xml version="1.0" encoding="GB2312"?>
<Control>
<CmdType>DeviceControl</CmdType>
<SN>%d</SN>
<DeviceID>%s</DeviceID>
<PTZCmd>%s</PTZCmd>
</Control>`, sn, ch.ID, ptzCmdString(cmd)))
	if err := c.sender.SendMessage(dev.ID, body); err != nil {
		return fmt.Errorf("gb28181: send PTZ to %s: %w", dev.ID, err)
	}
	return nil
}

// ptzCmdString formats the 8-byte PTZ command as space-separated uppercase hex
// (e.g. "A5 0F 01 08 00 20 00 DD"), the PTZCmd value GB/T 28181 devices expect.
func ptzCmdString(cmd []byte) string {
	parts := make([]string, len(cmd))
	for i, b := range cmd {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, " ")
}
