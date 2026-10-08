package onvif

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// systemDateAndTimeXML renders a GetSystemDateAndTimeResponse for the given
// device UTC time (test helper shared by time + credential tests).
func systemDateAndTimeXML(dev time.Time, dateTimeType, tz string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <GetSystemDateAndTimeResponse>
      <SystemDateAndTime>
        <DateTimeType>%s</DateTimeType>
        <DaylightSavings>false</DaylightSavings>
        <TimeZone><TZ>%s</TZ></TimeZone>
        <UTCDateTime>
          <Time><Hour>%d</Hour><Minute>%d</Minute><Second>%d</Second></Time>
          <Date><Year>%d</Year><Month>%d</Month><Day>%d</Day></Date>
        </UTCDateTime>
        <LocalDateTime>
          <Time><Hour>%d</Hour><Minute>%d</Minute><Second>%d</Second></Time>
          <Date><Year>%d</Year><Month>%d</Month><Day>%d</Day></Date>
        </LocalDateTime>
      </SystemDateAndTime>
    </GetSystemDateAndTimeResponse>
  </s:Body>
</s:Envelope>`,
		dateTimeType, tz,
		dev.UTC().Hour(), dev.UTC().Minute(), dev.UTC().Second(),
		dev.UTC().Year(), int(dev.UTC().Month()), dev.UTC().Day(),
		dev.UTC().Add(8*time.Hour).Hour(), dev.UTC().Add(8*time.Hour).Minute(), dev.UTC().Add(8*time.Hour).Second(),
		dev.UTC().Add(8*time.Hour).Year(), int(dev.UTC().Add(8*time.Hour).Month()), dev.UTC().Add(8*time.Hour).Day())
}

func TestGetDeviceTime_ParsesFieldsAndSkew(t *testing.T) {
	t.Helper()
	deviceClock := time.Now().UTC().Add(-90 * time.Second).Truncate(time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(systemDateAndTimeXML(deviceClock, "NTP", "CST-8")))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	dt, err := c.GetDeviceTime(context.Background())
	if err != nil {
		t.Fatalf("GetDeviceTime: %v", err)
	}
	if dt.DateTimeType != "NTP" {
		t.Errorf("DateTimeType = %q, want NTP", dt.DateTimeType)
	}
	if dt.TimeZone != "CST-8" {
		t.Errorf("TimeZone = %q, want CST-8", dt.TimeZone)
	}
	if !dt.UTCDateTime.Equal(deviceClock) {
		t.Errorf("UTCDateTime = %v, want %v", dt.UTCDateTime, deviceClock)
	}
	// Skew ≈ -90s (±RTT tolerance for the test loopback round trip).
	if dt.Skew < -95*time.Second || dt.Skew > -85*time.Second {
		t.Errorf("Skew = %v, want ≈ -90s", dt.Skew)
	}
}

func TestGetDeviceTime_UnusableClockIsError(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(systemDateAndTimeXML(time.Time{}, "Manual", "")))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	if _, err := c.GetDeviceTime(context.Background()); err == nil {
		t.Error("expected error for zero clock, got nil")
	}
}

func TestSetDeviceTime_RequiresCredentials(t *testing.T) {
	t.Helper()
	c := NewClient("http://127.0.0.1:1/", "", "")
	if err := c.SetDeviceTime(context.Background(), "CST-8"); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("SetDeviceTime without credentials = %v, want ErrNoCredentials", err)
	}
}

func TestSetDeviceTime_SendsManualWithSkewCompensatedTime(t *testing.T) {
	t.Helper()
	deviceClock := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		if strings.Contains(string(body), "GetSystemDateAndTime") {
			_, _ = w.Write([]byte(systemDateAndTimeXML(deviceClock, "Manual", "CST-8")))
			return
		}
		gotBody = string(body)
		_, _ = w.Write([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><SetSystemDateAndTimeResponse/></s:Body></s:Envelope>`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "admin", "secret")
	if err := c.SetDeviceTime(context.Background(), "CST-8"); err != nil {
		t.Fatalf("SetDeviceTime: %v", err)
	}
	for _, want := range []string{
		"<tds:DateTimeType>Manual</tds:DateTimeType>",
		"<tt:TZ>CST-8</tt:TZ>",
		"<wsse:Username>admin</wsse:Username>",
	} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("SetSystemDateAndTime body missing %q\nbody: %s", want, gotBody)
		}
	}
}

