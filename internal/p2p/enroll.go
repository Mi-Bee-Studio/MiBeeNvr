package p2p

// enroll.go — 设备引导绑定客户端（user-tenant-model §5，Phase 3 NVR 段）。
// App 在 LAN 内下发平台签发的一次性 enrollment 凭证；NVR 本地生成/复用
// Ed25519 身份密钥，向平台注册公钥并绑定归属，随后走既有 re-enroll 挑战
// 流程换得设备 JWT + 30 天轮换 refresh。私钥永不出 NVR。

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 引导结果的类型化错误（HTTP 层据此映射 4xx 语义给 App）。
var (
	ErrBadTicket     = errors.New("invalid or expired enrollment ticket")
	ErrDeviceBound   = errors.New("device already bound to another account")
	ErrDeviceRetired = errors.New("device identity retired; a fresh identity is required")
)

// EnrollResult 一次成功引导的产出：设备身份 + 首套令牌。
type EnrollResult struct {
	DeviceID  string `json:"device_id"`
	ProductID string `json:"product_id"`
	AccountID string `json:"account_id"`
	Username  string `json:"username"` // device:<product>:<device_id>
	Access    string `json:"-"`
	Refresh   string `json:"-"`
	ExpiresIn int    `json:"expires_in"`
}

// EnrollOptions 引导调用参数。BaseURL 是平台 HTTP 根（由信令地址同域
// 推导）；KeyPath 是 Ed25519 私钥文件（32B seed 或 64B 全钥，0600）；
// ProductID 是票券所属实例域（App 从票券响应透传）。
type EnrollOptions struct {
	BaseURL   string
	TicketID  string
	Secret    string
	ProductID string
	KeyPath   string
	// PreferredDeviceID 优先使用的设备 ID（空 = 自动生成 nvr-<6hex>）。
	PreferredDeviceID string
	HTTPClient        *http.Client
}

// EnrollHTTPBase 从信令地址推导平台 HTTP 根（与 DeriveTokenEndpoint 同
// 域规则：wss→https 同主机，ws→http 保留端口）。
func EnrollHTTPBase(serverURL string) string {
	u, err := url.Parse(serverURL)
	if err != nil {
		return ""
	}
	switch u.Scheme {
	case "wss":
		return "https://" + u.Hostname()
	case "ws":
		return "http://" + u.Host
	default:
		return ""
	}
}

// GenerateDeviceID 生成一个随机设备身份 ID（重绑=全新身份，见 §5）。
func GenerateDeviceID() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("nvr-%d", time.Now().UnixNano()%1e6)
	}
	return "nvr-" + hex.EncodeToString(b)
}

// LoadOrCreateDeviceKey 读取（或生成并落盘 0600）Ed25519 身份私钥。
// 兼容 32B seed 与 64B 全钥两种文件形态（与 SDK DeviceKeyPath 同源）。
func LoadOrCreateDeviceKey(keyPath string) (ed25519.PrivateKey, error) {
	if b, err := os.ReadFile(keyPath); err == nil {
		switch len(b) {
		case ed25519.SeedSize:
			return ed25519.NewKeyFromSeed(b), nil
		case ed25519.PrivateKeySize:
			return ed25519.PrivateKey(b), nil
		default:
			return nil, fmt.Errorf("device key %s: unexpected size %d", keyPath, len(b))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read device key: %w", err)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate device key: %w", err)
	}
	if dir := filepath.Dir(keyPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("device key dir: %w", err)
		}
	}
	if err := os.WriteFile(keyPath, priv.Seed(), 0o600); err != nil {
		return nil, fmt.Errorf("write device key: %w", err)
	}
	return priv, nil
}

// Enroll 执行 ③④ 两步：/device/enroll（注册公钥+绑定归属）→
// /device/re-enroll（Ed25519 挑战换 token）。device_bound/retired 且未指定
//首选 ID 时换新 ID 重试一次（全新身份天然绕开冲突与毒药）。
func Enroll(ctx context.Context, opts EnrollOptions) (*EnrollResult, error) {
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	priv, err := LoadOrCreateDeviceKey(opts.KeyPath)
	if err != nil {
		return nil, err
	}
	pubkey := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))

	deviceID := opts.PreferredDeviceID
	if deviceID == "" {
		deviceID = GenerateDeviceID()
	}
	for attempt := 0; ; attempt++ {
		res, err := enrollOnce(ctx, client, opts, priv, pubkey, deviceID)
		if err == nil {
			return res, nil
		}
		if (errors.Is(err, ErrDeviceBound) || errors.Is(err, ErrDeviceRetired)) &&
			opts.PreferredDeviceID == "" && attempt == 0 {
			deviceID = GenerateDeviceID()
			continue
		}
		return nil, err
	}
}

