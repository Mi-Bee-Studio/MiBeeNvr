package gb28181

import (
	"encoding/hex"
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

// PTZ preset instruction codes (GB/T 28181-2016 § A.3.4 表 A.8 — byte 4 of
// the command; identical in the 2022 revision). The preset number (1-255,
// 0 reserved) rides in byte 6.
const (
	ptzSetPreset    byte = 0x81 // 设置预置位
	ptzCallPreset   byte = 0x82 // 调用预置位
	ptzDeletePreset byte = 0x83 // 删除预置位
)

// Cruise instruction codes (§ A.3.5 表 A.9): cruise group in byte 5, the
// group's preset/value in byte 6 where applicable.
const (
	ptzAddCruisePoint byte = 0x84 // 加入巡航点
	ptzDelCruisePoint byte = 0x85 // 删除巡航点
	ptzCruiseSpeed    byte = 0x86 // 设置巡航速度
	ptzCruiseStayTime byte = 0x87 // 设置巡航停留时间
	ptzStartCruise    byte = 0x88 // 开始巡航
)

// PTZ preset/cruise actions accepted by BuildPTZPresetCommand /
// BuildPTZCruiseCommand.
const (
	PresetSet    = "set"
	PresetCall   = "call"
	PresetDelete = "delete"

	CruiseAddPoint = "add-point"
	CruiseDelPoint = "del-point"
	CruiseSpeed    = "speed"
	CruiseStay     = "stay"
	CruiseStart    = "start"
	CruiseStop     = "stop"
)

// BuildPTZPresetCommand builds a preset instruction: A5 0F 01 <81|82|83> 00
// <preset> <checksum>. Preset numbers are 1-255 (0 reserved per A.3.4).
func BuildPTZPresetCommand(action string, preset byte) ([]byte, error) {
	if preset == 0 {
		return nil, fmt.Errorf("gb28181: preset number 0 is reserved")
	}
	var code byte
	switch action {
	case PresetSet:
		code = ptzSetPreset
	case PresetCall:
		code = ptzCallPreset
	case PresetDelete:
		code = ptzDeletePreset
	default:
		return nil, fmt.Errorf("gb28181: unknown PTZ preset action %q", action)
	}
	cmd := [8]byte{0xA5, 0x0F, 0x01, code, 0x00, 0x00, preset, 0x00}
	return finishPTZCommand(cmd), nil
}

// BuildPTZCruiseCommand builds a cruise instruction: A5 0F 01 <code>
// <cruise group> <value> <checksum>. CruiseStop is not defined by the
// standard as a dedicated code — callers send the plain stop command.
func BuildPTZCruiseCommand(action string, cruise, value byte) ([]byte, error) {
	if cruise == 0 {
		return nil, fmt.Errorf("gb28181: cruise group must be 1-255")
	}
	var code byte
	switch action {
	case CruiseAddPoint:
		code = ptzAddCruisePoint
	case CruiseDelPoint:
		code = ptzDelCruisePoint
	case CruiseSpeed:
		code = ptzCruiseSpeed
	case CruiseStay:
		code = ptzCruiseStayTime
	case CruiseStart:
		code = ptzStartCruise
	default:
		return nil, fmt.Errorf("gb28181: unknown PTZ cruise action %q", action)
	}
	cmd := [8]byte{0xA5, 0x0F, 0x01, code, cruise, 0x00, value, 0x00}
	return finishPTZCommand(cmd), nil
}

// FI (Focus/Iris) lens instruction bits (GB/T 28181-2022 § A.3.3 表 A.6 —
// byte 4 low nibble of the command; the 2016 revision numbers the tables
// identically). Iris and focus may combine, but never opposite directions of
// the same lens axis (Bit3/Bit2 and Bit1/Bit0 are mutually exclusive pairs).
const (
	fiIrisCloseBits byte = 0x08 // 光圈缩小
	fiIrisOpenBits  byte = 0x04 // 光圈放大
	fiFocusNearBits byte = 0x02 // 聚焦近
	fiFocusFarBits  byte = 0x01 // 聚焦远
)

// FI lens actions accepted by BuildFICommand / SendFI.
const (
	FIIrisClose = "iris-close" // 光圈缩小
	FIIrisOpen  = "iris-open"  // 光圈放大
	FIFocusNear = "focus-near" // 聚焦近
	FIFocusFar  = "focus-far"  // 聚焦远
	FILensStop  = "stop"       // 镜头所有动作停止 (byte4=40H)
)

// fiActionBits maps a single FI action to its byte-4 bits.
var fiActionBits = map[string]byte{
	FIIrisClose: fiIrisCloseBits,
	FIIrisOpen:  fiIrisOpenBits,
	FIFocusNear: fiFocusNearBits,
	FIFocusFar:  fiFocusFarBits,
	FILensStop:  0x00,
}

// BuildFICommand builds an FI lens instruction (GB/T 28181-2022 § A.3.3
// 表 A.7): A5 0F 01 <0x40|bits> <focus speed> <iris speed> 00 <checksum>.
// Byte 5 (cmd[4]) is the focus speed, byte 6 (cmd[5]) the iris speed
// (00H-FFH slow→fast); only the acting axis's speed byte is set, mirroring
// BuildPTZCommand.
func BuildFICommand(action string, speed byte) ([]byte, error) {
	bits, ok := fiActionBits[action]
	if !ok {
		return nil, fmt.Errorf("gb28181: unknown FI lens action %q", action)
	}
	cmd := [8]byte{0xA5, 0x0F, 0x01, 0x40 | bits, 0x00, 0x00, 0x00, 0x00}
	switch action {
	case FIIrisClose, FIIrisOpen:
		cmd[5] = speed // 光圈速度 (字节6)
	case FIFocusNear, FIFocusFar:
		cmd[4] = speed // 聚焦速度 (字节5)
	case FILensStop:
		// all speed bytes stay 0
	}
	return finishPTZCommand(cmd), nil
}

// Auxiliary switch codes (GB/T 28181-2022 § A.3.7 表 A.11): byte 4 = 0x8C
// (switch on) / 0x8D (switch off), byte 5 = the switch number. The standard
// fixes number 1 = wiper (雨刷); light/ heater numbering is vendor convention
// with 2 = light the de-facto mainstream value.
const (
	ptzAuxSwitchOn  byte = 0x8C
	ptzAuxSwitchOff byte = 0x8D
)

// Fixed / conventional auxiliary switch numbers.
const (
	AuxSwitchWiper byte = 1 // 雨刷 — spec-fixed (表 A.11 注)
	AuxSwitchLight byte = 2 // 灯光/补光 — de-facto convention
)

// BuildAuxSwitchCommand builds an auxiliary switch instruction: A5 0F 01
// <8C|8D> <switch no> 00 00 <checksum>. Switch numbers are 1-255.
func BuildAuxSwitchCommand(switchNo byte, on bool) ([]byte, error) {
	if switchNo == 0 {
		return nil, fmt.Errorf("gb28181: auxiliary switch number must be 1-255")
	}
	code := ptzAuxSwitchOff
	if on {
		code = ptzAuxSwitchOn
	}
	cmd := [8]byte{0xA5, 0x0F, 0x01, code, switchNo, 0x00, 0x00, 0x00}
	return finishPTZCommand(cmd), nil
}

// finishPTZCommand fills the checksum byte (mod-256 sum of bytes 0-6).
func finishPTZCommand(cmd [8]byte) []byte {
	var sum byte
	for _, b := range cmd[:7] {
		sum += b
	}
	cmd[7] = sum
	return cmd[:]
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

// locateChannel resolves a channel ID across all registered devices,
// returning the channel and its owning device.
func (c *PTZController) locateChannel(channelID string) (*Channel, *Device, error) {
	for _, d := range c.devices.AllDevices() {
		if found, ok := c.devices.FindChannel(d.ID, channelID); ok {
			return found, d, nil
		}
	}
	return nil, nil, ErrChannelNotFound
}

// SendPTZ sends a PTZ command for channelID. The channel is located across all
// registered devices. Errors cover unknown channel, offline device, missing
// PTZ capability, pan/tilt-only devices rejecting zoom, and send failures.
func (c *PTZController) SendPTZ(channelID, direction string, speed byte) error {
	ch, dev, err := c.locateChannel(channelID)
	if err != nil {
		return err
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
	return c.sendCommand(ch, cmd)
}

// SendPTZPreset sends a preset instruction (set/call/delete) for channelID.
// Preset numbers are 1-255 (0 reserved).
func (c *PTZController) SendPTZPreset(channelID, action string, preset byte) error {
	ch, dev, err := c.locateChannel(channelID)
	if err != nil {
		return err
	}
	if dev.Status.Load() != DeviceOnline {
		return ErrDeviceOffline
	}
	if ch.PTZType == 0 {
		return ErrPTZUnsupported
	}
	cmd, err := BuildPTZPresetCommand(action, preset)
	if err != nil {
		return err
	}
	return c.sendCommand(ch, cmd)
}

// SendPTZCruise sends a cruise instruction for channelID (cruise group
// 1-255; value = preset number / speed / stay-time depending on action).
func (c *PTZController) SendPTZCruise(channelID, action string, cruise, value byte) error {
	ch, dev, err := c.locateChannel(channelID)
	if err != nil {
		return err
	}
	if dev.Status.Load() != DeviceOnline {
		return ErrDeviceOffline
	}
	if ch.PTZType == 0 {
		return ErrPTZUnsupported
	}
	if action == CruiseStop {
		return c.SendPTZ(channelID, DirStop, 0)
	}
	cmd, err := BuildPTZCruiseCommand(action, cruise, value)
	if err != nil {
		return err
	}
	return c.sendCommand(ch, cmd)
}

// SendFI sends an FI lens instruction (iris open/close, focus near/far, stop)
// for channelID. Requires a PTZ-capable channel, like the preset/cruise paths.
func (c *PTZController) SendFI(channelID, action string, speed byte) error {
	ch, dev, err := c.locateChannel(channelID)
	if err != nil {
		return err
	}
	if dev.Status.Load() != DeviceOnline {
		return ErrDeviceOffline
	}
	if ch.PTZType == 0 {
		return ErrPTZUnsupported
	}
	cmd, err := BuildFICommand(action, speed)
	if err != nil {
		return err
	}
	return c.sendCommand(ch, cmd)
}

// SendAuxSwitch toggles an auxiliary switch (wiper/light, GB/T 28181-2022
// § A.3.7) on channelID. Not gated on PTZType — a fixed camera can carry a
// wiper or light without a pan/tilt head.
func (c *PTZController) SendAuxSwitch(channelID string, switchNo byte, on bool) error {
	ch, dev, err := c.locateChannel(channelID)
	if err != nil {
		return err
	}
	if dev.Status.Load() != DeviceOnline {
		return ErrDeviceOffline
	}
	cmd, err := BuildAuxSwitchCommand(switchNo, on)
	if err != nil {
		return err
	}
	return c.sendCommand(ch, cmd)
}

// sendCommand transmits an 8-byte PTZ instruction wrapped in a DeviceControl
// MANSCDP body.
func (c *PTZController) sendCommand(ch *Channel, cmd []byte) error {
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
	if err := c.sender.SendMessage(ch.DeviceID, body); err != nil {
		return fmt.Errorf("gb28181: send PTZ to %s: %w", ch.DeviceID, err)
	}
	return nil
}

// ptzCmdString formats the 8-byte PTZ command as continuous uppercase hex
// (e.g. "A50F0108002000DD") — the PTZCmd form mainstream GB/T 28181
// implementations (wvp-pro, ZLMediaKit) emit and devices parse; some
// firmwares reject the space-separated form.
func ptzCmdString(cmd []byte) string {
	return strings.ToUpper(hex.EncodeToString(cmd))
}
