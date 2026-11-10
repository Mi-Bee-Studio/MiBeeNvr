package api

// #1125 follow-up (suggestion 3): a probe that reaches a live endpoint but
// gets no ONVIF answer (non-ONVIF server, or firmware that answers the
// envelopes with HTTP 200 + empty body / 404s) used to return a bare
// {"device": null} — indistinguishable from "offline" for the UI, which
// showed only a generic "probe failed". The nil-device path now carries a
// structured reason the frontend maps to a specific, actionable message.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// probeServerPort extracts the port of a local httptest server.
func probeServerPort(t *testing.T, ts *httptest.Server) int {
	t.Helper()
	parts := strings.Split(strings.TrimPrefix(ts.URL, "http://127.0.0.1:"), ":")
	port, err := strconv.Atoi(parts[0])
	require.NoError(t, err)
	return port
}

func TestONVIFProbe_NoResponseCarriesReason(t *testing.T) {
	// A live HTTP server that never speaks ONVIF: the TCP pre-check passes,
	// both probe envelopes get non-ONVIF answers, ProbeDevice → (nil, nil).
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(ts.Close)

	h := TestHandler(nil, nil)
	body := fmt.Sprintf(`{"host":"127.0.0.1","port":%d,"timeout":2}`, probeServerPort(t, ts))
	rr := doRequest(t, h.Routes(), "POST", "/api/onvif/probe", strings.NewReader(body), "", "")

	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"device":null`)
	require.Contains(t, rr.Body.String(), `"reason":"no_onvif_response"`,
		"the reachable-but-silent outcome must be distinguishable from offline/non-ONVIF for the UI")
}

func TestONVIFProbe_FoundDeviceHasNoReason(t *testing.T) {
	// A server answering the unauthenticated GetDeviceInformation with a
	// well-formed response: the probe succeeds and the payload must stay
	// shape-identical to today's (device present, no reason key).
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 512)
		n, _ := r.Body.Read(buf)
		s := string(buf[:n])
		if strings.Contains(s, ":Probe") || strings.Contains(s, "<Probe ") {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>`+
			`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>`+
			`<tds:GetDeviceInformationResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl">`+
			`<tds:Manufacturer>ProbeCam</tds:Manufacturer><tds:Model>PC-1</tds:Model>`+
			`<tds:SerialNumber>SN-123456</tds:SerialNumber>`+
			`</tds:GetDeviceInformationResponse></s:Body></s:Envelope>`)
	}))
	t.Cleanup(ts.Close)

	h := TestHandler(nil, nil)
	body := fmt.Sprintf(`{"host":"127.0.0.1","port":%d,"timeout":2}`, probeServerPort(t, ts))
	rr := doRequest(t, h.Routes(), "POST", "/api/onvif/probe", strings.NewReader(body), "", "")

	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "ProbeCam")
	require.NotContains(t, rr.Body.String(), "reason",
		"successful probes keep today's payload shape (no reason key)")
}
