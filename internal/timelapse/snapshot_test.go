package timelapse

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeriveSnapshotURL(t *testing.T) {
	tests := []struct {
		name      string
		streamURL string
		protocol  string
		want      string
		wantEmpty bool
	}{
		// --- RTSP cameras ---
		{
			name:      "rtsp simple host:port",
			streamURL: "rtsp://192.168.1.100:554/stream1",
			protocol:  "rtsp",
			want:      "http://192.168.1.100:554/cgi-bin/snapshot.cgi",
		},
		{
			name:      "rtsp_h264 protocol",
			streamURL: "rtsp://192.168.1.100:554/stream1",
			protocol: "rtsp",
			want:      "http://192.168.1.100:554/cgi-bin/snapshot.cgi",
		},
		{
			name:      "rtsp_h265 protocol",
			streamURL: "rtsp://192.168.1.100:554/stream1",
			protocol: "rtsp",
			want:      "http://192.168.1.100:554/cgi-bin/snapshot.cgi",
		},
		{
			name:      "rtsp_mjpeg protocol",
			streamURL: "rtsp://192.168.1.100:554/live.sdp",
			protocol: "rtsp",
			want:      "http://192.168.1.100:554/cgi-bin/snapshot.cgi",
		},
		{
			name:      "rtsp with default port implicit",
			streamURL: "rtsp://camera.local/live",
			protocol:  "rtsp",
			want:      "http://camera.local:80/cgi-bin/snapshot.cgi",
		},
		{
			name:      "rtsp with credentials is stripped",
			streamURL: "rtsp://admin:pass@192.168.1.100:554/path",
			protocol:  "rtsp",
			want:      "http://192.168.1.100:554/cgi-bin/snapshot.cgi",
		},
		{
			name:      "rtsp with non-standard port",
			streamURL: "rtsp://10.0.0.50:8554/stream1",
			protocol:  "rtsp",
			want:      "http://10.0.0.50:8554/cgi-bin/snapshot.cgi",
		},

		// --- HTTP cameras ---
		{
			name:      "http jpeg direct URL",
			streamURL: "http://192.168.1.100:8080/video",
			protocol:  "http",
			want:      "http://192.168.1.100:8080/video",
		},
		{
			name:      "http_jpeg protocol",
			streamURL: "http://192.168.1.100:8080/video",
			protocol: "http",
			want:      "http://192.168.1.100:8080/video",
		},
		{
			name:      "http with query params preserved",
			streamURL: "http://192.168.1.100/cgi-bin/image.jpg?size=full",
			protocol:  "http",
			want:      "http://192.168.1.100/cgi-bin/image.jpg?size=full",
		},

		// --- ONVIF cameras ---
		{
			name:      "onvif returns empty (delegates to ONVIF client)",
			streamURL: "rtsp://192.168.1.100:554/onvif/media",
			protocol:  "onvif",
			wantEmpty: true,
		},

		// --- Xiaomi cameras ---
		{
			name:      "xiaomi returns empty (no snapshot support)",
			streamURL: "rtsp://xiaomi-cam:554/stream",
			protocol:  "xiaomi",
			wantEmpty: true,
		},

		// --- Edge cases ---
		{
			name:      "empty URL returns empty",
			streamURL: "",
			protocol:  "rtsp",
			wantEmpty: true,
		},
		{
			name:      "empty protocol returns empty",
			streamURL: "rtsp://192.168.1.100:554/stream",
			protocol:  "",
			wantEmpty: true,
		},
		{
			name:      "unknown protocol returns empty",
			streamURL: "rtsp://192.168.1.100:554/stream",
			protocol:  "unknown",
			wantEmpty: true,
		},
		{
			name:      "malformed URL returns empty",
			streamURL: "://bad-url",
			protocol:  "rtsp",
			wantEmpty: true,
		},
		{
			name:      "RTSP with empty host returns empty",
			streamURL: "rtsp://:554/stream",
			protocol:  "rtsp",
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeriveSnapshotURL(tt.streamURL, tt.protocol)
			if tt.wantEmpty {
				assert.Empty(t, got, "expected empty string for %s/%s", tt.streamURL, tt.protocol)
			} else {
				assert.Equal(t, tt.want, got, "DeriveSnapshotURL(%q, %q)", tt.streamURL, tt.protocol)
			}
		})
	}
}

