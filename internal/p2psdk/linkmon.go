package peer

import (
	"os"
	"strconv"
	"sync"
	"time"
)

// Link-quality fast-switch monitor (#134⑤, research doc 11 §1.1).
//
// The stock quality machinery is failure-shaped: ICE restarts wait for
// Disconnected/Failed transitions and the pathological-pair eviction needs
// ~15s of sustained bad RTT (#82), while pion's automatic renomination —
// the in-band pair switcher — re-evaluates every 3s and only compares ICE
// RTT. What none of them see is PACKET LOSS: a pair can keep its ICE RTT
// low while the media on top of it collapses (downstream QoS policers shape
// traffic, not ICMP). The monitor closes that gap:
//
//   - input A (primary): the media stack's own loss measurement, fed in via
//     Session.ReportLinkQuality by the embedder (the transport SDK cannot
//     see decoder-level loss);
//   - input B (fallback, no fresh loss feed): ICE RTT spikes from the
//     stats tick.
//
// Two consecutive degraded windows escalate once per cooldown: the
// OnLinkDegraded event surfaces to the application, and the offerer runs a
// quality-triggered ICE restart — re-running the whole check list nominates
// the best available pair instead of waiting for renomination cycles that
// never compare loss. Candidate preheat is pion-native with renomination
// enabled (keepAliveCandidatesForRenomination pings every non-failed pair
// to keep RTT fresh), and the relay fallback is prewarmed at session level
// (connected in Dial, adopted in ~ms) — both sides of "备选对预热" are
// therefore covered; this file only adds the loss-aware trigger.
//
// Escalation reuses attemptICERestart (offerer-gated, failure-accounted):
// restarts are anti-glare offerer-only by design, so the answerer side
// fires the event and lets the controlling side act. Restarts bounded by
// cooldown + per-session cap; MIBEE_LINKMON_WINDOWS=0 disables the monitor
// entirely (default ON).

// Link monitor defaults. Windows are the stats tick (5s) apart, so 2
// windows ≈ 10s of sustained degradation before escalation — versus the
// 15s eviction and the blind 3s renomination loop.
const (
	linkMonLossDefault      = 20.0             // %, issue text: 丢包 >20%
	linkMonWindowsDefault   = 2                // consecutive degraded windows
	linkMonRTTDefaultMs     = 400.0            // fallback RTT spike threshold
	linkMonCooldownDefault  = 30 * time.Second // between escalations
	linkMonMaxEscalations   = 2                // per session (then event-only)
	linkLossFreshnessWindow = 10 * time.Second // app-fed sample validity
)

// linkMonCfg is the parsed (env-overridable) monitor configuration.
type linkMonCfg struct {
	lossPct        float64
	windows        int
	rttMs          float64
	cooldown       time.Duration
	maxEscalations int
}

var (
	linkMonOnce sync.Once
	linkMonCfgV *linkMonCfg // nil after MIBEE_LINKMON_WINDOWS=0 (monitor off)
)

// linkMonConfig parses the MIBEE_LINKMON_* knobs once per process. nil
// return = monitor disabled.
func linkMonConfig() *linkMonCfg {
	linkMonOnce.Do(func() {
		cfg := &linkMonCfg{
			lossPct:        linkMonLossDefault,
			windows:        linkMonWindowsDefault,
			rttMs:          linkMonRTTDefaultMs,
			cooldown:       linkMonCooldownDefault,
			maxEscalations: linkMonMaxEscalations,
		}
		if v, ok := envFloat("MIBEE_LINKMON_LOSS_PCT"); ok {
			cfg.lossPct = v
		}
		if v, ok := envInt("MIBEE_LINKMON_WINDOWS"); ok {
			cfg.windows = v
		}
		if v, ok := envInt("MIBEE_LINKMON_RTT_MS"); ok && v > 0 {
			cfg.rttMs = float64(v)
		}
		if d := envDurationMs("MIBEE_LINKMON_COOLDOWN_MS"); d >= 0 {
			cfg.cooldown = d
		}
		if v, ok := envInt("MIBEE_LINKMON_MAX_ESCALATIONS"); ok {
			cfg.maxEscalations = v
		}
		if cfg.windows <= 0 {
			cfg = nil // explicit off
		}
		linkMonCfgV = cfg
	})
	return linkMonCfgV
}

