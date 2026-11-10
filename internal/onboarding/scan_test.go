package onboarding

// #1120: the discovery sweep probes every address of the factory-default
// subnet with unauthenticated device probes and classifies what answers:
//   - ISAPI deviceInfo served with activated=false → a factory-fresh
//     (unactivated) camera — the true zero-touch target;
//   - ISAPI deviceInfo with activated=true → an activated camera whose
//     credentials the NVR must be told (adopt-by-guidance);
//   - HTTP 401 → activated, credentials required;
//   - an ONVIF-only answer (no ISAPI face) → classified via the serial the
//     unauthenticated GetDeviceInformation probe returns.
//
// Everything below runs against httptest servers — no network.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func isapiDeviceXML(activated bool, serial string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<DeviceInfo version="2.0" xmlns="http://www.isapi.org/ver20/XMLSchema">
<deviceName>IP Camera</deviceName>
<manufacturer>Hikvision</manufacturer>
<model>DS-TEST</model>
<serialNumber>%s</serialNumber>
<activated>%t</activated>
</DeviceInfo>`, serial, activated)
}

func hostOf(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(ts.URL, "http://"))
	// The sweep probes IP:80-style addresses; tests use loopback:port via
	// the classification entry point directly (classifyHost).
	return "127.0.0.1:" + port
}

func TestClassifyHost_Unactivated(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ISAPI/System/deviceInfo" {
			// Factory-fresh cameras serve this without auth.
			fmt.Fprint(w, isapiDeviceXML(false, "DS-NEW-1"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(ts.Close)

	c := classifyHost(context.Background(), hostOf(t, ts), time.Second)
	if c == nil {
		t.Fatal("classifyHost() = nil, want a candidate")
	}
	if c.State != CandidateUnactivated {
		t.Errorf("State = %q, want %q", c.State, CandidateUnactivated)
	}
	if c.Serial != "DS-NEW-1" {
		t.Errorf("Serial = %q, want DS-NEW-1", c.Serial)
	}
	if c.Manufacturer != "Hikvision" {
		t.Errorf("Manufacturer = %q", c.Manufacturer)
	}
}

func TestClassifyHost_ActivatedAuthRequired(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(ts.Close)

	c := classifyHost(context.Background(), hostOf(t, ts), time.Second)
	if c == nil {
		t.Fatal("classifyHost() = nil, want an auth-required candidate")
	}
	if c.State != CandidateAuthRequired {
		t.Errorf("State = %q, want %q", c.State, CandidateAuthRequired)
	}
}

func TestClassifyHost_NonONVIFSkipped(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<html><body>router admin page</body></html>")
	}))
	t.Cleanup(ts.Close)

	if c := classifyHost(context.Background(), hostOf(t, ts), time.Second); c != nil {
		t.Errorf("classifyHost() = %+v, want nil for a non-device host", c)
	}
}

func TestScanSubnet_FindsCandidates(t *testing.T) {
	// A single-address "subnet" pointing at the test server: the sweep must
	// find and classify it.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ISAPI/System/deviceInfo" {
			fmt.Fprint(w, isapiDeviceXML(false, "DS-SWEEP"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(ts.Close)

	_, port, _ := net.SplitHostPort(strings.TrimPrefix(ts.URL, "http://"))
	cands := ScanSubnet(context.Background(), []string{"127.0.0.1:" + port}, time.Second*2, 4)
	found := false
	for _, c := range cands {
		if c.Serial == "DS-SWEEP" && c.State == CandidateUnactivated {
			found = true
		}
	}
	if !found {
		t.Fatalf("ScanSubnet() missed the candidate: %+v", cands)
	}
}

func TestScanSubnet_Bounded(t *testing.T) {
	// Many dead addresses: the sweep must finish within the budget rather
	// than serializing the full timeout per host.
	dead := make([]string, 0, 16)
	for i := 1; i <= 16; i++ {
		dead = append(dead, fmt.Sprintf("127.0.0.1:%d", 1)) // port 1: refuses fast
	}
	start := time.Now()
	cands := ScanSubnet(context.Background(), dead, time.Millisecond*400, 16)
	if len(cands) != 0 {
		t.Errorf("expected no candidates on dead hosts, got %+v", cands)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("sweep took %v; bounded parallelism is broken", elapsed)
	}
}
