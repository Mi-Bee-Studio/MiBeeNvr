package p2p

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	peer "github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/tunnel"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
)

// peerDial 是对 SDK peer.Dial 的唯一调用缝（测试注入用——SignalingConfig
// 透传断言无法在真网络上做）。生产恒为 SDK 实现。
var peerDial = peer.Dial

// resumeGrace 覆盖 SDK 的 60s resume 窗口 + 重握手余量：断线后持续超过
// 该时长仍未恢复（OnReconnect 未到）即判定会话死亡，换新 token 重拨。
const resumeGrace = 90 * time.Second

// watchInterval 是监督循环的检查周期。
const watchInterval = 15 * time.Second

// defaultMaxClients 是「配置与授权文档均未给出配额」时的兜底并发上限
// （资源保护：每个客户端 = 一条 WebRTC 会话 + 一个隧道转发器）。
const defaultMaxClients = 3

// maxRejectionBackoff 是平台策略拒绝（license 类）后的重拨退避封顶。
const maxRejectionBackoff = 30 * time.Minute

// errPlatformRejected 标记一次因平台侧策略（1008/4003 license 类）终结
// 的会话/拨号——外层 Run 循环据此走指数退避而非普通重拨间隔。
var errPlatformRejected = errors.New("platform rejected the connection (license)")

// LicenseGate 报告 P2P 外网链路此刻是否被商业化授权放行（internal/license
// 的 *license.Manager 实现之；nil = 未启用授权，恒放行）。
type LicenseGate interface {
	Allowed() bool
	State() string
	// FeatureAllowed 报告命名能力（如 "p2p"）是否被授权文档授予。
	FeatureAllowed(feature string) bool
	// MaxClients 报告授权文档的并发客户端配额（0 = 不限）。
	MaxClients() int
}

// Event 是一条 p2p 链路事件（诊断环——journal 被第三方刷屏挤掉时，
// /api/p2p/status 的 recent_events 仍是权威观测面）。
type Event struct {
	At        time.Time `json:"at"`
	Type      string    `json:"type"` // connect_request|rejected|tunnel_up|tunnel_down|closed|online|offline|platform_rejected
	SessionID string    `json:"session_id,omitempty"`
	Detail    string    `json:"detail,omitempty"` // 拒绝原因/关闭原因/活跃数
}

const maxEvents = 120

// Rejection 记录平台侧策略拒绝（1008 握手拒绝 / 4003 会话中吊销）。
type Rejection struct {
	Code   int       `json:"code"`
	Reason string    `json:"reason"`
	Source string    `json:"source"` // "hello" | "session"
	At     time.Time `json:"at"`
}

// GateStatus 是授权门控对 P2P 的瞬时判定（状态快照用）。
type GateStatus struct {
	Allowed bool   `json:"allowed"`
	State   string `json:"state"`
}

// ICEStats 是设备侧一条 WebRTC 会话的 ICE 遥测（SDK OnStats 每 5s 轮询，
// 取最近一次）。relay（WSS）传输的会话没有 ICE 统计——不在此表即信号，
// 与 App 自报的 mode=relay 互证。字段口径对齐 App 自报（rtt_ms 等），
// 两端数据在 /api/p2p/status 同页可对账。
type ICEStats struct {
	SessionID     string    `json:"-"`
	LocalType     string    `json:"local_type"`           // host|srflx|prflx|relay(TURN)
	RemoteType    string    `json:"remote_type"`          // 对端候选类型
	LocalAddr     string    `json:"local_addr,omitempty"` // 本端 NAT 映射地址（诊断金料）
	RemoteAddr    string    `json:"remote_addr,omitempty"`
	IPVersion     string    `json:"ip_version,omitempty"`     // "4"|"6"——IPv6 直连可见
	RelayProtocol string    `json:"relay_protocol,omitempty"` // TURN 传输（udp/tcp/tls），非 TURN 空
	RTTMs         float64   `json:"rtt_ms"`
	AvailableKbps float64   `json:"available_kbps,omitempty"`
	BytesSent     uint64    `json:"bytes_sent,omitempty"`
	BytesReceived uint64    `json:"bytes_received,omitempty"`
	At            time.Time `json:"at"`
}

