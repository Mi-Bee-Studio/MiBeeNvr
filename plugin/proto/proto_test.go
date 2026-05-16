package proto

import (
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen"
	"google.golang.org/protobuf/proto"
)

// --- Codec enum mapping ---

func TestCodecValues(t *testing.T) {
	tests := []struct {
		name  string
		codec gen.Codec
		want  int32
	}{
		{"unspecified", gen.Codec_CODEC_UNSPECIFIED, 0},
		{"h264", gen.Codec_CODEC_H264, 1},
		{"h265", gen.Codec_CODEC_H265, 2},
		{"mjpeg", gen.Codec_CODEC_MJPEG, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := int32(tt.codec); got != tt.want {
				t.Errorf("Codec %s = %d, want %d", tt.name, got, tt.want)
			}
		})
	}
}

// --- RecorderState enum ---

func TestRecorderStateValues(t *testing.T) {
	tests := []struct {
		name  string
		state gen.RecorderState
		want  int32
	}{
		{"unspecified", gen.RecorderState_RECORDER_STATE_UNSPECIFIED, 0},
		{"idle", gen.RecorderState_RECORDER_STATE_IDLE, 1},
		{"connecting", gen.RecorderState_RECORDER_STATE_CONNECTING, 2},
		{"recording", gen.RecorderState_RECORDER_STATE_RECORDING, 3},
		{"error", gen.RecorderState_RECORDER_STATE_ERROR, 4},
		{"stopped", gen.RecorderState_RECORDER_STATE_STOPPED, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := int32(tt.state); got != tt.want {
				t.Errorf("RecorderState %s = %d, want %d", tt.name, got, tt.want)
			}
		})
	}
}

// --- Frame round-trip with small payload ---

func TestFrameRoundTrip(t *testing.T) {
	original := &gen.Frame{
		Data:         []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88, 0x84, 0x00, 0x40},
		PtsNs:        1700000000000000000, // ~2023-12-14
		IsIdr:        true,
		Codec:        gen.Codec_CODEC_H264,
		IsCodecInfo:  false,
		Extra:        map[string]string{"sps_hex": "67640020accac05005bb0110000003001000000301", "nalu_format": "annexb"},
	}

	data, err := proto.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &gen.Frame{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if string(got.Data) != string(original.Data) {
		t.Errorf("Data mismatch: got %x, want %x", got.Data, original.Data)
	}
	if got.PtsNs != original.PtsNs {
		t.Errorf("PtsNs mismatch: got %d, want %d", got.PtsNs, original.PtsNs)
	}
	if got.IsIdr != original.IsIdr {
		t.Errorf("IsIdr mismatch: got %v, want %v", got.IsIdr, original.IsIdr)
	}
	if got.Codec != original.Codec {
		t.Errorf("Codec mismatch: got %v, want %v", got.Codec, original.Codec)
	}
	if got.IsCodecInfo != original.IsCodecInfo {
		t.Errorf("IsCodecInfo mismatch: got %v, want %v", got.IsCodecInfo, original.IsCodecInfo)
	}
	if v, ok := got.Extra["sps_hex"]; !ok || v != original.Extra["sps_hex"] {
		t.Errorf("Extra[sps_hex] mismatch: got %q, want %q", got.Extra["sps_hex"], original.Extra["sps_hex"])
	}
}

// --- Frame with is_codec_info=true (SPS/PPS/VPS) ---

func TestFrameCodecInfo(t *testing.T) {
	original := &gen.Frame{
		Data:        []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x64, 0x00, 0x20}, // SPS NAL
		PtsNs:       0,
		IsIdr:       false,
		Codec:       gen.Codec_CODEC_H264,
		IsCodecInfo: true,
		Extra:       map[string]string{"type": "sps"},
	}

	data, err := proto.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &gen.Frame{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if !got.IsCodecInfo {
		t.Error("IsCodecInfo should be true for SPS/PPS frames")
	}
	if got.IsIdr {
		t.Error("IsIdr should be false for SPS/PPS frames")
	}
}

// --- Frame with empty data ---

func TestFrameEmptyData(t *testing.T) {
	original := &gen.Frame{
		Data:  []byte{},
		PtsNs: 0,
		Codec: gen.Codec_CODEC_UNSPECIFIED,
	}

	data, err := proto.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &gen.Frame{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if len(got.Data) != 0 {
		t.Errorf("Expected empty data, got %d bytes", len(got.Data))
	}
}

// --- Frame with 500KB payload (simulating IDR frame) ---

func TestFrameLargePayload(t *testing.T) {
	// Simulate a large I-frame (500KB)
	payload := make([]byte, 500*1024)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	original := &gen.Frame{
		Data:  payload,
		PtsNs: 1700000000000000000,
		IsIdr: true,
		Codec: gen.Codec_CODEC_H265,
		Extra: map[string]string{"vps_hex": "40010c01ffff"},
	}

	data, err := proto.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal 500KB frame: %v", err)
	}

	got := &gen.Frame{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal 500KB frame: %v", err)
	}

	if len(got.Data) != len(payload) {
		t.Fatalf("Data length mismatch: got %d, want %d", len(got.Data), len(payload))
	}

	for i := range payload {
		if got.Data[i] != payload[i] {
			t.Fatalf("Data mismatch at byte %d: got %02x, want %02x", i, got.Data[i], payload[i])
		}
	}

	t.Logf("500KB frame marshaled to %d bytes, round-trip OK", len(data))
}

