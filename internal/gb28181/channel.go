package gb28181

import "sync/atomic"

// Channel state values (Channel.Status).
const (
	// ChannelIdle means no media session is active for the channel.
	ChannelIdle int32 = 0
	// ChannelInviting means an INVITE was sent but the device has not
	// answered with a successful 200 OK / media start yet.
	ChannelInviting int32 = 1
	// ChannelPlaying means an INVITE succeeded and RTP is being received.
	ChannelPlaying int32 = 2
)

// Channel represents a video channel within a GB28181 device. Channels are
// created from the device's Catalog response (see RegisterChannel) and their
// Status transitions are driven by the session manager (idle → inviting →
// playing → idle).
type Channel struct {
	DeviceID string
	ID       string
	Name     string
	Parental int // 0=parental, 1=subordinate
	Status   atomic.Int32
	Device   *Device // back-reference, set by DeviceManager.RegisterChannel
	// PTZType is the GB/T 28181-2016 § 5.4.12 PTZ capability code from the
	// Catalog item: 0 = no PTZ, 1 = pan/tilt, 2 = pan/tilt + zoom.
	PTZType int
	// SubProbe marks a channel that was NOT in the device catalog but was
	// registered by the sub-channel prober (#560) from the vendor-convention
	// code offset. Catalog refreshes never list it, so the flag distinguishes
	// "real channel the device advertises" from "synthetic pull target".
	SubProbe bool
}
