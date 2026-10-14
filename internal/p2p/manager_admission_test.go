package p2p

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// TestClientCapResolution 钉死并发上限解析规则：配置与授权配额取更严者，
// 全零兜底 3，来源字符串可诊断。
func TestClientCapResolution(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cfgMax     int
		gate       *fakeGate
		wantMax    int
		wantSource string
	}{
		{name: "全零 → 默认 3", wantMax: 3, wantSource: "default"},
		{name: "仅 license", gate: &fakeGate{maxCli: 5}, wantMax: 5, wantSource: "license"},
		{name: "仅 config", cfgMax: 2, wantMax: 2, wantSource: "config"},
		{name: "license 5 config 2 → 取严 2", cfgMax: 2, gate: &fakeGate{maxCli: 5}, wantMax: 2, wantSource: "license+config"},
		{name: "license 2 config 5 → 取严 2", cfgMax: 5, gate: &fakeGate{maxCli: 2}, wantMax: 2, wantSource: "license+config"},
		{name: "license 0(不限) config 8 → 8", cfgMax: 8, gate: &fakeGate{maxCli: 0}, wantMax: 8, wantSource: "config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gate LicenseGate
			if tc.gate != nil {
				gate = tc.gate
			}
			m := New(config.P2PConfig{MaxClients: tc.cfgMax}, "127.0.0.1:9090", "", nil, gate)
			got := m.clientCap()
			if got.max != tc.wantMax {
				t.Fatalf("cap = %d, want %d", got.max, tc.wantMax)
			}
			if got.source != tc.wantSource {
				t.Fatalf("source = %q, want %q", got.source, tc.wantSource)
			}
		})
	}
}

// TestPlatformLicenseRejection 钉死平台策略拒绝的判定矩阵：4003 恒为
// license 类（该码专属吊销扫描），1008 仅 license* reason 认定，其它
// 1008 是普通握手拒绝，hello 阶段 error 消息字符串兜底。
func TestPlatformLicenseRejection(t *testing.T) {
	closeErr := func(code int, text string) error {
		return fmt.Errorf("read welcome: %w", &websocket.CloseError{Code: code, Text: text})
	}
	for _, tc := range []struct {
		name     string
		err      error
		wantOK   bool
		wantCode int
	}{
		{name: "4003 会话中吊销", err: closeErr(4003, "license_expired"), wantOK: true, wantCode: 4003},
		{name: "4003 无 reason 也认定", err: closeErr(4003, ""), wantOK: true, wantCode: 4003},
		{name: "1008 license_required", err: closeErr(1008, "license_required"), wantOK: true, wantCode: 1008},
		{name: "1008 其它 reason 非拒绝", err: closeErr(1008, "invalid_token")},
		{name: "1011 服务器错误", err: closeErr(1011, "boom")},
		{name: "普通网络错误", err: errors.New("dial tcp: refused")},
		{name: "nil", err: nil},
		{name: "hello error 消息形态", err: errors.New("server error: license_required: license required"), wantOK: true, wantCode: 1008},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, ok := platformLicenseRejection(tc.err)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && code != tc.wantCode {
				t.Fatalf("code = %d, want %d", code, tc.wantCode)
			}
		})
	}
}

// TestRejectionBackoff 指数退避：连击 1 = 基础间隔，逐次翻倍，封顶
// 30 分钟。
func TestRejectionBackoff(t *testing.T) {
	base := 30 * time.Second
	cases := []struct {
		n    int
		want time.Duration
	}{
		{1, 30 * time.Second},
		{2, time.Minute},
		{3, 2 * time.Minute},
		{7, 32 * time.Minute /* > cap → capped */},
		{50, 30 * time.Minute},
	}
	for _, tc := range cases {
		got := rejectionBackoff(base, tc.n)
		if got > maxRejectionBackoff {
			t.Fatalf("n=%d backoff %v exceeds cap", tc.n, got)
		}
		if tc.n <= 6 && got != tc.want {
			t.Fatalf("n=%d backoff = %v, want %v", tc.n, got, tc.want)
		}
		if tc.n >= 7 && got != maxRejectionBackoff {
			t.Fatalf("n=%d backoff = %v, want cap %v", tc.n, got, maxRejectionBackoff)
		}
	}
}

