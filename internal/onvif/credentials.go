package onvif

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Camera credential probing primitives (#credprobe).
//
// Purpose: the NVR's time-sync WRITE operations (SetSystemDateAndTime/SetNTP)
// and device management need ADMIN credentials. Cameras enrolled by
// auto-discovery often arrive with factory defaults — this surface lets the
// NVR test a small candidate list of industry-default credentials against a
// newly discovered camera (once per camera, enforced by the caller), and
// optionally rotate the matched account to the operator's chosen password
// (tds:SetUser).
//
// All calls here are single raw-SOAP posts with no GetServices handshake, so
// probing stays cheap and works before any Connect().

// CredentialVerdict classifies one credential attempt.
type CredentialVerdict int

const (
	// CredentialAccepted: the device answered 200 without a fault — the
	// credential authenticates at (at least) the level GetUsers requires.
	CredentialAccepted CredentialVerdict = iota
	// CredentialRejected: the device definitively refused the credential
	// (HTTP 401/403 or an auth fault). Safe to try the next candidate.
	CredentialRejected
	// CredentialInconclusive: transport error or an ambiguous device answer.
	// The caller should NOT keep hammering the device (lockout risk) — treat
	// the run as finished.
	CredentialInconclusive
)

// CredentialProber tests credentials against one ONVIF device endpoint and
// rotates passwords. It caches the device clock skew for its lifetime (one
// probe run), since re-measuring per candidate only adds round trips without
// changing the answer.
type CredentialProber struct {
	endpoint string

	mu           sync.Mutex
	skew         time.Duration
	skewMeasured bool
}

// NewCredentialProber creates a prober for a device endpoint (the camera's
// ONVIF device service URL).
func NewCredentialProber(endpoint string) *CredentialProber {
	return &CredentialProber{endpoint: endpoint}
}

// skewFor measures the device clock skew once per prober (unauthenticated
// GetSystemDateAndTime) and reuses it for every digest the prober builds.
func (p *CredentialProber) skewFor(ctx context.Context) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.skewMeasured {
		c := &Client{endpoint: p.endpoint}
		p.skew = c.measureClockSkew(ctx, p.endpoint)
		p.skewMeasured = true
	}
	return p.skew
}

// Try tests one username/password via tds:GetUsers — an admin-level call on
// mainstream firmware (Hikvision/Dahua/Axis all gate user management), which
// is the level the time-sync writes need. Digest auth first; on a definitive
// rejection it retries once with PasswordText (some budget firmwares reject
// digests outright — same fallback as DeviceManagerImpl.GetUsers).
func (p *CredentialProber) Try(ctx context.Context, username, password string) CredentialVerdict {
	const getUsers = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
 xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
  <s:Body><tds:GetUsers/></s:Body>
</s:Envelope>`

	skew := p.skewFor(ctx)
	status, body, err := postSOAPAuth(ctx, p.endpoint, getUsers, username, password, true, skew)
	if err != nil {
		return CredentialInconclusive
	}
	verdict := classifyAuthAnswer(status, body)
	if verdict == CredentialRejected {
		// PasswordText fallback for digest-hostile firmware.
		status, body, err = postSOAPAuth(ctx, p.endpoint, getUsers, username, password, false, skew)
		if err != nil {
			return CredentialInconclusive
		}
		verdict = classifyAuthAnswer(status, body)
	}
	return verdict
}

// SetUserPassword rotates targetUser's password to newPassword, authenticating
// as authUser/authPass (the credential a successful Try returned). Wire format
// mirrors the onvif-go Security().SetUser call (tds:SetUser / tds:User with
// tt:Username + tt:Password; empty UserLevel = modify-only semantics).
func (p *CredentialProber) SetUserPassword(ctx context.Context, authUser, authPass, targetUser, newPassword string) error {
	soapBody := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
 xmlns:tds="http://www.onvif.org/ver10/device/wsdl"
 xmlns:tt="http://www.onvif.org/ver10/schema">
  <s:Body>
    <tds:SetUser>
      <tds:User>
        <tt:Username>%s</tt:Username>
        <tt:Password>%s</tt:Password>
      </tds:User>
    </tds:SetUser>
  </s:Body>
</s:Envelope>`, xmlEscape(targetUser), xmlEscape(newPassword))

	skew := p.skewFor(ctx)
	status, body, err := postSOAPAuth(ctx, p.endpoint, soapBody, authUser, authPass, true, skew)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("SetUser failed with status %d: %s", status, truncateStr(string(body), 300))
	}
	return soapFaultCheck(body)
}

// classifyAuthAnswer maps one GetUsers round trip to a verdict.
func classifyAuthAnswer(status int, body []byte) CredentialVerdict {
	if status == http.StatusOK {
		if soapFaultCheck(body) != nil {
			// A 200-with-fault on GetUsers: auth faults read as rejection;
			// anything else (ActionNotSupported & friends) is inconclusive —
			// this device doesn't behave predictably, stop probing it.
			low := strings.ToLower(string(body))
			if strings.Contains(low, "notauthorized") || strings.Contains(low, "not authorized") ||
				strings.Contains(low, "unauthorized") || strings.Contains(low, "authentication") {
				return CredentialRejected
			}
			return CredentialInconclusive
		}
		return CredentialAccepted
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return CredentialRejected
	}
	// SOAP 1.2 faults commonly surface as 400/500 with an auth fault body.
	if status == http.StatusBadRequest || status == http.StatusInternalServerError {
		low := strings.ToLower(string(body))
		if strings.Contains(low, "notauthorized") || strings.Contains(low, "not authorized") ||
			strings.Contains(low, "unauthorized") || strings.Contains(low, "authentication") ||
			strings.Contains(low, "sender") {
			return CredentialRejected
		}
	}
	return CredentialInconclusive
}

// postSOAPAuth sends a raw SOAP envelope with a WS-Security UsernameToken
// (digest or plaintext per useDigest), with `created` compensated by the
// device's measured clock skew. Returns the HTTP status and body; err is
// non-nil only for transport-level failures.
func postSOAPAuth(ctx context.Context, endpoint, soapBody, username, password string, useDigest bool, skew time.Duration) (int, []byte, error) {
	created := time.Now().UTC().Add(skew)
	nonce := newNonce()
	var header string
	if useDigest {
		header = wsseDigestUsernameToken(username, password, created, nonce)
	} else {
		header = wssePasswordTextUsernameToken(username, password, created, nonce)
	}
	bodyWithAuth := strings.Replace(soapBody, "<s:Body>", header+"<s:Body>", 1)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(bodyWithAuth))
	if err != nil {
		return 0, nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read response: %w", err)
	}
	return resp.StatusCode, body, nil
}

// wssePasswordTextUsernameToken builds the weak-auth UsernameToken (plaintext
// password). Only used as a retry when a device rejects digest auth.
func wssePasswordTextUsernameToken(username, password string, created time.Time, nonce []byte) string {
	createdStr := created.UTC().Format(time.RFC3339)
	_ = nonce // nonce unused in the plaintext profile; kept for signature parity
	return `<s:Header><wsse:Security xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd" xmlns:wsu="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd"><wsse:UsernameToken><wsse:Username>` +
		xmlEscape(username) +
		`</wsse:Username><wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-username-token-profile-1.0#PasswordText">` +
		xmlEscape(password) +
		`</wsse:Password><wsu:Created>` + createdStr + `</wsu:Created></wsse:UsernameToken></wsse:Security></s:Header>`
}
