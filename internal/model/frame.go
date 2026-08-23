package model

// FrameMsg represents a decoded video frame passed through streaming channels.
// It replaces the duplicated frameMsg types in flv, webrtc, and wsstream.
type FrameMsg struct {
	PTS        int64
	AU         [][]byte
	IsKeyframe bool
	// IngestAt is the hub-entry wallclock (unix nanoseconds), stamped when the
	// frame is enqueued to a consumer. Live consumers (wsstream) relay it so the
	// player can measure end-to-end live latency. Zero means unset.
	IngestAt int64
}
