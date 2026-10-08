package timesync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// fakeCams implements CameraSource for tests.
type fakeCams struct {
	configs map[string]*config.CameraConfig
	ids     []string
}

func (f *fakeCams) GetCameraConfig(id string) *config.CameraConfig {
	return f.configs[id]
}

func (f *fakeCams) ONVIFCameraIDs() []string { return f.ids }

// timeServer fakes an ONVIF device serving GetSystemDateAndTime with an
// adjustable offset, plus SetSystemDateAndTime/SetNTP acceptance.
type timeServer struct {
	srv *httptest.Server
	// offset applied to the served clock; mutated by tests.
	offset time.Duration
	// setCalls counts SetSystemDateAndTime/SetNTP writes.
	setCalls int
	ntpHost  string
}

func newTimeServer(t *testing.T) *timeServer {
	t.Helper()
	ts := &timeServer{}
	ts.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		sb := string(body)
		switch {
		case strings.Contains(sb, "GetSystemDateAndTime"):
			dev := time.Now().UTC().Add(ts.offset).Truncate(time.Second)
			_, _ = w.Write([]byte(systemDTXML(dev)))
		case strings.Contains(sb, "SetNTP"):
			ts.setCalls++
			ts.ntpHost = extractTag(sb, "tt:IPv4Address>")
			_, _ = w.Write([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><SetNTPResponse/></s:Body></s:Envelope>`))
		case strings.Contains(sb, "SetSystemDateAndTime"):
			ts.setCalls++
			// Apply the write: reset offset to ~0.
			ts.offset = 0
			_, _ = w.Write([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><SetSystemDateAndTimeResponse/></s:Body></s:Envelope>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.srv.Close)
	return ts
}

func systemDTXML(dev time.Time) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><GetSystemDateAndTimeResponse><SystemDateAndTime>
<DateTimeType>Manual</DateTimeType><TimeZone><TZ>CST-8</TZ></TimeZone>
<UTCDateTime><Time><Hour>%d</Hour><Minute>%d</Minute><Second>%d</Second></Time><Date><Year>%d</Year><Month>%d</Month><Day>%d</Day></Date></UTCDateTime>
</SystemDateAndTime></GetSystemDateAndTimeResponse></s:Body></s:Envelope>`,
		dev.Hour(), dev.Minute(), dev.Second(), dev.Year(), int(dev.Month()), dev.Day())
}

func extractTag(s, tag string) string {
	i := strings.Index(s, "<"+tag)
	if i < 0 {
		return ""
	}
	rest := s[i:]
	end := strings.Index(rest, "</"+tag)
	if end < 0 {
		return ""
	}
	seg := rest[:end]
	if j := strings.Index(seg, ">"); j >= 0 {
		return seg[j+1:]
	}
	return ""
}

func onvifCam(id, endpoint, user, pass string) *config.CameraConfig {
	return &config.CameraConfig{ID: id, Name: id, Protocol: "onvif", ONVIFEndpoint: endpoint, Username: user, Password: pass}
}

func TestStatus_MeasuresAndReports(t *testing.T) {
	ts := newTimeServer(t)
	ts.offset = -120 * time.Second
	cams := &fakeCams{configs: map[string]*config.CameraConfig{"cam-1": onvifCam("cam-1", ts.srv.URL, "", "")}, ids: []string{"cam-1"}}
	svc := New(config.TimeSyncConfig{}, cams, nil, func() bool { return true })

	st, err := svc.Status(context.Background(), "cam-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Available || st.SkewSeconds > -115 || st.SkewSeconds < -125 {
		t.Errorf("status = available:%v skew:%v, want available skew≈-120", st.Available, st.SkewSeconds)
	}
	if st.TimeZone != "CST-8" || st.DateTimeType != "Manual" {
		t.Errorf("zone/type = %q/%q", st.TimeZone, st.DateTimeType)
	}
	if !st.SNTPEnabled {
		t.Error("sntp_enabled should mirror the provided probe func")
	}

	// Unknown camera errors.
	if _, err := svc.Status(context.Background(), "nope"); err == nil {
		t.Error("unknown camera should error")
	}
	// Non-ONVIF camera errors.
	cams.configs["cam-2"] = &config.CameraConfig{ID: "cam-2", Protocol: "rtsp_h264"}
	if _, err := svc.Status(context.Background(), "cam-2"); err == nil {
		t.Error("non-ONVIF camera should error")
	}
}

func TestCorrectCameraTime_SmallSkewNoWrite(t *testing.T) {
	ts := newTimeServer(t)
	ts.offset = 300 * time.Millisecond // < 1s → no write
	cams := &fakeCams{configs: map[string]*config.CameraConfig{"cam-1": onvifCam("cam-1", ts.srv.URL, "admin", "pw")}}
	svc := New(config.TimeSyncConfig{}, cams, nil, nil)

	res, err := svc.CorrectCameraTime(context.Background(), "cam-1", "")
	if err != nil {
		t.Fatalf("Correct: %v", err)
	}
	if res.Changed {
		t.Error("sub-second skew must not trigger a write")
	}
	if ts.setCalls != 0 {
		t.Errorf("device got %d writes, want 0", ts.setCalls)
	}
}

func TestCorrectCameraTime_WritesAndReReads(t *testing.T) {
	ts := newTimeServer(t)
	ts.offset = -90 * time.Second
	cams := &fakeCams{configs: map[string]*config.CameraConfig{"cam-1": onvifCam("cam-1", ts.srv.URL, "admin", "pw")}}
	svc := New(config.TimeSyncConfig{}, cams, nil, nil)

	res, err := svc.CorrectCameraTime(context.Background(), "cam-1", "CST-8")
	if err != nil {
		t.Fatalf("Correct: %v", err)
	}
	if !res.Changed {
		t.Fatal("90s skew should be corrected")
	}
	if ts.setCalls != 1 {
		t.Errorf("device writes = %d, want 1", ts.setCalls)
	}
	if res.AfterSeconds > 1 || res.AfterSeconds < -1 {
		t.Errorf("post-correction skew = %v, want ≈0", res.AfterSeconds)
	}
}

func TestCorrectCameraTime_NeedsCredentials(t *testing.T) {
	ts := newTimeServer(t)
	ts.offset = -90 * time.Second
	cams := &fakeCams{configs: map[string]*config.CameraConfig{"cam-1": onvifCam("cam-1", ts.srv.URL, "", "")}}
	svc := New(config.TimeSyncConfig{}, cams, nil, nil)

	_, err := svc.CorrectCameraTime(context.Background(), "cam-1", "")
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Errorf("expected credentials error, got %v", err)
	}
}

func TestSweep_CorrectsThresholdSkew_RespectsOptOut(t *testing.T) {
	ts := newTimeServer(t)
	ts.offset = -5 * time.Minute
	// Separate fake device for cam-2: cam-1's correction mutates its server's
	// offset, so sharing one server would hide whether cam-2 was written.
	ts2 := newTimeServer(t)
	ts2.offset = -5 * time.Minute
	optOut := false
	cams := &fakeCams{configs: map[string]*config.CameraConfig{
		"cam-1": onvifCam("cam-1", ts.srv.URL, "admin", "pw"),
		"cam-2": func() *config.CameraConfig {
			c := onvifCam("cam-2", ts2.srv.URL, "admin", "pw")
			c.AutoTimeSync = &optOut
			return c
		}(),
	}, ids: []string{"cam-1", "cam-2"}}
	svc := New(config.TimeSyncConfig{Auto: config.AutoTimeSyncConfig{Enabled: true, ThresholdSeconds: 30, MaxCorrectionSeconds: 3600, IntervalMinutes: 60}}, cams, nil, nil)

	ctx := context.Background()
	svc.sweep(ctx)

	if ts.setCalls != 1 {
		t.Errorf("cam-1 device writes = %d, want 1", ts.setCalls)
	}
	if ts2.setCalls != 0 {
		t.Errorf("opted-out cam-2 device writes = %d, want 0", ts2.setCalls)
	}
	st := svc.Statuses()["cam-1"]
	if st == nil || !st.Available {
		t.Fatal("cam-1 status missing after sweep")
	}
	if absF(st.SkewSeconds) > 2 {
		t.Errorf("cam-1 skew after sweep = %v, want ≈0", st.SkewSeconds)
	}
	// cam-2 stays at its measured (uncorrected) skew.
	st2 := svc.Statuses()["cam-2"]
	if st2 == nil {
		t.Fatal("opted-out camera should still get a measured status")
	}
	if absF(st2.SkewSeconds) < 240 {
		t.Errorf("cam-2 skew = %+v, want ≈-300 (opted out, measured only)", st2.SkewSeconds)
	}
}

func TestSweep_OverMaxNoWrite(t *testing.T) {
	ts := newTimeServer(t)
	ts.offset = -48 * time.Hour // beyond default max 1h
	cams := &fakeCams{configs: map[string]*config.CameraConfig{"cam-1": onvifCam("cam-1", ts.srv.URL, "admin", "pw")}, ids: []string{"cam-1"}}
	svc := New(config.TimeSyncConfig{Auto: config.AutoTimeSyncConfig{Enabled: true}}, cams, nil, nil)

	svc.sweep(context.Background())
	if ts.setCalls != 0 {
		t.Errorf("over-max skew must not write, got %d writes", ts.setCalls)
	}
}

func TestPointCameraAtNTP(t *testing.T) {
	ts := newTimeServer(t)
	cams := &fakeCams{configs: map[string]*config.CameraConfig{"cam-1": onvifCam("cam-1", ts.srv.URL, "admin", "pw")}}
	svc := New(config.TimeSyncConfig{}, cams, nil, nil)

	if err := svc.PointCameraAtNTP(context.Background(), "cam-1", "192.168.63.30"); err != nil {
		t.Fatalf("PointCameraAtNTP: %v", err)
	}
	if ts.ntpHost != "192.168.63.30" {
		t.Errorf("NTP host = %q, want 192.168.63.30", ts.ntpHost)
	}
	// SetNTP + DateTimeType flip = 2 device writes.
	if ts.setCalls != 2 {
		t.Errorf("device writes = %d, want 2 (SetNTP + mode flip)", ts.setCalls)
	}
}

func TestPosixLocalTZ(t *testing.T) {
	t.Helper()
	cases := []struct {
		name string
		t    time.Time
		want string
	}{
		{"UTC+8 whole hour", time.Date(2026, 10, 8, 12, 0, 0, 0, time.FixedZone("CST", 8*3600)), "CST-8"},
		{"UTC+5:30 fractional", time.Date(2026, 10, 8, 12, 0, 0, 0, time.FixedZone("IST", 5*3600+1800)), "IST-5:30"},
		{"UTC-5", time.Date(2026, 10, 8, 12, 0, 0, 0, time.FixedZone("EST", -5*3600)), "EST5"},
	}
	for _, c := range cases {
		oldLocal := time.Local
		time.Local = c.t.Location()
		got := posixLocalTZ(time.Now())
		time.Local = oldLocal
		if got != c.want {
			t.Errorf("%s: posixLocalTZ = %q, want %q", c.name, got, c.want)
		}
	}
}

func absF(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