func TestSetDeviceTime_FaultSurfaces(t *testing.T) {
	t.Helper()
	deviceClock := time.Now().UTC().Truncate(time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		if strings.Contains(string(body), "GetSystemDateAndTime") {
			_, _ = w.Write([]byte(systemDateAndTimeXML(deviceClock, "NTP", "")))
			return
		}
		// NTP-mode camera refuses Manual setting — HTTP 200 + SOAP fault.
		_, _ = w.Write([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><s:Fault><s:Value>s:Sender</s:Value><s:Subcode><s:Value>ter:NotAuthorized</s:Value></s:Subcode></s:Fault></s:Body></s:Envelope>`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "admin", "secret")
	err := c.SetDeviceTime(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "SOAP fault") {
		t.Errorf("expected surfaced SOAP fault, got %v", err)
	}
}

func TestSetNTPServer_IPv4AndDNSAndModeFlip(t *testing.T) {
	t.Helper()
	deviceClock := time.Now().UTC().Truncate(time.Second)
	var setNTPBody, flipBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		switch {
		case strings.Contains(string(body), "GetSystemDateAndTime"):
			_, _ = w.Write([]byte(systemDateAndTimeXML(deviceClock, "Manual", "")))
		case strings.Contains(string(body), "SetNTP"):
			setNTPBody = string(body)
			_, _ = w.Write([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><SetNTPResponse/></s:Body></s:Envelope>`))
		default:
			flipBody = string(body)
			_, _ = w.Write([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><SetSystemDateAndTimeResponse/></s:Body></s:Envelope>`))
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "admin", "secret")
	if err := c.SetNTPServer(context.Background(), "192.168.63.30"); err != nil {
		t.Fatalf("SetNTPServer(IPv4): %v", err)
	}
	for _, want := range []string{"<tt:Type>IPv4</tt:Type>", "<tt:IPv4Address>192.168.63.30</tt:IPv4Address>", "<tds:FromDHCP>false</tds:FromDHCP>"} {
		if !strings.Contains(setNTPBody, want) {
			t.Errorf("SetNTP body missing %q\nbody: %s", want, setNTPBody)
		}
	}
	if !strings.Contains(flipBody, "<tds:DateTimeType>NTP</tds:DateTimeType>") {
		t.Errorf("DateTimeType flip body missing NTP mode\nbody: %s", flipBody)
	}

	if err := c.SetNTPServer(context.Background(), "nvr.lan"); err != nil {
		t.Fatalf("SetNTPServer(DNS): %v", err)
	}
	if !strings.Contains(setNTPBody, "<tt:Type>DNS</tt:Type>") || !strings.Contains(setNTPBody, "<tt:DNSname>nvr.lan</tt:DNSname>") {
		t.Errorf("SetNTP DNS form wrong\nbody: %s", setNTPBody)
	}
}

func TestSetNTPServer_RequiresCredentialsAndHost(t *testing.T) {
	t.Helper()
	c := NewClient("http://127.0.0.1:1/", "", "")
	if err := c.SetNTPServer(context.Background(), "1.2.3.4"); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("no-credentials error = %v, want ErrNoCredentials", err)
	}
	c2 := NewClient("http://127.0.0.1:1/", "admin", "pw")
	if err := c2.SetNTPServer(context.Background(), "  "); err == nil {
		t.Error("empty host should error")
	}
}
