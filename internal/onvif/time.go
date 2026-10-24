package onvif

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	onvifdevice "github.com/mickeyzzc/onvif-go/v2/device"
	onvifgo "github.com/mickeyzzc/onvif-go/v2/onvif"
)

// Camera time-correction primitives (#time-sync).
//
// The SOAP mechanics live in onvif-go's device package (onvif-go#129): the
// write paths below are thin adapters over device.Service. The read path
// stays local: it adds the Skew/LocalReference measurement (half-RTT
// compensated against the NVR clock) that drives the timesync service's
// drift thresholds, built on the same querySystemDateAndTime machinery the
// WS-Security digest auth uses (wssecurity.go) — so camera time stays
// observable even with NO credentials configured, exactly when it matters.
//
// Write paths require admin credentials — ErrNoCredentials is returned
// otherwise (the library's sentinel, re-exported so callers keep one
// errors.Is target). The library client the writes ride was Initialized at
// Connect() with the measured clock skew applied, so they work on exactly
// the cameras that need them: ones whose clocks (and thus digest windows)
// have drifted.

// ErrNoCredentials is returned by the write operations when the client was
// constructed without a username/password. Callers surface it as an explicit
// "credentials required" state rather than a generic device error.
var ErrNoCredentials = onvifdevice.ErrNoCredentials

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
// The embedded UTC time is compensated by the measured skew, targeting
// sub-second residual error on a LAN (the library sends the instant
// verbatim, so the compensation happens here).
//
// Requires admin credentials. Works without Connect() — the timesync
// service must be able to fix the clock of a drifted camera whose full
// handshake (and digest window) is broken. A camera in NTP mode may refuse
// Manual setting with a Sender fault — the fault is surfaced verbatim for
// the caller to decide (e.g. point the camera at the NVR's SNTP server
// instead).
func (c *Client) SetDeviceTime(ctx context.Context, tz string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.username == "" {
		return ErrNoCredentials
	}

	// Skew first: the instant must be built on the camera's view of "now" or
	// a badly-drifted camera gets a stale clock written back.
	skew := c.measureClockSkew(ctx, c.endpoint)
	lib, err := c.deviceWriter(skew)
	if err != nil {
		return err
	}
	appliedAt := time.Now().UTC().Add(skew)

	return lib.Device().SetDeviceTime(ctx, onvifdevice.DeviceTime{
		UTCDateTime: appliedAt,
		TimeZone:    tz,
	})
}

// SetNTPServer points the camera at an NTP/SNTP server (host: IPv4 or DNS
// name) and flips its DateTimeType to NTP so it actually uses the server.
// Admin credentials required. Works without Connect() (same rationale as
// SetDeviceTime). Long-term self-healing path: after this, the camera
// re-syncs on its own schedule (including after power loss with a dead RTC
// battery) — pair with the NVR's built-in SNTP server.
func (c *Client) SetNTPServer(ctx context.Context, host string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.username == "" {
		return ErrNoCredentials
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("onvif: NTP server host is empty")
	}

	lib, err := c.deviceWriter(c.measureClockSkew(ctx, c.endpoint))
	if err != nil {
		return err
	}

	nh := onvifdevice.NetworkHost{Type: "DNS", DNSname: host}
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil {
			nh = onvifdevice.NetworkHost{Type: "IPv4", IPv4Address: host}
		} else {
			nh = onvifdevice.NetworkHost{Type: "IPv6", IPv6Address: host}
		}
	}

	return lib.Device().SetNTPServer(ctx, []onvifdevice.NetworkHost{nh})
}

// deviceWriter returns a library client for the admin time writes. The
// connected client is preferred (Initialize done, Connect-time skew already
// applied). Before Connect the writes must still work — an ephemeral client
// is built against c.endpoint with the freshly measured skew, mirroring the
// raw-SOAP path these wrappers replaced (fixing a drifted camera's clock
// cannot depend on a healthy full handshake).
func (c *Client) deviceWriter(skew time.Duration) (*onvifgo.Client, error) {
	if c.ready {
		return c.client, nil
	}
	lib, err := newLibClient(c.endpoint, c.username, c.password)
	if err != nil {
		return nil, fmt.Errorf("create ONVIF client: %w", err)
	}
	if skew != 0 {
		lib.SetClockSkew(skew)
	}
	return lib, nil
}

// soapFaultCheck scans a SOAP 1.2 response body for a Fault element. Some
// devices answer HTTP 200 with a faulted envelope; without this check a
// rejected request would look like success (used by the credentials probe).
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
