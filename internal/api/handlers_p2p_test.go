package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2p"
	"github.com/stretchr/testify/require"
)

// fakeP2PAgent 记录 Apply 收到的配置，回放可脚本快照。
type fakeP2PAgent struct {
	applied []config.P2PConfig
	snap    p2p.StatusSnapshot
}

func (f *fakeP2PAgent) Status() p2p.StatusSnapshot { return f.snap }

func (f *fakeP2PAgent) Apply(cfg config.P2PConfig) {
	f.applied = append(f.applied, cfg)
}

func setupP2PTestHandler(t *testing.T) (*Handler, *config.Config, *fakeP2PAgent) {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	cfgPath := filepath.Join(t.TempDir(), "test-config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("version: \"1.0\"\n"), 0o644))
	cfg := &config.Config{
		Version: config.ServerConfig{DeviceID: "dev-p2p-1"}.DeviceID,
		Server:  config.ServerConfig{DeviceID: "dev-p2p-1", Listen: ":9090"},
	}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, nil, cfgPath, nil, nil, nil, nil, nil, nil)
	agent := &fakeP2PAgent{}
	h.SetP2PAgent(agent)
	return h, cfg, agent
}

func doP2PPut(t *testing.T, r http.Handler, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPut, "/api/settings/p2p", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func TestP2PSettingsEndpoints(t *testing.T) {
	h, cfg, agent := setupP2PTestHandler(t)
	r := h.Routes()

	// 初始：全零配置。
	rr := doRequest(t, r, "GET", "/api/settings/p2p", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	require.Equal(t, false, got["enabled"])
	require.Equal(t, false, got["password_configured"])

	// 完整配置一轮：启用 + 凭据 + 上限。
	rr = doP2PPut(t, r, map[string]any{
		"enabled":     true,
		"server_url":  "wss://signal.example.com/signal",
		"username":    "nvr-svc",
		"password":    "secret",
		"max_clients": 5,
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.True(t, cfg.P2P.Enabled)
	require.Equal(t, "wss://signal.example.com/signal", cfg.P2P.ServerURL)
	require.Equal(t, 5, cfg.P2P.MaxClients)
	require.Len(t, agent.applied, 1, "settings PUT must hot-apply")
	require.True(t, agent.applied[0].Enabled)

	// peer_id 空串 → 安装级 DeviceID 默认。
	require.Equal(t, "dev-p2p-1", cfg.P2P.PeerID)

	// 密码空串 = 保持；GET 仍不回传。
	rr = doP2PPut(t, r, map[string]any{"password": ""})
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "secret", cfg.P2P.Password, "blank password must keep the stored one")

	rr = doRequest(t, r, "GET", "/api/settings/p2p", nil, "", "")
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	require.Equal(t, true, got["password_configured"])
	require.NotContains(t, rr.Body.String(), "secret")

	// 坏配置拒收：启用但缺 server_url。
	rr = doP2PPut(t, r, map[string]any{"enabled": true, "server_url": ""})
	require.Equal(t, http.StatusBadRequest, rr.Code)
	// 坏配置拒收：max_clients 越界。
	rr = doP2PPut(t, r, map[string]any{"max_clients": 100})
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// 清凭据（同时停用——启用态要求凭据完备，单清会被校验拒收）。
	rr = doP2PPut(t, r, map[string]any{"enabled": false, "clear_credentials": true})
	require.Equal(t, http.StatusOK, rr.Code)
	require.Empty(t, cfg.P2P.Username)
	require.Empty(t, cfg.P2P.Password)
}

func TestP2PStatusEndpoint(t *testing.T) {
	h, _, agent := setupP2PTestHandler(t)
	r := h.Routes()

	// 无 agent 能力：available=false。
	h.p2pAgent = nil
	rr := doRequest(t, r, "GET", "/api/p2p/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	require.Equal(t, false, got["available"])

	// 有 agent：透传快照。
	h.p2pAgent = agent
	agent.snap = p2p.StatusSnapshot{
		Enabled: true, Online: true, ActiveClients: 2, MaxClients: 5, CapSource: "license",
	}
	rr = doRequest(t, r, "GET", "/api/p2p/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var snap p2p.StatusSnapshot
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &snap))
	require.True(t, snap.Online)
	require.Equal(t, 2, snap.ActiveClients)
	require.Equal(t, 5, snap.MaxClients)
	require.Equal(t, "license", snap.CapSource)
}
