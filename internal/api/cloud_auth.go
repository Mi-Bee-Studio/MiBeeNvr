package api

import (
	"context"
)

// CloudAuthResult holds the result of a successful cloud authentication.
type CloudAuthResult struct {
	UserID    string `json:"user_id"`
	PassToken string `json:"pass_token"`
	Region    string `json:"region"`
}

// CloudVerificationRequired is returned when authentication requires user
// interaction (captcha or two-factor verification).
type CloudVerificationRequired struct {
	Captcha           []byte `json:"captcha,omitempty"`
	VerifyPhone       string `json:"verify_phone,omitempty"`
	VerifyEmail       string `json:"verify_email,omitempty"`
	CaptchaSessionID  string `json:"session_id,omitempty"`
}

// CloudDeviceInfo represents a discovered cloud device (e.g. camera).
type CloudDeviceInfo struct {
	DID      string `json:"did"`
	Name     string `json:"name"`
	Model    string `json:"model"`
	IP       string `json:"localip"`
	MAC      string `json:"mac"`
	IsOnline bool   `json:"isOnline"`
}

// CloudAuthProxy abstracts cloud authentication operations.
// The API handler uses this interface to proxy cloud auth calls to the xiaomi package.
// The main process uses this interface to proxy cloud auth API calls,
// delegating to either a local in-process implementation or a gRPC plugin.
type CloudAuthProxy interface {
	// SetCloudConfig pushes cloud credentials (user_id, token, region) to the xiaomi package.
	// underlying implementation (plugin process or local state).
	SetCloudConfig(ctx context.Context, userID, token, region string) error

	// SignIn authenticates with username/password. Returns (result, verification, error).
	// If verification is non-nil, the user must complete captcha/2FA.
	SignIn(ctx context.Context, username, password, region string) (*CloudAuthResult, *CloudVerificationRequired, error)

	// SubmitCaptcha submits a captcha code for a pending session.
	SubmitCaptcha(ctx context.Context, sessionID, captchaCode string) (*CloudAuthResult, *CloudVerificationRequired, error)

	// SubmitVerify submits a 2FA ticket (SMS/email code) for a pending session.
	SubmitVerify(ctx context.Context, sessionID, ticket string) (*CloudAuthResult, *CloudVerificationRequired, error)

	// ListDevices returns the list of cloud devices for the authenticated user.
	ListDevices(ctx context.Context) ([]CloudDeviceInfo, error)

// CheckVendor determines the vendor protocol for a Xiaomi device by DID.
// Returns vendor name ("cs2", "tutk", etc.) or error if unable to determine.
CheckVendor(ctx context.Context, did string) (string, error)
}
