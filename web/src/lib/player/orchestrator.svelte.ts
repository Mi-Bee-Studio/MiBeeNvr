/**
 * Player Orchestrator — adaptive protocol selection per camera.
 *
 * The orchestrator owns, for each registered camera, an ordered candidate
 * chain (built by {@link buildCandidateChain}) and a small state machine that
 * reacts to {@link HealthState} reports from the active player adapter:
 *
 *   failed     → demote immediately to the next chain entry (degrade)
 *   degraded   → demote after DEGRADE_THRESHOLD_MS (debounce; transient hiccups
 *                like a brief rebuffer should NOT trigger a protocol switch)
 *   ok         → after UPGRADE_STABLE_MS of stability, and only on an
 *                environment-change trigger (tab visible / explicit request),
 *                attempt to promote back to a lower-latency entry (upgrade).
 *                If the promotion doesn't reach `ok` within UPGRADE_PROBE_MS it
 *                reverts and cools that entry for ENTRY_COOLDOWN_MS (anti-flap).
 *
 * Visibility (tab hidden/visible) is handled HERE, not in each player, so we
 * delete the per-player visibility `$effect` that caused the WS reconnect storm
 * (a reactive loop: connect→setState(loading)→effect re-run→connect…).
 *
 * The orchestrator does NOT mount players itself — it exposes the *decision*
 * (`activeMode`) reactively; the {@link CameraPlayer} component reads it and
 * renders the matching player. This keeps the orchestrator DOM-free and unit-
 * testable (it never touches Svelte mount/unmount APIs).
 *
 * Anti-flap invariants:
 *  - A user override pins the chain to one entry; no auto-degrade/upgrade.
 *  - Promotions only happen on an explicit trigger (never on a timer alone) so
 *    a flaky network can't thrash between two protocols indefinitely.
 *  - A reverted promotion cools the offending entry for ENTRY_COOLDOWN_MS.
 */

import type { ReconnectCoordinator } from '$lib/reconnect-coordinator.svelte';
import { createReconnectCoordinator } from '$lib/reconnect-coordinator.svelte';
import type { Camera } from '$lib/api';
import {
  buildCandidateChain,
  resolveEncoding,
  type Candidate,
  type CameraMode,
  type BrowserCaps,
  type ProtocolsResponse,
} from '$lib/stream-selection';
import { getCaps } from './capabilities-cache';
import { clearCameraProtocolOverride } from '$lib/preferences';
import { health, type HealthState } from './health';

// ─── Timing constants (tuned for a home NVR; not user-facing) ───────────────

/** Demote only after this much continuous `degraded` health (debounce hiccups). */
const DEGRADE_THRESHOLD_MS = 8000;
/** Require this much continuous `ok` before a promotion is even considered. */
const UPGRADE_STABLE_MS = 30000;
/** After a promotion, require `ok` within this window or revert (anti-flap). */
const UPGRADE_PROBE_MS = 5000;
/** A reverted promotion cools the offending entry for this long. */
const ENTRY_COOLDOWN_MS = 60000;

// ─── Types ──────────────────────────────────────────────────────────────────

/** Inputs needed to (re)build a camera's candidate chain. */
export interface CameraRegistration {
  camera: Camera;
  resp: ProtocolsResponse | null;
  caps: BrowserCaps;
  override: string | null;
  isHlsCapable: boolean;
  isUnsupported: boolean;
}

/** The orchestrator's reactive view of one camera. Read by CameraPlayer. */
export interface CameraSlot {
  cameraId: string;
  /** The full candidate chain (for the upgrade badge / debug). */
  chain: Candidate[];
  /** Index into `chain` of the currently-active mode. Reactive. */
  activeIndex: number;
  /** Latest health reported by the active adapter. Reactive. */
  health: HealthState;
  /** True when the chain is pinned by a user override (no auto-adapt). */
  pinned: boolean;
}

/** Callback fired when the active mode for a camera changes (degrade/upgrade). */
export type ModeChangeCallback = (cameraId: string, from: CameraMode, to: CameraMode, reason: string) => void;

// ─── Orchestrator factory ───────────────────────────────────────────────────

