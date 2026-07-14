package onvif

import (
	"context"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // SHA1 required by the ONVIF/WSS UsernameToken profile
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// This file implements WS-Security UsernameToken with a DEVICE-DERIVED timestamp,
// fixing the time-skew auth failures that affect ONVIF cameras (notably Hikvision)
// when the NVR's clock and the camera's clock diverge beyond the device's replay
// window (commonly ±5 min).
//
// Background: the OASIS UsernameToken Profile bakes the `Created` timestamp into
// the password digest: digest = base64(sha1(nonce + created + password)). The
// onvif-go fork computes `Created` from the LOCAL clock (soap.go), so when the
// clocks disagree, every digest is rejected as "sender not authorized" — a
// generic-looking auth failure that's extremely hard to debug and impossible to
// fix without either syncing the clocks or using the device's own time.
//
// The fix here: query the device's clock via the unauthenticated
// GetSystemDateAndTime, compute the skew, and build the digest using the
// device's view of "now". This is the standard remedy (EdgeX device-onvif,
// node-onvif, ONVIF Device Manager all do this). We keep it in the NVR's raw-SOAP
// layer so no fork change is required.

// wsseDigestUsernameToken builds a WS-Security UsernameToken header using
// PasswordDigest, with `created` as the timestamp. This is the SHA1 digest form
// (the strong-auth default); PasswordText is a separate, weaker fallback.
//
// Per OASIS UsernameTokenProfile 1.0/1.1:
//
//	digest = base64( SHA1( nonce + created + password ) )
//
// where nonce is a random 16-byte value (base64-encoded in the element) and
// created is an ISO-8601 UTC timestamp.
//
// The `created` argument is what makes this skew-aware: pass the device's
// wall-clock time (now + measured skew) instead of the local clock.
func wsseDigestUsernameToken(username, password string, created time.Time, nonce []byte) string {
	createdStr := created.UTC().Format(time.RFC3339)
	// Raw SHA1 over nonce (raw bytes) + created + password, per the profile.
	digestInput := append(append([]byte{}, nonce...), []byte(createdStr)...)
	digestInput = append(digestInput, []byte(password)...)
	digested := sha1.Sum(digestInput) //nolint:gosec // SHA1 mandated by the ONVIF profile
	return `<s:Header><wsse:Security xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd" xmlns:wsu="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd"><wsse:UsernameToken><wsse:Username>` +
		xmlEscape(username) +
		`</wsse:Username><wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">` +
		base64.StdEncoding.EncodeToString(digested[:]) +
		`</wsse:Password><wsse:Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">` +
		base64.StdEncoding.EncodeToString(nonce) +
		`</wsse:Nonce><wsu:Created>` + createdStr + `</wsu:Created></wsse:UsernameToken></wsse:Security></s:Header>`
}

// newNonce returns a fresh 16-byte random nonce for a UsernameToken.
func newNonce() []byte {
	n := make([]byte, 16)
	_, _ = rand.Read(n) //nolint:gosec // best-effort randomness for replay protection
	return n
}

// xmlEscape escapes the five significant XML characters in a username. Usernames
// are typically simple, but a malformed one must not break the SOAP envelope.
func xmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}

// systemDateTimeResponse is the subset of GetSystemDateAndTime we need to read
// the device's wall clock. Namespaces follow the ONVIF core schema.
type systemDateTimeResponse struct {
	XMLName xml.Name `xml:"GetSystemDateAndTimeResponse"`
	Time    struct {
		XMLName xml.Name `xml:"SystemDateAndTime"`
		UTC     struct {
			XMLName xml.Name `xml:"UTCDateTime"`
			Date    struct {
				XMLName xml.Name `xml:"Date"`
				Year    int      `xml:"Year"`
				Month   int      `xml:"Month"`
				Day     int      `xml:"Day"`
			} `xml:"Date"`
			Time struct {
				XMLName xml.Name `xml:"Time"`
				Hour    int      `xml:"Hour"`
				Minute  int      `xml:"Minute"`
				Second  int      `xml:"Second"`
			} `xml:"Time"`
		} `xml:"UTCDateTime"`
	} `xml:"SystemDateAndTime"`
}

