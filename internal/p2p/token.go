// Package p2p 内嵌 MiBee P2P 设备角色 agent：token 自助铸造 + 信令会话
// 监督 + 本机服务隧道注册。外部访问经信令/P2P 隧道直达 NVR HTTP API，
// 无需端口映射或公网 IP。
package p2p

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrMintBackoff 表示距上一次铸造尝试太近（防锤：服务端有 5 次失败锁
// 15 分钟的登录防爆破，调用方必须退避后再试）。
var ErrMintBackoff = errors.New("p2p token mint backoff")

// ErrRebindNeeded 表示设备身份（enrollment 引导）的 refresh 链已死且无
// password 凭据可回退——用户需在 App 重新发起绑定（user-tenant-model §5）。
var ErrRebindNeeded = errors.New("p2p device identity expired; rebind required")

const defaultMinMintInterval = 30 * time.Second

// deviceIdentityMinMintInterval：设备身份模式（enrollment 引导，username
// 前缀 device: 且无 password）只走 refresh grant——服务端 5 次锁 15 分钟
// 的防爆破只作用于 password grant，refresh 链无锁定面，防锤间隔可降到
// 秒级。2026-10-06 E2E 实证：30s 间隔撞上信令重拨的铸造，访客首连
// viewer/login 被 ErrMintBackoff 顶回 429。
const deviceIdentityMinMintInterval = 2 * time.Second

// isDeviceIdentity reports whether the credentials came from enrollment
// (device: principal, passwordless).
func isDeviceIdentity(username, password string) bool {
	return password == "" && strings.HasPrefix(username, "device:")
}

// TokenManager 管理 OIDC 信令 token 的自助铸造：
//
//	冷启动 → refresh grant（轮换式，7 天 TTL）
//	        ↘ invalid_grant → password grant（配置文件凭据）
//	                        ↘ 设备身份（无密码）→ re-enroll（Ed25519 挑战，
//	                          平台代理签发 device_issue，#27 自愈）
//
// refresh token 持久化到 state 文件（0600，与 config 同目录），重启后
// 链条不断。**失败绝不自动重试**——单次 Mint = 单次 HTTP 尝试，且两次
// Mint 之间强制 [minMintInterval] 间隔。
type TokenManager struct {
	endpoint string
	clientID string
	username string
	password string

	mu          sync.Mutex
	refresh     string
	lastTry     time.Time
	statePath   string
	stateWarned bool // loadState 对同一损坏周期只告警一次（#6）
	httpClient  *http.Client
	minInterval time.Duration // 测试可缩；默认 30s 防锤

	// re-enroll 自愈（设备身份专用，#27）：platformBase 为平台 HTTP 根
	//（EnrollHTTPBase 同源推导）；deviceKey 惰性加载并缓存。
	reenrollBase string
	productID    string
	deviceID     string
	keyPath      string
	deviceKey    ed25519.PrivateKey
	keyErr       error
	logger       *slog.Logger
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
}

// NewTokenManager 创建铸造器。statePath 为 refresh token 持久化文件。
func NewTokenManager(endpoint, clientID, username, password, statePath string) *TokenManager {
	interval := defaultMinMintInterval
	if isDeviceIdentity(username, password) {
		interval = deviceIdentityMinMintInterval
	}
	return &TokenManager{
		endpoint:    endpoint,
		clientID:    clientID,
		username:    username,
		password:    password,
		statePath:   statePath,
		minInterval: interval,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Mint 返回一枚可用的 access token。优先 refresh 轮换，失效回退
// password grant。每次调用至多发起一次 HTTP 请求（refresh 失效后的
// password 回退在同一次调用内，属同账号不同 grant，不计为重试锤）。
func (t *TokenManager) Mint(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if time.Since(t.lastTry) < t.minInterval {
		return "", ErrMintBackoff
	}
	t.lastTry = time.Now()

	if t.refresh == "" {
		t.loadState()
	}

	if t.refresh != "" {
		resp, err := t.grant(ctx, url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {t.refresh},
			"client_id":     {t.clientID},
		})
		if err == nil {
			t.storeRefresh(resp.RefreshToken)
			return resp.AccessToken, nil
		}
		// refresh 失效（invalid_grant 等）：链断，走下方身份分流。
		t.refresh = ""
		t.removeState()
	}

	// 设备身份（无 password 凭据）：re-enroll 自愈（#27）——覆盖两条
	// 死路：①轮换链断裂（refresh invalid_grant）；②state 丢失/损坏
	//（此前直落空密码 password grant，即 M5 连打 3 天 400 的根因）。
	// 平台挑战签名换发新链；未配置设备密钥才宣告链死。服务账号模式
	// 回退 password grant。
	if t.username == "" || t.password == "" {
		if t.reenrollConfigured() {
			resp, err := t.reenrollGrant(ctx)
			if err != nil {
				return "", fmt.Errorf("re-enroll: %w", err)
			}
			t.storeRefresh(resp.RefreshToken)
			return resp.AccessToken, nil
		}
		return "", ErrRebindNeeded
	}

	resp, err := t.grant(ctx, url.Values{
		"grant_type": {"password"},
		"username":   {t.username},
		"password":   {t.password},
		"client_id":  {t.clientID},
	})
	if err != nil {
		return "", err
	}
	t.storeRefresh(resp.RefreshToken)
	return resp.AccessToken, nil
}

func (t *TokenManager) grant(ctx context.Context, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token endpoint: %w", err)
	}
	defer resp.Body.Close()

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("decode token response (HTTP %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK || tr.Error != "" || tr.AccessToken == "" {
		return nil, fmt.Errorf("token grant failed (HTTP %d): %s", resp.StatusCode,
			tr.ErrorOr("no access_token"))
	}
	return &tr, nil
}