export interface PlayerOrchestrator {
  /** Register (or refresh) a camera's chain. Safe to call repeatedly. */
  registerCamera(reg: CameraRegistration): void;
  /** Drop a camera (grid cell removed). */
  unregisterCamera(cameraId: string): void;
  /** Adapter reports the active player's health. Drives degrade/upgrade. */
  reportHealth(cameraId: string, h: HealthState): void;
  /** Pin a user override (ProtocolSwitcher manual selection). */
  setOverride(cameraId: string, protocol: string | null): void;
  /** Explicit "try the low-latency mode again" trigger (button / tab-visible). */
  requestUpgrade(cameraId: string): void;
  /** Signal tab visibility change — pauses/resumes via adapters indirectly. */
  setTabVisible(visible: boolean): void;
  /** The mode CameraPlayer should render right now (reactive read). */
  activeMode(cameraId: string): CameraMode | null;
  /**
   * The recorder-probed codec for a camera (e.g. 'h265'), authoritative over
   * the possibly-stale DB `camera.encoding`. CameraPlayer feeds this to the
   * player components so they configure the correct decoder (H.264 vs H.265)
   * for the ACTUAL stream — passing the DB encoding to a player whose chain
   * was built from the probed encoding produces a misconfigured decoder and a
   * black screen (issue #108: H80 stored as h264 but streaming h265).
   */
  resolvedEncoding(cameraId: string): string;
  /** Reactive snapshot for debugging / the upgrade badge. */
  slot(cameraId: string): CameraSlot | null;
  /** Subscribe to degrade/upgrade events (for toasts). */
  onModeChange(cb: ModeChangeCallback): () => void;
  /** The owned reconnect coordinator (adapters read it for thundering-herd). */
  readonly coordinator: ReconnectCoordinator;
  /** Tear everything down (route unmount). */
  dispose(): void;
}

