package flv

import "errors"

var (
	// ErrMaxViewers is returned when the maximum number of concurrent viewers is reached.
	ErrMaxViewers = errors.New("maximum FLV viewers reached")
	// ErrStreamNotActive is returned when attempting to write to a non-active stream.
	ErrStreamNotActive = errors.New("FLV stream not active")
	// ErrStreamExists is returned when a stream is already registered.
	ErrStreamExists = errors.New("FLV stream already exists")
)