func enrollOnce(ctx context.Context, client *http.Client, opts EnrollOptions,
	priv ed25519.PrivateKey, pubkey, deviceID string) (*EnrollResult, error) {
	base := strings.TrimRight(opts.BaseURL, "/")

	// ③ 注册公钥（票券一次性；归属域以票券为准，ProductID 仅作校验）。
	var enrollResp struct {
		DeviceID  string `json:"device_id"`
		ProductID string `json:"product_id"`
		AccountID string `json:"account_id"`
	}
	if err := postJSON(ctx, client, base+"/device/enroll", map[string]string{
		"ticket_id": opts.TicketID, "secret": opts.Secret,
		"product_id": opts.ProductID, "device_id": deviceID, "pubkey": pubkey,
	}, &enrollResp, ErrBadTicket, map[string]error{
		"device_bound":   ErrDeviceBound,
		"device_retired": ErrDeviceRetired,
	}); err != nil {
		return nil, err
	}

	// 服务端身份优先（平台 #47 幂等 upsert，PR#54）：同账号+同公钥重复
	// enroll 返回 200 + 既有 device_id + idempotent——后续 re-enroll 挑战
	// 与落配置都以其为准；全新 enroll 时响应回显请求 ID，采纳同样成立。
	if enrollResp.DeviceID != "" {
		deviceID = enrollResp.DeviceID
	}

	// ④ re-enroll 挑战签名换 token。签名口径与平台一致：
	// "mibee-re-enroll:v1:<product>:<device>:<challenge-hex>"。
	var initResp struct {
		Challenge string `json:"challenge"`
	}
	if err := postJSON(ctx, client, base+"/device/re-enroll/init", map[string]string{
		"product_id": enrollResp.ProductID, "device_id": deviceID,
	}, &initResp, nil, nil); err != nil {
		return nil, err
	}
	if initResp.Challenge == "" {
		return nil, fmt.Errorf("empty re-enroll challenge")
	}
	message := "mibee-re-enroll:v1:" + enrollResp.ProductID + ":" + deviceID + ":" + initResp.Challenge
	sig := ed25519.Sign(priv, []byte(message))

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := postJSON(ctx, client, base+"/device/re-enroll", map[string]string{
		"product_id": enrollResp.ProductID, "device_id": deviceID,
		"challenge": initResp.Challenge,
		"signature": base64.StdEncoding.EncodeToString(sig),
	}, &tokenResp, nil, nil); err != nil {
		return nil, err
	}
	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("re-enroll returned no token")
	}
	return &EnrollResult{
		DeviceID:  enrollResp.DeviceID,
		ProductID: enrollResp.ProductID,
		AccountID: enrollResp.AccountID,
		Username:  "device:" + enrollResp.ProductID + ":" + enrollResp.DeviceID,
		Access:    tokenResp.AccessToken,
		Refresh:   tokenResp.RefreshToken,
		ExpiresIn: tokenResp.ExpiresIn,
	}, nil
}

// postJSON 发一枚 JSON POST；2xx 时解码进 out，4xx/5xx 时按 error 字段
// 映射类型化错误（errMap 命中优先，其次 authErr，最后带 error 文本）。
func postJSON(ctx context.Context, client *http.Client, url string, body any,
	out any, authErr error, errMap map[string]error) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("platform request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(respBody, &e)

	if resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusUnauthorized && authErr != nil {
			return authErr
		}
		if e.Error != "" {
			// 平台错误字段常带可读后缀（"device_retired: …"）——按前缀
			// 子串命中类型化错误。
			for key, mapped := range errMap {
				if strings.Contains(e.Error, key) {
					return mapped
				}
			}
			return fmt.Errorf("platform: %s", e.Error)
		}
		return fmt.Errorf("platform HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(respBody, out)
	}
	return nil
}
