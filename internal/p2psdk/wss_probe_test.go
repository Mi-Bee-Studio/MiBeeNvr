//go:build prodprobe

// 一次性生产探针（手工触发，不入 CI）：
//
//	go test -tags prodprobe -run TestWssRelayEndpointProbe -count=1 -v
//
// 验证 Caddy 的 wss/443 反代路径能完整承载 relay 协议（client_auth 握手 +
// ping/pong），为 relay_url 切换 wss://…/relay 提供依据（2026-10-09 二次
// 调研 P0-1）。口令经 MIBEE_PROBE_PW 环境变量传入，绝不落文件。
package peer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/relay"
)

func TestWssRelayEndpointProbe(t *testing.T) {
	pw := os.Getenv("MIBEE_PROBE_PW")
	if pw == "" {
		t.Skip("MIBEE_PROBE_PW 未设置，跳过生产探针")
	}

	// ① 密码现铸 JWT（password grant，单次不重试——锁定红线）。
	jwt := mintProbeJWT(t, pw)

	// ② 直连信令拿 welcome（ws 明文路径即可——只取 RelayPubKey）。
	//    复用生产信令的公网入口（wss://…/signal）。
	sc := newSignalingClient(SignalingConfig{
		ServerURL: "wss://signal.example.invalid/signal",
		JWT:       jwt,
		ProductID: "mibeenvr",
		PeerID:    "probe-wss-relay",
		Role:      "client",
	})
	welcome, err := sc.connect(context.Background())
	require.NoError(t, err, "信令连接应成功")
	defer sc.close()
	t.Logf("welcome relay_url=%s", welcome.RelayURL)

	// ③ relay.Client 直连 wss/443 反代路径，做完整 client_auth 握手。
	rc := relay.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	require.NoError(t, rc.Connect(ctx,
		"wss://signal.example.invalid/relay", welcome.RelayPubKey),
		"wss/443 反代路径应能承载 relay client_auth 握手")
	defer rc.Close()

	// ④ 停留 3s（覆盖一轮 ping/pong + 读超时窗口的一半）。
	time.Sleep(3 * time.Second)
	t.Log("wss relay 探针 PASS：握手成功且 3s 存活")
}

func mintProbeJWT(t *testing.T, pw string) string {
	t.Helper()
	form := url.Values{
		"grant_type": {"password"},
		"username":   {"p2p-test"},
		"password":   {pw},
		"client_id":  {"p2p-signaling"},
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).PostForm(
		"https://signal.example.invalid/oidc/token", form)
	require.NoError(t, err)
	defer resp.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotEmpty(t, body.AccessToken, "OIDC 密码铸造应成功")
	return body.AccessToken
}