// SetReenroll 配置设备身份的 re-enroll 自愈参数（#27）。platformBase 为
// 平台 HTTP 根（EnrollHTTPBase 同源）；keyPath 指向 Ed25519 私钥文件
//（32B seed 或 64B 全钥，与 SDK DeviceKeyPath 同格式）。logger 可为 nil。
func (t *TokenManager) SetReenroll(platformBase, productID, deviceID, keyPath string, logger *slog.Logger) {
	t.reenrollBase = strings.TrimRight(platformBase, "/")
	t.productID = productID
	t.deviceID = deviceID
	t.keyPath = keyPath
	t.logger = logger
}

func (t *TokenManager) reenrollConfigured() bool {
	return t.reenrollBase != "" && t.productID != "" && t.deviceID != "" && t.keyPath != ""
}

// reenrollGrant 走平台两步 re-enroll：init 取挑战 → Ed25519 签名
// "mibee-re-enroll:v1:<product>:<device>:<challenge>" → 换发 token 对
//（mibee-oidc device_issue，服务端 6 次/小时限速）。失败原样上抛，
// 由 [minMintInterval] 节流后续重试。
func (t *TokenManager) reenrollGrant(ctx context.Context) (*tokenResponse, error) {
	key := t.loadDeviceKey()
	if key == nil {
		return nil, fmt.Errorf("device key unavailable: %w", t.keyErr)
	}

	var initResp struct {
		Challenge string `json:"challenge"`
	}
	if err := postJSON(ctx, t.httpClient, t.reenrollBase+"/device/re-enroll/init",
		map[string]string{"product_id": t.productID, "device_id": t.deviceID},
		&initResp, nil, nil); err != nil {
		return nil, fmt.Errorf("init: %w", err)
	}
	if initResp.Challenge == "" {
		return nil, errors.New("init: no challenge")
	}

	msg := "mibee-re-enroll:v1:" + t.productID + ":" + t.deviceID + ":" + initResp.Challenge
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(msg)))

	var tr tokenResponse
	if err := postJSON(ctx, t.httpClient, t.reenrollBase+"/device/re-enroll", map[string]string{
		"product_id": t.productID,
		"device_id":  t.deviceID,
		"challenge":  initResp.Challenge,
		"signature":  sig,
	}, &tr, nil, nil); err != nil {
		return nil, fmt.Errorf("issue: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, errors.New("issue: no access_token")
	}
	return &tr, nil
}

func (t *TokenManager) loadDeviceKey() ed25519.PrivateKey {
	if t.deviceKey != nil || t.keyErr != nil {
		return t.deviceKey
	}
	key, err := LoadOrCreateDeviceKey(t.keyPath)
	if err != nil {
		t.keyErr = err
		return nil
	}
	t.deviceKey = key
	return key
}

func (r *tokenResponse) ErrorOr(fallback string) string {
	if r.Error != "" {
		return r.Error
	}
	return fallback
}

// state 文件：{"refresh_token":"…"}，0600。
type tokenState struct {
	RefreshToken string `json:"refresh_token"`
}

