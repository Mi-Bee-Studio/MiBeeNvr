package p2p

// 服务端 2026-11 拆双进程后的错误语义适配：身份/商业端点被打到只服务
// 数据面的节点时，服务端答 503 identity_plane_moved（过渡期旧 worker
// 答 worker_node_no_identity_plane，或对 /oidc/token 直接 404 一个纯文本
// 节点标识）。这是**部署配置错误**——重试不会自愈。三类现场都必须被
// 识别成同一个类型化错误，否则会被当成瞬时故障无限重试。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestIsIdentityPlaneMovedBody 双串兼容：新契约与过渡期旧串都要认。
func TestIsIdentityPlaneMovedBody(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"new contract", `{"error":"identity_plane_moved: this endpoint is served by mibee-biz"}`, true},
		{"legacy worker", `{"error":"worker_node_no_identity_plane"}`, true},
		{"unrelated", `{"error":"invalid_grant"}`, false},
		{"empty", ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isIdentityPlaneMovedBody(tc.body); got != tc.want {
				t.Fatalf("isIdentityPlaneMovedBody(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestIsBareNodeBanner 过渡期旧 worker 对身份路径答纯文本节点标识
// （无 JSON error 字段），只能靠形态识别。
func TestIsBareNodeBanner(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"node banner", "MiBee P2P node (bj-temp)", true},
		{"with newline", "MiBee P2P node (sh-temp)\n", true},
		{"json error", `{"error":"missing device_code"}`, false},
		{"long text", "MiBee P2P node " + strings.Repeat("x", 300), false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBareNodeBanner([]byte(tc.body)); got != tc.want {
				t.Fatalf("isBareNodeBanner(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestGrantDetectsIdentityPlaneMoved 钉住 OIDC token 端点的识别：503 新串、
// 503 旧串、以及 404 纯文本节点标识，都必须返回 ErrIdentityPlaneMoved
// （而不是泛化的 "token grant failed"）。
func TestGrantDetectsIdentityPlaneMoved(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"503 new contract", http.StatusServiceUnavailable,
			`{"error":"identity_plane_moved: this endpoint is served by mibee-biz"}`},
		{"503 legacy worker", http.StatusServiceUnavailable,
			`{"error":"worker_node_no_identity_plane"}`},
		{"404 node banner", http.StatusNotFound, "MiBee P2P node (bj-temp)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			tm := NewTokenManager(srv.URL, "client", "u", "p", "")
			tm.minInterval = 0 // 允许立即尝试
			_, err := tm.Mint(context.Background())
			if !errors.Is(err, ErrIdentityPlaneMoved) {
				t.Fatalf("Mint error = %v, want ErrIdentityPlaneMoved", err)
			}
		})
	}
}

// TestGrantOrdinaryFailureIsNotConfigError 防过度识别：普通凭据失败不得
// 被当成配置错误（否则会掩盖真实的 auth 问题并阻止正常重试路径）。
func TestGrantOrdinaryFailureIsNotConfigError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()

	tm := NewTokenManager(srv.URL, "client", "u", "p", "")
	tm.minInterval = 0
	_, err := tm.Mint(context.Background())
	if err == nil {
		t.Fatal("expected an error for invalid_grant")
	}
	if errors.Is(err, ErrIdentityPlaneMoved) {
		t.Fatalf("ordinary auth failure misclassified as config error: %v", err)
	}
}
