package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// P2PForceRelay 是 p2p.force_relay 三态：""（或 false）保持历史行为
// （直连优先仲裁，逐字节一致）；true 恒钉 relay（跳过 ICE 仲裁）；
// auto 按最近一次 NAT 观测动态决定——v4 对称 NAT 且无全局 IPv6 时钉
// relay（跳过注定失败的仲裁窗），其余保持直连优先。YAML 同时接受
// bool 与字符串写法。
type P2PForceRelay string

const (
	// ForceRelayDefault（空/false）：历史直连优先仲裁。
	ForceRelayDefault P2PForceRelay = ""
	// ForceRelayAlways（true）：每个会话不经 ICE 仲裁直接落 relay。
	ForceRelayAlways P2PForceRelay = "true"
	// ForceRelayAuto：按 NAT 观测动态决定。
	ForceRelayAuto P2PForceRelay = "auto"
)

// UnmarshalYAML 接受 bool（true/false）与字符串（"true"/"false"/"auto"）
// 两种写法；其余值为配置错误。
func (f *P2PForceRelay) UnmarshalYAML(node *yaml.Node) error {
	var b bool
	if err := node.Decode(&b); err == nil {
		if b {
			*f = ForceRelayAlways
		} else {
			*f = ForceRelayDefault
		}
		return nil
	}
	var s string
	if err := node.Decode(&s); err != nil {
		return fmt.Errorf("p2p.force_relay: want bool or \"true\"/\"false\"/\"auto\"")
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "false":
		*f = ForceRelayDefault
	case "true":
		*f = ForceRelayAlways
	case "auto":
		*f = ForceRelayAuto
	default:
		return fmt.Errorf("p2p.force_relay: unknown value %q (want true/false/auto)", s)
	}
	return nil
}

