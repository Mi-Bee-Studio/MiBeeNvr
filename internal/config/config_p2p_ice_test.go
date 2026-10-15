package config

import "testing"

// validateICETuning 边界表（#13）：0=默认放行，负数/超界拒绝。
func TestValidateICETuning(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*P2PConfig)
		wantErr bool
	}{
		{"全零 = 库默认（零回归形态）", func(c *P2PConfig) {}, false},
		{"单项 keepalive 合法", func(c *P2PConfig) { c.ICEKeepaliveIntervalMS = 8000 }, false},
		{"三项全配合法", func(c *P2PConfig) {
			c.ICEKeepaliveIntervalMS = 10000
			c.ICEDisconnectedTimeoutMS = 20000
			c.ICEFailedTimeoutMS = 60000
		}, false},
		{"keepalive 负数拒绝", func(c *P2PConfig) { c.ICEKeepaliveIntervalMS = -1 }, true},
		{"keepalive 超上限拒绝", func(c *P2PConfig) { c.ICEKeepaliveIntervalMS = 600_001 }, true},
		{"disconnected 负数拒绝", func(c *P2PConfig) { c.ICEDisconnectedTimeoutMS = -5 }, true},
		{"failed 超上限拒绝", func(c *P2PConfig) { c.ICEFailedTimeoutMS = 7_200_001 }, true},
		{"mtu 下界 575 拒绝", func(c *P2PConfig) { c.ICEReceiveMTU = 575 }, true},
		{"mtu 1280 合法", func(c *P2PConfig) { c.ICEReceiveMTU = 1280 }, false},
		{"mtu 65535 合法（上界）", func(c *P2PConfig) { c.ICEReceiveMTU = 65535 }, false},
		{"mtu 65536 拒绝", func(c *P2PConfig) { c.ICEReceiveMTU = 65536 }, true},
		{"分片 4095 拒绝", func(c *P2PConfig) { c.TunnelMaxPayloadBytes = 4095 }, true},
		{"分片 16384 合法（App 侧同款）", func(c *P2PConfig) { c.TunnelMaxPayloadBytes = 16384 }, false},
		{"分片 32768 合法（默认值/上界）", func(c *P2PConfig) { c.TunnelMaxPayloadBytes = 32768 }, false},
		{"分片 32769 拒绝（SCTP 天花板）", func(c *P2PConfig) { c.TunnelMaxPayloadBytes = 32769 }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &P2PConfig{}
			tc.mutate(c)
			err := validateICETuning(c)
			if tc.wantErr && err == nil {
				t.Fatalf("want error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
