package peer

// tlsname.go — DNS plan A 的 SNI 覆盖（Go 客户端）。
//
// 为什么需要独立函数：TLS 的 SNI 默认取自 dial 的 host。方案 A 让客户端
// **dial IP、present 区域域名**——两者分离才能既跳过 per-node DNS 又保持
// 共享证书有效。这个函数返回一份克隆的 tls.Config（绝不改调用方的原件：
// 调用方可能在多处复用同一份配置）。

import (
	"crypto/tls"
	"errors"
)

// withServerName returns a copy of cfg with ServerName set. A nil cfg yields
// a minimal config with only ServerName. An empty serverName returns the
// caller's cfg untouched (no split needed).
func withServerName(cfg *tls.Config, serverName string) (*tls.Config, error) {
	if serverName == "" {
		return cfg, nil
	}
	var out *tls.Config
	if cfg == nil {
		out = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		out = cfg.Clone()
	}
	// A caller that already pinned a DIFFERENT ServerName is doing explicit
	// pinning: overriding it silently would defeat the pin.
	if out.ServerName != "" && out.ServerName != serverName {
		return nil, errors.New("tlsname: refusing to override an explicitly pinned ServerName")
	}
	out.ServerName = serverName
	return out, nil
}
