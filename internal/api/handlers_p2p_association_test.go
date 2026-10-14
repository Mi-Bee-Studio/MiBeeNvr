package api

// handlers_p2p_association_test.go — App 发起账号授权关联的本地状态机：
// request → confirm → cancel 全周期 + 幂等/冲突语义。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

func doGetAssociation(t *testing.T, r http.Handler) config.P2PAssociationState {
	t.Helper()
	rr := doRequest(t, r, "GET", "/api/p2p/association", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var st config.P2PAssociationState
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &st))
	return st
}

// doPostJSON 发 POST + JSON body（body 为 nil 时发空体）。
func doPostJSON(t *testing.T, r http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if raw, ok := body.(json.RawMessage); ok {
		rd = bytes.NewReader(raw)
	} else if body == nil {
		rd = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(http.MethodPost, path, rd)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func TestP2PAssociationLifecycle(t *testing.T) {
	h, cfg, agent := setupP2PTestHandler(t)
	r := h.Routes()

	// 初始：无 pending / 无 confirmed。
	st := doGetAssociation(t, r)
	require.Nil(t, st.Pending)
	require.Nil(t, st.Confirmed)

	// App 发起关联申请。
	rr2 := doPostJSON(t, r, "/api/p2p/association/request", map[string]any{
		"platform_user_id":  "user-42",
		"platform_username": "aming",
		"display_name":      "阿明的手机",
		"request_id":        "req-1",
	})
	require.Equal(t, http.StatusAccepted, rr2.Code)
	st = doGetAssociation(t, r)
	require.NotNil(t, st.Pending)
	require.Equal(t, "user-42", st.Pending.PlatformUserID)
	require.Equal(t, "阿明的手机", st.Pending.DisplayName)
	require.Equal(t, "req-1", st.Pending.RequestID)
	require.Nil(t, st.Confirmed)
	require.NotNil(t, cfg.P2P.Association, "state must persist into config")

	// 新申请覆盖旧 pending（换账号幂等）。
	rr2 = doPostJSON(t, r, "/api/p2p/association/request", map[string]any{
		"platform_user_id":  "user-43",
		"platform_username": "bobo",
		"request_id":        "req-2",
	})
	require.Equal(t, http.StatusAccepted, rr2.Code)
	st = doGetAssociation(t, r)
	require.Equal(t, "user-43", st.Pending.PlatformUserID)

	// confirm 的 request_id 不匹配 → 409（防确认到被覆盖的旧申请）。
	rr2 = doPostJSON(t, r, "/api/p2p/association/confirm", map[string]any{"request_id": "req-1"})
	require.Equal(t, http.StatusConflict, rr2.Code)

	// 正确确认 → pending 清空、confirmed 带确认时刻。
	rr2 = doPostJSON(t, r, "/api/p2p/association/confirm", map[string]any{"request_id": "req-2"})
	require.Equal(t, http.StatusOK, rr2.Code)
	st = doGetAssociation(t, r)
	require.Nil(t, st.Pending)
	require.NotNil(t, st.Confirmed)
	require.Equal(t, "user-43", st.Confirmed.PlatformUserID)
	require.NotNil(t, st.Confirmed.ConfirmedAt)

	// 已确认后再 confirm（无 pending）→ 409。
	rr2 = doPostJSON(t, r, "/api/p2p/association/confirm", nil)
	require.Equal(t, http.StatusConflict, rr2.Code)

	// 取消关联 → 全空，配置节点回收；解绑联动：P2P 停用 + 热应用断链
	//（凭据保留——App 无平台密码无法重发，重绑走 PUT enabled=true）。
	cfg.P2P.Enabled = true
	cfg.P2P.Username = "app-user"
	cfg.P2P.Password = "app-secret"
	rr2 = doPostJSON(t, r, "/api/p2p/association/cancel", nil)
	require.Equal(t, http.StatusOK, rr2.Code)
	st = doGetAssociation(t, r)
	require.Nil(t, st.Pending)
	require.Nil(t, st.Confirmed)
	require.Nil(t, cfg.P2P.Association)
	require.False(t, cfg.P2P.Enabled, "取消已确认关联应停用 P2P")
	require.Equal(t, "app-user", cfg.P2P.Username, "凭据保留供重绑复用")
	require.Equal(t, "app-secret", cfg.P2P.Password)
	require.NotEmpty(t, agent.applied, "取消应热应用断链")
	require.False(t, agent.applied[len(agent.applied)-1].Enabled)
}

func TestP2PAssociationValidation(t *testing.T) {
	h, _, _ := setupP2PTestHandler(t)
	r := h.Routes()

	// 缺 platform_user_id → 400。
	rr2 := doPostJSON(t, r, "/api/p2p/association/request", map[string]any{
		"platform_username": "no-id",
	})
	require.Equal(t, http.StatusBadRequest, rr2.Code)

	// 坏 JSON → 400。
	rrBad := doPostJSON(t, r, "/api/p2p/association/request", json.RawMessage("{not json"))
	require.Equal(t, http.StatusBadRequest, rrBad.Code)

	// 无 pending 时 confirm → 409；cancel 幂等成功。
	rr2 = doPostJSON(t, r, "/api/p2p/association/confirm", nil)
	require.Equal(t, http.StatusConflict, rr2.Code)
	rr2 = doPostJSON(t, r, "/api/p2p/association/cancel", nil)
	require.Equal(t, http.StatusOK, rr2.Code)
}
