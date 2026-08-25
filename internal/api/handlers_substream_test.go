package api

// Tests for the quality negotiation surface (#513): parseQuality / subKey /
// WHEP rejection of ?quality=sub. The full sub-stream egress path (acquire →
// register under "/sub" key → serve) needs a live RTSP sub source and is
// exercised by the internal/substream round-trip tests plus on-device M5
// verification.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseQuality(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw   string
		want  string
		valid bool
	}{
		{"", qualityMain, true},
		{"main", qualityMain, true},
		{"sub", qualitySub, true},
		{"MAIN", "", false},
		{"hd", "", false},
		{"", qualityMain, true},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, "/api/cameras/cam-1/stream.flv?quality="+tc.raw, nil)
		got, err := parseQuality(r)
		if !tc.valid {
			require.Error(t, err, "quality=%q", tc.raw)
			continue
		}
		require.NoError(t, err, "quality=%q", tc.raw)
		require.Equal(t, tc.want, got, "quality=%q", tc.raw)
	}
}

func TestSubKey(t *testing.T) {
	require.Equal(t, "cam-1/sub", subKey("cam-1"))
}

// WHEP must reject quality=sub with an explicit message pointing at the
// endpoints that support it (#513 v1).
func TestWHEP_RejectsSubQuality(t *testing.T) {
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "POST",
		"/api/cameras/cam-1/stream/webrtc?quality=sub",
		nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "not supported for WebRTC")
}

// WS endpoint validates the quality parameter before anything else.
func TestWS_RejectsBadQuality(t *testing.T) {
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET",
		"/api/cameras/cam-1/stream/ws?quality=hd",
		nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

// HLS rejects the query form (segments cannot carry it — the path form
// /stream/sub/index.m3u8 is the supported selector).
func TestHLS_RejectsQualityQuery(t *testing.T) {
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET",
		"/api/cameras/cam-1/stream/index.m3u8?quality=sub",
		nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "path form")
}
