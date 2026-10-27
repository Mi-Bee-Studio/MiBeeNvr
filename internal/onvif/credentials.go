package onvif

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	onvifgo "github.com/mickeyzzc/onvif-go/v2/onvif"
)

// Camera credential probing primitives (#credprobe).
//
// Purpose: the NVR's time-sync WRITE operations (SetSystemDateAndTime/SetNTP)
// and device management need ADMIN credentials. Cameras enrolled by
// auto-discovery often arrive with factory defaults — this surface lets the
// NVR test a small candidate list of industry-default credentials against a
// newly discovered camera (once per camera, enforced by the caller), and
// optionally rotate the matched account to the operator's chosen password.
//
// The SOAP round trips ride the library client (canonical wire format, auth
// ladder, fault detection); the verdict classification stays here because it
// is NVR policy: distinguishing "definitely wrong password" (safe to try the
// next candidate) from "device answered ambiguously" (stop probing, lockout
// risk). No Connect()/Initialize handshake is performed — probing stays cheap
// and works before any connection exists.

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
// GetSystemDateAndTime) and reuses it for every digest the probe builds.
func (p *CredentialProber) skewFor(ctx context.Context) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.skewMeasured {
		p.skew = measureDeviceSkew(ctx, p.endpoint)
		p.skewMeasured = true
	}
	return p.skew
}

// probeClient builds the library client for one credential attempt. The
// ladder deliberately stops at PasswordText: AuthNone/HTTP-Basic rungs do
// not bind to the password under test — an anonymous-access device would
// accept ANY credentials through them, defeating the probe.
func (p *CredentialProber) probeClient(username, password string, skew time.Duration) (*onvifgo.Client, error) {
	lib, err := onvifgo.NewClient(p.endpoint,
		onvifgo.WithCredentials(username, password),
		onvifgo.WithAuthFallback(onvifgo.AuthPasswordText),
	)
	if err != nil {
		return nil, err
	}
	if skew != 0 {
		lib.SetClockSkew(skew)
	}
	return lib, nil
}

// Try tests one username/password via tds:GetUsers — an admin-level call on
// mainstream firmware (Hikvision/Dahua/Axis all gate user management), which
// is the level the time-sync writes need. The library ladder runs digest then
// PasswordText (the digest-hostile-firmware retry); a definitive rejection of
// both classifies as Rejected.
func (p *CredentialProber) Try(ctx context.Context, username, password string) CredentialVerdict {
	skew := p.skewFor(ctx)
	lib, err := p.probeClient(username, password, skew)
	if err != nil {
		return CredentialInconclusive
	}
	_, getUsersErr := lib.Security().GetUsers(ctx)
	if getUsersErr == nil {
		return CredentialAccepted
	}
	return classifyCredentialError(getUsersErr)
}

// SetUserPassword rotates targetUser's password to newPassword, authenticating
// as authUser/authPass (the credential a successful Try returned).
// Empty UserLevel = modify-only semantics.
func (p *CredentialProber) SetUserPassword(ctx context.Context, authUser, authPass, targetUser, newPassword string) error {
	skew := p.skewFor(ctx)
	lib, err := p.probeClient(authUser, authPass, skew)
	if err != nil {
		return err
	}
	if err := lib.Security().SetUser(ctx, &onvifgo.User{
		Username: targetUser,
		Password: newPassword,
	}); err != nil {
		return fmt.Errorf("SetUser failed: %w", err)
	}
	return nil
}

// classifyCredentialError maps a library GetUsers failure to a verdict.
// The library wraps definitive rejections (401/403, auth faults, exhausted
// auth ladder) in ErrUnauthorized; transport failures and ambiguous answers
// propagate as themselves. A string-level sniff keeps custom-fault devices
// (ter:NotAuthorized spellings the library may not map) on the Rejected side.
func classifyCredentialError(err error) CredentialVerdict {
	if err == nil {
		return CredentialAccepted
	}
	if errors.Is(err, onvifgo.ErrUnauthorized) {
		return CredentialRejected
	}
	low := strings.ToLower(err.Error())
	for _, marker := range []string{"notauthorized", "not authorized", "unauthorized", "sender not authorized"} {
		if strings.Contains(low, marker) {
			return CredentialRejected
		}
	}
	return CredentialInconclusive
}
