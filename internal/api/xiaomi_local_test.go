package api

// Tests for xiaomi_local.go (#578) — pure helper surface only: the cloud
// sign-in/device-list paths dial the Xiaomi cloud and are not CI-hermetic.

import (
	"context"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/xiaomi"
	"github.com/stretchr/testify/require"
)

func TestXiaomiLocal_SessionToResult(t *testing.T) {
	t.Parallel()
	require.Nil(t, sessionToResult(nil))
	r := sessionToResult(&xiaomi.CloudSession{UserID: "u", PassToken: "p", Region: "cn"})
	require.NotNil(t, r)
	require.Equal(t, "u", r.UserID)
	require.Equal(t, "p", r.PassToken)
	require.Equal(t, "cn", r.Region)
}

func TestXiaomiLocal_VerificationMapping(t *testing.T) {
	t.Parallel()
	login := loginErrToVerification(&xiaomi.LoginError{Captcha: []byte("img"), VerifyPhone: "+86"}, "sess-1")
	require.NotNil(t, login.Captcha)
	require.Equal(t, "+86", login.VerifyPhone)
	require.Equal(t, "", login.VerifyEmail)
	require.Equal(t, "sess-1", login.CaptchaSessionID)

	captcha := captchaErrToVerification(&xiaomi.CaptchaSessionError{
		LoginError:       &xiaomi.LoginError{VerifyEmail: "a@b.c"},
		CaptchaSessionID: "sess-2",
	})
	require.Equal(t, "a@b.c", captcha.VerifyEmail)
	require.Equal(t, "sess-2", captcha.CaptchaSessionID)
}

func TestXiaomiLocal_ErrorClassification(t *testing.T) {
	t.Parallel()
	var loginErr *xiaomi.LoginError
	require.False(t, isLoginErr(context.Canceled, &loginErr))
	require.True(t, isLoginErr(&xiaomi.LoginError{Captcha: []byte("x")}, &loginErr))
	require.NotEmpty(t, loginErr.Captcha)

	var capErr *xiaomi.CaptchaSessionError
	require.False(t, isCaptchaErr(context.Canceled, &capErr))
	require.True(t, isCaptchaErr(&xiaomi.CaptchaSessionError{}, &capErr))
}

func TestXiaomiLocal_IsCameraModel(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"isa.camera.hlc8":  true,
		"lumi.cateye.v1":   true,
		"isa.feeder.cat1":  true,
		"isa.airpurifier":  false,
		"chuangmi.plug.v1": false,
		"":                 false,
	}
	for model, want := range cases {
		require.Equal(t, want, isXiaomiCameraModel(model), model)
	}
}

func TestXiaomiLocal_CheckVendorGuards(t *testing.T) {
	t.Parallel()
	// nil-config guard (LocalXiaomiAuth constructed without cfg).
	a := &LocalXiaomiAuth{}
	_, err := a.CheckVendor(context.Background(), "123")
	require.EqualError(t, err, "xiaomi config not available")

	// Constructor + SetCloudConfig are side-effect-free wiring.
	cfg := &config.Config{}
	auth := NewLocalXiaomiAuth(cfg)
	require.NoError(t, auth.SetCloudConfig(context.Background(), "u", "t", "de"))
}
