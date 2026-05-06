package hls

import "errors"

var (
	// ErrMaxStreamsReached is returned when the maximum number of concurrent HLS streams is reached.
	ErrMaxStreamsReached = errors.New("maximum HLS streams reached")
	// ErrStreamNotActive is returned when attempting to write to a non-active stream.
	ErrStreamNotActive = errors.New("HLS stream not active")
	// ErrUnsupportedProtocol is returned when HLS is requested for an unsupported camera protocol.
	ErrUnsupportedProtocol = errors.New("camera protocol does not support HLS")
)
