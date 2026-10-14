package p2p

// grants.go — 分享执行链的 NVR 侧平台件（user-tenant-model §7，Phase 4）。
// 平台是零知识转发层：摄像头级权限的权威判定在设备端。观看者连接时，
// NVR 持自己的设备身份 token 调平台核验观看者的用户 JWT，换回该账号在
// 本设备上的范围（owner 全功能 / 被分享账号按 grant 的 cameras 白名单，
// 空=整机）。每次登录核验都现拉——吊销即时生效（无本地缓存漂移窗口）。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ViewerScope 一次观看者核验的结论。
type ViewerScope struct {
	AccountID string   `json:"grantee_account_id"`
	IsOwner   bool     `json:"is_owner"`
	Cameras   []string `json:"cameras"` // 空 = 整机（全部摄像头）
	DeviceID  string   `json:"device_id"`
	ProductID string   `json:"product_id"`
}

// GrantsClient 用设备身份 token 调平台的分享核验端点。tokens 由 Manager
// 的 TokenManager 提供（设备模式：refresh 轮换，jti 单次使用=每次现铸）。
type GrantsClient struct {
	APIBase string
	PeerID  string
	Tokens  *TokenManager
	HTTP    *http.Client
}

func (g *GrantsClient) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// VerifyViewer 核验观看者的用户 JWT，返回其在本设备上的范围。
// 403 = 无有效分享（统一语义，不泄漏账号存在性）。
func (g *GrantsClient) VerifyViewer(ctx context.Context, viewerJWT string) (*ViewerScope, error) {
	jwt, err := g.Tokens.Mint(ctx)
	if err != nil {
		return nil, fmt.Errorf("device token: %w", err)
	}
	body, _ := json.Marshal(map[string]string{"viewer_token": viewerJWT})
	url := fmt.Sprintf("%s/api/device/grants/verify?device_id=%s", g.APIBase, g.PeerID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("platform verify: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusForbidden {
		return nil, ErrNoShare
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("platform verify HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	var scope ViewerScope
	if err := json.Unmarshal(respBody, &scope); err != nil {
		return nil, fmt.Errorf("decode verify response: %w", err)
	}
	return &scope, nil
}

// ErrNoShare — 观看者无本设备的有效分享（403 统一语义）。
var ErrNoShare = fmt.Errorf("no active share for this account")