// Normalized 返回对外展示/序列化的规范串（"" → "false"）。
func (f P2PForceRelay) Normalized() string {
	if f == ForceRelayDefault {
		return "false"
	}
	return string(f)
}

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

	// NodesURL 是联邦公共节点表端点（GET /api/nodes，就近选点）。空 =
	// 不启用（单节点行为不变）；设置后每次信令连接前并发 RTT 探测定
	// 节点，任何失败硬回退 server_url——选点永不阻塞、永不恶化连接。
	// 结果进 journal（node discovery selected endpoint / 回退 Warn）与
	// /api/p2p/status 的 node_selection 块。
	NodesURL string `yaml:"nodes_url"`

	// TransportTimeout 是单次 ICE/DataChannel 建立预算（含 relay 仲裁）。
	TransportTimeoutSec int `yaml:"transport_timeout_s"`

	// ForceRelay 钉选传输仲裁：""（默认，含 false）保持直连优先仲裁；
	// "true" 每个会话跳过 ICE 仲裁直接落 relay；"auto" 按最近一次 NAT
	// 观测动态决定（v4 对称 NAT 且无全局 IPv6 → 钉 relay，跳过注定失败
	// 的仲裁窗；其余直连优先）。决策与原因见 /api/p2p/status 的
	// transport_decision / decision_reason。
	ForceRelay P2PForceRelay `yaml:"force_relay"`

	// RedialMinIntervalSec 是会话彻底失败后的最小重拨间隔（防锤信令）。
	RedialMinIntervalSec int `yaml:"redial_min_interval_s"`

	// SessionIdleTimeoutSec 是无流量会话的空闲回收时长（秒）。手机熄屏
	// 后会话必死（OS 杀网络），relay 会话的 ICE restart 机器不适用——
	// 空闲扫是唯一收割者；默认 900s 下高频熄屏/亮屏会把死会话堆到并发
	// 上限（2026-10-14 实测 active_clients 5 分钟 4 条）。0 = SDK 默认
	// （15min）；负数 = 关闭清扫（不建议）。
	SessionIdleTimeoutSec int `yaml:"session_idle_timeout_s"`

	// MaxClients 是允许同时接入的 P2P 客户端上限。0 = 不覆盖（取授权
	// 文档配额，两者皆无则默认 3）。超限新连接在信令层即被拒
	// （connect_reject device_busy），WebRTC 拨号不发生。
	MaxClients int `yaml:"max_clients"`

	// DeviceKeyPath 指向 raw Ed25519 私钥文件（32B seed 或 64B 全钥，
	// 建议 0600）。设置后每个 hello 对 JWT 的 "<sub>:<jti>" 签名——
	// 平台侧设备档案绑定了公钥时强制要求（零信任身份绑定；同一密钥
	// 用于 re-enroll 的持有证明）。档案未绑公钥时可留空（无签名路径）。
	DeviceKeyPath string `yaml:"device_key"`

	// EgressPriorityBytes 是每条隧道流的高优出口预算（字节）。每流的
	// 前 N 字节走传输高优路径：小响应（API JSON/playlist/探测回包）
	// 全程命中，视频等批量下载拿到快速起步后降级普通车道——消除视频
	// 满载时 API 响应被排在 SCTP 队列后面的 HOL（实测 app_rtt 4.3s vs
	// net_rtt 14-28ms）。0 = 默认 32KiB；负数 = 关闭。
	EgressPriorityBytes int64 `yaml:"egress_priority_bytes"`

	// Advertise 是 ICE 通告端点（零配置直连路径，#10）：
	//   ""            默认行为（host/srflx/relay 常规 ICE 收集）；
	//   "auto"        每次会话构建跑 UPnP→NAT-PMP→STUN 阶梯（结果
	//                 TTL 5min 缓存，负结果也缓存）；
	//   "auto:<port>" 同上但钉固定 UDP 端口（默认 40000）；
	//   "<ip>:<port>" 固定公网端点直填（1:1 DNAT / 固定公网 IP 场景，
	//                 对端即使在对称 NAT 后也可直连）。
	// 阶梯/静态假设端口映射可达；不可达时 ICE 照常回落 TURN/relay——
	// 通告失败不破连通性。阶梯执行与结果记 journal（组件 p2p）。
	Advertise string `yaml:"advertise"`

	// ICE 计时器（毫秒；0 = 库默认：keepalive 2s / connected→disconnected
	// 5s / disconnected→failed 25s）。高抖动链路（弱 5G/地铁/跨境
	// 300-500ms RTT）的 p99 尖峰会被库默认读成链路死亡——ICE restart
	// 抖动而非容忍。可只调其中一项（其余回落库默认）。全 0 = 与历史
	// 行为逐字节一致。
	ICEKeepaliveIntervalMS   int `yaml:"ice_keepalive_interval_ms"`
	ICEDisconnectedTimeoutMS int `yaml:"ice_disconnected_timeout_ms"`
	ICEFailedTimeoutMS       int `yaml:"ice_failed_timeout_ms"`

	// ICEReceiveMTU 覆盖 ICE 接收 MTU（字节；0 = 库默认）。小 MTU 路径
	// （PPPoE/部分企业出口）可调；无 RFC 8829 协商，是静态旋钮。
	ICEReceiveMTU int `yaml:"ice_receive_mtu"`

	// TunnelMaxPayloadBytes 覆盖隧道分片大小（字节；0 = 默认 16KiB——与
	// 移动端对齐，relay TCP 路径的重传单元与在途字节减半。直连 SCTP 路径
	// 32KiB 实测上行吞吐高 ~20%，但两者都远超实际视频码率，要该余量的
	// 部署可显式调大）。接收方不校验分片大小，两端不同设置互通；上限
	// 32KiB 由 SCTP 64KiB 消息天花板约束（base64 膨胀 4/3）。
	TunnelMaxPayloadBytes int `yaml:"tunnel_max_payload_bytes"`

	// Association 是「App 发起账号授权关联」的本地状态（产品化 M1，
	// docs/05 方案）：App 在 LAN 内发起 request → 设备主人在 NVR 上
	// confirm → 之后 App 经 PUT /api/settings/p2p 下发运行配置。
	// 平台侧 user-device 绑定上报待平台联调后接入。
	Association *P2PAssociationState `yaml:"association,omitempty"`
}

// P2PAssociationEntry 一次关联的双方身份快照。
type P2PAssociationEntry struct {
	// PlatformUserID 是平台侧终端用户标识（OAuth sub）。M1 由 App 申报，
	// 平台联调后改为关联时平台签发核验。
	PlatformUserID string `yaml:"platform_user_id" json:"platform_user_id"`
	// PlatformUsername 是平台账号名（展示用）。
	PlatformUsername string `yaml:"platform_username" json:"platform_username"`
	// DisplayName 是人类可读名（如「小米手机 · 阿明」），NVR 确认界面展示。
	DisplayName string `yaml:"display_name" json:"display_name"`
	// RequestID 由发起方生成（幂等去重；confirm 时可校验一致）。
	RequestID string `yaml:"request_id" json:"request_id"`
	// At 是 request 创建时刻。
	At time.Time `yaml:"at" json:"at"`
	// ConfirmedAt 是设备主人确认时刻（仅 confirmed 条目有值）。
	ConfirmedAt *time.Time `yaml:"confirmed_at,omitempty" json:"confirmed_at,omitempty"`
}

