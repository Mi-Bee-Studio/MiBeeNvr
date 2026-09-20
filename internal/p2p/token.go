// Package p2p 内嵌 MiBee P2P 设备角色 agent：token 自助铸造 + 信令会话
// 监督 + 本机服务隧道注册。外部访问经信令/P2P 隧道直达 NVR HTTP API，
// 无需端口映射或公网 IP。
package p2p

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

const defaultMinMintInterval = 30 * time.Second

// TokenManager 管理 OIDC 信令 token 的自助铸造：
//
//	冷启动 → refresh grant（轮换式，7 天 TTL）
//	        ↘ invalid_grant → password grant（配置文件凭据）
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
	httpClient  *http.Client
	minInterval time.Duration // 测试可缩；默认 30s 防锤
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
}

// NewTokenManager 创建铸造器。statePath 为 refresh token 持久化文件。
func NewTokenManager(endpoint, clientID, username, password, statePath string) *TokenManager {
	return &TokenManager{
		endpoint:    endpoint,
		clientID:    clientID,
		username:    username,
		password:    password,
		statePath:   statePath,
		minInterval: defaultMinMintInterval,
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
		// refresh 失效（invalid_grant 等）→ 回退 password grant。
		t.refresh = ""
		t.removeState()
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
		return
	}
	var s tokenState
	if json.Unmarshal(b, &s) == nil && s.RefreshToken != "" {
		t.refresh = s.RefreshToken
	}
}

func (t *TokenManager) storeRefresh(refresh string) {
	t.refresh = refresh
	if refresh == "" || t.statePath == "" {
		return
	}
	b, _ := json.Marshal(tokenState{RefreshToken: refresh})
	tmp := t.statePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, t.statePath)
}

func (t *TokenManager) removeState() {
	_ = os.Remove(t.statePath)
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
		return fmt.Sprintf("https://%s/oidc/token", u.Hostname())
	case "ws":
		return fmt.Sprintf("http://%s/oidc/token", u.Host)
	default:
		return ""
	}
}

// StatePathFor 返回给定 config 路径旁的 token state 文件路径。
func StatePathFor(configPath string) string {
	if configPath == "" {
		return ""
	}
	ext := filepath.Ext(configPath)
	return strings.TrimSuffix(configPath, ext) + ".p2p-token.json"
}
