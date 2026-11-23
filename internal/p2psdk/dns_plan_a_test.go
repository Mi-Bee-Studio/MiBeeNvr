package peer

// dns_plan_a_test.go — DNS 方案 A 的客户端契约：
//   - PublicNode 携带 signal_ip / signal_domain
//   - TLSServerName 优先用 signal_domain，回退解析 signal_url
//   - DialAddr 把 host 换成 signal_ip 且保留端口，SNI 仍是区域域名（最关键的
//     组合：dial IP + present domain）
//   - withServerName 拒绝覆盖调用方显式 pin 的 ServerName

import (
	"crypto/tls"
	"encoding/json"
	"testing"
)

func TestPublicNode_DualFieldsDecode(t *testing.T) {
	raw := `{"node_id":"nd-hk","signal_url":"wss://p2p-cn.example.com:443/signal",
	          "signal_ip":"1.2.3.4","signal_domain":"p2p-cn.example.com","online":true}`
	var n PublicNode
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if n.SignalIP != "1.2.3.4" {
		t.Errorf("signal_ip = %q", n.SignalIP)
	}
	if n.SignalDomain != "p2p-cn.example.com" {
		t.Errorf("signal_domain = %q", n.SignalDomain)
	}
	if got := n.TLSServerName(); got != "p2p-cn.example.com" {
		t.Errorf("TLSServerName = %q, want the region domain", got)
	}
}

func TestPublicNode_DialAddrSplitsAddressFromSNI(t *testing.T) {
	n := PublicNode{
		SignalURL:    "wss://p2p-cn.example.com:443/signal",
		SignalIP:     "1.2.3.4",
		SignalDomain: "p2p-cn.example.com",
	}

	dialed, serverName, changed := n.DialAddr(n.SignalURL)
	if !changed {
		t.Fatal("a published signal_ip must change the dial address")
	}
	if dialed != "wss://1.2.3.4:443/signal" {
		t.Errorf("dialed = %q, want the IP with the original port preserved", dialed)
	}
	if serverName != "p2p-cn.example.com" {
		t.Errorf("serverName = %q, want the region domain", serverName)
	}
}

func TestPublicNode_DialAddrWithPortInSignalIP(t *testing.T) {
	n := PublicNode{
		SignalURL: "wss://p2p-cn.example.com:443/signal",
		SignalIP:  "1.2.3.4:8443",
	}
	dialed, _, changed := n.DialAddr(n.SignalURL)
	if !changed {
		t.Fatal("expected a rewrite")
	}
	if dialed != "wss://1.2.3.4:8443/signal" {
		t.Errorf("dialed = %q, want signal_ip's own port to win", dialed)
	}
}

func TestPublicNode_NoSignalIPKeepsURL(t *testing.T) {
	n := PublicNode{
		SignalURL:    "wss://p2p-cn.example.com/signal",
		SignalDomain: "p2p-cn.example.com",
	}
	dialed, serverName, changed := n.DialAddr(n.SignalURL)
	if changed {
		t.Error("without signal_ip the URL must not be rewritten")
	}
	if dialed != n.SignalURL {
		t.Errorf("dialed = %q", dialed)
	}
	if serverName != "p2p-cn.example.com" {
		t.Errorf("serverName = %q — SNI is still the domain even when no IP split exists", serverName)
	}
}

func TestTLSServerName_FallsBackToHost(t *testing.T) {
	n := PublicNode{SignalURL: "wss://nd-7.example.com/signal"}
	if got := n.TLSServerName(); got != "nd-7.example.com" {
		t.Errorf("TLSServerName = %q, want the host parsed from signal_url", got)
	}
}

func TestWithServerName_ClonesAndPreservesCallerConfig(t *testing.T) {
	original := &tls.Config{MinVersion: tls.VersionTLS13}
	out, err := withServerName(original, "p2p-cn.example.com")
	if err != nil {
		t.Fatalf("withServerName: %v", err)
	}
	if out.ServerName != "p2p-cn.example.com" {
		t.Errorf("ServerName = %q", out.ServerName)
	}
	if original.ServerName != "" {
		t.Error("the caller's config must not be mutated")
	}
	if out.MinVersion != tls.VersionTLS13 {
		t.Error("the clone must keep every other setting")
	}
}

func TestWithServerName_RefusesToOverridePinnedName(t *testing.T) {
	pinned := &tls.Config{ServerName: "pinned.example.com"}
	_, err := withServerName(pinned, "other.example.com")
	if err == nil {
		t.Fatal("overriding an explicitly pinned ServerName must be an error")
	}
}

func TestWithServerName_EmptyNameIsNoOp(t *testing.T) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	out, err := withServerName(cfg, "")
	if err != nil {
		t.Fatalf("withServerName: %v", err)
	}
	if out != cfg {
		t.Error("an empty name must return the caller's config untouched")
	}
}