// StatusSnapshot 是 P2P 链路的诊断快照（GET /api/p2p/status）。
type StatusSnapshot struct {
	Enabled       bool                `json:"enabled"`
	Online        bool                `json:"online"`
	PeerID        string              `json:"peer_id"`
	ProductID     string              `json:"product_id"`
	ServiceName   string              `json:"service"`
	ServerURL     string              `json:"server_url"`
	ActiveClients int                 `json:"active_clients"`
	MaxClients    int                 `json:"max_clients"`
	CapSource     string              `json:"cap_source"` // license|config|license+config|default
	RejectedTotal int64               `json:"rejected_total"`
	License       *GateStatus         `json:"license,omitempty"`
	LastRejection *Rejection          `json:"last_rejection,omitempty"`
	RecentEvents  []Event             `json:"recent_events,omitempty"`
	ICESessions   map[string]ICEStats `json:"ice_sessions,omitempty"`

	// TunnelDroppedFrames 是隧道入站投递队列的累计丢帧数（消费端停滞/
	// 队列满——有序流不反压是设计选择，丢帧是「消费者停转」信号而非
	// 链路错误）。live 会话之和 + 已关闭会话结转，单调递增。
	TunnelDroppedFrames uint64 `json:"tunnel_dropped_frames,omitempty"`

	// ICEUDPOnly 如实标注传输能力边界：pion v4 只支持 UDP 候选——被
	// QoS/企业网关整形的网段只能落 relay，没有 TCP 兜底。
	ICEUDPOnly bool `json:"ice_udp_only"`
}

// Manager 监督 P2P 设备会话的完整生命周期：铸 token → 信令上线 → 每个
// 接入的客户端 DataChannel 上注册本机服务隧道 → 断线超过 resume 窗口
// 换新 token 重拨。P2P 永不阻塞主服务：一切失败只记日志 + 退避重试。
type Manager struct {
	cfg       config.P2PConfig
	localAddr string
	tokens    *TokenManager
	logger    *slog.Logger
	gate      LicenseGate

	runCtx    context.Context
	runCancel context.CancelFunc
	done      chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once

	// 会话健康标记（信令 OnDisconnect/OnReconnect 驱动）。
	disconnectedAt atomic.Int64 // unix nano; 0 = 从未断线或已恢复
	redial         chan struct{}
	online         atomic.Bool

	// 并发客户端记账：当前信令会话名下的活跃隧道（map 尺寸即活跃数）。
	// 信令会话终结时整表换新——迟到的 OnClose 回调只碰旧表，无跨会话
	// 串扰。
	clientsMu     sync.Mutex
	clients       map[*tunnel.Tunnel]struct{}
	rejectedTotal atomic.Int64

	// closedTunnelDrops 结转已关闭会话的隧道丢帧（Tunnel 关闭后计数
	// 器不可再读；live 部分在 tunnelDroppedTotal 里现场求和）。
	closedTunnelDrops atomic.Uint64
	consecRejects     int // 平台策略拒绝连击数（成功上线清零；Run 循环读写）
	lastRejection     atomic.Pointer[Rejection]

	// 上次记录的授权门控状态（状态变化才记日志，防周期刷屏）。
	lastGateState string

	// 诊断事件环（上限 [maxEvents] 覆写；快照整表拷贝）。
	eventsMu sync.Mutex
	events   []Event

	// 设备侧 ICE 遥测（SDK OnStats，#12）：sessionID → 最近一次快照。
	// 会话关闭即时修剪；信令会话重拨整表换新（与 clients 同生命周期）。
	iceMu sync.Mutex
	ice   map[string]ICEStats

	// metrics 可为 nil（未接线时遥测只进状态快照，不进 Prometheus）。
	recorder *metrics.Metrics

	// OnEvent 事件外流钩子（Start 前设置一次；nil = 不外流）。仪表盘的
	// 连接记录经此实时收流（API 层落环 + 落盘），不依赖有人轮询状态页。
	// 在 addEvent 的事件锁外调用，回调里允许再入 Manager 公开方法。
	OnEvent func(Event)
}