// envFloat parses a float env var; ok=false when unset or invalid.
func envFloat(name string) (float64, bool) {
	v := os.Getenv(name)
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return 0, false
	}
	return f, true
}

// envInt parses a non-negative int env var; ok=false when unset or invalid.
func envInt(name string) (int, bool) {
	v := os.Getenv(name)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// linkQuality is one app-fed loss sample (ReportLinkQuality). Pointer
// bytes first (fieldalignment): the atomic slot stores this struct by
// pointer, but the struct layout itself is checked too.
type linkQuality struct {
	at      time.Time
	lossPct float64
}

// linkDegraded evaluates one window: a fresh app-fed loss sample is the
// primary signal (a healthy loss reading short-circuits — RTT spikes under
// queuing are not loss); without one, an ICE RTT above the threshold
// degrades the window.
func (p *p2pSession) linkDegraded(cfg *linkMonCfg, snap StatsSnapshot) (bool, string) {
	if lq := p.linkLoss.Load(); lq != nil && time.Since(lq.at) <= linkLossFreshnessWindow {
		if lq.lossPct > cfg.lossPct {
			return true, "loss"
		}
		return false, ""
	}
	if snap.ICERTTSeconds > 0 && snap.ICERTTSeconds*1000 > cfg.rttMs {
		return true, "rtt"
	}
	return false, ""
}

// checkLinkQuality runs one monitor window off the stats tick. Escalation:
// OnLinkDegraded event + offerer-side quality ICE restart, bounded by
// cooldown and a per-session cap. Playback-policy sessions are excluded —
// #136 owns their fate (a degraded playback session must degrade with a
// notice, not restart).
func (p *p2pSession) checkLinkQuality(snap StatsSnapshot) {
	cfg := linkMonConfig()
	if cfg == nil {
		return
	}
	if p.relayActiveSnapshot() || p.playbackTunnelOnlyArmed() {
		return
	}

	degraded, cause := p.linkDegraded(cfg, snap)
	p.mu.Lock()
	if !degraded {
		p.linkStreak = 0
		p.mu.Unlock()
		return
	}
	p.linkStreak++
	if p.linkStreak < cfg.windows {
		p.mu.Unlock()
		return
	}
	p.linkStreak = 0
	if p.linkEscalations >= cfg.maxEscalations ||
		(!p.linkLastEscalation.IsZero() && time.Since(p.linkLastEscalation) < cfg.cooldown) {
		p.mu.Unlock()
		return
	}
	p.linkEscalations++
	p.linkLastEscalation = time.Now()
	p.mu.Unlock()

	// Diagnostics: only the live signal of the trigger is logged (fresh
	// loss sample vs RTT fallback), keeping every slog call a clean
	// key-value sequence.
	rttMs := int(snap.ICERTTSeconds * 1000)
	if loss, ok := p.latestLossPct(); ok {
		p.log().Warn("link quality degraded — fast-switch escalation (#134⑤)",
			causeKey, cause, lossPctKey, loss, rttMsKey, rttMs,
			cooldownKey, cfg.cooldown.String())
	} else {
		p.log().Warn("link quality degraded — fast-switch escalation (#134⑤)",
			causeKey, cause, rttMsKey, rttMs, cooldownKey, cfg.cooldown.String())
	}
	if cb := p.owner.linkDegradedCallback(); cb != nil {
		cb(p.sessionID, cause)
	}
	// attemptICERestart is offerer-gated internally (anti-glare) and
	// failure-accounted — a restart that gets no answer counts toward the
	// same abandonment cap as the failure ladder, which is the right
	// semantics for a peer that has gone silent.
	p.attemptICERestart()
}

// latestLossPct reads the freshest app-fed loss sample for diagnostics
// (ok=false when no sample exists).
func (p *p2pSession) latestLossPct() (float64, bool) {
	if lq := p.linkLoss.Load(); lq != nil && time.Since(lq.at) <= linkLossFreshnessWindow {
		return lq.lossPct, true
	}
	return 0, false
}
