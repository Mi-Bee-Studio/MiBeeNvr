package onvif

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// Camera time-correction primitives (#time-sync).
//
// Read path: GetDeviceTime uses the pre-auth GetSystemDateAndTime (the same
// call measureClockSkew already makes for WS-Security digest auth), so camera
// time is observable even with NO credentials configured.
//
// Write paths: SetDeviceTime (tds:SetSystemDateAndTime, Manual) and SetNTPServer
// (tds:SetNTP + flip DateTimeType to NTP). Both are admin-level (WRITE_SYSTEM)
// operations and REQUIRE credentials — ErrNoCredentials is returned otherwise.
// They ride the raw-SOAP digest path with the device-time skew correction, so
// they work on exactly the cameras that need them: ones whose clocks (and thus
// digest windows) have drifted.

// ErrNoCredentials is returned by the write operations when the client was
// constructed without a username/password. Callers surface it as an explicit
// "credentials required" state rather than a generic device error.
var ErrNoCredentials = errors.New("onvif: camera credentials required for this operation")

// DeviceTime is the result of reading a camera's wall clock.
type DeviceTime struct {
	// UTCDateTime is the camera's clock, converted to UTC. Second resolution
	// (the ONVIF struct carries no sub-second field).
	UTCDateTime time.Time
	// LocalDateTime is the camera's clock in its own configured zone.
	LocalDateTime time.Time
	// TimeZone is the camera's POSIX TZ string (e.g. "CST-8"). May be empty
	// on devices that don't report one.
	TimeZone string
	// DateTimeType is how the camera sets its clock: "Manual" or "NTP".
	DateTimeType string
	// DaylightSavings reports the camera's DST flag.
	DaylightSavings bool
	// Skew is deviceTime − localTime measured with half-RTT compensation
	// (positive = camera ahead).
	Skew time.Duration
	// LocalReference is the NVR clock the skew was measured against.
	LocalReference time.Time
}

// GetDeviceTime reads the camera's clock via the unauthenticated
// GetSystemDateAndTime. It does NOT require Connect() — the raw SOAP path
// works on a bare NewClient(endpoint, ...) — so camera time is observable
// even when the recorder/auth is broken (which is exactly when it matters).
func (c *Client) GetDeviceTime(ctx context.Context) (*DeviceTime, error) {
	resp, localMid, err := c.querySystemDateAndTime(ctx, c.endpoint)
	if err != nil {
		return nil, err
	}
	d := resp.Time.UTC.Date
	t := resp.Time.UTC.Time
	if d.Year < 2000 || d.Month < 1 || d.Day < 1 {
		return nil, fmt.Errorf("onvif: device returned no usable clock (year=%d)", d.Year)
	}
	deviceUTC := time.Date(d.Year, time.Month(d.Month), d.Day, t.Hour, t.Minute, t.Second, 0, time.UTC)
	ld := resp.Time.Local.Date
	lt := resp.Time.Local.Time
	local := time.Time{}
	if ld.Year >= 2000 {
		local = time.Date(ld.Year, time.Month(ld.Month), ld.Day, lt.Hour, lt.Minute, lt.Second, 0, time.UTC)
	}
	return &DeviceTime{
		UTCDateTime:     deviceUTC,
		LocalDateTime:   local,
		TimeZone:        resp.Time.TimeZone.TZ,
		DateTimeType:    resp.Time.DateTimeType,
		DaylightSavings: resp.Time.DaylightSavings,
		Skew:            deviceUTC.Sub(localMid),
		LocalReference:  localMid,
	}, nil
}

