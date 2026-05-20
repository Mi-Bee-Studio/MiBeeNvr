package webrtc

import "errors"

var (
	// ErrMaxPeersReached is returned when the maximum number of concurrent
	// WebRTC peers is reached for a camera.
	ErrMaxPeersReached = errors.New("maximum WebRTC peers reached for camera")
	// ErrSessionNotFound is returned when the requested WHEP session does not exist.
	ErrSessionNotFound = errors.New("WebRTC session not found")
	// ErrUnsupportedCodec is returned when the codec is not supported for WebRTC.
	ErrUnsupportedCodec = errors.New("unsupported codec for WebRTC (H.264 only)")
)