func (t *TokenManager) loadState() {
	b, err := os.ReadFile(t.statePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) && !t.stateWarned {
			// 存在但读不了（权限/IO）不是正常态；每次 Mint 重读照旧（手工
			// 重建 state 的运维自愈路径依赖它），但告警只出一次防刷屏
			// （#6：静默落密码流=极难排障）。
			t.stateWarned = true
			slog.Error("p2p token state 读取失败，忽略 state 走凭据流",
				"path", t.statePath, "err", err)
		}
		return
	}
	var s tokenState
	if err := json.Unmarshal(b, &s); err != nil {
		if !t.stateWarned {
			t.stateWarned = true
			slog.Error("p2p token state 损坏（半写/截断），忽略 state 走凭据流",
				"path", t.statePath, "err", err, "size", len(b))
		}
		return
	}
	if s.RefreshToken == "" {
		if !t.stateWarned {
			t.stateWarned = true
			slog.Error("p2p token state 缺 refresh_token，走凭据流重铸",
				"path", t.statePath, "size", len(b))
		}
		return
	}
	t.stateWarned = false
	t.refresh = s.RefreshToken
}

func (t *TokenManager) storeRefresh(refresh string) {
	t.refresh = refresh
	if refresh == "" || t.statePath == "" {
		return
	}
	b, _ := json.Marshal(tokenState{RefreshToken: refresh})
	tmp := t.statePath + ".tmp"
	// #6：写→sync→改名→回读校验——半写文件在下次启动被 loadState 判废
	// （RefreshToken 解不出即静默重铸），但改名前不 fsync 的 rename 在
	// 断电/内核慌乱下可能留空文件（2026-10-10 生产两起重启丢 token 的
	// 防线收口）。
	if err := writeStateDurably(tmp, b); err != nil {
		return
	}
	if err := os.Rename(tmp, t.statePath); err != nil {
		return
	}
	if back, err := os.ReadFile(t.statePath); err != nil || string(back) != string(b) {
		slog.Error("p2p token state 回读校验失败", "err", err)
	}
}

// writeStateDurably 落盘并强制同步（rename 前的 fsync 是崩溃一致性的
// 必要件，WriteFile 不做）。
func writeStateDurably(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (t *TokenManager) removeState() {
	_ = os.Remove(t.statePath)
}

// AdoptRefresh 采信 enrollment 引导换得的首枚 refresh token（落 state
// 文件，Manager 重建后即走 refresh 轮换链）。调用方保证此时无并发 Mint。
func (t *TokenManager) AdoptRefresh(refresh string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.storeRefresh(refresh)
}

// Drain 等待在途 Mint 完成（含轮换落盘）——进程关停前必调。在途铸造若被
// SIGTERM 掐断，refresh 已被服务端单次消费而新轮换没机会落盘，下次启动
// 即「设备身份过期需重绑」（2026-10-08 两次生产事故同源：14:45 与 22:26，
// 均为关停窗口恰逢 redial 触发的 mint）。排水上限 12s（grant HTTP 超时
// 10s + 落盘余量）；超时放行——拿不到锁说明铸造卡死，进程照退不误。
func (t *TokenManager) Drain() {
	done := make(chan struct{})
	go func() {
		t.mu.Lock()
		//nolint:staticcheck // empty on purpose: acquiring the lock
		// waits for an in-flight mint to finish before the process exits.
		t.mu.Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(12 * time.Second):
	}
}

// DeriveTokenEndpoint 从信令地址推导 OIDC token 端点：
// wss://host[:port]/… → https://host/oidc/token（同域反代，443）；
// ws://host:port/…    → http://host:port/oidc/token（保留端口，本地调试）。
func DeriveTokenEndpoint(serverURL string) string {
	u, err := url.Parse(serverURL)
	if err != nil {
		return ""
	}
	switch u.Scheme {
	case "wss":
		return "https://" + deriveOrigin(u) + "/oidc/token"
	case "ws":
		return "http://" + deriveOrigin(u) + "/oidc/token"
	default:
		return ""
	}
}

// deriveOrigin returns the host[:port] of a parsed server URL. Explicit ports
// are preserved: a signaling server on wss://h:8443 serves its OIDC stack on
// the same origin — dropping the port silently aimed every token/enroll/grant
// call at :443 (home-lab/mibee-nvr #11; only deployments without an explicit
// port stay on the scheme default).
func deriveOrigin(u *url.URL) string {
	if u.Port() == "" {
		return u.Hostname()
	}
	return u.Host
}

// StatePathFor 返回给定 config 路径旁的 token state 文件路径。
func StatePathFor(configPath string) string {
	if configPath == "" {
		return ""
	}
	ext := filepath.Ext(configPath)
	return strings.TrimSuffix(configPath, ext) + ".p2p-token.json"
}
