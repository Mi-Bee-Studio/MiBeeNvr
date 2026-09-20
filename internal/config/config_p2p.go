package config

import (
	"fmt"
	"strings"
)

// P2PConfig 配置外网 P2P 兜底链路（设备角色，内嵌 MiBee P2P SDK）。
//
// 形态：NVR 作为 device peer 连接信令服务器，把本机 HTTP API 注册为命名
// 服务（默认 nvr-api）；手机 App（client）经 P2P 隧道以 net.Conn 语义
// 访问，无需端口映射/公网 IP。
//
// 注意：server_url / 凭据均为部署环境信息，**不设默认值**——enabled 时
// 必须显式配置（校验强制），保证代码本身不携带任何部署细节。
type P2PConfig struct {
	Enabled bool `yaml:"enabled"`

	// ServerURL 是信令服务器 WebSocket 地址（如 wss://…/signal）。必填。
	ServerURL string `yaml:"server_url"`

	// ProductID 是平台侧产品命名空间。
	ProductID string `yaml:"product_id"`

	// PeerID 是本设备在产品内的唯一 ID。空 = 启动时取 "nvr-<hostname>"。
	PeerID string `yaml:"peer_id"`

	// ServiceName 是对外注册的隧道服务名（App 按此拨号）。
	ServiceName string `yaml:"service_name"`

	// LocalAddr 是注册到隧道的本机服务地址。空 = 由 server.listen 推导
	// （127.0.0.1:<port>）。
	LocalAddr string `yaml:"local_addr"`

	// Username / Password 是 OIDC 服务账号（password grant 自助铸信令
	// JWT；refresh token 轮换持久化，失败回退 password——不自动重试，
	// 服务端有 5 次失败锁 15 分钟的防爆破）。
	Username string `yaml:"username"`
	Password string `yaml:"password"`

	// ClientID 是 OIDC client。空 = "p2p-signaling"。
	ClientID string `yaml:"client_id"`

	// TokenEndpoint 是 OIDC token 端点。空 = 由 ServerURL 同域推导
	// （wss://host:port/… → https://host/oidc/token，经同一反代）。
	TokenEndpoint string `yaml:"token_endpoint"`

	// TransportTimeout 是单次 ICE/DataChannel 建立预算（含 relay 仲裁）。
	TransportTimeoutSec int `yaml:"transport_timeout_s"`

	// RedialMinIntervalSec 是会话彻底失败后的最小重拨间隔（防锤信令）。
	RedialMinIntervalSec int `yaml:"redial_min_interval_s"`
}

func (c *P2PConfig) applyDefaults(deviceID string) {
	if c.ProductID == "" {
		c.ProductID = "mibeenvr"
	}
	// PeerID 默认取安装级稳定身份（Server.DeviceID，UUIDv4 持久化，同
	// /api/health 暴露的 device_id）——App 侧正是以该 ID 作为 P2P 寻址
	// 锚点（发现→绑定→外网拨号同源）。
	if c.PeerID == "" && deviceID != "" {
		c.PeerID = deviceID
	}
	if c.ServiceName == "" {
		c.ServiceName = "nvr-api"
	}
	if c.ClientID == "" {
		c.ClientID = "p2p-signaling"
	}
	if c.TransportTimeoutSec == 0 {
		c.TransportTimeoutSec = 20
	}
	if c.TransportTimeoutSec < 5 {
		c.TransportTimeoutSec = 5
	}
	if c.RedialMinIntervalSec == 0 {
		c.RedialMinIntervalSec = 30
	}
	if c.RedialMinIntervalSec < 5 {
		c.RedialMinIntervalSec = 5
	}
}

func (c *P2PConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.ServerURL) == "" {
		return fmt.Errorf("p2p.server_url is required when p2p.enabled")
	}
	if !strings.HasPrefix(c.ServerURL, "wss://") && !strings.HasPrefix(c.ServerURL, "ws://") {
		return fmt.Errorf("p2p.server_url must be a ws:// or wss:// URL")
	}
	if strings.TrimSpace(c.Username) == "" || strings.TrimSpace(c.Password) == "" {
		return fmt.Errorf("p2p.username and p2p.password are required when p2p.enabled")
	}
	return nil
}

// LocalAddrFromListen 从 server.listen（":9090" 或 "0.0.0.0:9090"）推导
// 回环地址。listen 已在 ServerConfig 默认/校验环节保证带端口。
func (c *P2PConfig) LocalAddrFromListen(listen string) string {
	if c.LocalAddr != "" {
		return c.LocalAddr
	}
	i := strings.LastIndex(listen, ":")
	if i < 0 {
		return "127.0.0.1:9090"
	}
	return "127.0.0.1" + listen[i:]
}