// New 构造 Manager。localAddr 是注册到隧道的本机服务地址（HTTP API）。
// gate 可为 nil（未启用商业化授权）。
func New(cfg config.P2PConfig, localAddr, tokenStatePath string, logger *slog.Logger, gate LicenseGate) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	endpoint := cfg.TokenEndpoint
	if endpoint == "" {
		endpoint = DeriveTokenEndpoint(cfg.ServerURL)
	}
	return &Manager{
		cfg:       cfg,
		localAddr: localAddr,
		tokens:    NewTokenManager(endpoint, cfg.ClientID, cfg.Username, cfg.Password, tokenStatePath),
		logger:    logger.With("component", "p2p"),
		gate:      gate,
		redial:    make(chan struct{}, 1),
		clients:   make(map[*tunnel.Tunnel]struct{}),
		ice:       make(map[string]ICEStats),
	}
}

// SetMetrics 接 Prometheus 记录器（可选；nil 安全——未接线时 ICE 遥测
// 仍进 /api/p2p/status，只是不进 /metrics）。
func (m *Manager) SetMetrics(rec *metrics.Metrics) { m.recorder = rec }

// Online 报告信令会话当前是否在线（探活/诊断用）。
func (m *Manager) Online() bool { return m.online.Load() }

// Start 在后台启动监督循环（幂等；App.Stop 不会取消 start ctx，故
// 生命周期由 [Manager.Stop] 显式终结）。
func (m *Manager) Start(parent context.Context) {
	m.startOnce.Do(func() {
		m.runCtx, m.runCancel = context.WithCancel(parent)
		m.done = make(chan struct{})
		go func() {
			defer close(m.done)
			m.Run(m.runCtx)
		}()
	})
}

// VerifyViewer 核验一位观看者的用户 JWT（分享执行链，user-tenant-model
// §7）：平台按本设备身份判定其范围——owner 全功能 / 被分享账号按 grant
// 的 cameras 白名单。每次调用现拉（吊销即时生效，无本地缓存窗口）。
func (m *Manager) VerifyViewer(ctx context.Context, viewerJWT string) (*ViewerScope, error) {
	gc := &GrantsClient{
		APIBase: EnrollHTTPBase(m.cfg.ServerURL),
		PeerID:  m.cfg.PeerID,
		Tokens:  m.tokens,
	}
	return gc.VerifyViewer(ctx, viewerJWT)
}

// Stop 终结监督循环并等待退出（幂等）。最后排水 TokenManager：在途
// mint 的轮换必须落盘后再让进程退——否则 refresh 单次消费+未持久化
// = 下次启动身份死亡（2026-10-08 两次生产事故）。
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		if m.runCancel != nil {
			m.runCancel()
			<-m.done
		}
		m.tokens.Drain()
	})
}

// Run 阻塞运行监督循环直到 ctx 取消。
func (m *Manager) Run(ctx context.Context) {
	redialMin := time.Duration(m.cfg.RedialMinIntervalSec) * time.Second
	var lastStart time.Time

	for {
		if ctx.Err() != nil {
			return
		}

		// 会话失败后的最小重拨间隔（防锤信令/token 端点）。平台策略
		// 拒绝（license 类）在其上按连击数指数退避，封顶 30 分钟。
		wait := redialMin
		if m.consecRejects > 0 {
			wait = rejectionBackoff(redialMin, m.consecRejects)
		}
		if !lastStart.IsZero() && wait > 0 {
			if sleep := wait - time.Since(lastStart); sleep > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(sleep):
				}
			}
		}
		lastStart = time.Now()

		// 商业化授权门控：未获放行则不铸 token 不拨号，周期复查待
		// 授权热生效（API 安装新文档后自动恢复）。
		if !m.gateAllowed() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(watchInterval):
			}
			continue
		}

		jwt, err := m.tokens.Mint(ctx)
		if err != nil {
			m.logger.Warn("token mint failed; backing off", "error", err)
			if wait > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			}
			continue
		}

		err = m.runSession(ctx, jwt)
		if errors.Is(err, errPlatformRejected) {
			m.consecRejects++
			m.logger.Warn("platform license rejection; backing off exponentially",
				"consecutive", m.consecRejects,
				"next_try_in", rejectionBackoff(redialMin, m.consecRejects).String())
			continue
		}
		if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
			m.logger.Warn("p2p session ended", "error", err)
		}
	}
}

