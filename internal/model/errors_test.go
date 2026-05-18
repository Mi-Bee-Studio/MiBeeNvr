package model

import (
	"errors"
	"testing"
)

func TestCameraNotFoundError(t *testing.T) {
	t.Helper()
	err := &CameraNotFoundError{CameraID: "front-door"}
	if got := err.Error(); got != "camera not found: front-door" {
		t.Errorf("Error() = %q, want %q", got, "camera not found: front-door")
	}
	if got := err.Code(); got != "CAMERA_NOT_FOUND" {
		t.Errorf("Code() = %q, want %q", got, "CAMERA_NOT_FOUND")
	}
}

func TestCameraAlreadyRunningError(t *testing.T) {
	t.Helper()
	err := &CameraAlreadyRunningError{CameraID: "cam-1"}
	if got := err.Error(); got != "camera already running: cam-1" {
		t.Errorf("Error() = %q, want %q", got, "camera already running: cam-1")
	}
	if got := err.Code(); got != "CAMERA_ALREADY_RUNNING" {
		t.Errorf("Code() = %q, want %q", got, "CAMERA_ALREADY_RUNNING")
	}
}

func TestCameraDisabledError(t *testing.T) {
	t.Helper()
	err := &CameraDisabledError{CameraID: "cam-2"}
	if got := err.Error(); got != "camera is disabled: cam-2" {
		t.Errorf("Error() = %q, want %q", got, "camera is disabled: cam-2")
	}
	if got := err.Code(); got != "CAMERA_DISABLED" {
		t.Errorf("Code() = %q, want %q", got, "CAMERA_DISABLED")
	}
}

func TestRecordingNotFoundError(t *testing.T) {
	t.Helper()
	err := &RecordingNotFoundError{RecordingID: "rec-123"}
	if got := err.Error(); got != "recording not found: rec-123" {
		t.Errorf("Error() = %q, want %q", got, "recording not found: rec-123")
	}
	if got := err.Code(); got != "RECORDING_NOT_FOUND" {
		t.Errorf("Code() = %q, want %q", got, "RECORDING_NOT_FOUND")
	}
}

func TestStorageFullError(t *testing.T) {
	t.Helper()
	err := &StorageFullError{Message: "disk 95% full"}
	if got := err.Error(); got != "storage full: disk 95% full" {
		t.Errorf("Error() = %q, want %q", got, "storage full: disk 95% full")
	}
	if got := err.Code(); got != "STORAGE_FULL" {
		t.Errorf("Code() = %q, want %q", got, "STORAGE_FULL")
	}
}

func TestAuthFailedError(t *testing.T) {
	t.Helper()
	err := &AuthFailedError{Reason: "bad password"}
	if got := err.Error(); got != "authentication failed: bad password" {
		t.Errorf("Error() = %q, want %q", got, "authentication failed: bad password")
	}
	if got := err.Code(); got != "AUTH_FAILED" {
		t.Errorf("Code() = %q, want %q", got, "AUTH_FAILED")
	}
}

func TestErrAuthRequired(t *testing.T) {
	t.Helper()
	if got := ErrAuthRequired.Error(); got != "authentication required" {
		t.Errorf("Error() = %q, want %q", got, "authentication required")
	}
}

func TestInvalidInputError(t *testing.T) {
	t.Helper()
	err := &InvalidInputError{Message: "name is required"}
	if got := err.Error(); got != "invalid input: name is required" {
		t.Errorf("Error() = %q, want %q", got, "invalid input: name is required")
	}
	if got := err.Code(); got != "INVALID_INPUT" {
		t.Errorf("Code() = %q, want %q", got, "INVALID_INPUT")
	}
}

func TestPathTraversalError(t *testing.T) {
	t.Helper()
	err := &PathTraversalError{Path: "../../../etc/passwd"}
	if got := err.Error(); got != "path traversal detected" {
		t.Errorf("Error() = %q, want %q", got, "path traversal detected")
	}
	if got := err.Code(); got != "PATH_TRAVERSAL" {
		t.Errorf("Code() = %q, want %q", got, "PATH_TRAVERSAL")
	}
}

func TestHLSMaxStreamsError(t *testing.T) {
	t.Helper()
	err := &HLSMaxStreamsError{}
	if got := err.Error(); got != "maximum HLS streams reached" {
		t.Errorf("Error() = %q, want %q", got, "maximum HLS streams reached")
	}
	if got := err.Code(); got != "HLS_MAX_STREAMS" {
		t.Errorf("Code() = %q, want %q", got, "HLS_MAX_STREAMS")
	}
}

func TestHLSSupportedCodecError(t *testing.T) {
	t.Helper()
	err := &HLSSupportedCodecError{CameraID: "mjpeg-cam"}
	if got := err.Error(); got != "camera recorder does not support HLS" {
		t.Errorf("Error() = %q, want %q", got, "camera recorder does not support HLS")
	}
	if got := err.Code(); got != "HLS_UNSUPPORTED_CODEC" {
		t.Errorf("Code() = %q, want %q", got, "HLS_UNSUPPORTED_CODEC")
	}
}

func TestErrorCode(t *testing.T) {
	t.Helper()
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"coded error", &CameraNotFoundError{CameraID: "x"}, "CAMERA_NOT_FOUND"},
		{"auth required sentinel", ErrAuthRequired, "AUTH_REQUIRED"},
		{"plain error", errors.New("something"), "INTERNAL"},
		{"nil error", nil, "INTERNAL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Helper()
			if got := ErrorCode(tc.err); got != tc.want {
				t.Errorf("ErrorCode() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestErrorsIs(t *testing.T) {
	t.Helper()
	err := &CameraNotFoundError{CameraID: "cam-1"}
	if !errors.Is(err, err) {
		t.Error("errors.Is should match same pointer")
	}
}

func TestErrorsAs(t *testing.T) {
	t.Helper()
	err := &CameraNotFoundError{CameraID: "cam-1"}
	var target *CameraNotFoundError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match CameraNotFoundError")
	}
	if target.CameraID != "cam-1" {
		t.Errorf("CameraID = %q, want %q", target.CameraID, "cam-1")
	}

	var wrongTarget *RecordingNotFoundError
	if errors.As(err, &wrongTarget) {
		t.Error("errors.As should not match RecordingNotFoundError")
	}
}

func TestCodedErrorInterface(t *testing.T) {
	t.Helper()
	var camErr error = &CameraNotFoundError{CameraID: "x"}
	var ce CodedError
	if !errors.As(camErr, &ce) {
		t.Fatal("CameraNotFoundError should implement CodedError")
	}
	if ce.Code() != "CAMERA_NOT_FOUND" {
		t.Errorf("Code() = %q, want %q", ce.Code(), "CAMERA_NOT_FOUND")
	}
}
