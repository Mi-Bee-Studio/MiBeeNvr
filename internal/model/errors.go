package model

import "errors"

// --- Camera errors ---

// CameraNotFoundError indicates the requested camera does not exist.
type CameraNotFoundError struct {
	CameraID string
}

func (e *CameraNotFoundError) Error() string { return "camera not found: " + e.CameraID }
func (e *CameraNotFoundError) Code() string  { return "CAMERA_NOT_FOUND" }

// CameraAlreadyRunningError indicates the camera recorder is already active.
type CameraAlreadyRunningError struct {
	CameraID string
}

func (e *CameraAlreadyRunningError) Error() string { return "camera already running: " + e.CameraID }
func (e *CameraAlreadyRunningError) Code() string  { return "CAMERA_ALREADY_RUNNING" }

// CameraDisabledError indicates the camera is disabled and cannot be started.
type CameraDisabledError struct {
	CameraID string
}

func (e *CameraDisabledError) Error() string { return "camera is disabled: " + e.CameraID }
func (e *CameraDisabledError) Code() string  { return "CAMERA_DISABLED" }

// --- Recording errors ---

// RecordingNotFoundError indicates the requested recording does not exist.
type RecordingNotFoundError struct {
	RecordingID string
}

func (e *RecordingNotFoundError) Error() string { return "recording not found: " + e.RecordingID }
func (e *RecordingNotFoundError) Code() string  { return "RECORDING_NOT_FOUND" }

// StorageFullError indicates disk space is critically low.
type StorageFullError struct {
	Message string
}

func (e *StorageFullError) Error() string { return "storage full: " + e.Message }
func (e *StorageFullError) Code() string  { return "STORAGE_FULL" }

// --- Auth errors ---

// ErrAuthRequired indicates authentication is required but not provided.
var ErrAuthRequired = errors.New("authentication required")

// AuthFailedError indicates authentication credentials were rejected.
type AuthFailedError struct {
	Reason string
}

func (e *AuthFailedError) Error() string { return "authentication failed: " + e.Reason }
func (e *AuthFailedError) Code() string  { return "AUTH_FAILED" }

// --- Input validation errors ---

// InvalidInputError indicates the request contains invalid parameters.
type InvalidInputError struct {
	Message string
}

func (e *InvalidInputError) Error() string { return "invalid input: " + e.Message }
func (e *InvalidInputError) Code() string  { return "INVALID_INPUT" }

// PathTraversalError indicates a path traversal attempt was detected.
type PathTraversalError struct {
	Path string
}

func (e *PathTraversalError) Error() string { return "path traversal detected" }
func (e *PathTraversalError) Code() string  { return "PATH_TRAVERSAL" }

// --- HLS errors ---

// HLSMaxStreamsError indicates the maximum concurrent HLS stream limit was reached.
type HLSMaxStreamsError struct{}

func (e *HLSMaxStreamsError) Error() string { return "maximum HLS streams reached" }
func (e *HLSMaxStreamsError) Code() string  { return "HLS_MAX_STREAMS" }

// HLSSupportedCodecError indicates the camera codec is not supported for HLS streaming.
type HLSSupportedCodecError struct {
	CameraID string
}

func (e *HLSSupportedCodecError) Error() string {
	return "camera recorder does not support HLS"
}
func (e *HLSSupportedCodecError) Code() string { return "HLS_UNSUPPORTED_CODEC" }

// --- CodedError interface ---

// CodedError is an error that includes a machine-readable error code.
type CodedError interface {
	error
	Code() string
}

// ErrorCode extracts the error code from a CodedError, or returns "INTERNAL" if unavailable.
func ErrorCode(err error) string {
	var ce CodedError
	if errors.As(err, &ce) {
		return ce.Code()
	}
	if errors.Is(err, ErrAuthRequired) {
		return "AUTH_REQUIRED"
	}
	return "INTERNAL"
}
