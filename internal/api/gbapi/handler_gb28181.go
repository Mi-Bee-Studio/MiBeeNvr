package gbapi

// GB28181 wiring on Handler: the capability interfaces the platform package
// implements, the setters that attach them, the session-status DTO and the
// route registration for the GB28181 endpoints. Split out of handler.go.

import (
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api/streamsapi"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/mickeyzzc/gb28181-go/manscdp"
	"github.com/mickeyzzc/gb28181-go/platform"
)

// GB28181CascadeStatus reports the lower-level cascade client's registration
// state. Implemented by the cascade service; declared here to avoid importing
// cascade in Handler.
type GB28181CascadeStatus interface {
	Online() bool
	RegistrationSince() (time.Duration, bool)
	ForwardCount() int
}

// SetGB28181Cascade wires the cascade client for the status endpoint.

// SetGB28181Timezone pins the zone used to interpret/format naive GB/T 28181
// device-clock timestamps (RecordInfo entries). nil keeps time.Local.

// GB28181InviteSender sends a SIP INVITE to start a media session on a channel.
// Implemented by the SIP server; declared here to avoid importing sip in Handler.
type GB28181InviteSender interface {
	InviteChannel(deviceID, channelID string) error
}

// SetGB28181Inviter wires the SIP server for the channel INVITE endpoint.

// GB28181SessionStatus mirrors the GB library's session observability for
// the flow view. The struct lives in streamsapi (flow-response data); this
// alias keeps the setter signature and app-layer references unchanged.
type GB28181SessionStatus = streamsapi.GB28181SessionStatus

// SetGB28181Sessions wires the GB28181 session observability provider
// (live session snapshot + stall-watchdog recycle count).

// GB28181ByeSender stops a channel media session end-to-end (SIP BYE to the
// device, local receiver teardown, bound-camera recorder state). Implemented
// by the SIP server; declared here to avoid importing sip in Handler.
type GB28181ByeSender interface {
	ByeChannelByID(channelID string) error
}

// SetGB28181ByeSender wires the SIP server for the channel BYE endpoint.

// GB28181DeviceMedia is the device-side recording API (RecordInfo query,
// playback INVITE fetch, MANSRTSP control) implemented by the SIP server;
// declared here to avoid importing sip in Handler (#337).
type GB28181DeviceMedia interface {
	// QueryChannelRecords sends a RecordInfo query and collects the paged
	// responses for a time range.
	QueryChannelRecords(deviceID, channelID string, start, end time.Time) ([]manscdp.RecordItem, error)
	// StartPlayback starts a fetch that muxes the device recording into the
	// normal recordings pipeline of the bound camera.
	StartPlayback(deviceID, channelID string, start, end time.Time) error
	// StartDownload starts an s=Download fetch (file-speed transfer, #378) —
	// media lands in the recordings pipeline like a playback fetch.
	StartDownload(deviceID, channelID string, start, end time.Time) error
	// StopPlayback stops a fetch (SIP BYE + finalize).
	StopPlayback(channelID string) error
	// PlaybackStatusFor reports fetch progress (ok=false when idle).
	PlaybackStatusFor(channelID string) (platform.PlaybackInfo, bool)
	// PlaybackControl sends a MANSRTSP control (pause/resume/seek).
	PlaybackControl(channelID, action string, scale, position float64) error
	// StartTalk establishes a voice intercom with a channel (audio-only
	// INVITE; #341). Idempotent.
	StartTalk(cameraID, deviceID, channelID string) error
	// StopTalk ends the intercom (SIP BYE + socket close).
	StopTalk(channelID string) error
	// WriteTalkAudio packetizes one G.711 A-law frame to the device.
	WriteTalkAudio(channelID string, alaw []byte)
	// TalkStatusFor reports the intercom state of a camera.
	TalkStatusFor(cameraID string) platform.TalkStatus
	// SubscribeTalkUplink returns the device's return-audio frames (the
	// sendrecv uplink leg, gb28181-go #141) for the intercom WebSocket to
	// push back to the browser, plus an unsubscribe func. A nil channel
	// means the platform has no uplink consumer wired.
	SubscribeTalkUplink(channelID string) (<-chan []byte, func())
	// GB28181Alarms returns the device's recent alarms (latest first).
	GB28181Alarms(deviceID string) []event.GB28181AlarmEvent
	// GB28181Positions returns the device's recent mobile positions.
	GB28181Positions(deviceID string) []platform.GBPosition
}

// SetGB28181DeviceMedia wires the SIP server for the device-recording
// endpoints.

// registerGB28181Routes registers GB28181 device and channel endpoints.