// measureClockSkew queries the device's unauthenticated GetSystemDateAndTime and
// returns the offset (deviceTime - localTime) the caller should ADD to its clock
// when building digest timestamps for this device. Returns 0 (no skew) when the
// device's time can't be read (then the caller falls back to local time — the
// legacy behavior — rather than failing outright).
func (c *Client) measureClockSkew(ctx context.Context, endpoint string) time.Duration {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <tds:GetSystemDateAndTime xmlns:tds="http://www.onvif.org/ver10/device/wsdl"/>
  </s:Body>
</s:Envelope>`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return 0
	}
	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")

	// Snapshot local time at send so network latency doesn't pollute the skew.
	localAtSend := time.Now().UTC()
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0
	}
	// Account for half the round-trip (assume symmetric latency) to tighten the skew.
	localNow := time.Now().UTC()

	var parsed systemDateTimeResponse
	if err := xml.Unmarshal(respBody, &parsed); err != nil {
		return 0
	}
	d := parsed.Time.UTC.Date
	t := parsed.Time.UTC.Time
	if d.Year < 2000 || d.Month < 1 || d.Day < 1 {
		return 0 // unparsable / zero time — skip skew correction
	}
	deviceTime := time.Date(d.Year, time.Month(d.Month), d.Day, t.Hour, t.Minute, t.Second, 0, time.UTC)
	localMid := localAtSend.Add(localNow.Sub(localAtSend) / 2)
	return deviceTime.Sub(localMid)
}

// doRawSOAPDigestDeviceTime sends a raw SOAP request authenticated with a
// PasswordDigest UsernameToken whose timestamp is adjusted by the device's
// measured clock skew. Use this as a fallback when the standard (local-time)
// digest fails on a camera that enforces a tight replay window.
//
// The soapBody must be a full SOAP envelope (with <s:Envelope> and <s:Body>);
// the WS-Security header is injected before <s:Body>.
func (c *Client) doRawSOAPDigestDeviceTime(ctx context.Context, endpoint, soapBody string) ([]byte, error) {
	skew := c.measureClockSkew(ctx, endpoint)
	created := time.Now().UTC().Add(skew)
	header := wsseDigestUsernameToken(c.username, c.password, created, newNonce())
	bodyWithAuth := strings.Replace(soapBody, "<s:Body>", header+"<s:Body>", 1)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(bodyWithAuth))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SOAP request failed with status %d: %s", resp.StatusCode, truncateStr(string(body), 500))
	}
	if skew != 0 {
		logger.Debug("authenticated SOAP with device-time digest", "endpoint", endpoint, "skew_seconds", skew.Seconds())
	}
	return body, nil
}

// AuthDiagnosis is the result of probing why an ONVIF device rejects credentials.
// It distinguishes the three common root causes that all look like "auth failed"
// to the user: a clock skew (digest replay-window violation), wrong credentials,
// or the device not speaking ONVIF auth at all.
type AuthDiagnosis struct {
	SkewDetected bool    // a significant clock skew was measured
	SkewSeconds  float64 // deviceTime - localTime, in seconds (signed)
	DigestOK     bool    // the device-time digest SOAP call succeeded
	Diagnosis    string  // human-readable explanation + suggested fix
}

// skewThreshold is the magnitude beyond which clock skew likely causes digest
// rejection. ONVIF replay windows are commonly ±5 min; we flag anything over 2
// min so the user gets a heads-up before hitting the device's tighter limit.
const skewThreshold = 2 * time.Minute

// DiagnoseAuth runs the time-skew diagnosis: measure the device's clock skew,
// then attempt an authenticated GetDeviceInformation using a device-time-adjusted
// digest. When DigestOK is true despite the standard (local-time) auth having
// failed, the root cause is confirmed as clock skew — the caller can surface a
// targeted "sync your camera's clock" message instead of a generic auth error.
//
// Best-effort and non-fatal: any network/parse failure returns a zeroed
// diagnosis (SkewDetected=false), so callers fall back to their existing error.
func (c *Client) DiagnoseAuth(ctx context.Context) AuthDiagnosis {
	skew := c.measureClockSkew(ctx, c.endpoint)
	out := AuthDiagnosis{SkewSeconds: skew.Seconds()}
	if absDuration(skew) < skewThreshold {
		out.Diagnosis = "no significant clock skew detected"
		return out
	}
	out.SkewDetected = true
	// Retry GetDeviceInformation with the device's clock. If it succeeds, skew
	// is confirmed as the auth-failure root cause.
	soapBody := `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
  <s:Body>
    <tds:GetDeviceInformation/>
  </s:Body>
</s:Envelope>`
	if _, err := c.doRawSOAPDigestDeviceTime(ctx, c.endpoint, soapBody); err != nil {
		out.Diagnosis = fmt.Sprintf("clock skew of %.0f min detected but device-time digest still failed: %v — credentials may ALSO be wrong", skew.Minutes(), err)
		return out
	}
	out.DigestOK = true
	out.Diagnosis = fmt.Sprintf("clock skew of %.0f min detected — the camera's clock differs from the NVR's, which breaks digest auth. Sync the camera's time (NTP) or set it manually. A device-time-adjusted request succeeded, confirming skew is the root cause.", skew.Minutes())
	return out
}

// absDuration returns the absolute value of a time.Duration.
func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
