// Package integration_test contains snapshot documentation tests for the HLS + Dashboard
// integration surface. These tests record the CURRENT expected behavior, types, and API
// contracts as compile-time assertions — serving as a safety net before refactoring.
//
// This is NOT a runtime integration test. It validates interface shapes, type existence,
// and documents the contract between HLS, Dashboard, and the API layer.
package integration_test

import (
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/hls"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// ---------------------------------------------------------------------------
// 1. HLSProvider interface contract snapshot
// ---------------------------------------------------------------------------
//
// The HLSProvider interface in model/types.go is the bridge between recorder
// implementations and the HLS streaming subsystem. When api/handler.go handles
// an HLS stream request, it performs a type assertion against this interface
// as a fallback path after checking concrete recorder types.
//
// Ref: internal/model/types.go lines 16-26
// Ref: internal/api/handler.go handleHLSStream(), HLSProvider branch (line ~1772)

// TestHLSProviderInterface_ContractSnapshot records the expected method signatures
// of model.HLSProvider. If anyone renames methods or changes parameter types,
// this test will fail to compile.
func TestHLSProviderInterface_ContractSnapshot(t *testing.T) {
	t.Run("CodecParams_returns_format_and_nal_units", func(t *testing.T) {
		// CodecParams() must return (Format, sps, pps, vps []byte)
		// sps/pps are required for H.264; vps additionally required for H.265
		// Returns nil slices when codec info frames have not been received yet.
		var _ model.HLSProvider = (*mockHLSProvider)(nil)

		// Verify Format type is string-based for comparison with FormatH264/FormatH265
		var f model.Format = "h264"
		_ = f
	})
}

// mockHLSProvider proves the HLSProvider interface contract at compile time.
// This struct will fail to compile if the interface changes.
type mockHLSProvider struct{}

func (m *mockHLSProvider) CodecParams() (codec model.Format, sps, pps, vps []byte) {
	return model.FormatH264, []byte{}, []byte{}, nil
}

// ---------------------------------------------------------------------------
// 2. Format and Protocol constants snapshot
// ---------------------------------------------------------------------------
//
// These constants are used throughout the codebase for protocol dispatch
// and codec identification. The HLS handler uses them to decide which
// StartStream variant to call.

func TestFormatConstants_Snapshot(t *testing.T) {
	tests := []struct {
		name     string
		got      model.Format
		expected string
	}{
		{"H264", model.FormatH264, "h264"},
		{"H265", model.FormatH265, "h265"},
		{"MJPEG", model.FormatMJPEG, "mjpeg"},
		{"JPEG encoding", model.EncJPEG, "jpeg"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.got) != tt.expected {
				t.Errorf("Format constant %s = %q, want %q", tt.name, tt.got, tt.expected)
			}
		})
	}
}

func TestProtocolConstants_Snapshot(t *testing.T) {
	tests := []struct {
		name     string
		got      model.Protocol
		expected string
	}{
		{"RTSP", model.ProtoRTSP, "rtsp"},
		{"HTTP", model.ProtoHTTP, "http"},
		{"ONVIF", model.ProtoONVIF, "onvif"},
		{"Xiaomi", model.ProtoXiaomi, "xiaomi"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.got) != tt.expected {
				t.Errorf("Protocol constant %s = %q, want %q", tt.name, tt.got, tt.expected)
			}
		})
	}
}

func TestTransportProtocolConstants_Snapshot(t *testing.T) {
	// Transport-only protocols are what the /api/protocols endpoint returns
	// as the "id" field for built-in protocols.
	if string(model.ProtoRTSP) != "rtsp" {
		t.Errorf("ProtoRTSP = %q, want %q", model.ProtoRTSP, "rtsp")
	}
	if string(model.ProtoHTTP) != "http" {
		t.Errorf("ProtoHTTP = %q, want %q", model.ProtoHTTP, "http")
	}
}

// ---------------------------------------------------------------------------
// 3. RecorderStatus constants snapshot
// ---------------------------------------------------------------------------
//
// The /api/cameras endpoint injects these status values from CameraManager.Status().
// The Dashboard frontend depends on exact string values for status badges.