// runSession 建立一个信令会话并阻塞监督它，直到需要重拨或 ctx 取消。
func (m *Manager) runSession(ctx context.Context, jwt string) error {
	m.disconnectedAt.Store(0)
	m.resetClients()
	m.resetICESessions()
	m.online.Store(true)

	sess, err := peerDial(ctx, peer.SignalingConfig{
		ServerURL:     m.cfg.ServerURL,
		JWT:           jwt,
		ProductID:     m.cfg.ProductID,
		PeerID:        m.cfg.PeerID,
		Role:          peer.RoleDevice,
		DeviceKeyPath: m.cfg.DeviceKeyPath,
		Logger:        m.logger,
		// 信令重连梯子的逐次重铸：连接活着时 token 过期无害（服务器只
		// 在 hello 验），但任何掉线后重连都会被旧 token 卡死（2026-10-14
		// 生产：服务器重启 → 梯子拿过期 token 死循环 23 分钟直到人工重启）。
		TokenFunc: m.ladderTokenFunc(),
		// 零配置直连端点（#10）："" 保持默认；"auto"/"auto:N" 每次会话
		// 构建跑 UPnP→NAT-PMP→STUN 阶梯（SDK 侧 TTL 缓存 + journal 日
		// 志）；"<ip>:<port>" 直填。通告不可达时 ICE 照常回落 TURN/relay。
		AdvertiseAddr: m.cfg.Advertise,
		// ICE 计时器/MTU（#13）：0 = 库默认（不触碰引擎，行为与历史
		// 逐字节一致）；单项配置时 SDK 侧对其余项回填库默认。
		ICETimeouts: peer.ICETimeouts{
			KeepaliveInterval:   msToDuration(m.cfg.ICEKeepaliveIntervalMS),
			DisconnectedTimeout: msToDuration(m.cfg.ICEDisconnectedTimeoutMS),
			FailedTimeout:       msToDuration(m.cfg.ICEFailedTimeoutMS),
		},
		ICEReceiveMTU: m.cfg.ICEReceiveMTU,
		TransportTimeout: func() time.Duration {
			if m.cfg.TransportTimeoutSec > 0 {
				return time.Duration(m.cfg.TransportTimeoutSec) * time.Second
			}
			return 20 * time.Second
		}(),
		SessionIdleTimeout: time.Duration(m.cfg.SessionIdleTimeoutSec) * time.Second,
		OnDisconnect: func(err error) {
			m.disconnectedAt.Store(time.Now().UnixNano())
			m.online.Store(false)
			m.addEvent("offline", "", errShort(err))
			// 会话中途被平台吊销（4003）：记录并立即结束本会话，交给
			// 外层指数退避——90s resume 等待对策略拒绝没有意义。
			if code, reason, ok := platformLicenseRejection(err); ok {
				m.recordRejection(code, reason, "session")
				m.requestRedial()
			}
		},
		OnReconnect: func() {
			m.disconnectedAt.Store(0)
			m.online.Store(true)
		},
	})
	if err != nil {
		m.online.Store(false)
		if code, reason, ok := platformLicenseRejection(err); ok {
			m.recordRejection(code, reason, "hello")
			return errPlatformRejected
		}
		return fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = sess.Close() }()
	// 成功上线：清拒绝连击；兜底关掉本会话名下所有隧道（正常路径由
	// 各 DataChannel 的 OnClose 自行收尾）。
	m.consecRejects = 0
	defer m.closeClients()
	m.addEvent("online", "", "signaling connected")

	// 设备侧 ICE 遥测（#12）：候选类型/IP 版本/RTT 进状态快照与
	// /metrics。Session 级回调须在首个入站会话建立前挂好；SDK 注入
	// SessionID 后回调；异常静默降级，绝不影响建链。
	sess.OnStats = m.onICEStats

	m.logger.Info("p2p device online",
		"peer_id", m.cfg.PeerID,
		"service", m.cfg.ServiceName,
		"local", m.localAddr,
		"max_clients", m.clientCap().max)

	// 准入闸门（信令层）：超限的 connect_request 直接回 device_busy，
	// WebRTC 拨号不发生。
	sess.SetOnConnectRequest(func(req peer.IncomingConnect) bool {
		lim := m.clientCap()
		if active := m.activeClients(); active >= int64(lim.max) {
			m.rejectedTotal.Add(1)
			m.addEvent("rejected", req.SessionID,
				fmt.Sprintf("device_busy: active=%d cap=%d(%s)", active, lim.max, lim.source))
			m.logger.Warn("p2p client rejected: over capacity",
				"active", active, "cap", lim.max, "cap_source", lim.source)
			return false
		}
		m.addEvent("connect_request", req.SessionID,
			fmt.Sprintf("service=%s active=%d cap=%d", req.ServiceName, m.activeClients(), lim.max))
		return true
	})

	// 每个客户端 DataChannel 一个独立隧道实例，注册全部本机服务。
	// 闸门与这里的复核双保险（connect_request 与 DataChannel 打开之间
	// 存在竞态窗口）；隧道随通道关闭自动回收。
	sess.SetOnDataChannel(func(dc peer.DataChannel) {
		lim := m.clientCap()
		m.clientsMu.Lock()
		if int64(len(m.clients)) >= int64(lim.max) {
			m.clientsMu.Unlock()
			m.rejectedTotal.Add(1)
			m.logger.Warn("p2p client datachannel over capacity; closing",
				"active", len(m.clients), "cap", lim.max)
			_ = dc.Close()
			return
		}
		tun := tunnel.New(peer.NewDataChannelTransport(dc),
			tunnel.WithMaxPayloadBytes(m.cfg.TunnelMaxPayloadBytes))
		if len(dc.E2EEKey) > 0 {
			// 机会式隧道层 E2EE：对端未武装时自动回落明文（混合版本
			// 兼容），relay 转发层零知识。
			if err := tun.EnableE2EE(dc.E2EEKey); err != nil {
				m.logger.Warn("tunnel e2ee arm failed; proceeding plaintext", "error", err)
			}
		}
		m.clients[tun] = struct{}{}
		m.clientsMu.Unlock()

		if dc.OnClose != nil {
			dc.OnClose(func() {
				_ = tun.Close()
				m.dropClient(tun)
			})
		}
		tun.RegisterService(m.cfg.ServiceName, m.localAddr)
		// 出口优先级（#46 v2 设备侧）：每流前 N 字节走高优路径，消除
		// 视频满载时 API 响应在 SCTP 队列里的 HOL。负数=显式关闭。
		tun.EnableEgressPriority(m.cfg.EgressPriorityBytes)
		go tun.Serve()
		m.addEvent("tunnel_up", "",
			fmt.Sprintf("service=%s active=%d", m.cfg.ServiceName, m.activeClients()))
		m.logger.Info("client tunnel up",
			"service", m.cfg.ServiceName,
			"active_clients", m.activeClients())
	})

	// 会话回收观测（SDK e83ad7e+）：远端 session_close 与本地 abandon
	// （ice_restart_failed）两条路径都在资源释放后触发。
	sess.OnSessionClose = func(sessionID, reason string) {
		m.dropICESession(sessionID)
		m.addEvent("closed", sessionID, reason)
		m.logger.Info("p2p session closed", "session_id", sessionID, "reason", reason)
	}

	// 服务端错误消息：鉴权类（token 失效/jti 重放）无法自愈 → 触发换新
	// token 重拨；其余交给断线看门狗。
	sess.OnError = func(code, msg string) {
		if isAuthError(code) {
			m.logger.Warn("auth-class signaling error; redialing with fresh token",
				"code", code, "message", msg)
			m.requestRedial()
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.redial:
			return nil
		case <-time.After(watchInterval):
			// 会话中途授权失效（过期/吊销换钥）：优雅退出本会话，外层
			// Run 循环的门控检查会停拨等待；续期后自动重拨。
			if !m.gateAllowed() {
				m.logger.Warn("license no longer allows p2p; tearing down session",
					"license_state", m.gate.State())
				return nil
			}
			at := m.disconnectedAt.Load()
			if at > 0 && time.Since(time.Unix(0, at)) > resumeGrace {
				m.logger.Warn("signaling lost beyond resume grace; redialing",
					"grace", resumeGrace)
				return nil
			}
		}
	}
}

