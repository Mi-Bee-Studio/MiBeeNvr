package app

// p2p_service.go adapts the internal/p2p device-role Manager onto the App
// service lifecycle. The App registry is start-once, so hot reconfiguration
// (Settings → P2P PUT，免 ssh 改 yaml）goes through this Agent: it owns the
// current Manager and can stop/respawn it under a new config at runtime.

import (
	"context"
	"log/slog"
	"sync"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2p"
)

// p2pAgent 持有当前 p2p.Manager 并支持运行中换配置（Apply）。
// 停用态（enabled=false）是一个合法的常驻状态——服务恒注册，保证
// PUT /api/settings/p2p 打开开关时无需重启即可拉起链路。
type p2pAgent struct {
	mu             sync.Mutex
	cfg            config.P2PConfig
	listen         string // server.listen，供 localAddr 推导
	tokenStatePath string
	logger         *slog.Logger
	gate           p2p.LicenseGate
	viewerBinder   func(*p2p.Manager) // 分享观看者核验挂接（可空）
	onEvent        func(p2p.Event)    // 链路事件外流（仪表盘连接记录；可空）

	parentCtx context.Context
	started   bool
	mgr       *p2p.Manager
}

// Name 实现 app.Service。
func (a *p2pAgent) Name() string { return "p2p" }

// Start 记录父 ctx 并按当前配置拉起（停用态为 no-op）。
func (a *p2pAgent) Start(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.parentCtx = ctx
	a.started = true
	a.spawnLocked()
	return nil
}

// Stop 终结当前 Manager（幂等）。
func (a *p2pAgent) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.mgr != nil {
		a.mgr.Stop()
		a.mgr = nil
	}
	a.started = false
	return nil
}

// Apply 热应用新配置：停掉当前 Manager，按需以新配置重建。未 Start（如
// 启动早期/测试）时只暂存配置。
func (a *p2pAgent) Apply(cfg config.P2PConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg = cfg
	if a.mgr != nil {
		a.mgr.Stop()
		a.mgr = nil
	}
	if a.started {
		a.spawnLocked()
	}
}

// Status 返回诊断快照；停用/未拉起时返回仅含配置要点的最小快照。
func (a *p2pAgent) Status() p2p.StatusSnapshot {
	a.mu.Lock()
	mgr, cfg := a.mgr, a.cfg
	a.mu.Unlock()
	if mgr != nil {
		return mgr.Status()
	}
	return p2p.StatusSnapshot{
		Enabled:     cfg.Enabled,
		PeerID:      cfg.PeerID,
		ProductID:   cfg.ProductID,
		ServiceName: cfg.ServiceName,
		ServerURL:   cfg.ServerURL,
	}
}

// spawnLocked 用当前配置构造并启动 Manager（调用方持锁；停用态 no-op）。
func (a *p2pAgent) spawnLocked() {
	if !a.cfg.Enabled {
		return
	}
	a.mgr = p2p.New(a.cfg, a.cfg.LocalAddrFromListen(a.listen),
		a.tokenStatePath, a.logger, a.gate)
	// 连接记录外流（Start 前设置，Manager 内无锁竞争）。
	a.mgr.OnEvent = a.onEvent
	a.mgr.Start(a.parentCtx)
	// 分享执行链：Manager 存活即挂上观看者核验（设备身份→平台判定）。
	if a.viewerBinder != nil {
		a.viewerBinder(a.mgr)
	}
}

// registerP2PService wires the P2P device agent as an App service. 恒注册
// （enabled=false 时是 no-op 常驻），启用开关经设置 API 热生效。
func registerP2PService(a *App, deps *appDeps) error {
	agent := &p2pAgent{
		cfg:            deps.cfg.P2P,
		listen:         deps.cfg.Server.Listen,
		tokenStatePath: p2p.StatePathFor(deps.configPath),
		logger:         slog.Default(),
		gate:           deps.licenseMgr,
		viewerBinder:   deps.viewerLoginBinder,
		onEvent:        deps.handler.RecordP2pLinkEvent,
	}
	deps.handler.SetP2PAgent(agent)
	return a.Register(agent)
}
