<script lang="ts">
  // 远程访问（P2P）互联质量卡片 — 仪表盘主 tab（2026-10-07 用户需求）。
  // 可见性口径（用户裁定）：P2P 已启用 且 账号已关联才渲染；取消关联/
  // 未绑定即整卡消失。数据由 App 周期上报（/api/p2p/viewer-report），
  // 3 分钟无上报视为陈旧（App 退后台/断线）。
  // 多观众（2026-10-09 用户需求）：每台 App 各自记录/显示（键 = 上报的
  // self_addr）；连接记录（链路事件）随 GET /api/p2p/viewer 一并返回，
  // 同一张卡内展示——同样只在已关联时可见。
  import { onMount } from 'svelte';
  import { t } from '$lib/i18n';
  import { getP2pViewer, type P2pViewer, type P2pViewerStatus } from '$lib/api/p2p';
  import { formatFileSize } from '$lib/format';
  import { Smartphone, ArrowDown, ArrowUp, ScrollText } from 'lucide-svelte';
  import MiniTimeChart from './MiniTimeChart.svelte';

  let status = $state<P2pViewerStatus | null>(null);
  let timer: ReturnType<typeof setInterval> | undefined;

  async function refresh() {
    try {
      status = await getP2pViewer();
    } catch {
      // 网络抖动：保留上一次数据，下一轮再试。
    }
  }

  onMount(() => {
    refresh();
    timer = setInterval(refresh, 20000);
    return () => clearInterval(timer);
  });

  // 多观众；旧后端无 viewers 时退回单观众兼容字段。
  let viewers = $derived<P2pViewer[]>(
    status?.viewers ?? (status?.viewer ? [status.viewer] : []),
  );

  // 连接记录：最新在上，最多展示 12 条。
  let recentEvents = $derived((status?.events ?? []).slice(-12).reverse());

  function evLabel(type: string): string {
    const known = [
      'online', 'offline', 'tunnel_up', 'tunnel_down',
      'closed', 'connect_request', 'rejected', 'platform_rejected',
    ];
    return known.includes(type) ? t(`dashboard.p2p.ev.${type}`) : type;
  }

  function rttPoints(v: P2pViewer) {
    return v.samples.map((s) => ({ t: Date.parse(s.at), v: s.rtt_ms }));
  }
  function downPoints(v: P2pViewer) {
    return v.samples.map((s) => ({ t: Date.parse(s.at), v: s.kbps_down }));
  }
  function fmtClock(iso: string): string {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return '--';
    const p = (n: number) => String(n).padStart(2, '0');
    return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
  }
</script>

{#if status?.enabled && status?.associated}
  <div class="card p-4 border th-border mb-6">
    <div class="flex items-center justify-between mb-3">
      <h3 class="text-sm font-semibold th-text-primary flex items-center gap-2">
        <Smartphone size={16} class="text-accent" />
        {t('dashboard.p2p.title')}
        {#if status.associ_name}
          <span class="text-xs th-text-muted font-normal">· {status.associ_name}</span>
        {/if}
      </h3>
      {#if viewers.length > 1}
        <span class="text-[11px] px-2 py-0.5 rounded-full font-medium th-bg-tertiary th-text-muted">
          ×{viewers.length}
        </span>
      {/if}
    </div>

    {#if viewers.length === 0}
      <p class="text-sm th-text-muted py-2">{t('dashboard.p2p.waiting')}</p>
    {:else}
      {#each viewers as v, i (v.viewer_id || v.self_addr)}
        <div class={i > 0 ? 'pt-4 mt-4 border-t th-border' : ''}>
          <div class="flex flex-wrap items-center gap-x-3 gap-y-1 mb-2 text-xs">
            <span
              class="text-[11px] px-2 py-0.5 rounded-full font-medium"
              class:th-bg-tertiary={v.stale}
              style={!v.stale
                ? 'background-color: color-mix(in srgb, var(--color-live, #22c55e) 16%, transparent); color: var(--color-live, #22c55e)'
                : ''}
            >
              {v.mode === 'relay' ? t('dashboard.p2p.relay') : t('dashboard.p2p.tunnel')}
            </span>
            <span class="font-mono th-text-primary">{v.viewer_id || v.self_addr || '--'}</span>
            {#if v.stale}
              <span class="text-amber-500">{t('dashboard.p2p.noRecent')}</span>
            {/if}
            {#if v.app_version}
              <span class="th-text-muted ml-auto">v{v.app_version.replace(/^v/, '')}</span>
            {/if}
          </div>

          <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
            <div>
              <MiniTimeChart points={rttPoints(v)} unit="ms" color="var(--color-info, #3b82f6)" />
              <div class="flex items-center justify-between mt-2 text-xs">
                <span class="th-text-muted">{t('dashboard.p2p.rtt')}</span>
                <span class="font-mono th-text-primary">{v.rtt_ms} ms</span>
              </div>
            </div>
            <div>
              <MiniTimeChart points={downPoints(v)} unit="kb/s" color="var(--color-live, #22c55e)" />
              <div class="flex items-center justify-between mt-2 text-xs">
                <span class="th-text-muted">{t('dashboard.p2p.rate')}</span>
                <span class="font-mono th-text-primary flex items-center gap-3">
                  <span class="inline-flex items-center gap-1">
                    <ArrowDown size={12} />{v.kbps_down} kb/s
                  </span>
                  <span class="inline-flex items-center gap-1">
                    <ArrowUp size={12} />{v.kbps_up} kb/s
                  </span>
                </span>
              </div>
            </div>
          </div>
          <div class="flex flex-wrap items-center gap-x-5 gap-y-1 mt-2 text-xs">
            <span class="th-text-muted">
              {t('dashboard.p2p.traffic')}
              <span class="font-mono th-text-primary ml-1">{formatFileSize(v.bytes_total)}</span>
            </span>
          </div>
        </div>
      {/each}
    {/if}

    <!-- 连接记录（链路事件，最新在上） -->
    <div class="mt-4 pt-3 border-t th-border">
      <h4 class="text-xs font-semibold th-text-secondary flex items-center gap-1.5 mb-2">
        <ScrollText size={13} />
        {t('dashboard.p2p.events')}
      </h4>
      {#if recentEvents.length === 0}
        <p class="text-xs th-text-muted">{t('dashboard.p2p.noEvents')}</p>
      {:else}
        <ul class="space-y-1 max-h-44 overflow-y-auto pr-1">
          {#each recentEvents as ev (ev.at + ev.type)}
            <li class="flex items-baseline gap-2 text-xs leading-5">
              <span class="font-mono th-text-muted shrink-0">{fmtClock(ev.at)}</span>
              <span
                class="shrink-0 font-medium"
                class:th-color-danger={ev.type === 'rejected' || ev.type === 'platform_rejected' || ev.type === 'offline' || ev.type === 'tunnel_down'}
              >{evLabel(ev.type)}</span>
              {#if ev.detail}
                <span class="th-text-muted truncate" title={ev.detail}>{ev.detail}</span>
              {/if}
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  </div>
{/if}