// clientCapResult 是一次并发上限解析的结果。
type clientCapResult struct {
	max    int
	source string
}

// clientCap 解析当前生效的并发客户端上限：配置与授权文档中所有非零
// 值取更严者；全部为零时兜底 [defaultMaxClients]。
func (m *Manager) clientCap() clientCapResult {
	var found []int
	source := ""
	if m.gate != nil {
		if q := m.gate.MaxClients(); q > 0 {
			found = append(found, q)
			source = "license"
		}
	}
	if m.cfg.MaxClients > 0 {
		found = append(found, m.cfg.MaxClients)
		if source == "license" {
			source = "license+config"
		} else {
			source = "config"
		}
	}
	if len(found) == 0 {
		return clientCapResult{max: defaultMaxClients, source: "default"}
	}
	lim := found[0]
	for _, c := range found[1:] {
		if c < lim {
			lim = c
		}
	}
	return clientCapResult{max: lim, source: source}
}

// activeClients 报告当前活跃客户端隧道数。
func (m *Manager) activeClients() int64 {
	m.clientsMu.Lock()
	defer m.clientsMu.Unlock()
	return int64(len(m.clients))
}

// resetClients 整表换新（新信令会话开始）——迟到的旧通道 OnClose 回调
// 经 dropClient 找不到条目，自然无害。
func (m *Manager) resetClients() {
	m.clientsMu.Lock()
	m.clients = make(map[*tunnel.Tunnel]struct{})
	m.clientsMu.Unlock()
}