// TestStatusSnapshotSurfacesRejection 验证拒绝记录与上限进入快照（API
// 呈现「平台授权被拒」的数据源）。
func TestStatusSnapshotSurfacesRejection(t *testing.T) {
	gate := &fakeGate{allowed: true, state: "valid"}
	m := New(config.P2PConfig{Enabled: true, MaxClients: 4}, "127.0.0.1:9090", "", nil, gate)

	m.recordRejection(1008, "license_required", "hello")
	m.rejectedTotal.Add(2)

	snap := m.Status()
	if !snap.Enabled {
		t.Fatal("snapshot must report enabled")
	}
	if snap.MaxClients != 4 || snap.CapSource != "config" {
		t.Fatalf("cap = %d/%q, want 4/config", snap.MaxClients, snap.CapSource)
	}
	if snap.RejectedTotal != 2 {
		t.Fatalf("rejected_total = %d, want 2", snap.RejectedTotal)
	}
	if snap.LastRejection == nil || snap.LastRejection.Code != 1008 || snap.LastRejection.Source != "hello" {
		t.Fatalf("last_rejection = %+v, want 1008/hello", snap.LastRejection)
	}
	if snap.License == nil || !snap.License.Allowed || snap.License.State != "valid" {
		t.Fatalf("license = %+v, want allowed/valid", snap.License)
	}
}

// TestGateFeatureCheck 授权文档限定能力清单且不含 p2p 时，门控不放行。
func TestGateFeatureCheck(t *testing.T) {
	m := New(config.P2PConfig{}, "127.0.0.1:9090", "", nil,
		&fakeGate{allowed: true, state: "valid", feature: map[string]bool{"audio": true}})
	if m.gateAllowed() {
		t.Fatal("license without p2p feature must block dialing")
	}
	// 空 features（nil map）= 全能 → 放行。
	m2 := New(config.P2PConfig{}, "127.0.0.1:9090", "", nil,
		&fakeGate{allowed: true, state: "valid"})
	if !m2.gateAllowed() {
		t.Fatal("empty feature list must allow everything")
	}
}

// TestEventRingRecordsAndCaps 事件环：记录顺序保留、上限覆写、快照隔离。
func TestEventRingRecordsAndCaps(t *testing.T) {
	m := New(config.P2PConfig{}, "127.0.0.1:9090", "", nil, nil)

	m.addEvent("online", "", "signaling connected")
	m.addEvent("connect_request", "sess-1", "service= active=0 cap=3")
	m.addEvent("rejected", "sess-2", "device_busy: active=1 cap=1(config)")

	ev := m.RecentEvents()
	if len(ev) != 3 {
		t.Fatalf("events = %d, want 3", len(ev))
	}
	if ev[0].Type != "online" || ev[2].SessionID != "sess-2" {
		t.Fatalf("order/session mismatch: %+v", ev)
	}

	// 环形覆写：灌 maxEvents+10 条，只留最近 maxEvents 条。
	for range maxEvents + 10 {
		m.addEvent("closed", "s", "bulk")
	}
	ev = m.RecentEvents()
	if len(ev) != maxEvents {
		t.Fatalf("ring size = %d, want %d", len(ev), maxEvents)
	}

	// 快照是副本：改副本不影响内部环。
	ev[0].Type = "mutated"
	if m.RecentEvents()[0].Type == "mutated" {
		t.Fatal("snapshot must be a copy")
	}

	// 事件进入状态快照。
	snap := m.Status()
	if len(snap.RecentEvents) != maxEvents {
		t.Fatalf("status recent_events = %d, want %d", len(snap.RecentEvents), maxEvents)
	}
}
