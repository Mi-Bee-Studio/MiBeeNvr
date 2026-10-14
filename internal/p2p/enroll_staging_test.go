package p2p

// enroll_staging_test.go — 对本地平台靶场（tmp/identity-staging：oidc
// :18085 + p2p :18080）打真端点的端到端验证。默认跳过；设
// P2P_ENROLL_STAGING=1 且靶场在线时启用。流程：
//
//	用户域登录铸 token → POST /api/enrollment/tickets →
//	p2p.Enroll（/device/enroll + re-enroll 挑战）→ TokenManager 采信
//	refresh 后再 Mint（refresh 轮换）→ /api/my/devices 投影可见 →
//	解绑（平台侧）→ 同身份再引导=409 retired（新身份才可）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const stagingOIDC = "http://127.0.0.1:18085"
const stagingP2P = "http://127.0.0.1:18080"

func stagingEnabled() bool { return os.Getenv("P2P_ENROLL_STAGING") == "1" }

func stagingMint(t *testing.T) string {
	t.Helper()
	resp, err := http.PostForm(stagingOIDC+"/token", url.Values{
		"grant_type": {"password"}, "username": {"p2p-test"},
		"password":   {"WAFmhUsqMCuIS8Tcvn"}, "product_id": {"mibeenvr"},
		"client_id":  {"p2p-signaling"},
	})
	if err != nil {
		t.Fatalf("staging login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.AccessToken == "" {
		t.Fatalf("staging login decode: %v", err)
	}
	return body.AccessToken
}

func stagingTicket(t *testing.T, tok string) (id, secret string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, stagingP2P+"/api/enrollment/tickets", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ticket: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		TicketID string `json:"ticket_id"`
		Secret   string `json:"secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.TicketID == "" {
		t.Fatalf("ticket decode (HTTP %d): %v", resp.StatusCode, err)
	}
	return body.TicketID, body.Secret
}

func TestEnrollStaging_FullFlow(t *testing.T) {
	if !stagingEnabled() {
		t.Skip("set P2P_ENROLL_STAGING=1 with the tmp/identity-staging stack online")
	}
	tok := stagingMint(t)
	ticketID, secret := stagingTicket(t, tok)

	key := filepath.Join(t.TempDir(), "staging-device.key")
	res, err := Enroll(context.Background(), EnrollOptions{
		BaseURL: stagingP2P, TicketID: ticketID, Secret: secret,
		ProductID: "mibeenvr", KeyPath: key,
	})
	if err != nil {
		t.Fatalf("enroll against staging platform: %v", err)
	}
	if !strings.HasPrefix(res.Username, "device:mibeenvr:") {
		t.Fatalf("unexpected username %q", res.Username)
	}
	if res.Access == "" || res.Refresh == "" {
		t.Fatalf("no tokens: %+v", res)
	}

	// TokenManager 采信引导 refresh → refresh 轮换 Mint 成功（设备模式）。
	tm := NewTokenManager(stagingOIDC+"/token", "p2p-signaling", "", "",
		filepath.Join(t.TempDir(), "state.json"))
	tm.minInterval = 0
	tm.AdoptRefresh(res.Refresh)
	access, err := tm.Mint(context.Background())
	if err != nil || access == "" {
		t.Fatalf("device-mode refresh mint: %v", err)
	}

	// 绑定投影可见。
	req, _ := http.NewRequest(http.MethodGet, stagingP2P+"/api/my/devices", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("my devices: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var mine struct {
		Devices []map[string]any `json:"devices"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&mine)
	found := false
	for _, d := range mine.Devices {
		if d["device_id"] == res.DeviceID {
			found = true
		}
	}
	if !found {
		t.Fatalf("enrolled device %s missing from projection: %+v", res.DeviceID, mine.Devices)
	}

	// 平台侧解绑 → 同身份再引导必须 retired。（jti 单次使用契约：每发
	// 一次 REST 调用都重新铸用户 token。）
	tok2 := stagingMint(t)
	req, _ = http.NewRequest(http.MethodPost, stagingP2P+"/api/enrollment/unbind",
		strings.NewReader(fmt.Sprintf(`{"device_id":%q}`, res.DeviceID)))
	req.Header.Set("Authorization", "Bearer "+tok2)
	req.Header.Set("Content-Type", "application/json")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusOK {
		status := 0
		if resp != nil {
			status = resp.StatusCode
			_ = resp.Body.Close()
		}
		t.Fatalf("unbind: %v (HTTP %d)", err, status)
	}

	ticket2, secret2 := stagingTicket(t, stagingMint(t))
	_, err = Enroll(context.Background(), EnrollOptions{
		BaseURL: stagingP2P, TicketID: ticket2, Secret: secret2,
		ProductID: "mibeenvr", KeyPath: key,
		PreferredDeviceID: res.DeviceID, // 同 ID：应 surfaced retired（不自动重试）
	})
	if err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("same-identity re-enroll after unbind must fail retired, got %v", err)
	}
	_ = time.Second
}