// dropClient 移除一个结束的客户端隧道。
func (m *Manager) dropClient(tun *tunnel.Tunnel) {
	m.clientsMu.Lock()
	delete(m.clients, tun)
	n := len(m.clients)
	m.clientsMu.Unlock()
	// 丢帧计数结转：Tunnel 关闭后其计数不可再读，先把终值并入累计。
	if drops := tun.DroppedFrames(); drops > 0 {
		m.closedTunnelDrops.Add(drops)
	}
	m.recorder.SetP2PTunnelDroppedFrames(m.tunnelDroppedTotal())
	m.addEvent("tunnel_down", "", fmt.Sprintf("active=%d", n))
	m.logger.Info("client tunnel down", "active_clients", n, "dropped_frames_total", m.tunnelDroppedTotal())
}

// tunnelDroppedTotal = 已关闭会话结转 + live 会话现场求和（单调）。
func (m *Manager) tunnelDroppedTotal() uint64 {
	total := m.closedTunnelDrops.Load()
	m.clientsMu.Lock()
	for tun := range m.clients {
		total += tun.DroppedFrames()
	}
	m.clientsMu.Unlock()
	return total
}

// msToDuration 毫秒 → Duration（0 保持 0 = 库默认）。
func msToDuration(ms int) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

// ladderTokenFunc 返回信令梯子的逐次铸币闭包（独立方法以便测试）。Mint
// 自带防锤间隔：撞上 ErrMintBackoff 时梯子该次尝试顺延，由其退避节流。
func (m *Manager) ladderTokenFunc() func() (string, error) {
	return func() (string, error) {
		mctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return m.tokens.Mint(mctx)
	}
}

// closeClients 兜底关闭当前会话名下全部隧道（信令会话终结路径）。
// 并行关闭：单个隧道的排水可能吃到出口调度器的 stall 预算（对端不排空
// 时至多 45s），串行 ×N 会把 Manager 的重拨/关停拖过 systemd 的 90s
// 停机超时（2026-10-14 生产：SIGKILL 收场）。
func (m *Manager) closeClients() {
	m.clientsMu.Lock()
	tunnels := make([]*tunnel.Tunnel, 0, len(m.clients))
	for tun := range m.clients {
		tunnels = append(tunnels, tun)
	}
	m.clients = make(map[*tunnel.Tunnel]struct{})
	m.clientsMu.Unlock()
	var wg sync.WaitGroup
	for _, tun := range tunnels {
		wg.Add(1)
		go func(t *tunnel.Tunnel) {
			defer wg.Done()
			_ = t.Close()
		}(tun)
	}
	wg.Wait()
}

// recordRejection 记录平台策略拒绝（状态 API 呈现「平台授权被拒」）。
func (m *Manager) recordRejection(code int, reason, source string) {
	m.lastRejection.Store(&Rejection{
		Code:   code,
		Reason: reason,
		Source: source,
		At:     time.Now(),
	})
	m.addEvent("platform_rejected", "",
		fmt.Sprintf("code=%d reason=%s source=%s", code, reason, source))
}

