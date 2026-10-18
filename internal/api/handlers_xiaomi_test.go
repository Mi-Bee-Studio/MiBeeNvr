package api

// Tests for handlers_xiaomi.go — Xiaomi cloud auth/devices/sync/check-vendor
// endpoints (#232). The success paths need a live cloud proxy (real Xiaomi
// auth); here we cover the not-available and not-authenticated error paths.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestXiaomi_Devices_NotAvailable(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store) // cloudProxy is nil

	rr := doRequest(t, h.Routes(), "GET", "/api/xiaomi/devices", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestXiaomi_Devices_NotAuthenticated(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	// cloudProxy is nil → 503 short-circuits. To exercise the no-token branch
	// (200 "not authenticated") we'd need a non-nil cloudProxy; that requires
	// a stub. Document the 503 ordering here.
	rr := doRequest(t, h.Routes(), "GET", "/api/xiaomi/devices", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestXiaomi_Sync_NotAvailable(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "POST", "/api/xiaomi/sync", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestXiaomi_CheckVendor_NotConfigured(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store) // cloudProxy nil → 200 unknown/compatible

	rr := doRequest(t, h.Routes(), "GET", "/api/xiaomi/check-vendor?did=123", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	parseJSON(t, rr, &resp)
	require.Equal(t, "unknown", resp["vendor"])
	require.Equal(t, true, resp["compatible"])
}

func TestXiaomi_ExtractDIDFromURL(t *testing.T) {
	t.Parallel()
	require.Equal(t, "123456789", extractDIDFromURL("xiaomi://123456789"))
	require.Equal(t, "abc-did", extractDIDFromURL("xiaomi://abc-did"))
	require.Equal(t, "", extractDIDFromURL("::not-a-url::"))
}

// failingCloudProxy stubs CloudAuthProxy with every auth path failing —
// regression coverage for #1014: upstream credential errors must surface as
// 502 (upstream rejection), never 401 (which the frontend's global handler
// treats as "NVR session expired" — clearing the token and bouncing the user
// to the login page on a typo'd Xiaomi password).
type failingCloudProxy struct{}

func (failingCloudProxy) SetCloudConfig(ctx context.Context, userID, token, region string) error {
	return nil
}

func (failingCloudProxy) SignIn(ctx context.Context, username, password, region string) (*CloudAuthResult, *CloudVerificationRequired, error) {
	return nil, nil, errors.New("invalid username or password")
}

func (failingCloudProxy) SubmitCaptcha(ctx context.Context, sessionID, captchaCode string) (*CloudAuthResult, *CloudVerificationRequired, error) {
	return nil, nil, errors.New("captcha mismatch")
}

func (failingCloudProxy) SubmitVerify(ctx context.Context, sessionID, ticket string) (*CloudAuthResult, *CloudVerificationRequired, error) {
	return nil, nil, errors.New("ticket rejected")
}

func (failingCloudProxy) ListDevices(ctx context.Context) ([]CloudDeviceInfo, error) {
	return nil, errors.New("not signed in")
}

func (failingCloudProxy) CheckVendor(ctx context.Context, did string) (string, error) {
	return "", errors.New("unknown")
}

func TestXiaomi_Auth_UpstreamFailureIs502Not401(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	h.cloudProxy = failingCloudProxy{}

	body := strings.NewReader(`{"username":"u@example.com","password":"wrong"}`)
	rr := doRequest(t, h.Routes(), "POST", "/api/xiaomi/auth", body, "", "")
	require.Equal(t, http.StatusBadGateway, rr.Code,
		"upstream credential rejection must not look like an NVR session failure (#1014)")
}

func TestXiaomi_Captcha_UpstreamFailureIs502(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	h.cloudProxy = failingCloudProxy{}

	body := strings.NewReader(`{"session_id":"s1","captcha_code":"0000"}`)
	rr := doRequest(t, h.Routes(), "POST", "/api/xiaomi/captcha", body, "", "")
	require.Equal(t, http.StatusBadGateway, rr.Code)
}

func TestXiaomi_Sync_NotLoggedInIs409(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	h.cloudProxy = failingCloudProxy{}

	rr := doRequest(t, h.Routes(), "POST", "/api/xiaomi/sync", nil, "", "")
	require.Equal(t, http.StatusConflict, rr.Code,
		"\"log into Xiaomi first\" is a precondition, not an NVR auth failure (#1014)")
}
