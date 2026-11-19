package p2p

// Observation and transport-policy helpers on Manager: client/tunnel counters,
// NAT and node-selection snapshots, the tunnel echo sampler and the pinned /
// auto-relay transport decisions. Split out of manager.go — none of these own
// the session lifecycle.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"

	peer "github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/tunnel"
)

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

// onNATObservation 消费 SDK 的 NAT 行为观测：Status/metrics/事件流三面落。
func (m *Manager) onNATObservation(o peer.NatObservation) {
	m.natObs.Store(&o)
	m.addEvent("nat_observation", "",
		fmt.Sprintf("class=%s stun=%d dual_stack=%v prefer_relay=%v", o.Class, o.Responsive, o.DualStack, peer.PreferRelay(o.Class)))
	m.recorder.SetP2PNATClass(string(o.Class))
	m.recorder.SetP2PNATDualStack(o.DualStack)
	m.logger.Info("NAT behavior observed",
		"class", string(o.Class), "stun_responsive", o.Responsive,
		"local_addr", o.LocalAddr, "dual_stack", o.DualStack,
		"prefer_relay", peer.PreferRelay(o.Class))
}

// natSnapshot 把 SDK 观测转成对外视图（nil 当未观测）。
func (m *Manager) natSnapshot() *NATObservation {
	o := m.natObs.Load()
	if o == nil {
		return nil
	}
	return &NATObservation{
		Class:         string(o.Class),
		StunReachable: o.StunReachable,
		Responsive:    o.Responsive,
		LocalAddr:     o.LocalAddr,
		PreferRelay:   peer.PreferRelay(o.Class),
		DualStack:     o.DualStack,
		At:            o.At,
	}
}

// relayStatusPtr 取地址小工具（Status 恒携带 relay 块）。
func relayStatusPtr(r RelayCircuit) *RelayCircuit { return &r }

// NodeSelectionView 是就近选点的对外视图（peer.NodeSelection）。
type NodeSelectionView struct {
	URL   string    `json:"url"`
	RTTMs int64     `json:"rtt_ms"`
	At    time.Time `json:"at"`
}

// nodeSelectionSnapshot 汇出最近一次选点（未配置/未选出 = nil）。
func (m *Manager) nodeSelectionSnapshot() *NodeSelectionView {
	if m.cfg.NodesURL == "" {
		return nil
	}
	sel, ok := peer.LastNodeSelection(m.cfg.NodesURL)
	if !ok {
		return nil
	}
	return &NodeSelectionView{URL: sel.URL, RTTMs: sel.RTTMs, At: sel.At}
}

// tunnelEchoSnapshot 汇出最近一次 T2 探测（nil 当无客户端隧道）。
func (m *Manager) tunnelEchoSnapshot() *TunnelEchoObs {
	return m.echoObs.Load()
}

// tunnelEchoInterval 是 T2 双车道探测周期（30s 与 App 质量上报同窗）。
const tunnelEchoInterval = 30 * time.Second

// startTunnelEchoSampler 周期探测一条客户端隧道的 T2 单跳 RTT（#87）：
// 普通车道走业务数据同款排队路径，优先车道反映控制面体感；回显按
// MaxPayloadBytes 补齐——测的就是数据帧尺寸的排队，不是小包特供通道。
// 对端不回显（旧 App）连续 3 次未应答 → 标记 unsupported 退出（缺答是
// 有效答案，不是故障）；任何发送失败（隧道关闭）也退出。
func (m *Manager) startTunnelEchoSampler(tun *tunnel.Tunnel) (stop func()) {
	stopCh := make(chan struct{})
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			select {
			case <-stopCh:
				cancel()
			case <-ctx.Done():
			}
		}()
		probe := func() bool { // false = stop sampling
			want := tunnel.MaxPayloadBytes
			normal, errN := tun.ProbeEcho(ctx, want, false)
			if errN != nil {
				if errors.Is(errN, tunnel.ErrEchoUnsupported) {
					m.echoObs.Store(&TunnelEchoObs{WantBytes: want, Unsupported: true, At: time.Now()})
					m.logger.Info("tunnel echo unsupported by peer — T2 unmeasurable", "want_bytes", want)
					return false
				}
				if strings.Contains(errN.Error(), "echo timed out") {
					return true // miss: keep sampling, give-up logic is in ProbeEcho
				}
				return false // send failure: tunnel closing/closed
			}
			prio, errP := tun.ProbeEcho(ctx, want, true)
			if errP != nil {
				return false
			}
			m.echoObs.Store(&TunnelEchoObs{
				NormalMS:  normal.RTT.Milliseconds(),
				PrioMS:    prio.RTT.Milliseconds(),
				LaneUsed:  prio.LanePriority,
				WantBytes: want,
				At:        time.Now(),
			})
			return true
		}
		if !probe() {
			return
		}
		ticker := time.NewTicker(tunnelEchoInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				if !probe() {
					return
				}
			}
		}
	}()
	return sync.OnceFunc(func() { close(stopCh) })
}

// transportDecision 汇出当前仲裁钉选及其来源（状态面可观测，#18）。
// 纯 true 恒钉；auto 读最近一次 NAT 观测（v4 对称且无全局 IPv6 才钉）；
// 其余（含缺省）保持直连优先——与历史行为一致。
func (m *Manager) transportDecision() (pinned bool, reason string) {
	pinned, reason = m.transportPin()
	// relay 熔断冷却期（#23 P1）：钉 relay 的决策让位直连仲裁——relay
	// 死不能拖死整线；冷却结束探测在途时恢复钉选语义（如实反映）。
	if pinned && m.relay.cooldownActive() {
		return false, "relay_cooldown"
	}
	return pinned, reason
}

// transportPin 是不含熔断让位的原始钉选判定（#18 矩阵）。
func (m *Manager) transportPin() (pinned bool, reason string) {
	switch m.cfg.ForceRelay {
	case config.ForceRelayAlways:
		return true, "force_relay"
	case config.ForceRelayAuto:
		if o := m.natObs.Load(); o != nil && o.Class == peer.NATSymmetric && !o.DualStack {
			return true, "nat_symmetric"
		}
	}
	return false, "default"
}