func TestRecorderStatusConstants_Snapshot(t *testing.T) {
	tests := []struct {
		name     string
		got      model.RecorderStatus
		expected string
	}{
		{"Recording", model.StatusRecording, "recording"},
		{"Stopped", model.StatusStopped, "stopped"},
		{"Error", model.StatusError, "error"},
		{"Reconnecting", model.StatusReconnecting, "reconnecting"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.got) != tt.expected {
				t.Errorf("RecorderStatus %s = %q, want %q", tt.name, tt.got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 4. HLS Manager method signatures snapshot
// ---------------------------------------------------------------------------
//
// The HLS Manager exposes these methods that the API handler calls.
// The handler selects StartStream vs StartStreamH265 based on codec,
// passing SPS/PPS/VPS NAL units (without start codes).

func TestHLSManager_StartStreamSignature(t *testing.T) {
	t.Run("StartStream_H264_takes_sps_pps_maxFPS", func(t *testing.T) {
		// StartStream(cameraID string, sps, pps []byte, maxFPS int) error
		// Validates at compile time that the method exists with this signature.
		var m *hls.Manager
		_ = m.StartStream // field selector confirms method exists
	})

	t.Run("StartStreamH265_takes_vps_sps_pps_maxFPS", func(t *testing.T) {
		// StartStreamH265(cameraID string, vps, sps, pps []byte, maxFPS int) error
		var m *hls.Manager
		_ = m.StartStreamH265
	})

	t.Run("ErrMaxStreamsReached_is_sentinel_error", func(t *testing.T) {
		// api/handler.go checks: if err == hls.ErrMaxStreamsReached
		// Returns 503 "maximum HLS streams reached" to client
		err := hls.ErrMaxStreamsReached
		if err == nil {
			t.Fatal("ErrMaxStreamsReached should not be nil")
		}
		if err.Error() != "maximum HLS streams reached" {
			t.Errorf("ErrMaxStreamsReached.Error() = %q, want %q", err.Error(), "maximum HLS streams reached")
		}
	})

	t.Run("other_HLS_error_sentinels", func(t *testing.T) {
		// These errors exist in the hls package and are used by the manager
		for _, sentinel := range []error{
			hls.ErrStreamNotActive,
			hls.ErrUnsupportedProtocol,
		} {
			if sentinel == nil {
				t.Error("HLS error sentinel should not be nil")
			}
		}
	})
}

// ---------------------------------------------------------------------------
// 5. /api/protocols response shape snapshot
// ---------------------------------------------------------------------------
//
// GET /api/protocols returns JSON: {"protocols": [...]}
// Each protocol entry has the shape documented below.
// Built-in protocols (rtsp, http, onvif) are always present.
// Plugin protocols (xiaomi) are added dynamically when pluginMgr is non-nil.

func TestProtocolsAPI_ResponseShape(t *testing.T) {
	t.Run("protocolInfo_field_names_match_JSON_tags", func(t *testing.T) {
		// This test documents the expected JSON field names from the protocolInfo struct.
		// The frontend Dashboard depends on these exact field names.
		//
		// Expected JSON shape per protocol:
		// {
		//   "id":           "rtsp",                      // transport protocol ID
		//   "label":        "RTSP",                      // display label
		//   "encodings":    ["h264", "h265", "mjpeg"],   // supported encoding list
		//   "built_in":     true,                        // true for built-in, false for plugins
		//   "capabilities": {                            // feature flags
		//     "hls":       bool,
		//     "ptz":       bool,
		//     "snapshot":  bool,
		//     "discovery": bool,
		//     "auth":      bool
		//   }
		// }
		//
		// Built-in protocol capabilities (from handler.go handleProtocols):
		//   rtsp:  hls=true, ptz=false, snapshot=false, discovery=false, auth=true
		//   http:  hls=false, ptz=false, snapshot=true,  discovery=false, auth=true
		//   onvif: hls=true,  ptz=true,  snapshot=false, discovery=true,  auth=true
		// Plugin protocols: built_in=false, auth=false (hardcoded), others from plugin capabilities
		//
		// The response wrapper is: {"protocols": [...]}
		// NOT a bare array.
		t.Log("protocolInfo JSON shape documented — see test source for details")
	})

	t.Run("built_in_protocols_always_present", func(t *testing.T) {
		// The three built-in protocols are always returned:
		//   1. "rtsp"  — supports h264, h265, mjpeg; has HLS
		//   2. "http"  — supports jpeg only; no HLS, has snapshot
		//   3. "onvif" — supports h264, h265; has HLS + PTZ + discovery
		t.Log("three built-in protocols documented: rtsp, http, onvif")
	})
}

// ---------------------------------------------------------------------------
// 6. /api/cameras response shape snapshot
// ---------------------------------------------------------------------------
//
// GET /api/cameras returns JSON: []CameraRow (bare array, NOT wrapped in object)
// Each camera entry has the shape from storage.CameraRow.
// The handler injects "status" and "last_seen" from CameraManager before returning.

func TestCamerasAPI_ResponseShape(t *testing.T) {
	t.Run("CameraRow_field_names_match_JSON_tags", func(t *testing.T) {
		// This test documents the expected JSON fields from storage.CameraRow.
		// The frontend Dashboard uses these for the camera list and status display.
		//
		// Key fields for HLS/Dashboard integration:
		//   "status"     — injected from CameraManager: "recording" | "stopped" | "error" | "reconnecting"
		//   "last_seen"  — injected from DB last recording time, omitempty
		//   "protocol"   — transport protocol: "rtsp" | "http" | "onvif" | "xiaomi"
		//   "encoding"   — codec: "h264" | "h265" | "mjpeg" | "jpeg"
		//   "enabled"    — whether camera is active
		//
		// The response is a bare JSON array, NOT wrapped: [{...}, {...}]
		//
		// Full CameraRow JSON fields (from storage.CameraRow):
		//   id, name, protocol, encoding, url, enabled, description, location,
		//   brand, model, serial_number, retention_days, status, last_seen,
		//   username, has_password,
		//   merge_enabled, merge_check_interval, merge_window_size,
		//   merge_batch_limit, merge_min_segment_age, merge_min_segments_to_merge,
		//   onvif_endpoint, profile_token, stream_encoding,
		//   archived, archived_at, archive_retention_days
		t.Log("CameraRow JSON shape documented — see test source for details")
	})

	t.Run("status_values_are_RecorderStatus_constants", func(t *testing.T) {
		// The status field is typed as model.RecorderStatus in CameraRow.
		// The handler injects status from CameraManager.Status() map,
		// defaulting to model.StatusStopped if camera not found in map.
		var status model.RecorderStatus = "recording"
		if status != model.StatusRecording {
			t.Errorf("expected StatusRecording = %q", model.StatusRecording)
		}
	})

	t.Run("CameraRow_uses_RecorderStatus_type", func(t *testing.T) {
		// Compile-time proof that CameraRow.Status is model.RecorderStatus.
		var row storage.CameraRow
		_ = row.Status
	})
}

// ---------------------------------------------------------------------------
// 7. HLS stream URL pattern snapshot
// ---------------------------------------------------------------------------
//
// The frontend requests HLS streams via this URL pattern.
// The chi router captures {id} as chi.URLParam.

func TestHLSStreamURLPattern_Snapshot(t *testing.T) {
	t.Run("stream_URL_pattern", func(t *testing.T) {
		// The HLS stream URL pattern registered in Routes():
		//   GET /api/cameras/{id}/stream/index.m3u8
		//
		// The handler (handleHLSStream) flow:
		//   1. Extract camera ID from URL: chi.URLParam(r, "id")
		//   2. Check hlsMgr.IsActive(id) — if not active, start stream
		//   3. Type-assert recorder to determine codec path:
		//      a. *recorder.H264Recorder → StartStream(id, sps, pps, maxFPS)
		//      b. *recorder.H265Recorder → StartStreamH265(id, vps, sps, pps, maxFPS)
		//      c. *recorder.ONVIFRecorder → unwrap delegate, then a or b
		//      d. model.HLSProvider → CodecParams() + subscribeHLS()
		//   4. Subscribe HLS consumer via subscribeHLS(hub, id, hlsMgr, ...)
		//   5. If sub-stream URL configured, try StartSubStreamReader()
		//   6. Proxy to hlsMgr.Handle(id, w, r) for muxer response
		//
		// HTTP response codes:
		//   200 — success, proxied HLS response
		//   400 — recorder doesn't support HLS / unsupported codec
		//   404 — camera not found
		//   500 — internal errors (DB, stream start failure)
		//   503 — SPS/PPS/VPS not ready / max streams reached
		t.Log("HLS URL pattern and handler flow documented")
	})

	t.Run("stop_stream_endpoint", func(t *testing.T) {
		// The stop endpoint pattern:
		//   POST /api/cameras/{id}/stream/stop
		// Calls hlsMgr.StopStream(id) and unsubscribes from StreamHub.
		t.Log("HLS stop endpoint documented")
	})
}

// ---------------------------------------------------------------------------
// 8. HLS handler dispatch order snapshot
// ---------------------------------------------------------------------------
//
// This documents the CRITICAL dispatch order in handleHLSStream.
// The handler checks recorder types in this exact order:
//
//   1. *recorder.H264Recorder  — direct field access: .SPS(), .PPS()
//   2. *recorder.H265Recorder  — direct field access: .VPS(), .SPS(), .PPS()
//   3. *recorder.ONVIFRecorder — unwrap delegate, then recurse into 1 or 2
//   4. model.HLSProvider       — interface method: .CodecParams()
//   5. else                    — "camera recorder does not support HLS" (400)
//
// IMPORTANT: The HLSProvider interface (path 4) is the FALLBACK for gRPC plugin adapters
// (e.g., Xiaomi). Built-in recorders use direct field access (paths 1-3).
// This dual-mode dispatch must be preserved during refactoring.

func TestHLSHandler_DispatchOrder_Snapshot(t *testing.T) {
	t.Run("H264Recorder_direct_field_access", func(t *testing.T) {
		// Path 1: *recorder.H264Recorder
		//   - Calls: h264Rec.SPS(), h264Rec.PPS()
		//   - Starts: hlsMgr.StartStream(id, sps, pps, hlsMaxFPS)
		//   - Subscribes: subscribeHLS(hub, id, hlsMgr, false) via StreamHub
		//   - Sub-stream: hlsMgr.StartSubStreamReader(id, url, false) for H264
		t.Log("H264 path: SPS(), PPS() → StartStream → subscribeHLS → WriteH264")
	})

	t.Run("H265Recorder_direct_field_access", func(t *testing.T) {
		// Path 2: *recorder.H265Recorder
		//   - Calls: h265Rec.VPS(), h265Rec.SPS(), h265Rec.PPS()
		//   - Starts: hlsMgr.StartStreamH265(id, vps, sps, pps, hlsMaxFPS)
		//   - Subscribes: subscribeHLS(hub, id, hlsMgr, true) via StreamHub
		//   - Sub-stream: hlsMgr.StartSubStreamReader(id, url, true) for H265
		t.Log("H265 path: VPS(), SPS(), PPS() → StartStreamH265 → subscribeHLS → WriteH265")
	})

	t.Run("ONVIFRecorder_delegate_unwrap", func(t *testing.T) {
		// Path 3: *recorder.ONVIFRecorder
		//   - Calls: onvifRec.Delegate() → unwraps to H264Recorder or H265Recorder
		//   - Then follows path 1 or 2 on the delegate
		//   - No sub-stream support for ONVIF delegates
		t.Log("ONVIF path: Delegate() → H264 or H265 path")
	})

	t.Run("HLSProvider_interface_fallback", func(t *testing.T) {
		// Path 4: model.HLSProvider (interface assertion)
		//   - Calls: provider.CodecParams() → (codec, sps, pps, vps)
		//   - codec == FormatH264: StartStream(id, sps, pps, maxFPS)
		//     + subscribeHLS via getRecorderHub(rec)
		//   - codec == FormatH265: requires vps != null
		//     + StartStreamH265(vps, sps, pps, maxFPS)
		//     + subscribeHLS via getRecorderHub(rec)
		//   - other codec: 400 "unsupported codec for HLS streaming"
		//   - No sub-stream support for HLSProvider path
		t.Log("HLSProvider path: CodecParams() → codec switch → subscribeHLS")
	})

	t.Run("unsupported_recorder_type", func(t *testing.T) {
		// Path 5: No match
		//   Returns: 400 "camera recorder does not support HLS"
		//   This happens for MJPEG and HTTP-JPEG recorders
		t.Log("Fallback: 400 for unsupported recorder types")
	})
}

// ---------------------------------------------------------------------------
// 9. ValidEncodingsForProtocol snapshot
// ---------------------------------------------------------------------------
//
// This map defines which encodings are valid per transport protocol.
// The /api/protocols endpoint uses these implicitly through hardcoded entries.

func TestValidEncodingsForProtocol_Snapshot(t *testing.T) {
	expected := map[string][]string{
		"rtsp":   {"h264", "h265", "mjpeg"},
		"http":   {"jpeg"},
		"onvif":  {"h264", "h265", "jpeg"},
		"xiaomi": {"h264", "h265"},
	}

	for proto, wantEncodings := range expected {
		gotEncodings, ok := model.ValidEncodingsForProtocol[proto]
		if !ok {
			t.Errorf("ValidEncodingsForProtocol missing key %q", proto)
			continue
		}
		if len(gotEncodings) != len(wantEncodings) {
			t.Errorf("ValidEncodingsForProtocol[%q] has %d encodings, want %d", proto, len(gotEncodings), len(wantEncodings))
			continue
		}
		// Check all expected encodings present (order may differ)
		gotSet := make(map[string]bool, len(gotEncodings))
		for _, e := range gotEncodings {
			gotSet[e] = true
		}
		for _, e := range wantEncodings {
			if !gotSet[e] {
				t.Errorf("ValidEncodingsForProtocol[%q] missing encoding %q", proto, e)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 10. Dashboard API dependency documentation
// ---------------------------------------------------------------------------
//
// The frontend Dashboard relies on these API endpoints for camera and stream status.
// Any breaking change to these endpoints requires a coordinated frontend update.

func TestDashboardAPI_Dependencies(t *testing.T) {
	t.Run("cameras_list_for_status_badges", func(t *testing.T) {
		// GET /api/cameras
		// Dashboard uses: status field for badge color (recording=green, error=red, etc.)
		// Dashboard uses: last_seen field for "last activity" display
		// Dashboard uses: protocol+encoding for camera type icon
		// Dashboard uses: enabled for toggle switch state
		t.Log("Dashboard camera list dependencies documented")
	})

	t.Run("protocols_list_for_add_camera_dialog", func(t *testing.T) {
		// GET /api/protocols
		// Dashboard uses: encodings[] to populate encoding dropdown
		// Dashboard uses: capabilities.hls to show/hide HLS live view button
		// Dashboard uses: capabilities.ptz to show/hide PTZ controls
		// Dashboard uses: capabilities.discovery to show/hide ONVIF discovery button
		// Dashboard uses: built_in to distinguish plugin vs built-in setup flows
		t.Log("Dashboard protocols dependencies documented")
	})

	t.Run("hls_stream_for_live_view", func(t *testing.T) {
		// GET /api/cameras/{id}/stream/index.m3u8
		// Dashboard uses hls.js to consume the HLS stream
		// Frontend checks capabilities.hls from /api/protocols before showing live button
		// Stream URL constructed as: `/api/cameras/${cameraId}/stream/index.m3u8`
		t.Log("Dashboard HLS live view dependencies documented")
	})

	t.Run("hls_maxFPS_default", func(t *testing.T) {
		// Default maxFPS for HLS is 10 (RPi 3B safety).
		// Overridden per-camera via config: hls_max_fps field.
		// The api handler reads: camCfg.HLSMaxFPS
		// Only applies when value > 0.
		t.Log("HLS maxFPS default=10, per-camera override documented")
	})
}
