package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseLegacyProtocol(t *testing.T) {
	tests := []struct {
		input       string
		wantProto   string
		wantEnc     string
		wantErr     bool
	}{
		{"rtsp_h264", "rtsp", "h264", false},
		{"rtsp_h265", "rtsp", "h265", false},
		{"rtsp_mjpeg", "rtsp", "mjpeg", false},
		{"http_jpeg", "http", "jpeg", false},
		{"onvif", "onvif", "", false},
		{"unknown", "", "", true},
		{"", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			proto, enc, err := ParseLegacyProtocol(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantProto, proto)
			require.Equal(t, tt.wantEnc, enc)
		})
	}
}

func TestValidateProtocolEncoding(t *testing.T) {
	validCombos := []struct{ proto, enc string }{
		{"rtsp", "h264"},
		{"rtsp", "h265"},
		{"rtsp", "mjpeg"},
		{"http", "jpeg"},
		{"onvif", "h264"},
		{"onvif", "h265"},
	}
	for _, c := range validCombos {
		t.Run("valid_"+c.proto+"_"+c.enc, func(t *testing.T) {
			require.NoError(t, ValidateProtocolEncoding(c.proto, c.enc))
		})
	}

	invalidCombos := []struct{ proto, enc string }{
		{"http", "h264"},
		{"rtsp", "jpeg"},
		{"onvif", "jpeg"},
		{"http", "h265"},
		{"", ""},
		{"foo", "bar"},
	}
	for _, c := range invalidCombos {
		t.Run("invalid_"+c.proto+"_"+c.enc, func(t *testing.T) {
			require.Error(t, ValidateProtocolEncoding(c.proto, c.enc))
		})
	}
}
