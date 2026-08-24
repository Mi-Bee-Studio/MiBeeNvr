<script lang="ts">
  import { onMount } from 'svelte';
  import { getFlowStreams } from '$lib/api/flow';
  import { getHealthEvents, listCameras } from '$lib/api';
  import type { FlowStream } from '$lib/api/flow';
  import type { Camera, HealthEvent } from '$lib/api';
  import { t } from '$lib/i18n';
  import { Users, Gauge, Radio, Pause, Play } from 'lucide-svelte';

  const POLL_INTERVAL = 2000;
  const EVENTS_REFRESH = 30_000;

  let streams = $state<FlowStream[]>([]);
  let cameras = $state<Camera[]>([]);
  let healthEvents = $state<Record<string, HealthEvent[]>>({});
  let error = $state('');
  let lastUpdated = $state<Date | null>(null);

  // Freeze mode: while paused, polling stops and the UI holds a static
  // snapshot so the user can read and analyze without numbers flashing.
  // `userPaused` is the explicit toggle; `tabHidden` auto-pauses when the
  // dashboard tab is in the background. Polling runs only when neither is set.
  let userPaused = $state(false);
  let tabHidden = $state(false);
  let expanded = $state<Record<string, boolean>>({});
  const paused = $derived(userPaused || tabHidden);

  // Derived rates: diff cumulative counters across polls.
  // prev holds {framesIn, bytesIn, at} per camera from the previous poll.
  let prev: Record<string, { framesIn: number; bytesIn: number; at: number }> = {};
  let rates = $state<Record<string, { fps: number; kbps: number }>>({});

  function statusColor(status: string): string {
    const s = status.toLowerCase();
    if (s === 'recording' || s === 'active') return 'var(--color-success)';
    if (s === 'reconnecting') return 'var(--color-warning)';
    if (s === 'error' || s === 'failed') return 'var(--color-danger)';
    return 'var(--text-tertiary)';
  }

  function ageMs(iso: string): number {
    if (!iso) return Number.POSITIVE_INFINITY;
    // Anchor to the snapshot time, not Date.now(), so frozen cards show the
    // age as of the pause instead of a ticking counter over stale data.
    const ref = lastUpdated ? lastUpdated.getTime() : Date.now();
    return ref - new Date(iso).getTime();
  }

  function fmtAge(ms: number): string {
    if (!Number.isFinite(ms)) return '—';
    if (ms < 1000) return `${Math.round(ms)}ms`;
    if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
    if (ms < 3_600_000) return `${Math.round(ms / 60_000)}m`;
    return `${Math.round(ms / 3_600_000)}h`;
  }

  function fmtBytes(n: number): string {
    if (n < 1024) return `${n} B`;
    if (n < 1024 ** 2) return `${(n / 1024).toFixed(1)} KB`;
    if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MB`;
    return `${(n / 1024 ** 3).toFixed(2)} GB`;
  }

  function consumerKind(id: string): string {
    // Strip the camera-id suffix: "ws-cam-1" → "ws", "hls" → "hls".
    for (const prefix of ['ws-audio-', 'ws-', 'flv-', 'webrtc-audio-', 'webrtc-', 'hls', 'health-stats-', 'health-freeze-', 'keyframe-extractor-', 'relay-rtsp-', 'relay-rtmp-', 'relay-transcode-', 'cascade-']) {
      if (id.startsWith(prefix)) return prefix.replace(/-$/, '');
    }
    return id;
  }

  function viewerSummary(s: FlowStream): string {
    const entries = Object.entries(s.viewers ?? {}).filter(([, n]) => n > 0);
    if (!entries.length) return t('flow.noViewers');
    return entries.map(([proto, n]) => `${proto}: ${n}`).join(' · ');
  }

  async function poll(): Promise<void> {
    try {
      const res = await getFlowStreams();
      const now = Date.now();
      const nextRates: Record<string, { fps: number; kbps: number }> = {};
      for (const s of res.streams) {
        const p = prev[s.camera_id];
        if (p && now > p.at) {
          const dt = (now - p.at) / 1000;
          const fps = Math.max(0, (s.frames_in - p.framesIn) / dt);
          const kbps = Math.max(0, ((s.bytes_in - p.bytesIn) / dt) / 128);
          nextRates[s.camera_id] = { fps: Math.round(fps * 10) / 10, kbps: Math.round(kbps) };
        } else {
          nextRates[s.camera_id] = rates[s.camera_id] ?? { fps: 0, kbps: 0 };
        }
        prev[s.camera_id] = { framesIn: s.frames_in, bytesIn: s.bytes_in, at: now };
      }
      rates = nextRates;
      streams = res.streams;
      error = '';
      lastUpdated = new Date();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  async function refreshEvents(): Promise<void> {
    try {
      const res = await getHealthEvents({ limit: 100 });
      const byCam: Record<string, HealthEvent[]> = {};
      for (const ev of res.events ?? []) {
        (byCam[ev.camera_id] ??= []).push(ev);
      }
      for (const list of Object.values(byCam)) list.length = Math.min(list.length, 5);
      healthEvents = byCam;
    } catch {
      // Non-fatal: the events strip just stays empty.
    }
  }

  onMount(() => {
    poll();
    refreshEvents();
    listCameras().then((cs) => (cameras = cs)).catch(() => {});
    const pollTimer = setInterval(() => {
      if (!paused) poll();
    }, POLL_INTERVAL);
    const evTimer = setInterval(refreshEvents, EVENTS_REFRESH);
    const onVisibility = () => (tabHidden = document.hidden);
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      clearInterval(pollTimer);
      clearInterval(evTimer);
      document.removeEventListener('visibilitychange', onVisibility);
    };
  });

  function cameraName(id: string): string {
    return cameras.find((c) => c.id === id)?.name ?? streams.find((s) => s.camera_id === id)?.name ?? id;
  }
</script>

<div class="flow-panel">
  <div class="flow-header">
    <p class="subtitle">{t('flow.subtitle')}</p>
    <div class="header-actions">
      {#if paused}
        <span class="paused-badge">{t('flow.pausedAt')}: {lastUpdated?.toLocaleTimeString() ?? '—'}</span>
      {/if}
      <button class="pause-btn" onclick={() => (userPaused = !userPaused)} title={userPaused ? t('flow.resume') : t('flow.pause')}>
        {#if userPaused}
          <Play size={13} /> {t('flow.resume')}
        {:else}
          <Pause size={13} /> {t('flow.pause')}
        {/if}
      </button>
      {#if !paused && lastUpdated}
        <span class="updated">{t('flow.updatedAt')}: {lastUpdated.toLocaleTimeString()}</span>
      {/if}
    </div>
  </div>

  {#if error}
    <div class="error-banner">{error}</div>
  {/if}

  {#if streams.length === 0 && !error}
    <div class="empty">
      <p>{t('flow.empty')}</p>
    </div>
  {/if}

  <div class="flow-grid">
    {#each streams as s (s.camera_id)}
      <div class="flow-card" class:collapsed={expanded[s.camera_id] === false}>
        <div class="card-head" onclick={() => (expanded[s.camera_id] = expanded[s.camera_id] === false)} role="button" tabindex="0"
          onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); expanded[s.camera_id] = expanded[s.camera_id] === false; } }}>
          <div class="cam-title">
            <span class="status-dot" style="background: {statusColor(s.status)}"></span>
            <span class="cam-name">{cameraName(s.camera_id)}</span>
            <span class="cam-source">{s.source}</span>
          </div>
          <div class="cam-meta">
            {#if s.encoding}
              <span class="chip">{s.encoding}{#if s.width && s.height}&nbsp;{s.width}×{s.height}{/if}</span>
            {/if}
            <span class="chip viewers"><Users size={12} /> {viewerSummary(s)}</span>
            {#if expanded[s.camera_id] === false}
              <span class="chip">{rates[s.camera_id]?.fps ?? 0} fps · {s.consumers.length}</span>
            {/if}
            <span class="chev">{expanded[s.camera_id] ? '▾' : '▸'}</span>
          </div>
        </div>

        {#if expanded[s.camera_id] !== false}
        <div class="hub-row">
          <div class="hub-node">
            <Radio size={14} />
            <span class="node-label">{t('flow.hub')}</span>
            <span class="node-metric">{rates[s.camera_id]?.fps ?? 0} fps</span>
            <span class="node-metric">{rates[s.camera_id]?.kbps ?? 0} kbps</span>
            <span class="node-metric {ageMs(s.last_frame_at) > 10_000 ? 'metric-warn' : ''}">
              {t('flow.lastFrame')}: {fmtAge(ageMs(s.last_frame_at))}
            </span>
            {#if s.jitter_active}<span class="chip chip-warn">{t('flow.jitter')}</span>{/if}
          </div>
          <div class="arrow">→</div>
          <div class="total-out">
            {t('flow.totalIn')}: {s.frames_in} · {fmtBytes(s.bytes_in)}
          </div>
        </div>

        <table class="consumers">
          <thead>
            <tr>
              <th>{t('flow.consumer')}</th>
              <th>{t('flow.sends')}</th>
              <th>{t('flow.drops')}</th>
              <th>{t('flow.dropRate')}</th>
              <th>{t('flow.buffer')}</th>
              <th>{t('flow.dwell')}</th>
            </tr>
          </thead>
          <tbody>
            {#each s.consumers as c (c.id)}
              <tr>
                <td>
                  <span class="consumer-kind">{consumerKind(c.id)}</span>
                  <span class="consumer-id">{c.id}</span>
                </td>
                <td>{c.sends}</td>
                <td class="{c.drops > 0 ? 'cell-warn' : ''}">
                  {c.drops}{#if c.idr_drops > 0}&nbsp;<span class="idr-drops">({c.idr_drops} IDR)</span>{/if}
                </td>
                <td class="{c.drop_rate > 0.05 ? 'cell-danger' : c.drop_rate > 0.01 ? 'cell-warn' : ''}">
                  {(c.drop_rate * 100).toFixed(1)}%
                </td>
                <td>{c.buffer_depth}/{c.buffer_capacity}</td>
                <td>{c.dwell_avg_ms.toFixed(1)} / {c.dwell_max_ms.toFixed(0)} ms</td>
              </tr>
            {/each}
            {#if s.consumers.length === 0}
              <tr><td colspan="6" class="no-consumers">{t('flow.noConsumers')}</td></tr>
            {/if}
          </tbody>
        </table>

        {#if healthEvents[s.camera_id]?.length}
          <div class="events-strip">
            <Gauge size={12} />
            {#each healthEvents[s.camera_id].slice(0, 3) as ev}
              <span class="event-item" data-type={ev.event_type}>
                {t(`health.eventTypes.${ev.event_type === 'connection_lost' ? 'connectionLost' : ev.event_type === 'connection_restored' ? 'connectionRestored' : ev.event_type === 'stream_anomaly' ? 'streamAnomaly' : ev.event_type === 'freeze_detected' ? 'freezeDetected' : 'freezeRecovered'}`)}
                · {new Date(ev.created_at).toLocaleTimeString()}
              </span>
            {/each}
          </div>
        {/if}
        {/if}
      </div>
    {/each}
  </div>
</div>

<style>
  .flow-panel {
    margin-top: 1rem;
  }
  .flow-header {
    display: flex;
    align-items: baseline;
    gap: 0.75rem;
    flex-wrap: wrap;
    margin-bottom: 0.25rem;
  }
  .flow-header h1 {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 1.4rem;
    margin: 0;
  }
  .subtitle {
    color: var(--text-tertiary);
    font-size: 0.875rem;
    margin: 0;
    flex-basis: 100%;
  }
  .header-actions {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    margin-left: auto;
  }
  .pause-btn {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    background: rgba(128, 128, 128, 0.12);
    color: var(--text-secondary);
    border: 1px solid var(--border, rgba(128, 128, 128, 0.3));
    border-radius: 8px;
    padding: 0.25rem 0.7rem;
    font-size: 0.78rem;
    cursor: pointer;
  }
  .pause-btn:hover {
    background: rgba(128, 128, 128, 0.22);
  }
  .paused-badge {
    color: var(--color-warning);
    font-size: 0.75rem;
    background: rgba(245, 158, 11, 0.12);
    border-radius: 999px;
    padding: 0.15rem 0.6rem;
  }
  .updated {
    color: var(--text-tertiary);
    font-size: 0.75rem;
    margin-left: auto;
  }
  .error-banner {
    background: rgba(239, 68, 68, 0.1);
    color: var(--color-danger);
    border: 1px solid rgba(239, 68, 68, 0.25);
    border-radius: 8px;
    padding: 0.6rem 1rem;
    margin-bottom: 1rem;
    font-size: 0.875rem;
  }
  .empty {
    text-align: center;
    color: var(--text-tertiary);
    padding: 3rem 0;
  }
  .flow-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(520px, 1fr));
    gap: 1rem;
    margin-top: 1rem;
  }
  .flow-card {
    background: var(--bg-secondary, var(--bg));
    border: 1px solid var(--border, rgba(128, 128, 128, 0.2));
    border-radius: 12px;
    padding: 1rem;
  }
  .card-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
    margin-bottom: 0.6rem;
    cursor: pointer;
    user-select: none;
  }
  .flow-card.collapsed .card-head {
    margin-bottom: 0;
  }
  .chev {
    color: var(--text-tertiary);
    font-size: 0.8rem;
  }
  .cam-title {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-weight: 600;
  }
  .status-dot {
    width: 9px;
    height: 9px;
    border-radius: 50%;
    flex-shrink: 0;
  }
  .cam-source {
    color: var(--text-tertiary);
    font-size: 0.75rem;
    font-weight: 400;
  }
  .cam-meta {
    display: flex;
    gap: 0.4rem;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    background: rgba(128, 128, 128, 0.12);
    border-radius: 999px;
    padding: 0.1rem 0.55rem;
    font-size: 0.72rem;
    color: var(--text-secondary);
  }
  .chip-warn {
    background: rgba(245, 158, 11, 0.15);
    color: var(--color-warning);
  }
  .hub-row {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    flex-wrap: wrap;
    margin-bottom: 0.6rem;
  }
  .hub-node {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
    border: 1px dashed var(--border, rgba(128, 128, 128, 0.35));
    border-radius: 8px;
    padding: 0.35rem 0.6rem;
  }
  .node-label {
    font-weight: 600;
    font-size: 0.8rem;
  }
  .node-metric {
    font-size: 0.78rem;
    color: var(--text-secondary);
    font-variant-numeric: tabular-nums;
  }
  .metric-warn {
    color: var(--color-warning);
  }
  .arrow {
    color: var(--text-tertiary);
  }
  .total-out {
    font-size: 0.75rem;
    color: var(--text-tertiary);
    font-variant-numeric: tabular-nums;
  }
  .consumers {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.78rem;
    font-variant-numeric: tabular-nums;
  }
  .consumers th {
    text-align: left;
    color: var(--text-tertiary);
    font-weight: 500;
    padding: 0.25rem 0.5rem;
    border-bottom: 1px solid var(--border, rgba(128, 128, 128, 0.2));
  }
  .consumers td {
    padding: 0.3rem 0.5rem;
    border-bottom: 1px solid rgba(128, 128, 128, 0.08);
  }
  .consumer-kind {
    font-weight: 600;
    margin-right: 0.35rem;
  }
  .consumer-id {
    color: var(--text-tertiary);
    font-size: 0.7rem;
  }
  .cell-warn {
    color: var(--color-warning);
  }
  .cell-danger {
    color: var(--color-danger);
    font-weight: 600;
  }
  .idr-drops {
    color: var(--color-danger);
    font-size: 0.7rem;
  }
  .no-consumers {
    color: var(--text-tertiary);
    text-align: center;
    padding: 0.6rem 0;
  }
  .events-strip {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
    margin-top: 0.6rem;
    color: var(--text-tertiary);
    font-size: 0.72rem;
  }
  .event-item[data-type='connection_lost'],
  .event-item[data-type='freeze_detected'] {
    color: var(--color-danger);
  }
  .event-item[data-type='stream_anomaly'] {
    color: var(--color-warning);
  }
</style>