export function createPlayerOrchestrator(): PlayerOrchestrator {
  const coordinator = createReconnectCoordinator();
  const modeChangeCallbacks = new Set<ModeChangeCallback>();

  // Reactive map of cameraId → slot. Using `$state` on a record lets
  // CameraPlayer read `activeMode(id)` reactively without manual subscriptions.
  // The slot objects themselves are replaced on each transition (not mutated)
  // so Svelte's reactivity sees the change.
  let slots = $state<Record<string, CameraSlot>>({});

  // Non-reactive internal bookkeeping (timers, cooldowns) — kept in a parallel
  // map so it doesn't bloat the reactive snapshot.
  interface Internal {
    degradeTimer: ReturnType<typeof setTimeout> | null;
    upgradeTimer: ReturnType<typeof setTimeout> | null;
    probeTimer: ReturnType<typeof setTimeout> | null;
    okSince: number | null; // when continuous `ok` began
    lastRegistration: CameraRegistration;
    // Cooldowns: cameraMode → epoch millis until which a promotion is blocked.
    cooldowns: Partial<Record<CameraMode, number>>;
  }
  const internal = new Map<string, Internal>();
  let tabVisible = true;

  function emit(cameraId: string, from: CameraMode, to: CameraMode, reason: string): void {
    for (const cb of modeChangeCallbacks) cb(cameraId, from, to, reason);
  }

  function buildChain(reg: CameraRegistration): Candidate[] {
    return buildCandidateChain(reg.camera, reg.resp, reg.caps, {
      override: reg.override,
      isHlsCapable: reg.isHlsCapable,
      isUnsupported: reg.isUnsupported,
    });
  }

  function ensureInternal(cameraId: string, reg: CameraRegistration): Internal {
    let it = internal.get(cameraId);
    if (!it) {
      it = {
        degradeTimer: null,
        upgradeTimer: null,
        probeTimer: null,
        okSince: null,
        lastRegistration: reg,
        cooldowns: {},
      };
      internal.set(cameraId, it);
    }
    it.lastRegistration = reg;
    return it;
  }

  function clearTimers(it: Internal): void {
    if (it.degradeTimer) {
      clearTimeout(it.degradeTimer);
      it.degradeTimer = null;
    }
    if (it.upgradeTimer) {
      clearTimeout(it.upgradeTimer);
      it.upgradeTimer = null;
    }
    if (it.probeTimer) {
      clearTimeout(it.probeTimer);
      it.probeTimer = null;
    }
  }

  function setSlot(cameraId: string, patch: Partial<CameraSlot>, base?: CameraSlot): CameraSlot {
    const prev = base ?? slots[cameraId];
    const next: CameraSlot = {
      cameraId,
      chain: patch.chain ?? prev?.chain ?? [],
      activeIndex: patch.activeIndex ?? prev?.activeIndex ?? 0,
      health: patch.health ?? prev?.health ?? health('ok'),
      pinned: patch.pinned ?? prev?.pinned ?? false,
    };
    slots = { ...slots, [cameraId]: next };
    return next;
  }

  function registerCamera(reg: CameraRegistration): void {
    const cameraId = reg.camera.id;
    const it = ensureInternal(cameraId, reg);
    const chain = buildChain(reg);
    const pinned = chain.length === 1 && !!chain[0].pinned;
    // If the chain changed shape (e.g. codec re-probed) and the active index is
    // now out of range, reset to the head. A pinned override always resets to 0.
    const prev = slots[cameraId];
    let activeIndex = pinned ? 0 : Math.min(prev?.activeIndex ?? 0, Math.max(0, chain.length - 1));
    setSlot(cameraId, { chain, activeIndex, pinned, health: prev?.health ?? health('ok') }, prev);
    clearTimers(it);
    it.okSince = null;
    it.cooldowns = {};
  }

  function unregisterCamera(cameraId: string): void {
    const it = internal.get(cameraId);
    if (it) {
      clearTimers(it);
      internal.delete(cameraId);
    }
    coordinator.cancelRequest(cameraId);
    const next = { ...slots };
    delete next[cameraId];
    slots = next;
  }

  function reportHealth(cameraId: string, h: HealthState): void {
    const slot = slots[cameraId];
    const it = internal.get(cameraId);
    if (!slot || !it) return; // unknown camera — ignore
    if (slot.pinned) {
      // Pinned (user override) — record health. If the pinned protocol reports
      // a terminal failure (e.g. WebRTC 503 for a Xiaomi CS2 camera that can't
      // be served via WHEP), staying pinned means a permanent black screen.
      // Fall back to the FULL auto-selection chain (as if no override) so the
      // camera finds a working protocol. The user's override is preserved in
      // localStorage; we just stop forcing it while it's broken. They can
      // re-pin via the ProtocolSwitcher once the protocol works again.
      setSlot(cameraId, { health: h }, slot);
      if (h.status === 'failed') {
        // Rebuild the chain WITHOUT the override → full auto-selection chain,
        // then jump to a workable entry. The pinned protocol just proved
        // broken; staying on it = permanent black screen. Find the most-
        // compatible fallback (HLS is universal) in the full chain and switch
        // to it.
        //
        // Issue #112: we also CLEAR the localStorage override (previously it was
        // only ignored for the session, so a stale pin re-asserted on the next
        // route mount — e.g. a 'hls' override pinned when an MJPEG camera was
        // briefly unreachable kept forcing HLS across sessions). The override
        // has now demonstrably failed; clearing it lets auto-selection take
        // over permanently. The user can re-pin via ProtocolSwitcher anytime.
        clearCameraProtocolOverride(cameraId);
        const fullChain = buildChain({ ...it.lastRegistration, override: null });
        // Prefer HLS (universal fallback); else the last entry (most compatible).
        const hlsIdx = fullChain.findIndex(c => c.mode === 'hls');
        const fallbackIdx = hlsIdx >= 0 ? hlsIdx : fullChain.length - 1;
        if (fullChain.length > 1 && fallbackIdx > 0) {
          const from = slot.chain[slot.activeIndex]?.mode ?? 'pinned';
          const to = fullChain[fallbackIdx].mode;
          setSlot(cameraId, {
            chain: fullChain,
            activeIndex: fallbackIdx,
            pinned: false,
            health: health('ok'),
          }, slot);
          emit(cameraId, from, to, h.reason ?? 'pinned-protocol-failed');
        }
      }
      return;
    }

    // Refresh the slot's health reactively — BUT only when the (status, reason)
    // actually changed. Cameras that can never reach steady-state 'playing'
    // (e.g. an H.265 camera whose WS returns 401) repeatedly report 'failed',
    // and `healthFromStreamState('error')` returns a NEW object every call
    // (`since: Date.now()` differs). Without this short-circuit, every report
    // reassigned `slots` to a new object, which invalidated every `$derived(mode)`
    // consumer and — combined with player `$effect`s that read `mode` — drove an
    // unbounded synchronous effect chain (`effect_update_depth_exceeded`).
    // The `since` timestamp refreshing is NOT worth churning every dependent;
    // timers/demote below use `it` (internal, non-reactive) bookkeeping, so
    // skipping the slots write here does not affect adaptive decisions.
    const cur = slot.health;
    const healthUnchanged = cur.status === h.status && cur.reason === h.reason;
    if (!healthUnchanged) {
      setSlot(cameraId, { health: h }, slot);
    }

    if (h.status === 'failed') {
      // Immediate demote. Cancel any pending upgrade/probe.
      if (it.degradeTimer) {
        clearTimeout(it.degradeTimer);
        it.degradeTimer = null;
      }
      if (it.upgradeTimer) {
        clearTimeout(it.upgradeTimer);
        it.upgradeTimer = null;
      }
      if (it.probeTimer) {
        clearTimeout(it.probeTimer);
        it.probeTimer = null;
      }
      it.okSince = null;
      demote(cameraId, h.reason ?? 'fatal-error');
      return;
    }

    if (h.status === 'degraded') {
      // Arm a debounce: only demote if still degraded after the threshold.
      // `h.since` lets repeated degraded reports NOT reset the window.
      if (!it.degradeTimer) {
        it.degradeTimer = setTimeout(() => {
          it.degradeTimer = null;
          const cur = slots[cameraId];
          if (cur && cur.health.status === 'degraded') {
            demote(cameraId, cur.health.reason ?? 'no-frames');
          }
        }, DEGRADE_THRESHOLD_MS);
      }
      return;
    }

    // status === 'ok'
    if (it.degradeTimer) {
      clearTimeout(it.degradeTimer);
      it.degradeTimer = null;
    }
    if (it.okSince === null) it.okSince = Date.now();
    // If we're mid-probe of a promotion and it reached ok, confirm it.
    if (it.probeTimer) {
      clearTimeout(it.probeTimer);
      it.probeTimer = null;
    }
    // NOTE: the health was already written to the slot above (line 269's setSlot,
    // gated by the healthUnchanged short-circuit). Do NOT redundantly rewrite
    // slots here — that would churn every $derived(mode) dependent on every
    // 'ok' report and re-arm the effect_update_depth_exceeded loop for cameras
    // that oscillate around the ok/degraded boundary.
  }

  function demote(cameraId: string, reason: string): boolean {
    const slot = slots[cameraId];
    const it = internal.get(cameraId);
    if (!slot || !it || slot.pinned) return false;
    if (slot.activeIndex + 1 >= slot.chain.length) return false; // exhausted
    const from = slot.chain[slot.activeIndex].mode;
    const nextIndex = slot.activeIndex + 1;
    const to = slot.chain[nextIndex].mode;
    // Cool the entry we're leaving: don't try promoting back to it immediately.
    it.cooldowns[from] = Date.now() + ENTRY_COOLDOWN_MS;
    it.okSince = null;
    setSlot(cameraId, { activeIndex: nextIndex, health: health('ok') }, slot);
    emit(cameraId, from, to, reason);
    return true;
  }

  function attemptUpgrade(cameraId: string, trigger: string, opts: { bypassCooldown?: boolean } = {}): void {
    const slot = slots[cameraId];
    const it = internal.get(cameraId);
    if (!slot || !it || slot.pinned) return;
    if (!tabVisible) return; // never upgrade while hidden
    // Need continuous stability first.
    if (it.okSince === null || Date.now() - it.okSince < UPGRADE_STABLE_MS) return;
    if (slot.activeIndex === 0) return; // already at the top

    // Walk upward to the lowest-index entry not under cooldown.
    let target = -1;
    for (let i = slot.activeIndex - 1; i >= 0; i--) {
      const mode = slot.chain[i].mode;
      const cool = it.cooldowns[mode];
      if (!opts.bypassCooldown && cool && cool > Date.now()) break; // blocked; don't skip past it
      target = i;
      break;
    }
    if (target < 0) return;

    const fromMode = slot.chain[slot.activeIndex].mode;
    const toMode = slot.chain[target].mode;
    const revertIndex = slot.activeIndex; // capture for the probe revert
    it.okSince = null; // require fresh ok after the switch
    setSlot(cameraId, { activeIndex: target, health: health('degraded', 'reconnecting') }, slot);
    emit(cameraId, fromMode, toMode, `upgrade-${trigger}`);

    // Arm the probe: if we don't see `ok` within the window, revert + cool.
    it.probeTimer = setTimeout(() => {
      it.probeTimer = null;
      const cur = slots[cameraId];
      if (!cur) return;
      // Only revert if we're STILL on the promoted entry. If a `failed` report
      // demoted us further during the probe window, that demotion already
      // cooled the entry — don't double-revert.
      if (cur.activeIndex === target && cur.health.status !== 'ok') {
        it.cooldowns[toMode] = Date.now() + ENTRY_COOLDOWN_MS;
        it.okSince = null;
        setSlot(cameraId, { activeIndex: revertIndex, health: health('degraded', 'reconnecting') }, cur);
        emit(cameraId, toMode, fromMode, 'upgrade-reverted');
      }
    }, UPGRADE_PROBE_MS);
  }

  function setOverride(cameraId: string, protocol: string | null): void {
    const it = internal.get(cameraId);
    if (!it) return;
    // Rebuild the chain with the new override; buildCandidateChain pins when
    // override is usable, otherwise falls through to auto-selection.
    it.lastRegistration = { ...it.lastRegistration, override: protocol };
    registerCamera(it.lastRegistration);
  }

  function requestUpgrade(cameraId: string): void {
    // Manual "retry HD" is an explicit user action: bypass BOTH the stability
    // window and per-entry cooldowns. If the user insists, we let them try —
    // the probe still reverts+cools on failure, so a single click can't flap.
    const it = internal.get(cameraId);
    const slot = slots[cameraId];
    if (!it || !slot || slot.pinned) return;
    if (!tabVisible) return;
    if (slot.activeIndex === 0) return; // nothing above
    it.okSince = Date.now() - UPGRADE_STABLE_MS; // pretend stable
    attemptUpgrade(cameraId, 'manual', { bypassCooldown: true });
  }

  function setTabVisible(visible: boolean): void {
    if (tabVisible === visible) return;
    tabVisible = visible;
    // NOTE: we do NOT auto-upgrade on tab-visible anymore. The previous
    // attemptUpgrade(id, 'tab-visible') for every camera caused mode-flapping
    // (HLS stable → upgrade to wasm/webrtc → fails → degrade → repeat) which,
    // combined with the now-removed per-player visibility effects, produced the
    // console-freezing reactive loop. Mode changes now happen ONLY on explicit
    // health reports (degrade on failure) or manual requestUpgrade. When the
    // tab is hidden the browser pauses <video> automatically; WS connections
    // sit idle (harmless). This is the simplest correct behavior.
  }

  function activeMode(cameraId: string): CameraMode | null {
    const slot = slots[cameraId];
    if (!slot) return null;
    return slot.chain[slot.activeIndex]?.mode ?? null;
  }

  function resolvedEncoding(cameraId: string): string {
    // The chain was built from the recorder-probed resp.encoding (authoritative)
    // via resolveEncoding; return that same resolution so CameraPlayer can feed
    // the ACTUAL codec to the player decoder config. Returns '' when the camera
    // isn't registered yet — CameraPlayer falls back to camera.encoding then.
    const it = internal.get(cameraId);
    if (!it) return '';
    return resolveEncoding(it.lastRegistration.camera, it.lastRegistration.resp);
  }

  function slot(cameraId: string): CameraSlot | null {
    return slots[cameraId] ?? null;
  }

  function onModeChange(cb: ModeChangeCallback): () => void {
    modeChangeCallbacks.add(cb);
    return () => modeChangeCallbacks.delete(cb);
  }

  function dispose(): void {
    for (const [, it] of internal) clearTimers(it);
    internal.clear();
    slots = {};
    modeChangeCallbacks.clear();
    coordinator.dispose();
  }

  return {
    registerCamera,
    unregisterCamera,
    reportHealth,
    setOverride,
    requestUpgrade,
    setTabVisible,
    activeMode,
    resolvedEncoding,
    slot,
    onModeChange,
    coordinator,
    dispose,
  };
}

// ─── Convenience: build a CameraRegistration from common route state ─────────

/**
 * Helper for routes to assemble a {@link CameraRegistration} from their cached
 * per-camera protocol response + the global device caps. Centralizes the
 * caps-plumbing so Surveillance/LiveView don't repeat it.
 */
export function makeRegistration(
  camera: Camera,
  resp: ProtocolsResponse | null,
  opts: { override?: string | null; isHlsCapable: boolean; isUnsupported: boolean },
): CameraRegistration {
  const caps: BrowserCaps = {
    h265MSE: getCaps().mseH265,
    webCodecs: getCaps().webCodecs,
    wasmH265: getCaps().wasmH265,
  };
  // resolveEncoding is re-exported through stream-selection for convenience.
  void resolveEncoding(camera, resp); // ensure encoding normalization side-effect-free
  return {
    camera,
    resp,
    caps,
    override: opts.override ?? null,
    isHlsCapable: opts.isHlsCapable,
    isUnsupported: opts.isUnsupported,
  };
}
