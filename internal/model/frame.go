package model

// FrameMsg represents a decoded video frame passed through streaming channels.
// It replaces the duplicated frameMsg types in flv, webrtc, and wsstream.
type FrameMsg struct {
	PTS        int64
	AU         [][]byte
	IsKeyframe bool
}