func TestSnapshotCandidates(t *testing.T) {
	tests := []struct {
		name      string
		streamURL string
		protocol  string
		wantLen   int
		wantFirst string
		wantNil   bool
	}{
		{
			name:      "RTSP candidates ordered by likelihood",
			streamURL: "rtsp://192.168.1.100:554/stream1",
			protocol:  "rtsp",
			wantLen:   7,
			wantFirst: "http://192.168.1.100:554/cgi-bin/snapshot.cgi",
		},
		{
			name:      "RTSP with path adds path-relative candidates",
			streamURL: "rtsp://camera.local:554/onvif/media",
			protocol: "rtsp",
			wantLen:   7,
			wantFirst: "http://camera.local:554/cgi-bin/snapshot.cgi",
		},
		{
			name:      "HTTP returns single candidate",
			streamURL: "http://192.168.1.100:8080/video",
			protocol:  "http",
			wantLen:   1,
			wantFirst: "http://192.168.1.100:8080/video",
		},
		{
			name:      "Xiaomi returns nil",
			streamURL: "rtsp://xiaomi:554/stream",
			protocol:  "xiaomi",
			wantNil:   true,
		},
		{
			name:      "ONVIF returns nil",
			streamURL: "rtsp://onvif:554/stream",
			protocol:  "onvif",
			wantNil:   true,
		},
		{
			name:      "Unknown protocol returns nil",
			streamURL: "rtsp://host:554/stream",
			protocol:  "unknown",
			wantNil:   true,
		},
		{
			name:      "Empty URL returns nil",
			streamURL: "",
			protocol:  "rtsp",
			wantNil:   true,
		},
		{
			name:      "Empty protocol returns nil",
			streamURL: "rtsp://host:554/stream",
			protocol:  "",
			wantNil:   true,
		},
		{
			name:      "Malformed RTSP returns nil",
			streamURL: "://bad-url",
			protocol:  "rtsp",
			wantNil:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SnapshotCandidates(tt.streamURL, tt.protocol)
			if tt.wantNil {
				assert.Nil(t, got, "expected nil for %s/%s", tt.streamURL, tt.protocol)
				return
			}
			assert.NotNil(t, got)
			assert.Len(t, got, tt.wantLen, "SnapshotCandidates(%q, %q) length", tt.streamURL, tt.protocol)
			if tt.wantFirst != "" && len(got) > 0 {
				assert.Equal(t, tt.wantFirst, got[0], "first candidate should be the most likely match")
			}
		})
	}
}

func TestDeriveSnapshotURL_RTSP_RealWorld(t *testing.T) {
	// Real-world RTSP URLs that cameras commonly use
	tests := []struct {
		name  string
		url   string
		proto string
		want  string
	}{
		{
			name:  "Hikvision RTSP",
			url:   "rtsp://192.168.1.100:554/Streaming/Channels/101",
			proto: "rtsp",
			want:  "http://192.168.1.100:554/cgi-bin/snapshot.cgi",
		},
		{
			name:  "Dahua RTSP",
			url:   "rtsp://192.168.1.100:554/cam/realmonitor?channel=1&subtype=0",
			proto: "rtsp",
			want:  "http://192.168.1.100:554/cgi-bin/snapshot.cgi",
		},
		{
			name:  "Generic ONVIF RTSP",
			url:   "rtsp://192.168.1.100:554/onvif/media?profile=Profile_1",
			proto: "onvif",
			want:  "", // ONVIF returns empty
		},
		{
			name:  "HTTP MJPEG camera",
			url:   "http://192.168.1.100:8080/videostream.cgi",
			proto: "http",
			want:  "http://192.168.1.100:8080/videostream.cgi",
		},
		{
			name:  "Axis RTSP",
			url:   "rtsp://192.168.1.100:554/axis-media/media.amp",
			proto: "rtsp",
			want:  "http://192.168.1.100:554/cgi-bin/snapshot.cgi",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeriveSnapshotURL(tt.url, tt.proto)
			assert.Equal(t, tt.want, got, "DeriveSnapshotURL(%q, %q)", tt.url, tt.proto)
		})
	}
}

func TestSnapshotCandidates_RTSP_Contains(t *testing.T) {
	// Verify candidates list contains expected snapshot endpoints
	candidates := SnapshotCandidates("rtsp://192.168.1.100:554/Streaming/Channels/101", "rtsp")
	assert.NotNil(t, candidates)
	assert.Contains(t, candidates, "http://192.168.1.100:554/cgi-bin/snapshot.cgi")
	assert.Contains(t, candidates, "http://192.168.1.100:554/snapshot.jpg")
	assert.Contains(t, candidates, "http://192.168.1.100:554/?snap=1")
	assert.Contains(t, candidates, "http://192.168.1.100:554/Streaming/Channels/snapshot.jpg")
	assert.Contains(t, candidates, "http://192.168.1.100:554/Streaming/Channels/snapshot")
}

func TestDeriveSnapshotURL_UsernamePassword(t *testing.T) {
	// RTSP URLs with credentials should still work (credentials stripped by url.Parse)
	got := DeriveSnapshotURL("rtsp://admin:password123@192.168.1.100:554/stream", "rtsp")
	assert.Equal(t, "http://192.168.1.100:554/cgi-bin/snapshot.cgi", got,
		"credentials should be stripped from derived URL")
}