// SetDeviceTime writes the NVR's current time to the camera
// (tds:SetSystemDateAndTime, DateTimeType=Manual). tz is a POSIX TZ string
// (e.g. "CST-8"); empty omits the TimeZone element (camera keeps its zone).
// The embedded UTC time is compensated by the measured skew and by the send
// delay, targeting sub-second residual error on a LAN.
//
// Requires admin credentials. A camera in NTP mode may refuse Manual setting
// with a Sender fault — the fault is surfaced verbatim for the caller to
// decide (e.g. point the camera at the NVR's SNTP server instead).
func (c *Client) SetDeviceTime(ctx context.Context, tz string) error {
	if c.username == "" {
		return ErrNoCredentials
	}

	// Skew first: digest timestamps must be built on the camera's view of
	// "now" or a badly-drifted camera rejects the very call that would fix it.
	skew := c.measureClockSkew(ctx, c.endpoint)
	appliedAt := time.Now().UTC().Add(skew)

	var tzElem string
	if tz != "" {
		tzElem = fmt.Sprintf("<tds:TimeZone><tt:TZ>%s</tt:TZ></tds:TimeZone>", xmlEscape(tz))
	}
	soapBody := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
 xmlns:tds="http://www.onvif.org/ver10/device/wsdl"
 xmlns:tt="http://www.onvif.org/ver10/schema">
  <s:Body>
    <tds:SetSystemDateAndTime>
      <tds:DateTimeType>Manual</tds:DateTimeType>
      <tds:DaylightSavings>false</tds:DaylightSavings>
      %s
      <tds:UTCDateTime>
        <tt:Time>
          <tt:Hour>%d</tt:Hour>
          <tt:Minute>%d</tt:Minute>
          <tt:Second>%d</tt:Second>
        </tt:Time>
        <tt:Date>
          <tt:Year>%d</tt:Year>
          <tt:Month>%d</tt:Month>
          <tt:Day>%d</tt:Day>
        </tt:Date>
      </tds:UTCDateTime>
    </tds:SetSystemDateAndTime>
  </s:Body>
</s:Envelope>`,
		tzElem,
		appliedAt.Hour(), appliedAt.Minute(), appliedAt.Second(),
		appliedAt.Year(), int(appliedAt.Month()), appliedAt.Day())

	respBody, err := c.doRawSOAPDigestDeviceTime(ctx, c.endpoint, soapBody)
	if err != nil {
		return err
	}
	return soapFaultCheck(respBody)
}

// SetNTPServer points the camera at an NTP/SNTP server (host: IPv4 or DNS
// name) and flips its DateTimeType to NTP so it actually uses the server.
// Admin credentials required. Long-term self-healing path: after this, the
// camera re-syncs on its own schedule (including after power loss with a dead
// RTC battery) — pair with the NVR's built-in SNTP server.
func (c *Client) SetNTPServer(ctx context.Context, host string) error {
	if c.username == "" {
		return ErrNoCredentials
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("onvif: NTP server host is empty")
	}

	var hostType, hostElem string
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil {
			hostType = "IPv4"
			hostElem = fmt.Sprintf("<tt:IPv4Address>%s</tt:IPv4Address>", host)
		} else {
			hostType = "IPv6"
			hostElem = fmt.Sprintf("<tt:IPv6Address>%s</tt:IPv6Address>", host)
		}
	} else {
		hostType = "DNS"
		hostElem = fmt.Sprintf("<tt:DNSname>%s</tt:DNSname>", xmlEscape(host))
	}

	ntpBody := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
 xmlns:tds="http://www.onvif.org/ver10/device/wsdl"
 xmlns:tt="http://www.onvif.org/ver10/schema">
  <s:Body>
    <tds:SetNTP>
      <tds:FromDHCP>false</tds:FromDHCP>
      <tds:NTPManual>
        <tt:Type>%s</tt:Type>
        %s
      </tds:NTPManual>
    </tds:SetNTP>
  </s:Body>
</s:Envelope>`, hostType, hostElem)

	respBody, err := c.doRawSOAPDigestDeviceTime(ctx, c.endpoint, ntpBody)
	if err != nil {
		return err
	}
	if err := soapFaultCheck(respBody); err != nil {
		return err
	}

	// Flip DateTimeType to NTP (no time value needed — the camera pulls it).
	// Some firmwares accept SetNTP alone and switch implicitly; the explicit
	// flip covers the ones that don't. A fault here leaves the NTP server
	// configured but the mode unchanged — still report it so the caller knows
	// the camera may need a manual sync in the meantime.
	flipBody := `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
 xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
  <s:Body>
    <tds:SetSystemDateAndTime>
      <tds:DateTimeType>NTP</tds:DateTimeType>
      <tds:DaylightSavings>false</tds:DaylightSavings>
    </tds:SetSystemDateAndTime>
  </s:Body>
</s:Envelope>`
	respBody, err = c.doRawSOAPDigestDeviceTime(ctx, c.endpoint, flipBody)
	if err != nil {
		return fmt.Errorf("NTP server set, but switching DateTimeType to NTP failed: %w", err)
	}
	if err := soapFaultCheck(respBody); err != nil {
		return fmt.Errorf("NTP server set, but switching DateTimeType to NTP failed: %w", err)
	}
	return nil
}

// soapFaultCheck scans a SOAP 1.2 response body for a Fault element. Some
// devices answer HTTP 200 with a faulted envelope; without this check a
// rejected SetSystemDateAndTime would look like success.
func soapFaultCheck(body []byte) error {
	s := string(body)
	for _, marker := range []string{"<s:Fault>", "<SOAP-ENV:Fault>", "<soap:Fault>", "<SOAP-ENV:fault>"} {
		if i := strings.Index(s, marker); i >= 0 {
			frag := s[i:]
			if len(frag) > 600 {
				frag = frag[:600]
			}
			return fmt.Errorf("onvif: SOAP fault: %s", frag)
		}
	}
	return nil
}
