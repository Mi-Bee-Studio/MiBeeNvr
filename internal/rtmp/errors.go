package rtmp

import "errors"

var (
	// ErrInvalidStreamKey is returned when the stream key doesn't match any configured camera.
	ErrInvalidStreamKey = errors.New("invalid stream key")

	// ErrServerClosed is returned when the RTMP server is stopped.
	ErrServerClosed = errors.New("rtmp server closed")

	// ErrNoVideoTrack is returned when no H.264 video track is found in the RTMP stream.
	ErrNoVideoTrack = errors.New("no H.264 video track found")

	// ErrAlreadyPublishing is returned when a stream key is already being published.
	ErrAlreadyPublishing = errors.New("stream key already publishing")
)