// P2PAssociationState M1 形态：同一时刻至多一个待确认申请、至多一个已
// 关联账号（新关联覆盖旧关联——家庭单主模型；多用户等平台维度支持）。
type P2PAssociationState struct {
	Pending   *P2PAssociationEntry `yaml:"pending,omitempty" json:"pending,omitempty"`
	Confirmed *P2PAssociationEntry `yaml:"confirmed,omitempty" json:"confirmed,omitempty"`
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
	if c.EgressPriorityBytes == 0 {
		c.EgressPriorityBytes = 32 << 10
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
	// 设备身份模式（enrollment 引导，user-tenant-model §5）：username 为
	// device:<product>:<id>，无 password（token 只经 refresh 轮换链）。
	if strings.TrimSpace(c.Username) == "" ||
		(strings.TrimSpace(c.Password) == "" && !strings.HasPrefix(c.Username, "device:")) {
		return fmt.Errorf("p2p.username and p2p.password are required when p2p.enabled " +
			"(device-identity mode: device:… username, empty password)")
	}
	if c.MaxClients < 0 || c.MaxClients > 64 {
		return fmt.Errorf("p2p.max_clients must be between 0 (auto) and 64, got %d", c.MaxClients)
	}
	// 密钥文件存在性前置校验：绑定公钥的档案下无签名 hello 会被拒，
	// 与其上线后被信令反复打回，不如启动时就把配错暴露出来。
	if c.DeviceKeyPath != "" {
		if fi, err := os.Stat(c.DeviceKeyPath); err != nil {
			return fmt.Errorf("p2p.device_key %q: %w", c.DeviceKeyPath, err)
		} else if fi.IsDir() {
			return fmt.Errorf("p2p.device_key %q is a directory", c.DeviceKeyPath)
		}
	}
	if c.NodesURL != "" {
		if u, err := url.Parse(c.NodesURL); err != nil || u.Host == "" ||
			(u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("p2p.nodes_url must be an absolute http(s) URL when set")
		}
	}
	if err := validateAdvertise(c.Advertise); err != nil {
		return fmt.Errorf("p2p.advertise: %w", err)
	}
	if err := validateICETuning(c); err != nil {
		return err
	}
	return nil
}

// validateICETuning 校验 ICE 计时器/MTU/分片三组旋钮（#13）。0 = 默认，
// 负数/超界拒绝——计时器上限取「能容忍但仍会拆链」的量级（keepalive
// ≤10min、disconnected ≤30min、failed ≤2h），再大等于关掉保活语义，
// 属于误配。MTU 下界 576（IPv4 最小重组缓冲），上界 65535。分片
// 4096..32768（SCTP 天花板）。
func validateICETuning(c *P2PConfig) error {
	for _, kv := range []struct {
		name string
		v    int
		max  int
	}{
		{"p2p.ice_keepalive_interval_ms", c.ICEKeepaliveIntervalMS, 600_000},
		{"p2p.ice_disconnected_timeout_ms", c.ICEDisconnectedTimeoutMS, 1_800_000},
		{"p2p.ice_failed_timeout_ms", c.ICEFailedTimeoutMS, 7_200_000},
	} {
		if kv.v < 0 || kv.v > kv.max {
			return fmt.Errorf("%s must be 0 (library default) or 1..%dms, got %d", kv.name, kv.max, kv.v)
		}
	}
	if mtu := c.ICEReceiveMTU; mtu != 0 && (mtu < 576 || mtu > 65535) {
		return fmt.Errorf("p2p.ice_receive_mtu must be 0 (library default) or 576..65535, got %d", mtu)
	}
	if n := c.TunnelMaxPayloadBytes; n != 0 && (n < 4096 || n > 32768) {
		return fmt.Errorf("p2p.tunnel_max_payload_bytes must be 0 (default 16384) or 4096..32768, got %d", n)
	}
	return nil
}

// validateAdvertise 校验 p2p.advertise 形态：""（关）/ "auto" /
// "auto:<1-65535>" / "<ip>:<port>"（IP 字面量，v4/v6 均可）。SDK 对
// 非法值「响亮失败」（建链报错），这里前置到配置面——坏值在落盘前就
// 被拒，而不是每次会话构建都死一遍。
func validateAdvertise(spec string) error {
	if spec == "" || spec == "auto" {
		return nil
	}
	if rest, ok := strings.CutPrefix(spec, "auto:"); ok {
		n, err := strconv.Atoi(rest)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("auto port must be 1-65535, got %q", rest)
		}
		return nil
	}
	host, portStr, err := net.SplitHostPort(spec)
	if err != nil {
		return fmt.Errorf("must be \"auto\"/\"auto:<port>\"/\"<ip>:<port>\": %w", err)
	}
	if net.ParseIP(host) == nil {
		return fmt.Errorf("%q is not an IP literal (NAT1To1 通告需要固定 IP，DDNS 域名请解析后填 IP)", host)
	}
	n, err := strconv.Atoi(portStr)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("port must be 1-65535, got %q", portStr)
	}
	return nil
}

// ValidateP2P 暴露 p2p 节校验给设置 API（安装前预检，避免坏配置落盘）。
func (c *Config) ValidateP2P() error { return c.P2P.validate() }

// ValidateP2PConfig 校验一份独立的 P2P 配置副本（设置 API 的「副本预检、
// 通过才提交」流程用——被拒请求不得污染内存态）。
func ValidateP2PConfig(c *P2PConfig) error { return c.validate() }

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
