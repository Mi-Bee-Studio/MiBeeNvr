package p2p

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	peer "github.com/Mi-Bee-Studio/MiBeeP2PServer/sdk/go"
	"github.com/Mi-Bee-Studio/MiBeeP2PServer/sdk/go/tunnel"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// resumeGrace 覆盖 SDK 的 60s resume 窗口 + 重握手余量：断线后持续超过
// 该时长仍未恢复（OnReconnect 未到）即判定会话死亡，换新 token 重拨。
const resumeGrace = 90 * time.Second

// watchInterval 是监督循环的检查周期。
const watchInterval = 15 * time.Second

// Manager 监督 P2P 设备会话的完整生命周期：铸 token → 信令上线 → 每个
// 接入的客户端 DataChannel 上注册本机服务隧道 → 断线超过 resume 窗口
// 换新 token 重拨。P2P 永不阻塞主服务：一切失败只记日志 + 退避重试。
type Manager struct {
	cfg       config.P2PConfig
	localAddr string
	tokens    *TokenManager
	logger    *slog.Logger

	runCtx    context.Context
	runCancel context.CancelFunc
	done      chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once

	// 会话健康标记（信令 OnDisconnect/OnReconnect 驱动）。
	disconnectedAt atomic.Int64 // unix nano; 0 = 从未断线或已恢复
	redial         chan struct{}
	online         atomic.Bool
}

// New 构造 Manager。localAddr 是注册到隧道的本机服务地址（HTTP API）。
func New(cfg config.P2PConfig, localAddr, tokenStatePath string, logger *slog.Logger) *Manager {
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
		redial:    make(chan struct{}, 1),
	}
}

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

// Stop 终结监督循环并等待退出（幂等）。
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		if m.runCancel != nil {
			m.runCancel()
			<-m.done
		}
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

		// 会话失败后的最小重拨间隔（防锤信令/token 端点）。
		if !lastStart.IsZero() {
			if wait := redialMin - time.Since(lastStart); wait > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			}
		}
		lastStart = time.Now()

		jwt, err := m.tokens.Mint(ctx)
		if err != nil {
			m.logger.Warn("token mint failed; backing off", "error", err)
			if wait := redialMin; wait > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			}
			continue
		}

		if err := m.runSession(ctx, jwt); err != nil && ctx.Err() == nil {
			m.logger.Warn("p2p session ended", "error", err)
		}
	}
}

// runSession 建立一个信令会话并阻塞监督它，直到需要重拨或 ctx 取消。
func (m *Manager) runSession(ctx context.Context, jwt string) error {
	m.disconnectedAt.Store(0)
	m.online.Store(true)

	sess, err := peer.Dial(ctx, peer.SignalingConfig{
		ServerURL: m.cfg.ServerURL,
		JWT:       jwt,
		ProductID: m.cfg.ProductID,
		PeerID:    m.cfg.PeerID,
		Role:      peer.RoleDevice,
		Logger:    m.logger,
		TransportTimeout: func() time.Duration {
			if m.cfg.TransportTimeoutSec > 0 {
				return time.Duration(m.cfg.TransportTimeoutSec) * time.Second
			}
			return 20 * time.Second
		}(),
		OnDisconnect: func(error) {
			m.disconnectedAt.Store(time.Now().UnixNano())
			m.online.Store(false)
		},
		OnReconnect: func() {
			m.disconnectedAt.Store(0)
			m.online.Store(true)
		},
	})
	if err != nil {
		m.online.Store(false)
		return fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = sess.Close() }()
	m.logger.Info("p2p device online",
		"peer_id", m.cfg.PeerID,
		"service", m.cfg.ServiceName,
		"local", m.localAddr)

	// 每个客户端 DataChannel 一个独立隧道实例，注册全部本机服务。
	sess.OnDataChannel = func(dc peer.DataChannel) {
		tun := tunnel.New(peer.NewDataChannelTransport(dc))
		tun.RegisterService(m.cfg.ServiceName, m.localAddr)
		go tun.Serve()
		m.logger.Info("client tunnel up", "service", m.cfg.ServiceName)
	}

	// 会话回收观测（SDK e83ad7e+）：远端 session_close 与本地 abandon
	// （ice_restart_failed）两条路径都在资源释放后触发。
	sess.OnSessionClose = func(sessionID, reason string) {
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
			at := m.disconnectedAt.Load()
			if at > 0 && time.Since(time.Unix(0, at)) > resumeGrace {
				m.logger.Warn("signaling lost beyond resume grace; redialing",
					"grace", resumeGrace)
				return nil
			}
		}
	}
}

func (m *Manager) requestRedial() {
	select {
	case m.redial <- struct{}{}:
	default:
	}
}

// isAuthError 判定服务端错误码是否鉴权类（不可自愈，需换 token）。
func isAuthError(code string) bool {
	switch code {
	case "invalid_token", "unauthorized", "token_expired", "jti_replayed", "auth_failed":
		return true
	}
	return false
}