// errShort 取错误串首 80 字符（事件明细防刷屏）。
func errShort(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 80 {
		return s[:80]
	}
	return s
}

// addEvent 追加一条诊断事件（环形上限 [maxEvents]），并外流给 OnEvent
// 钩子（锁外调用，见字段注释）。
func (m *Manager) addEvent(typ, sessionID, detail string) {
	ev := Event{At: time.Now(), Type: typ, SessionID: sessionID, Detail: detail}
	m.eventsMu.Lock()
	m.events = append(m.events, ev)
	if len(m.events) > maxEvents {
		m.events = m.events[len(m.events)-maxEvents:]
	}
	m.eventsMu.Unlock()
	if m.OnEvent != nil {
		m.OnEvent(ev)
	}
}

// RecentEvents 返回诊断事件副本（旧→新）。
func (m *Manager) RecentEvents() []Event {
	m.eventsMu.Lock()
	defer m.eventsMu.Unlock()
	out := make([]Event, len(m.events))
	copy(out, m.events)
	return out
}

// onICEStats 吸收 SDK 的 ICE/DataChannel 快照（OnStats 回调，每会话每
// 5s 一次）。无可选对（建链早期/relay 传输）的快照跳过——不覆盖既有
// 条目。任何异常只丢遥测，绝不影响会话（#12 验收：静默降级）。
func (m *Manager) onICEStats(snap peer.StatsSnapshot) {
	if snap.SessionID == "" || snap.ICELocalCandidateType == "" {
		return
	}
	st := ICEStats{
		SessionID:     snap.SessionID,
		LocalType:     snap.ICELocalCandidateType,
		RemoteType:    snap.ICERemoteCandidateType,
		LocalAddr:     snap.ICELocalCandidateAddress,
		RemoteAddr:    snap.ICERemoteCandidateAddress,
		IPVersion:     snap.ICEIPVersion,
		RelayProtocol: snap.ICERelayProtocol,
		RTTMs:         snap.ICERTTSeconds * 1000,
		AvailableKbps: snap.ICEAvailableBitrateBits / 1000,
		BytesSent:     snap.DataChannelBytesSent,
		BytesReceived: snap.DataChannelBytesReceived,
		At:            time.Now(),
	}
	m.iceMu.Lock()
	m.ice[snap.SessionID] = st
	counts := make(map[string]int, len(m.ice))
	for _, s := range m.ice {
		counts[s.LocalType]++
	}
	m.iceMu.Unlock()

	if m.recorder != nil {
		m.recorder.ObserveP2PICERTT(st.LocalType, st.RTTMs)
		for typ, n := range counts {
			m.recorder.SetP2PICESessions(typ, n)
			m.recorder.SetP2PTunnelDroppedFrames(m.tunnelDroppedTotal())
		}
	}
}

// dropICESession 修剪一条已关闭会话的遥测条目（幂等）。被清空的类型
// 必须下发 0（series 删除）——只更新剩余类型会让 gauge 滞留旧值。
func (m *Manager) dropICESession(sessionID string) {
	m.iceMu.Lock()
	prev, ok := m.ice[sessionID]
	delete(m.ice, sessionID)
	seen := map[string]struct{}{}
	counts := map[string]int{}
	if ok && prev.LocalType != "" {
		seen[prev.LocalType] = struct{}{}
	}
	for _, s := range m.ice {
		seen[s.LocalType] = struct{}{}
		counts[s.LocalType]++
	}
	m.iceMu.Unlock()
	if !ok {
		return
	}
	if m.recorder != nil {
		for typ := range seen {
			m.recorder.SetP2PICESessions(typ, counts[typ])
			m.recorder.SetP2PTunnelDroppedFrames(m.tunnelDroppedTotal())
		}
	}
}

// resetICESessions 整表换新（新信令会话开始——与 resetClients 同节拍）。
func (m *Manager) resetICESessions() {
	m.iceMu.Lock()
	m.ice = make(map[string]ICEStats)
	m.iceMu.Unlock()
}

// iceSnapshot 返回 ICE 遥测副本（空表返回 nil——JSON omitempty 不出键）。
func (m *Manager) iceSnapshot() map[string]ICEStats {
	m.iceMu.Lock()
	defer m.iceMu.Unlock()
	if len(m.ice) == 0 {
		return nil
	}
	out := make(map[string]ICEStats, len(m.ice))
	for k, v := range m.ice {
		out[k] = v
	}
	return out
}