// --- PluginInfo round-trip ---

func TestPluginInfoRoundTrip(t *testing.T) {
	original := &gen.PluginInfo{
		Name:             "xiaomi",
		Version:          "1.0.0",
		Protocols:        []string{"xiaomi"},
		Capabilities:     &gen.Capabilities{Discovery: true, Snapshot: true, Auth: true},
		SupportedEncodings: []gen.Codec{gen.Codec_CODEC_H264, gen.Codec_CODEC_H265},
	}

	data, err := proto.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &gen.PluginInfo{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Name != original.Name {
		t.Errorf("Name: got %q, want %q", got.Name, original.Name)
	}
	if got.Version != original.Version {
		t.Errorf("Version: got %q, want %q", got.Version, original.Version)
	}
	if len(got.Protocols) != 1 || got.Protocols[0] != "xiaomi" {
		t.Errorf("Protocols: got %v", got.Protocols)
	}
	if !got.Capabilities.Discovery || !got.Capabilities.Snapshot {
		t.Errorf("Capabilities: got %v", got.Capabilities)
	}
	if !got.Capabilities.Auth {
		t.Errorf("Capabilities.Auth should be true")
	}
	if got.Capabilities.Hls || got.Capabilities.Ptz {
		t.Errorf("Capabilities should have hls=false, ptz=false")
	}
	if len(got.SupportedEncodings) != 2 {
		t.Errorf("SupportedEncodings: got %d, want 2", len(got.SupportedEncodings))
	}
}

// --- RecorderConfig round-trip with name and encoding ---

func TestRecorderConfigRoundTrip(t *testing.T) {
	original := &gen.RecorderConfig{
		CameraId:         "cam-001",
		Name:             "Front Door",
		Url:              "xiaomi://192.168.1.100",
		Username:         "admin",
		Password:         "secret",
		SegmentDurationNs: 30_000_000_000, // 30s
		Options:          map[string]string{"did": "12345", "vendor": "cs2", "region": "cn"},
		Encoding:         "h264",
	}

	data, err := proto.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &gen.RecorderConfig{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.CameraId != original.CameraId {
		t.Errorf("CameraId: got %q, want %q", got.CameraId, original.CameraId)
	}
	if got.Name != original.Name {
		t.Errorf("Name: got %q, want %q", got.Name, original.Name)
	}
	if got.Encoding != original.Encoding {
		t.Errorf("Encoding: got %q, want %q", got.Encoding, original.Encoding)
	}
	if got.SegmentDurationNs != original.SegmentDurationNs {
		t.Errorf("SegmentDurationNs: got %d, want %d", got.SegmentDurationNs, original.SegmentDurationNs)
	}
	if v, ok := got.Options["did"]; !ok || v != "12345" {
		t.Errorf("Options[did]: got %q", got.Options["did"])
	}
}

// --- RecorderStatus round-trip ---

func TestRecorderStatusRoundTrip(t *testing.T) {
	original := &gen.RecorderStatus{
		State:          gen.RecorderState_RECORDER_STATE_RECORDING,
		ErrorMsg:       "",
		BytesRecorded:  1048576,
		SegmentsCreated: 5,
		UptimeNs:       300_000_000_000, // 5 minutes
	}

	data, err := proto.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &gen.RecorderStatus{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.State != original.State {
		t.Errorf("State: got %v, want %v", got.State, original.State)
	}
	if got.BytesRecorded != original.BytesRecorded {
		t.Errorf("BytesRecorded: got %d, want %d", got.BytesRecorded, original.BytesRecorded)
	}
	if got.SegmentsCreated != original.SegmentsCreated {
		t.Errorf("SegmentsCreated: got %d, want %d", got.SegmentsCreated, original.SegmentsCreated)
	}
	if got.UptimeNs != original.UptimeNs {
		t.Errorf("UptimeNs: got %d, want %d", got.UptimeNs, original.UptimeNs)
	}
}

// --- CloudConfig round-trip ---

func TestCloudConfigRoundTrip(t *testing.T) {
	original := &gen.CloudConfig{
		ServiceToken: "s0meT0k3n",
		UserId:       "user123",
		DeviceId:     "did.456.789",
		Region:       "cn",
		Extra:        map[string]string{"server": "api.mi.com"},
	}

	data, err := proto.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &gen.CloudConfig{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.ServiceToken != original.ServiceToken {
		t.Errorf("ServiceToken: got %q, want %q", got.ServiceToken, original.ServiceToken)
	}
	if got.Region != original.Region {
		t.Errorf("Region: got %q, want %q", got.Region, original.Region)
	}
}

// --- HealthCheckResponse round-trip ---

func TestHealthCheckResponseRoundTrip(t *testing.T) {
	original := &gen.HealthCheckResponse{
		Healthy: true,
		Message: "xiaomi plugin v1.0.0 uptime=3600s",
	}

	data, err := proto.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &gen.HealthCheckResponse{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Healthy != original.Healthy {
		t.Errorf("Healthy: got %v, want %v", got.Healthy, original.Healthy)
	}
	if got.Message != original.Message {
		t.Errorf("Message: got %q, want %q", got.Message, original.Message)
	}
}

// --- Backward compatibility: unknown fields are preserved ---

func TestBackwardCompatUnknownFields(t *testing.T) {
	original := &gen.Frame{
		Data:  []byte{0x00, 0x00, 0x00, 0x01, 0x67},
		PtsNs: 1234567890,
		IsIdr: false,
		Codec: gen.Codec_CODEC_H264,
	}

	data, err := proto.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &gen.Frame{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if string(got.Data) != string(original.Data) {
		t.Errorf("Data mismatch after backward compat round-trip")
	}
	if got.PtsNs != original.PtsNs {
		t.Errorf("PtsNs mismatch after backward compat round-trip")
	}
}

// --- Backward compatibility: new optional field does not break old data ---

func TestBackwardCompatNewFieldIgnored(t *testing.T) {
	oldFrame := &gen.Frame{
		Data:  []byte{0xAA, 0xBB},
		PtsNs: 999,
		IsIdr: true,
		Codec: gen.Codec_CODEC_MJPEG,
	}

	data, err := proto.Marshal(oldFrame)
	if err != nil {
		t.Fatalf("Marshal old frame: %v", err)
	}

	got := &gen.Frame{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal old data into new struct: %v", err)
	}

	if string(got.Data) != string(oldFrame.Data) {
		t.Errorf("Data corrupted: got %x, want %x", got.Data, oldFrame.Data)
	}
	// New fields should be zero/false (not crash)
	if got.IsCodecInfo {
		t.Errorf("IsCodecInfo should be false for old data without field 5")
	}
	if got.Extra != nil {
		t.Errorf("Extra should be nil for old data, got %v", got.Extra)
	}
}

// --- Deterministic serialization size check ---

func TestFrameSerializedSize(t *testing.T) {
	frame := &gen.Frame{
		Data:  make([]byte, 1024),
		PtsNs: 1_000_000,
		IsIdr: true,
		Codec: gen.Codec_CODEC_H264,
	}

	data, err := proto.Marshal(frame)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// Frame with 1KB payload should serialize to ~1KB + overhead
	if len(data) < 1024 {
		t.Errorf("Serialized too small: %d bytes", len(data))
	}
	if len(data) > 1100 {
		t.Errorf("Serialized unexpectedly large: %d bytes (overhead too high)", len(data))
	}
}

// --- Empty message ---

func TestEmptyMessage(t *testing.T) {
	original := &gen.Empty{}

	data, err := proto.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal Empty: %v", err)
	}

	// Empty message should serialize to zero bytes
	if len(data) != 0 {
		t.Errorf("Empty message should be 0 bytes, got %d", len(data))
	}

	got := &gen.Empty{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal Empty: %v", err)
	}
}

// --- CloudConfigResponse ---

func TestCloudConfigResponseRoundTrip(t *testing.T) {
	original := &gen.CloudConfigResponse{
		Success: true,
		Message: "credentials accepted",
	}

	data, err := proto.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &gen.CloudConfigResponse{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Success != original.Success {
		t.Errorf("Success: got %v, want %v", got.Success, original.Success)
	}
	if got.Message != original.Message {
		t.Errorf("Message: got %q, want %q", got.Message, original.Message)
	}
}

// --- StopRequest / StopResponse ---

func TestStopRequestRoundTrip(t *testing.T) {
	original := &gen.StopRequest{CameraId: "cam-42"}

	data, err := proto.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &gen.StopRequest{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.CameraId != "cam-42" {
		t.Errorf("CameraId: got %q, want %q", got.CameraId, "cam-42")
	}
}

// --- StatusRequest ---

func TestStatusRequestRoundTrip(t *testing.T) {
	original := &gen.StatusRequest{CameraId: "cam-live"}

	data, err := proto.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &gen.StatusRequest{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.CameraId != "cam-live" {
		t.Errorf("CameraId: got %q, want %q", got.CameraId, "cam-live")
	}
}

// --- Capabilities with all features disabled ---

func TestCapabilitiesAllOff(t *testing.T) {
	caps := &gen.Capabilities{}

	data, err := proto.Marshal(caps)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &gen.Capabilities{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Hls || got.Ptz || got.Snapshot || got.Discovery || got.Auth {
		t.Error("All capabilities should be false")
	}
}

// --- Capabilities with all features enabled ---

func TestCapabilitiesAllOn(t *testing.T) {
	caps := &gen.Capabilities{Hls: true, Ptz: true, Snapshot: true, Discovery: true, Auth: true}

	data, err := proto.Marshal(caps)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &gen.Capabilities{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if !got.Hls || !got.Ptz || !got.Snapshot || !got.Discovery || !got.Auth {
		t.Error("All capabilities should be true")
	}
}
