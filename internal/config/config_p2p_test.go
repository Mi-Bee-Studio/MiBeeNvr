package config

import (
	"strings"
	"testing"
)

func TestP2PDefaults(t *testing.T) {
	c := &P2PConfig{}
	c.applyDefaults("11111111-2222-4333-8444-555555555555")

	if c.ProductID != "mibeenvr" {
		t.Errorf("product_id default = %q, want mibeenvr", c.ProductID)
	}
	if c.PeerID != "11111111-2222-4333-8444-555555555555" {
		t.Errorf("peer_id default = %q, want install device_id", c.PeerID)
	}
	if c.ServiceName != "nvr-api" {
		t.Errorf("service_name default = %q, want nvr-api", c.ServiceName)
	}
	if c.ClientID != "p2p-signaling" {
		t.Errorf("client_id default = %q", c.ClientID)
	}
	if c.TransportTimeoutSec != 20 {
		t.Errorf("transport_timeout_s default = %d, want 20", c.TransportTimeoutSec)
	}
	if c.RedialMinIntervalSec != 30 {
		t.Errorf("redial_min_interval_s default = %d, want 30", c.RedialMinIntervalSec)
	}

	// 显式配置不覆盖。
	c2 := &P2PConfig{PeerID: "custom", TransportTimeoutSec: 3, RedialMinIntervalSec: 4}
	c2.applyDefaults("dev-1")
	if c2.PeerID != "custom" {
		t.Errorf("explicit peer_id overwritten: %q", c2.PeerID)
	}
	if c2.TransportTimeoutSec != 5 {
		t.Errorf("transport_timeout_s clamp = %d, want 5", c2.TransportTimeoutSec)
	}
	if c2.RedialMinIntervalSec != 5 {
		t.Errorf("redial_min_interval_s clamp = %d, want 5", c2.RedialMinIntervalSec)
	}
}

func TestP2PValidate(t *testing.T) {
	// 关闭状态零要求。
	if err := (&P2PConfig{}).validate(); err != nil {
		t.Fatalf("disabled config should pass, got %v", err)
	}

	// 开启但缺 server_url。
	err := (&P2PConfig{Enabled: true, Username: "u", Password: "p"}).validate()
	if err == nil || !strings.Contains(err.Error(), "server_url") {
		t.Fatalf("want server_url error, got %v", err)
	}

	// 非 ws scheme。
	err = (&P2PConfig{Enabled: true, ServerURL: "https://x/signal", Username: "u", Password: "p"}).validate()
	if err == nil || !strings.Contains(err.Error(), "ws://") {
		t.Fatalf("want scheme error, got %v", err)
	}

	// 缺凭据。
	err = (&P2PConfig{Enabled: true, ServerURL: "wss://x/signal"}).validate()
	if err == nil || !strings.Contains(err.Error(), "username") {
		t.Fatalf("want credential error, got %v", err)
	}

	// 完整配置通过。
	ok := &P2PConfig{Enabled: true, ServerURL: "wss://x/signal", Username: "u", Password: "p"}
	if err := ok.validate(); err != nil {
		t.Fatalf("complete config should pass, got %v", err)
	}
}

func TestP2PLocalAddrFromListen(t *testing.T) {
	c := &P2PConfig{}
	if got := c.LocalAddrFromListen(":9090"); got != "127.0.0.1:9090" {
		t.Errorf(":9090 → %q, want 127.0.0.1:9090", got)
	}
	if got := c.LocalAddrFromListen("0.0.0.0:8080"); got != "127.0.0.1:8080" {
		t.Errorf("0.0.0.0:8080 → %q", got)
	}
	c.LocalAddr = "127.0.0.1:1"
	if got := c.LocalAddrFromListen(":9090"); got != "127.0.0.1:1" {
		t.Errorf("explicit override ignored: %q", got)
	}
}

// enabled 的 p2p 节要能通过整体 Validate（默认值在 applyDefaults 已就位）。
func TestP2PValidateIntegration(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	cfg.P2P.Enabled = true
	cfg.P2P.ServerURL = "wss://example/signal"
	cfg.P2P.Username = "u"
	cfg.P2P.Password = "p"
	if err := Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
}