// Status 返回 P2P 链路诊断快照。disabled 时仅报 enabled=false。
func (m *Manager) Status() StatusSnapshot {
	lim := m.clientCap()
	snap := StatusSnapshot{
		Enabled:       m.cfg.Enabled,
		Online:        m.online.Load(),
		PeerID:        m.cfg.PeerID,
		ProductID:     m.cfg.ProductID,
		ServiceName:   m.cfg.ServiceName,
		ServerURL:     m.cfg.ServerURL,
		ActiveClients: int(m.activeClients()),
		MaxClients:    lim.max,
		CapSource:     lim.source,
		RejectedTotal: m.rejectedTotal.Load(),
		ICESessions:   m.iceSnapshot(),
		// pion supportedNetworkTypes() 仅 UDP4/UDP6（TCP 候选上游未支
		// 持）——如实上报，排障时先排除「UDP 被整形只剩 relay」。
		ICEUDPOnly:          true,
		TunnelDroppedFrames: m.tunnelDroppedTotal(),
	}
	if m.gate != nil {
		snap.License = &GateStatus{Allowed: m.gate.Allowed(), State: m.gate.State()}
	}
	if r := m.lastRejection.Load(); r != nil {
		snap.LastRejection = r
	}
	snap.RecentEvents = m.RecentEvents()
	return snap
}

// gateAllowed 报告授权门控是否放行（无门控恒放行）。状态变化时记一条
// 日志（含恢复），稳态不刷屏。授权强制时文档必须授予 "p2p" 能力
// （features 空 = 全能，兼容存量文档）。
func (m *Manager) gateAllowed() bool {
	if m.gate == nil {
		return true
	}
	allowed := m.gate.Allowed()
	state := m.gate.State()
	if allowed && !m.gate.FeatureAllowed("p2p") {
		allowed = false
		state += ":no-p2p-feature"
	}
	if allowed {
		state = "allowed:" + state
	} else {
		state = "blocked:" + state
	}
	if state != m.lastGateState {
		m.lastGateState = state
		if allowed {
			m.logger.Info("p2p license gate allowing traffic", "license_state", m.gate.State())
		} else {
			m.logger.Warn("p2p license gate blocking; not dialing", "license_state", m.gate.State())
		}
	}
	return allowed
}

func (m *Manager) requestRedial() {
	select {
	case m.redial <- struct{}{}:
	default:
	}
}

// rejectionBackoff 按连击数对基础间隔做指数退避（n=1 → 基础间隔）。
func rejectionBackoff(base time.Duration, n int) time.Duration {
	d := base
	for i := 1; i < n; i++ {
		d *= 2
		if d >= maxRejectionBackoff {
			return maxRejectionBackoff
		}
	}
	if d > maxRejectionBackoff {
		return maxRejectionBackoff
	}
	return d
}

// platformLicenseRejection 判定错误是否平台侧 license 策略拒绝：
// 4003 = 会话中吊销（license watcher 专用码）；1008 = 握手期策略拒绝，
// 仅当 close reason 指明 license 类时认定（1008 也用于其它握手拒绝）。
// 兼容 hello 阶段的 error 消息形态（"server error: license_required: …"）。
func platformLicenseRejection(err error) (code int, reason string, ok bool) {
	if err == nil {
		return 0, "", false
	}
	if c, text, isClose := peer.CloseInfo(err); isClose {
		if c == 4003 {
			r := text
			if r == "" {
				r = "license_revoked"
			}
			return c, r, true
		}
		if c == 1008 && strings.HasPrefix(text, "license") {
			return c, text, true
		}
	}
	msg := err.Error()
	for _, marker := range []string{"license_required", "license_expired"} {
		if strings.Contains(msg, marker) {
			return 1008, marker, true
		}
	}
	return 0, "", false
}

// isAuthError 判定服务端错误码是否鉴权类（不可自愈，需换 token）。
func isAuthError(code string) bool {
	switch code {
	case "invalid_token", "unauthorized", "token_expired", "jti_replayed", "auth_failed":
		return true
	}
	return false
}
