<script lang="ts">
  import { t } from '$lib/i18n';
  import { normalizeProtocol } from '$lib/api';
  import type { Camera, ProtocolInfo } from '$lib/api';
  import type { CameraHealth } from '$lib/api/health';
  import { Pencil, RotateCw, Eye, MoreVertical, Archive, Loader2, AlertCircle, RefreshCw, ArrowUpRight, WifiOff } from 'lucide-svelte';

  interface Props {
    camera: Camera;
    protocolsMap: Map<string, ProtocolInfo>;
    health?: CameraHealth;
    onedit: (camera: Camera) => void;
    ondelete: (camera: Camera) => void;
    onstart: (camera: Camera) => void;
    onstop: (camera: Camera) => void;
    onrestart: (camera: Camera) => void;
   onsaveName: (camera: Camera, name: string) => void;
   mergeStatus?: string;
   mergeProgress?: number;
   mergeError?: string;
   onRetryMerge?: (camera: Camera) => void;
   onrediscover?: (camera: Camera) => void;
 }

  let {
    camera,
    protocolsMap,
    health,
    onedit,
    ondelete,
    onstart,
    onstop,
    onrestart,
   onsaveName,
   mergeStatus = 'idle',
   mergeProgress = 0,
   mergeError = '',
   onRetryMerge,
   onrediscover
 }: Props = $props();

  let menuOpen = $state(false);
  let editingName = $state(false);
  let nameInput = $state('');
  $effect(() => { nameInput = camera.name; });

  let variant = $derived(
    camera.status === 'recording'
      ? 'active'
      : 'stopped'
  );

  let isHls = $derived(
    protocolsMap.get(normalizeProtocol(camera.protocol))?.capabilities?.hls ?? false
  );

  let protocolLabel = $derived(
    protocolsMap.get(camera.protocol)?.label ||
    t('cameras.protocol.' + camera.protocol) ||
    camera.protocol
  );

  let encodingLabel = $derived(
    camera.encoding ? (t('cameras.encoding.' + camera.encoding) || camera.encoding) : ''
  );
  let streamTransport = $derived.by(() => {
    const proto = camera.protocol;
    const enc = (camera.encoding || '').toLowerCase();
    if (proto === 'rtsp' || proto === 'rtsp_h264' || proto === 'rtsp_h265' || proto === 'rtsp_mjpeg') return 'rtsp';
    if (proto === 'http' || proto === 'http_jpeg') return 'http';
    if (proto === 'onvif' && (enc === 'h264' || enc === 'h265')) return 'rtsp';
    if (proto === 'onvif' && (enc === 'jpeg' || enc === 'mjpeg')) return 'http';
    return null;
  });

  function getHealthColor(status?: string): string {
    if (status === 'healthy') return 'bg-emerald-400';
    if (status === 'warning') return 'bg-amber-400';
    if (status === 'error') return 'bg-red-500';
    return 'bg-gray-400';
 }

 function getMergeBadgeClass(status?: string): string {
   if (status === 'merging') return 'badge-info';
   if (status === 'failed') return 'badge-error';
   return 'badge-neutral';
 }

let healthDotClass = $derived.by(() => {
  if (!health) return 'bg-gray-400';
  const st = health.status;
  if (camera.status === 'recording' && st === 'warning') return 'bg-amber-400 animate-pulse';
  if (camera.status === 'recording' && st === 'error') return 'bg-amber-400';
  return getHealthColor(st);
});

