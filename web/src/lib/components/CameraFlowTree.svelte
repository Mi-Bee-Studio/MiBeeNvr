<script lang="ts">
  // Single-camera flow tree — rendered inside the dashboard's camera-health
  // list when a row is expanded. Polls /api/streams only while mounted
  // (i.e. while the row is expanded); layout is fixed, only numbers refresh.
  import { onMount } from 'svelte';
  import { getFlowStreams } from '$lib/api';
  import type { FlowStream } from '$lib/api/flow';
  import { t } from '$lib/i18n';
  import { Radio } from 'lucide-svelte';

  let {
    cameraId,
    name = '',
    status = '',
    recordingEnabled = true,
  }: { cameraId: string; name?: string; status?: string; recordingEnabled?: boolean } = $props();

  const POLL_INTERVAL = 2000;

  let stream = $state<FlowStream | null>(null);
  let error = $state('');

  let prev: { framesIn: number; bytesIn: number; at: number } | null = null;
  let rate = $state({ fps: 0, kbps: 0 });
  let prevSub: { framesIn: number; bytesIn: number; at: number } | null = null;
  let subRate = $state({ fps: 0, kbps: 0 });
  let prevC: Record<string, { sends: number; at: number }> = {};
  let cRates = $state<Record<string, number>>({});
  let centered = false;
  let rootEl: HTMLDivElement | null = $state(null);

  // 'healthy' is the health-tracker's status for a camera that is streaming
  // and writing segments — the recorder status and health status use
  // different vocabularies, both mean "recording" here.
  const isRecording = $derived(['recording', 'active', 'healthy'].includes(status.toLowerCase()));
  const hasLiveViewer = $derived(
    !!stream?.consumers.some((c) => /^(ws|webrtc|flv|hls)/.test(consumerKind(c.id))),
  );
  // Is anything actually flowing into the hub right now? Drives the
  // green/gray/orange state coloring of every node.
  const live = $derived(rate.fps > 0);
  const frameStale = $derived(live && lastFrameAge(stream?.last_frame_at ?? '') > 10_000);

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
    for (const prefix of ['vision-sublayer-', 'ws-audio-', 'ws-', 'flv-', 'webrtc-audio-', 'webrtc-', 'hls', 'health-stats-', 'health-freeze-', 'keyframe-extractor-', 'relay-rtsp-', 'relay-rtmp-', 'relay-transcode-', 'cascade-']) {
      if (id.startsWith(prefix)) return prefix.replace(/-$/, '');
    }
    return id;
  }

  // Localize consumer kinds — raw IDs like "health-stats" mean nothing to users.
  const kindI18n: Record<string, string> = {
    'ws': 'ws', 'ws-audio': 'wsAudio', 'webrtc': 'webrtc', 'webrtc-audio': 'webrtcAudio',
    'flv': 'flv', 'hls': 'hls', 'health-stats': 'healthStats', 'health-freeze': 'healthFreeze',
    'keyframe-extractor': 'keyframeExtractor', 'relay-rtsp': 'relayRtsp', 'relay-rtmp': 'relayRtmp',
    'relay-transcode': 'relayTranscode', 'cascade': 'cascade',
    'vision-sublayer': 'visionSublayer',
  };

  function kindLabel(id: string): string {
    const kind = consumerKind(id);
    return kindI18n[kind] ? t(`flow.kind.${kindI18n[kind]}`) : kind;
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
        if (s.sub) {
          if (prevSub && dt > 0) {
            subRate = {
              fps: Math.round((Math.max(0, (s.sub.frames_in - prevSub.framesIn) / dt)) * 10) / 10,
              kbps: Math.round(Math.max(0, ((s.sub.bytes_in - prevSub.bytesIn) / dt) / 128)),
            };
          }
          prevSub = { framesIn: s.sub.frames_in, bytesIn: s.sub.bytes_in, at: now };
        } else {
          prevSub = null;
        }
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
    // Re-center once the tree has actually rendered — the parent's
    // scrollIntoView at click time runs against the title-only skeleton
    // (first poll is still in flight), leaving the grown tree half out of
    // view.
    if (!centered) {
      centered = true;
      requestAnimationFrame(() => rootEl?.scrollIntoView({ behavior: 'smooth', block: 'center' }));
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

<div class="cam-flow" bind:this={rootEl}>
  <div class="flow-title">
    <span class="title-left"><Radio size={12} /> {t('flow.treeTitle')}{name ? ` · ${name}` : ''}</span>
  </div>
  {#if error}
    <div class="flow-error">{error}</div>
  {:else if !stream}
    <div class="flow-empty">{t('flow.emptyCamera')}</div>
  {:else}
    <div class="tree">
      <div class="node node-src" class:con-off={!live}>
        <span class="node-title">{t('flow.source')}</span>
        <span class="node-line">{stream.source}</span>
        {#if stream.encoding}
          <span class="node-line dim">{stream.encoding}{#if stream.width && stream.height} {stream.width}×{stream.height}{/if}</span>
        {/if}
      </div>

      <div class="tree-link"></div>

      <div class="hub-col">
        <div class="node node-hub">
          <span class="node-title"><Radio size={12} /> {t('flow.hubLabel')}</span>
          <span class="node-line" class:ok-text={live} class:t-off={!live}>{rate.fps} fps · {rate.kbps} kbps</span>
          <span class="node-line dim" class:t-warn={frameStale}>
            {t('flow.lastFrame')}: {fmtAge(lastFrameAge(stream.last_frame_at))}
          </span>
          {#if stream.jitter_active}<span class="node-line warn">{t('flow.jitter')}</span>{/if}
        </div>
        <div class="node-total dim">{t('flow.totalIn')}: {stream.frames_in} · {fmtBytes(stream.bytes_in)}</div>
      </div>

      <div class="tree-link"></div>

      <div class="branches">
        <!-- Recording-to-disk is not a hub consumer (the recorder IS the
             producer and writes segments directly), but users expect the
             full pipeline — always show the branch. -->
        {#if recordingEnabled}
          <div class="branch">
            <div class="node node-con" class:con-off={!isRecording || !live}>
              <span class="node-title" class:t-off={!isRecording} class:t-warn={isRecording && !live} class:ok-text={isRecording && live}>{t('flow.recordDisk')}</span>
              <span class="node-line" class:t-off={!isRecording} class:t-warn={isRecording && !live} class:ok-text={isRecording && live}>
                {#if !isRecording}{t('flow.recordOff')}{:else if live}{t('flow.recording')}{:else}{t('flow.noFrames')}{/if}
              </span>
              <span class="node-line dim">{rate.kbps} kbps → {t('flow.disk')}</span>
              {#if stream.recording}
                {#if stream.recording.segmenting && stream.recording.segment_dur_s}
                  <span class="node-line dim">
                    {t('flow.segProgress', {
                      elapsed: stream.recording.segment_elapsed_s?.toFixed(0) ?? '0',
                      dur: stream.recording.segment_dur_s.toFixed(0),
                      frames: stream.recording.segment_frames ?? 0,
                    })}
                  </span>
                {/if}
                {#if stream.recording.ring_buf_cap > 0}
                  <span
                    class="node-line dim"
                    class:t-warn={stream.recording.ring_buf_len / stream.recording.ring_buf_cap > 0.5}
                  >
                    {t('flow.ringWater', { len: stream.recording.ring_buf_len, cap: stream.recording.ring_buf_cap })}
                  </span>
                {/if}
                {#if stream.recording.ring_buf_drops_total > 0}
                  <span class="node-line t-warn">
                    {t('flow.ringDrops', { n: stream.recording.ring_buf_drops_total })}
                  </span>
                {/if}
              {/if}
              {#if stream.merge_pending !== undefined && stream.merge_pending > 0}
                <span class="node-line dim">{t('flow.mergePending', { n: stream.merge_pending })}</span>
              {/if}
            </div>
          </div>
        {/if}
        {#each stream.consumers as c (c.id)}
          {@const rate_ = cRates[c.id] ?? 0}
          <div class="branch">
            <div class="node node-con" class:con-warn={c.drop_rate > 0.01} class:con-danger={c.drop_rate > 0.05}>
              <span
                class="node-title"
                class:t-danger={c.drop_rate > 0.05}
                class:t-warn={c.drop_rate > 0.01 && c.drop_rate <= 0.05}
                class:ok-text={c.drop_rate <= 0.01 && rate_ > 0}
                class:t-off={rate_ === 0}
              >
                {kindLabel(c.id)}
                <span class="dim">{rate_}/s</span>
                {#if c.idr_drops > 0}<span class="idr-drops">{c.idr_drops} IDR</span>{/if}
              </span>
              <span class="node-line" class:t-danger={c.drop_rate > 0.05} class:t-warn={c.drop_rate > 0.01 && c.drop_rate <= 0.05}>
                {t('flow.sends')} {c.sends} · {t('flow.dropRate')} {(c.drop_rate * 100).toFixed(1)}%
              </span>
              <span class="node-line dim">{t('flow.buffer')} {c.buffer_depth}/{c.buffer_capacity} · {c.dwell_avg_ms.toFixed(0)}/{c.dwell_max_ms.toFixed(0)} ms</span>
            </div>
          </div>
        {:else}
          <div class="branch"><div class="node node-con dim">{t('flow.noConsumers')}</div></div>
        {/each}
        <!-- Surveillance grid only appears as a real consumer (ws/…) while
             someone is watching; show a dim placeholder otherwise so the
             branch is always visible. -->
        {#if !hasLiveViewer}
          <div class="branch">
            <div class="node node-con con-off">
              <span class="node-title t-off">{t('flow.surveillance')}</span>
              <span class="node-line dim">{t('flow.notWatching')}</span>
            </div>
          </div>
        {/if}
      </div>
    </div>

    {#if stream.sub}
      {@const subKinds = [...new Set(stream.sub.consumers.map((c) => kindLabel(c.id)))]}
      <div class="sub-strip">
        <div class="tree-link sub-link"></div>
        <div class="node node-sub" class:con-off={subRate.fps === 0}>
          <span class="node-title" class:ok-text={subRate.fps > 0}>
            {t('flow.subStream')}
            <span class="dim">{stream.sub.state}{stream.sub.codec ? ` · ${stream.sub.codec}` : ''}</span>
          </span>
          <span class="node-line" class:ok-text={subRate.fps > 0} class:t-off={subRate.fps === 0}>
            {subRate.fps} fps · {subRate.kbps} kbps
          </span>
          <span class="node-line dim">
            {t('flow.subRefs')} {stream.sub.refs} · {t('flow.consumer')} {stream.sub.consumers.length} · {t('flow.totalIn')} {stream.sub.frames_in}
          </span>
          <span class="node-line dim" class:t-warn={lastFrameAge(stream.sub.last_frame_at) > 10_000}>
            {t('flow.lastFrame')}: {fmtAge(lastFrameAge(stream.sub.last_frame_at))}
          </span>
          {#if subKinds.length}
            <span class="node-line dim">{subKinds.join(' · ')}</span>
          {/if}
        </div>
      </div>
    {/if}
  {/if}
</div>

<style>
  .cam-flow {
    margin-top: 0.5rem;
  }
  .flow-title {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
    flex-wrap: wrap;
    font-weight: 600;
    font-size: 0.78rem;
    margin-bottom: 0.4rem;
  }
  .title-left {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
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
    gap: 0.1rem;
    border: 1px solid var(--border, rgba(128, 128, 128, 0.3));
    border-radius: 8px;
    padding: 0.3rem 0.55rem;
    width: 180px;
    box-sizing: border-box;
    font-variant-numeric: tabular-nums;
    flex-shrink: 0;
  }
  .node-title {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    font-weight: 600;
    font-size: 0.74rem;
  }
  .node-line {
    font-size: 0.68rem;
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
  /* State coloring — green = flowing, gray = idle, orange = trouble,
     red = severe. Applied to node text so status reads at a glance. */
  .ok-text {
    color: var(--color-success);
  }
  .t-warn {
    color: var(--color-warning);
  }
  .t-danger {
    color: var(--color-danger);
  }
  .t-off {
    color: var(--text-tertiary);
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
  .node-con.con-off {
    opacity: 0.55;
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
    width: 18px;
    height: 0;
    border-top: 2px solid rgba(125, 130, 140, 0.7);
    flex-shrink: 0;
  }
  .branches {
    position: relative;
    display: flex;
    flex-direction: column;
    justify-content: center;
    gap: 0.3rem;
    padding-left: 20px;
    flex-shrink: 0;
    /* Auto-height so the common case (≤5 branches: recording + 3 consumers
       + surveillance ghost) never scrolls; hard cap keeps pathological
       consumer counts bounded. */
    max-height: 340px;
    overflow-y: auto;
  }
  .branches::before {
    content: '';
    position: absolute;
    left: 0;
    top: 10px;
    bottom: 10px;
    border-left: 2px solid rgba(125, 130, 140, 0.7);
  }
  .branch {
    position: relative;
  }
  /* Sub-stream tier (#513): a parallel source+hub pair, rendered as a dashed
     node under the main tree — visually a sibling source, not a consumer. */
  .sub-strip {
    display: flex;
    align-items: center;
    margin-top: 0.35rem;
  }
  .sub-link {
    margin-right: 0;
  }
  .node-sub {
    border-style: dashed;
    border-color: rgba(168, 85, 247, 0.45);
    background: rgba(168, 85, 247, 0.05);
  }
  .branch::before {
    content: '';
    position: absolute;
    left: -20px;
    top: 50%;
    width: 20px;
    border-top: 2px solid rgba(125, 130, 140, 0.7);
  }
</style>
