package api

import (
	"context"
	"errors"
	"strings"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/plugins/xiaomi"
)

// LocalXiaomiAuth implements CloudAuthProxy by calling the xiaomi package directly.
// This is the in-process bridge used until the xiaomi plugin runs as a separate
// gRPC process.
type LocalXiaomiAuth struct {
	cfg *config.Config
}

// NewLocalXiaomiAuth creates a LocalXiaomiAuth and initializes cloud config.
// The config pointer is kept so auth results can update it in-place.
func NewLocalXiaomiAuth(cfg *config.Config) *LocalXiaomiAuth {
	// Initialize cloud config for MISS URL resolution (was previously called
	// from main.go as xiaomi.SetCloudConfig(cfg.Xiaomi)).
	xiaomi.SetCloudConfig(cfg.Xiaomi)
	return &LocalXiaomiAuth{cfg: cfg}
}

// SetCloudConfig pushes credentials to the xiaomi plugin's in-process state.
func (a *LocalXiaomiAuth) SetCloudConfig(_ context.Context, userID, token, region string) error {
	xiaomi.SetCloudConfig(config.XiaomiConfig{
		UserID: userID,
		Token:  token,
		Region: region,
	})
	return nil
}

// SignIn calls the xiaomi cloud sign-in flow.
func (a *LocalXiaomiAuth) SignIn(_ context.Context, username, password, region string) (*CloudAuthResult, *CloudVerificationRequired, error) {
	session, captchaSessionID, err := xiaomi.SignInWithCaptcha(username, password, region)
	if err != nil {
		var loginErr *xiaomi.LoginError
		if isLoginErr(err, &loginErr) {
			return nil, loginErrToVerification(loginErr, captchaSessionID), nil
		}
		return nil, nil, err
	}
	return sessionToResult(session), nil, nil
}

// SubmitCaptcha submits a captcha code for a pending auth session.
func (a *LocalXiaomiAuth) SubmitCaptcha(_ context.Context, sessionID, captchaCode string) (*CloudAuthResult, *CloudVerificationRequired, error) {
	session, err := xiaomi.LoginWithCaptcha(sessionID, captchaCode)
	if err != nil {
		var captchaErr *xiaomi.CaptchaSessionError
		if isCaptchaErr(err, &captchaErr) {
			return nil, captchaErrToVerification(captchaErr), nil
		}
		return nil, nil, err
	}
	return sessionToResult(session), nil, nil
}

// SubmitVerify submits a 2FA ticket for a pending auth session.
func (a *LocalXiaomiAuth) SubmitVerify(_ context.Context, sessionID, ticket string) (*CloudAuthResult, *CloudVerificationRequired, error) {
	session, err := xiaomi.LoginWithVerify(sessionID, ticket)
	if err != nil {
		var captchaErr *xiaomi.CaptchaSessionError
		if isCaptchaErr(err, &captchaErr) {
			return nil, captchaErrToVerification(captchaErr), nil
		}
		return nil, nil, err
	}
	return sessionToResult(session), nil, nil
}

// ListDevices returns Xiaomi cloud devices filtered to camera models.
func (a *LocalXiaomiAuth) ListDevices(_ context.Context) ([]CloudDeviceInfo, error) {
	session, err := xiaomi.SignInWithToken(a.cfg.Xiaomi.UserID, a.cfg.Xiaomi.Token, a.cfg.Xiaomi.Region)
	if err != nil {
		return nil, err
	}

	devices, err := xiaomi.GetDeviceList(session)
	if err != nil {
		return nil, err
	}

	// Filter for camera devices only
	cameras := make([]CloudDeviceInfo, 0, len(devices))
	for _, d := range devices {
		if isXiaomiCameraModel(d.Model) {
			cameras = append(cameras, CloudDeviceInfo{
				DID:      d.DID,
				Name:     d.Name,
				Model:    d.Model,
				IP:       d.IP,
				MAC:      d.MAC,
				IsOnline: d.IsOnline,
			})
		}
	}
	return cameras, nil
}

// --- helpers ---

func sessionToResult(s *xiaomi.CloudSession) *CloudAuthResult {
	if s == nil {
		return nil
	}
	return &CloudAuthResult{
		UserID:    s.UserID,
		PassToken: s.PassToken,
		Region:    s.Region,
	}
}

func loginErrToVerification(e *xiaomi.LoginError, sessionID string) *CloudVerificationRequired {
	return &CloudVerificationRequired{
		Captcha:          e.Captcha,
		VerifyPhone:      e.VerifyPhone,
		VerifyEmail:      e.VerifyEmail,
		CaptchaSessionID: sessionID,
	}
}

func captchaErrToVerification(e *xiaomi.CaptchaSessionError) *CloudVerificationRequired {
	return &CloudVerificationRequired{
		Captcha:          e.Captcha,
		VerifyPhone:      e.VerifyPhone,
		VerifyEmail:      e.VerifyEmail,
		CaptchaSessionID: e.CaptchaSessionID,
	}
}

func isLoginErr(err error, target **xiaomi.LoginError) bool {
	return errors.As(err, target)
}

func isCaptchaErr(err error, target **xiaomi.CaptchaSessionError) bool {
	return errors.As(err, target)
}

// isXiaomiCameraModel returns true if the model string looks like a Xiaomi camera.
// Uses Contains matching like go2rtc: .camera., .cateye., .feeder.
func isXiaomiCameraModel(model string) bool {
	return strings.Contains(model, ".camera.") ||
		strings.Contains(model, ".cateye.") ||
		strings.Contains(model, ".feeder.")
}
