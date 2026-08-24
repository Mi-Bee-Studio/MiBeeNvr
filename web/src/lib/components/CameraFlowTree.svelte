<script lang="ts">
  // Single-camera flow tree — rendered inside the dashboard's camera-health
  // list when a row is expanded. Polls /api/streams only while mounted
  // (i.e. while the row is expanded); layout is fixed, only numbers refresh.
  import { onMount } from 'svelte';
  import { getFlowStreams } from '$lib/api/flow';
  import type { FlowStream } from '$lib/api/flow';
  import { t } from '$lib/i18n';
  import { Radio } from 'lucide-svelte';

  let { cameraId, name = '' }: { cameraId: string; name?: string } = $props();

  const POLL_INTERVAL = 2000;

  let stream = $state<FlowStream | null>(null);
  let error = $state('');

  let prev: { framesIn: number; bytesIn: number; at: number } | null = null;
  let rate = $state({ fps: 0, kbps: 0 });
  let prevC: Record<string, { sends: number; at: number }> = {};
  let cRates = $state<Record<string, number>>({});

  function lastFrameAge(iso: string): number {
    const t = iso ? new Date(iso).getTime() : 0;
    if (!Number.isFinite(t) || t < 978307200000) return Number.POSITIVE_INFINITY;
    return Date.now() - t;
  }

  function fmtAge(ms: number): string {    if (!Number.isFinite(ms)) return '—';
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
    for (const prefix of ['ws-audio-', 'ws-', 'flv-', 'webrtc-audio-', 'webrtc-', 'hls', 'health-stats-', 'health-freeze-', 'keyframe-extractor-', 'relay-rtsp-', 'relay-rtmp-', 'relay-transcode-', 'cascade-']) {
      if (id.startsWith(prefix)) return prefix.replace(/-$/, '');
    }
    return id;
  }

  async function poll(): Promise<void> {
    try {
      const res = await getFlowStreams();
      const s = res.streams.find((x) => x.camera_id === cameraId) ?? null;
      const now = Date.now();
      if (s) {
        const dt = prev && now > prev.at ? (now - prev.at) / 1000 : 0;
        if (prev && dt > 0) {
          rate = {
            fps: Math.round((Math.max(0, (s.frames_in - prev.framesIn) / dt)) * 10) / 10,
            kbps: Math.round(Math.max(0, ((s.bytes_in - prev.bytesIn) / dt) / 128)),
          };
        }
        prev = { framesIn: s.frames_in, bytesIn: s.bytes_in, at: now };
        const next: Record<string, number> = {};
        for (const c of s.consumers) {
          const pc = prevC[c.id];
          next[c.id] = pc && dt > 0 ? Math.round((Math.max(0, (c.sends - pc.sends) / dt)) * 10) / 10 : (cRates[c.id] ?? 0);
          prevC[c.id] = { sends: c.sends, at: now };
        }
        cRates = next;
      }
      stream = s;
      error = '';
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  onMount(() => {
    poll();
    const timer = setInterval(() => {
      if (!document.hidden) poll();
    }, POLL_INTERVAL);
    return () => clearInterval(timer);
  });
</script>

<div class="cam-flow">
  <div class="flow-title"><Radio size={12} /> {t('flow.treeTitle')}{name ? ` · ${name}` : ''}</div>
  {#if error}
    <div class="flow-error">{error}</div>
  {:else if !stream}
    <div class="flow-empty">{t('flow.emptyCamera')}</div>
  {:else}
    <div class="tree">
      <div class="node node-src">
        <span class="node-title">{t('flow.source')}</span>
        <span class="node-line">{stream.source}</span>
        {#if stream.encoding}
          <span class="node-line dim">{stream.encoding}{#if stream.width && stream.height} {stream.width}×{stream.height}{/if}</span>
        {/if}
      </div>

      <div class="tree-link"></div>

      <div class="hub-col">
        <div class="node node-hub">
          <span class="node-title"><Radio size={12} /> {t('flow.hub')}</span>
          <span class="node-line">{rate.fps} fps · {rate.kbps} kbps</span>
          <span class="node-line dim">
            {t('flow.lastFrame')}: {fmtAge(lastFrameAge(stream.last_frame_at))}
          </span>
          {#if stream.jitter_active}<span class="node-line warn">{t('flow.jitter')}</span>{/if}
        </div>
        <div class="node-total dim">{t('flow.totalIn')}: {stream.frames_in} · {fmtBytes(stream.bytes_in)}</div>
      </div>

      <div class="tree-link"></div>

      <div class="branches">
        {#each stream.consumers as c (c.id)}
          <div class="branch">
            <div class="node node-con" class:con-warn={c.drop_rate > 0.01} class:con-danger={c.drop_rate > 0.05}>
              <span class="node-title">
                {consumerKind(c.id)}
                <span class="dim">{cRates[c.id] ?? 0}/s</span>
                {#if c.idr_drops > 0}<span class="idr-drops">{c.idr_drops} IDR</span>{/if}
              </span>
              <span class="node-line">{t('flow.sends')} {c.sends} · {t('flow.dropRate')} {(c.drop_rate * 100).toFixed(1)}%</span>
              <span class="node-line dim">{t('flow.buffer')} {c.buffer_depth}/{c.buffer_capacity} · {c.dwell_avg_ms.toFixed(0)}/{c.dwell_max_ms.toFixed(0)} ms</span>
            </div>
          </div>
        {:else}
          <div class="branch"><div class="node node-con dim">{t('flow.noConsumers')}</div></div>
        {/each}
      </div>
    </div>
  {/if}
</div>

<style>
  .cam-flow {
    margin-top: 0.5rem;
  }
  .flow-title {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    font-weight: 600;
    font-size: 0.78rem;
    margin-bottom: 0.4rem;
  }
  .flow-error {
    color: var(--color-danger);
    font-size: 0.78rem;
  }
  .flow-empty {
    color: var(--text-tertiary);
    font-size: 0.78rem;
  }
  .tree {
    display: flex;
    align-items: center;
    overflow-x: auto;
    padding: 0.3rem 0;
  }
  .node {
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
    border: 1px solid var(--border, rgba(128, 128, 128, 0.3));
    border-radius: 10px;
    padding: 0.45rem 0.7rem;
    width: 190px;
    box-sizing: border-box;
    font-variant-numeric: tabular-nums;
    flex-shrink: 0;
  }
  .node-title {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    font-weight: 600;
    font-size: 0.78rem;
  }
  .node-line {
    font-size: 0.72rem;
    color: var(--text-secondary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .dim {
    color: var(--text-tertiary);
  }
  .warn {
    color: var(--color-warning);
  }
  .idr-drops {
    color: var(--color-danger);
    font-size: 0.7rem;
  }
  .node-src {
    border-style: dashed;
  }
  .node-hub {
    border-color: rgba(59, 130, 246, 0.45);
    background: rgba(59, 130, 246, 0.06);
  }
  .node-con.con-warn {
    border-color: rgba(245, 158, 11, 0.5);
  }
  .node-con.con-danger {
    border-color: rgba(239, 68, 68, 0.55);
    background: rgba(239, 68, 68, 0.05);
  }
  .hub-col {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    align-items: flex-start;
    flex-shrink: 0;
  }
  .node-total {
    font-size: 0.68rem;
    padding-left: 0.7rem;
  }
  .tree-link {
    width: 22px;
    height: 0;
    border-top: 2px solid rgba(125, 130, 140, 0.7);
    flex-shrink: 0;
  }
  .branches {
    position: relative;
    display: flex;
    flex-direction: column;
    justify-content: center;
    gap: 0.35rem;
    padding-left: 24px;
    flex-shrink: 0;
    height: 190px;
    overflow-y: auto;
  }
  .branches::before {
    content: '';
    position: absolute;
    left: 0;
    top: 14px;
    bottom: 14px;
    border-left: 2px solid rgba(125, 130, 140, 0.7);
  }
  .branch {
    position: relative;
  }
  .branch::before {
    content: '';
    position: absolute;
    left: -24px;
    top: 50%;
    width: 24px;
    border-top: 2px solid rgba(125, 130, 140, 0.7);
  }
</style>
