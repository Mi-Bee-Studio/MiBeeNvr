package model

import (
	"errors"
	"fmt"
	"testing"
)

func TestCameraNotFoundError(t *testing.T) {
	t.Helper()
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	if got := ErrAuthRequired.Error(); got != "authentication required" {
		t.Errorf("Error() = %q, want %q", got, "authentication required")
	}
}

func TestInvalidInputError(t *testing.T) {
	t.Helper()
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	err := &CameraNotFoundError{CameraID: "cam-1"}
	if !errors.Is(err, err) {
		t.Error("errors.Is should match same pointer")
	}
}

func TestErrorsAs(t *testing.T) {
	t.Helper()
	t.Parallel()
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
	t.Parallel()
	var camErr error = &CameraNotFoundError{CameraID: "x"}
	var ce CodedError
	if !errors.As(camErr, &ce) {
		t.Fatal("CameraNotFoundError should implement CodedError")
	}
	if ce.Code() != "CAMERA_NOT_FOUND" {
		t.Errorf("Code() = %q, want %q", ce.Code(), "CAMERA_NOT_FOUND")
	}
}

// --- ONVIF error tests ---

func TestCameraAlreadyExistsError(t *testing.T) {
	t.Helper()
	t.Parallel()
	err := &CameraAlreadyExistsError{CameraID: "front-door"}
	if got := err.Error(); got != "camera already exists: front-door" {
		t.Errorf("Error() = %q, want %q", got, "camera already exists: front-door")
	}
	if got := err.Code(); got != "CAMERA_ALREADY_EXISTS" {
		t.Errorf("Code() = %q, want %q", got, "CAMERA_ALREADY_EXISTS")
	}
}

func TestONVIFNotCameraError(t *testing.T) {
	t.Helper()
	t.Parallel()
	err := &ONVIFNotCameraError{CameraID: "cam-1"}
	if got := err.Error(); got != "camera is not an ONVIF device: cam-1" {
		t.Errorf("Error() = %q, want %q", got, "camera is not an ONVIF device: cam-1")
	}
	if got := err.Code(); got != "ONVIF_NOT_CAMERA" {
		t.Errorf("Code() = %q, want %q", got, "ONVIF_NOT_CAMERA")
	}
}

func TestONVIFConnectionError(t *testing.T) {
	t.Helper()
	t.Parallel()
	inner := errors.New("timeout")
	err := &ONVIFConnectionError{CameraID: "cam-1", Err: inner}
	if got := err.Error(); got != "connect to ONVIF camera cam-1: timeout" {
		t.Errorf("Error() = %q, want %q", got, "connect to ONVIF camera cam-1: timeout")
	}
	if got := err.Code(); got != "ONVIF_CONNECTION_FAILED" {
		t.Errorf("Code() = %q, want %q", got, "ONVIF_CONNECTION_FAILED")
	}
	// Test Unwrap
	if !errors.Is(err, inner) {
		t.Error("errors.Is should match wrapped error")
	}
}

func TestONVIFNoProfilesError(t *testing.T) {
	t.Helper()
	t.Parallel()
	err := &ONVIFNoProfilesError{CameraID: "cam-1"}
	if got := err.Error(); got != "no media profiles found for camera: cam-1" {
		t.Errorf("Error() = %q, want %q", got, "no media profiles found for camera: cam-1")
	}
	if got := err.Code(); got != "ONVIF_NO_PROFILES" {
		t.Errorf("Code() = %q, want %q", got, "ONVIF_NO_PROFILES")
	}
}

// --- Error wrapping tests ---

func TestErrorWrappingWithFmtErrorf(t *testing.T) {
	t.Helper()
	t.Parallel()
	original := &CameraNotFoundError{CameraID: "cam-1"}
	wrapped := fmt.Errorf("operation failed: %w", original)
	var target *CameraNotFoundError
	if !errors.As(wrapped, &target) {
		t.Fatal("errors.As should unwrap CameraNotFoundError")
	}
	if target.CameraID != "cam-1" {
		t.Errorf("CameraID = %q, want %q", target.CameraID, "cam-1")
	}
}

func TestErrorWrappingDouble(t *testing.T) {
	t.Helper()
	t.Parallel()
	inner := &CameraNotFoundError{CameraID: "cam-1"}
	middle := fmt.Errorf("middle: %w", inner)
	outer := fmt.Errorf("outer: %w", middle)
	var target *CameraNotFoundError
	if !errors.As(outer, &target) {
		t.Fatal("errors.As should unwrap through double wrapping")
	}
}

func TestErrorCodeWithWrapping(t *testing.T) {
	t.Helper()
	t.Parallel()
	original := &CameraNotFoundError{CameraID: "cam-1"}
	wrapped := fmt.Errorf("failed: %w", original)
	if got := ErrorCode(wrapped); got != "CAMERA_NOT_FOUND" {
		t.Errorf("ErrorCode() = %q, want %q", got, "CAMERA_NOT_FOUND")
	}
}

func TestErrorCodeWithONVIFConnectionError(t *testing.T) {
	t.Helper()
	t.Parallel()
	err := &ONVIFConnectionError{CameraID: "cam-1", Err: errors.New("timeout")}
	wrapped := fmt.Errorf("failed: %w", err)
	if got := ErrorCode(wrapped); got != "ONVIF_CONNECTION_FAILED" {
		t.Errorf("ErrorCode() = %q, want %q", got, "ONVIF_CONNECTION_FAILED")
	}
}

// --- All error types implement CodedError ---

func TestAllErrorsImplementCodedError(t *testing.T) {
	t.Helper()
	t.Parallel()
	errors := []CodedError{
		&CameraNotFoundError{CameraID: "x"},
		&CameraAlreadyRunningError{CameraID: "x"},
		&CameraDisabledError{CameraID: "x"},
		&CameraAlreadyExistsError{CameraID: "x"},
		&RecordingNotFoundError{RecordingID: "x"},
		&StorageFullError{Message: "x"},
		&AuthFailedError{Reason: "x"},
		&InvalidInputError{Message: "x"},
		&PathTraversalError{Path: "x"},
		&HLSMaxStreamsError{},
		&HLSSupportedCodecError{CameraID: "x"},
		&ONVIFNotCameraError{CameraID: "x"},
		&ONVIFConnectionError{CameraID: "x", Err: errors.New("x")},
		&ONVIFNoProfilesError{CameraID: "x"},
	}
	codes := []string{
		"CAMERA_NOT_FOUND",
		"CAMERA_ALREADY_RUNNING",
		"CAMERA_DISABLED",
		"CAMERA_ALREADY_EXISTS",
		"RECORDING_NOT_FOUND",
		"STORAGE_FULL",
		"AUTH_FAILED",
		"INVALID_INPUT",
		"PATH_TRAVERSAL",
		"HLS_MAX_STREAMS",
		"HLS_UNSUPPORTED_CODEC",
		"ONVIF_NOT_CAMERA",
		"ONVIF_CONNECTION_FAILED",
		"ONVIF_NO_PROFILES",
	}
	for i, e := range errors {
		if e.Code() != codes[i] {
			t.Errorf("error[%d].Code() = %q, want %q", i, e.Code(), codes[i])
		}
	}
}
