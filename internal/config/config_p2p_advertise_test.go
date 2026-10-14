package config

import "testing"

// TestValidateAdvertise 钉住 p2p.advertise 形态校验：合法三态 + 非法值
// 在配置面被拒（SDK 侧对非法值「响亮失败」，这里前置到落盘前）。
func TestValidateAdvertise(t *testing.T) {
	cases := []struct {
		spec string
		ok   bool
	}{
		{"", true},
		{"auto", true},
		{"auto:40000", true},
		{"auto:1", true},
		{"auto:65535", true},
		{"auto:0", false},
		{"auto:65536", false},
		{"auto:xyz", false},
		{"auto:", false},
		{"203.0.113.5:41913", true},
		{"[2001:db8::1]:40000", true},
		{"example.com:4000", false}, // 域名不允许——NAT1To1 需要固定 IP
		{"1.2.3.4", false},          // 缺端口
		{"1.2.3.4:0", false},
		{"1.2.3.4:70000", false},
		{"definitely-bad", false},
		{"AUTO", false}, // 大小写敏感（与 SDK 阶梯一致）
	}
	for _, tc := range cases {
		if err := validateAdvertise(tc.spec); (err == nil) != tc.ok {
			t.Errorf("validateAdvertise(%q) err=%v, want ok=%v", tc.spec, err, tc.ok)
		}
	}
}

// TestP2PValidateRejectsBadAdvertise 走完整 validate() 链路确认报错带
// p2p.advertise 前缀（API 面直接把该文案回给用户）。
func TestP2PValidateRejectsBadAdvertise(t *testing.T) {
	c := &P2PConfig{
		Enabled:   true,
		ServerURL: "wss://s.example.com/signal",
		Username:  "device:mibeenvr:nvr-x",
		Advertise: "example.com:4000",
	}
	err := c.validate()
	if err == nil || err.Error() == "" {
		t.Fatal("bad advertise must fail validation")
	}
	if got := err.Error(); got[:13] != "p2p.advertise" {
		t.Fatalf("error must carry p2p.advertise prefix, got %q", got)
	}
}