let healthShowWarningIcon = $derived(
  health?.status === 'error' && camera.status === 'recording'
);
  function closeMenu() {
    menuOpen = false;
  }

  function toggleMenu(e: MouseEvent) {
    e.stopPropagation();
    menuOpen = !menuOpen;
  }


  function startEditName() {
    nameInput = camera.name;
    editingName = true;
  }

  function saveName() {
    const trimmed = nameInput.trim();
    if (trimmed && trimmed !== camera.name) {
      onsaveName(camera, trimmed);
    }
    editingName = false;
  }

  function cancelEditName() {
    editingName = false;
    nameInput = camera.name;
  }

  $effect(() => {
    if (!menuOpen) return;
    const handler = (e: MouseEvent) => { menuOpen = false; };
    window.addEventListener('click', handler);
    return () => window.removeEventListener('click', handler);
  });
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  class="card camera-card border th-border p-4 transition-all {menuOpen ? 'is-menu-open' : ''}"
>
  <!-- Top: Name + Status -->
  <div class="flex items-start justify-between gap-2 mb-3">
    <div class="min-w-0 flex-1">
      {#if editingName}
        <input
          type="text"
          class="input py-1 px-2 text-sm w-full"
          bind:value={nameInput}
          onkeydown={(e) => {
            if (e.key === 'Enter') saveName();
            if (e.key === 'Escape') cancelEditName();
          }}
          onblur={saveName}
        />
      {:else}
        <button
          class="font-medium th-text-primary hover:underline cursor-pointer flex items-center gap-1.5 text-left"
          onclick={startEditName}
          title={t('cameras.editName')}
        >
          <span class="truncate">{camera.name}</span>
          <Pencil size={12} class="th-text-tertiary shrink-0" />
        </button>
      {/if}
    </div>
    <div class="shrink-0 flex items-center gap-1.5">
      {#if health}
        <div class="relative group" title={health.last_event?.message || health.status}>
          <span class="inline-flex items-center gap-0.5">
            <span class="inline-block h-2.5 w-2.5 rounded-full {healthDotClass}"></span>
            {#if healthShowWarningIcon}
              <AlertCircle size={10} class="text-amber-400" />
            {/if}
          </span>
          <div class="absolute bottom-full left-1/2 -translate-x-1/2 mb-2 hidden group-hover:block z-10">
            <div class="bg-gray-900 text-white text-xs rounded py-1 px-2 whitespace-nowrap shadow-lg">
              <div class="font-medium capitalize">{health.status}</div>
              {#if health.last_event}
                <div class="text-gray-400">{health.last_event.message}</div>
              {/if}
            </div>
          </div>
        </div>
      {/if}
      {#if camera.error_type === 'tutk_incompatible'}
        <span class="badge badge-error" title={camera.error_detail || ''}>
          {t('cameras.tutkCardBadge')}
        </span>
   {/if}
   {#if camera.error_type === 'connect_failed'}
     <span class="badge badge-error" title={camera.error_detail || ''}>
       {t('cameras.connectFailedBadge')}
     </span>
   {/if}
   <!-- Merge status badge -->
   {#if mergeStatus === 'merging'}
     <span class="badge badge-info flex items-center gap-1" title={mergeProgress + '%'} >
       <Loader2 size={10} class="animate-spin" />
       {mergeProgress}%
     </span>
   {:else if mergeStatus === 'failed'}
     <span class="badge badge-error flex items-center gap-1" title={mergeError || 'Merge failed'} >
       <AlertCircle size={10} />
       Failed
     </span>
   {/if}
   {#if camera.push_targets && camera.push_targets.length > 0}
     <span class="badge badge-info flex items-center gap-1" title={t('cameras.pushOutTitle')}>
       <ArrowUpRight size={10} />
       {camera.push_targets.filter(pt => pt.enabled).length}/{camera.push_targets.length}
     </span>
   {/if}
   {#if camera.status === 'recording'}
        <span class="badge badge-success">{t('cameras.statusRecording')}</span>
      {:else if camera.status === 'error'}
        <span class="badge badge-error">{t('cameras.statusError')}</span>
      {:else if camera.status === 'reconnecting'}
        <span class="badge badge-warning">{t('cameras.statusReconnecting')}</span>
      {:else}
        <span class="badge badge-neutral">{t('cameras.statusStopped')}</span>
      {/if}
    </div>
  </div>

  <!-- Middle: Protocol + Encoding + URL -->
  <div class="space-y-1.5 mb-3">
    <div class="flex items-center gap-2 flex-wrap">
      <span class="text-xs font-medium th-text-secondary px-2 py-0.5 rounded th-bg-tertiary">{protocolLabel}</span>
      {#if encodingLabel}
        <span class="text-xs th-text-tertiary px-2 py-0.5 rounded th-bg-tertiary">{encodingLabel}</span>
      {/if}
      {#if streamTransport === 'rtsp'}
        <span class="badge badge-info">{t('cameras.streamTransportRtsp')}</span>
      {:else if streamTransport === 'http'}
        <span class="badge badge-success">{t('cameras.streamTransportHttp')}</span>
      {/if}
    </div>
    <p class="text-xs th-text-tertiary truncate font-mono" title={camera.url}>{camera.url}</p>
  </div>

  <!-- Bottom: Action bar -->
  <div class="flex items-center justify-between pt-3 border-t th-border">
    <!-- Recording toggle (left) -->
    <div class="flex items-center gap-1.5">
      <button
        type="button"
        class="rec-toggle {camera.status === 'recording' || camera.status === 'reconnecting' ? 'is-on' : ''}"
        onclick={() => (camera.status === 'recording' || camera.status === 'reconnecting') ? onstop(camera) : onstart(camera)}
        onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); (camera.status === 'recording' || camera.status === 'reconnecting') ? onstop(camera) : onstart(camera); } }}
        role="switch"
        aria-checked={camera.status === 'recording' || camera.status === 'reconnecting'}
        aria-label={camera.status === 'recording' || camera.status === 'reconnecting' ? t('cameras.action.stopLabel') : t('cameras.action.startLabel')}
      >
        <span class="rec-toggle-thumb"></span>
      </button>
      <span class="text-xs th-text-tertiary">{camera.status === 'recording' || camera.status === 'reconnecting' ? t('cameras.action.stopLabel') : t('cameras.action.startLabel')}</span>
    </div>

    <!-- Action buttons (right) -->
    <div class="flex items-center gap-1">

        {#if camera.status === 'recording' || camera.status === 'error' || camera.status === 'reconnecting'}
          <button
            class="btn btn-ghost px-2 py-1 text-sm"
            onclick={() => onrestart(camera)}
            title={t('cameras.restart')}
            aria-label={t('cameras.action.restartLabel')}
          >
            <RotateCw size={14} />
            <span class="hidden sm:inline-flex ml-1.5 text-xs">{t('cameras.action.restartLabel')}</span>
          </button>
     {/if}
     <!-- Rediscover IP: re-locate an ONVIF camera whose address changed (AP
          reboot / roaming). Only shown for ONVIF cameras that are failing to
          connect AND have a stable_id (self-healing identity). -->
     {#if onrediscover && (camera.status === 'error' || camera.status === 'reconnecting') && normalizeProtocol(camera.protocol) === 'onvif' && camera.stable_id}
        <button
          class="btn btn-ghost px-2 py-1 text-sm text-sky-400 hover:text-sky-300"
          onclick={() => onrediscover(camera)}
          title={t('cameras.action.rediscoverHint')}
          aria-label={t('cameras.action.rediscoverLabel')}
        >
          <WifiOff size={14} />
          <span class="hidden sm:inline-flex ml-1.5 text-xs">{t('cameras.action.rediscoverLabel')}</span>
        </button>
     {/if}
     <!-- Retry merge button when merge failed -->
     {#if mergeStatus === 'failed' && onRetryMerge}
        <button
          class="btn btn-ghost px-2 py-1 text-sm text-amber-400 hover:text-amber-300"
          onclick={() => onRetryMerge(camera)}
          title={t('merge.retryMerge')}
          aria-label={t('merge.retryMerge')}
        >
          <RefreshCw size={14} />
        </button>
     {/if}

      {#if isHls}
        <a
          href="#/live/{camera.id}"
          class="btn btn-ghost px-2 py-1 text-sm"
          title={t('cameras.live')}
          aria-label={t('cameras.action.liveLabel')}
        >
          <Eye size={14} />
          <span class="hidden sm:inline-flex ml-1.5 text-xs">{t('cameras.action.liveLabel')}</span>
        </a>
      {/if}

      <!-- More menu -->
      <div class="relative">
        <button
          class="btn btn-ghost px-2 py-1 text-sm"
          onclick={toggleMenu}
          title={t('cameras.moreActions')}
          aria-label={t('cameras.moreActions')}
        >
          <MoreVertical size={14} />
        </button>

        {#if menuOpen}
          <div class="dropdown-menu" role="menu" tabindex="-1" onclick={(e) => e.stopPropagation()} onkeydown={(e) => e.stopPropagation()}>
            <button class="dropdown-item" onclick={() => { closeMenu(); onedit(camera); }}>
              <Pencil size={14} />
              {t('cameras.edit')}
            </button>
            <button class="dropdown-item dropdown-item--danger" onclick={() => { closeMenu(); ondelete(camera); }}>
              <Archive size={14} />
              {t('cameras.action.archive')}
            </button>
          </div>
        {/if}
      </div>
    </div>
  </div>
</div>

<style>
  .camera-card {
    display: flex;
    flex-direction: column;
    position: relative;
  }

  .camera-card.is-menu-open {
    z-index: 100;
  }

  /* Recording toggle switch */
  .rec-toggle {
    position: relative;
    display: inline-flex;
    align-items: center;
    width: 2.75rem;
    height: 1.5rem;
    border-radius: 9999px;
    background-color: var(--bg-tertiary);
    transition: background-color var(--duration-fast) var(--ease-out);
    border: none;
    cursor: pointer;
    padding: 0;
    flex-shrink: 0;
  }

  .rec-toggle.is-on {
    background-color: #22c55e;
  }

  .rec-toggle .rec-toggle-thumb {
    display: block;
    width: 1rem;
    height: 1rem;
    border-radius: 9999px;
    background-color: #ffffff;
    transition: transform var(--duration-fast) var(--ease-out);
    transform: translateX(0.25rem);
  }

  .rec-toggle.is-on .rec-toggle-thumb {
    transform: translateX(1.5rem);
  }

  .rec-toggle:focus-visible {
    box-shadow: var(--focus-ring);
    outline: none;
  }

  /* Dropdown menu */
  .dropdown-menu {
    position: absolute;
    right: 0;
    top: 100%;
    margin-top: 0.25rem;
    min-width: 10rem;
    background-color: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    box-shadow: var(--shadow-lg);
    z-index: 9999;
    padding: 0.25rem 0;
    animation: dropdown-enter 0.12s var(--ease-out);
  }

  @keyframes dropdown-enter {
    from {
      opacity: 0;
      transform: translateY(-4px);
    }
    to {
      opacity: 1;
      transform: translateY(0);
    }
  }

  .dropdown-item {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    padding: 0.5rem 0.75rem;
    font-size: 0.8125rem;
    color: var(--text-primary);
    background: transparent;
    border: none;
    cursor: pointer;
    text-align: left;
    text-decoration: none;
    transition: background-color var(--duration-fast) var(--ease-out);
  }

  .dropdown-item:hover {
    background-color: var(--bg-hover);
  }

  .dropdown-item--danger {
    color: var(--color-danger);
  }

  .dropdown-item--danger:hover {
    background-color: rgba(239, 68, 68, 0.1);
  }
</style>
