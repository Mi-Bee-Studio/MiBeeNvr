<script lang="ts">
  import { onMount } from 'svelte';
  import { t } from '$lib/i18n';
  import { apiRequest } from '$lib/api';
  import { showToast } from '$lib/toast';
  import { detectWebCodecs, getWebCodecsUnavailableReason, detectWasmH265 } from '$lib/webcodecs-player/capabilities';
  import { setCameraProtocolOverride, clearCameraProtocolOverride } from '$lib/preferences';

  export type StreamingProtocol = 'wasm' | 'hls' | 'll-hls' | 'webrtc' | 'flv' | 'mjpeg' | 'auto';

  interface ProtocolOption {
    id: StreamingProtocol;
    label: string;
    latency: string;
    viewers: string;
    resource: string;
  }

  interface CameraProtocol {
    Protocol: string;
    Available: boolean;
    Reason: string;
  }

  interface ProtocolsResponse {
    protocols: CameraProtocol[];
    encoding: string;
    default: string;
  }

  let {
    cameraId,
    cameraEncoding = '',
    selected = 'auto',
    onchange,
  }: {
    cameraId: string;
    cameraEncoding?: string;
    selected?: StreamingProtocol;
    /** Fired on explicit user selection. `null` (clears the override → back to
     *  auto) is only emitted for the 'auto' option; protocol picks emit the
     *  protocol id. The initial auto-selection is owned by the Player
     *  Orchestrator, so this component does NOT emit onchange on mount. */
    onchange?: (protocol: StreamingProtocol | null) => void;
  } = $props();

  let availableProtocols = $state<string[]>([]);
  let protocolReasons = $state<Record<string, string>>({});
  let loading = $state(true);
  let open = $state(false);
  let tooltipId = $state<string | null>(null);
  let dropdownEl: HTMLDivElement | undefined = $state();

  let isH265 = $derived((cameraEncoding || '').toLowerCase() === 'h265');
  let browserSupportsWasm = $state(false);
  let browserSupportsWasmH265 = $state(false);
  let wasmUnavailableReason: string | null = $state(null);

  let protocolOptions = [
    // 'auto' is the default and recommended choice — the Player Orchestrator
    // picks the lowest-latency protocol the browser+codec can handle and
    // adapts at runtime. Selecting a specific protocol below pins it (disables
    // auto-degrade/upgrade) for users who want explicit control.
	{ id: 'auto', label: t('live.protocol.auto') || 'Auto', latency: t('live.protocol.autoLatency') || 'adaptive', viewers: '', resource: '' },
	{ id: 'wasm', label: 'WebCodecs', latency: '<100ms', viewers: t('live.protocol.viewers.webrtc'), resource: t('live.protocol.resource.webrtc') },
	{ id: 'webrtc', label: 'WebRTC', latency: t('live.protocol.latency.webrtc'), viewers: t('live.protocol.viewers.webrtc'), resource: t('live.protocol.resource.webrtc') },
	{ id: 'flv', label: 'HTTP-FLV', latency: t('live.protocol.latency.flv'), viewers: t('live.protocol.viewers.flv'), resource: t('live.protocol.resource.flv') },
	{ id: 'mjpeg', label: 'MJPEG', latency: t('live.protocol.latency.mjpeg'), viewers: t('live.protocol.viewers.mjpeg'), resource: t('live.protocol.resource.mjpeg') },
	{ id: 'hls', label: 'HLS', latency: t('live.protocol.latency.hls'), viewers: t('live.protocol.viewers.hls'), resource: t('live.protocol.resource.hls') },
	{ id: 'll-hls', label: 'LL-HLS', latency: t('live.protocol.latency.llHls'), viewers: t('live.protocol.viewers.hls'), resource: t('live.protocol.resource.hls') },
  ];

  let currentOption = $derived(
    protocolOptions.find(p => p.id === selected) || protocolOptions.find(p => p.id === 'auto') || protocolOptions[0],
  );

  // Always show all options — wasm is marked unavailable with tooltip instead of hidden
  let visibleOptions = $derived(protocolOptions);

  function isAvailable(protocol: StreamingProtocol): boolean {
    // 'auto' is always available — it delegates to the orchestrator.
    if (protocol === 'auto') return true;
    // WebCodecs (HTTPS) for any codec, OR libde265 WASM fallback for H.265 (HTTP)
    if (protocol === 'wasm' && !browserSupportsWasm && !(isH265 && browserSupportsWasmH265)) return false;
    // H.265 cameras cannot use WebRTC
    if (protocol === 'webrtc' && isH265) return false;
    return availableProtocols.includes(protocol);
  }

  function getUnavailableReason(protocol: StreamingProtocol): string | null {
    if (protocol === 'auto') return null;
    if (protocol === 'wasm' && !browserSupportsWasm && !(isH265 && browserSupportsWasmH265)) {
      // Only show the WebCodecs reason when WASM H.265 fallback also can't help
      if (!isH265 || !browserSupportsWasmH265) {
        return wasmUnavailableReason || 'Browser does not support WebCodecs';
      }
    }
    if (protocol === 'webrtc' && isH265) {
      return t('live.protocol.tooltip.h265Note');
    }
    if (!availableProtocols.includes(protocol)) {
      const backendReason = protocolReasons[protocol];
      if (backendReason) return backendReason;
      return t('live.protocol.unavailable');
    }
    return null;
  }

  async function loadProtocols() {
    loading = true;
    try {
      const result = await apiRequest<ProtocolsResponse>(`/cameras/${cameraId}/protocols`);
      availableProtocols = result.protocols
        .filter(p => p.available)
        .map(p => p.protocol);
      // Store backend reasons for unavailable protocols
      const reasons: Record<string, string> = {};
      for (const p of result.protocols) {
        if (!p.available && p.reason) {
          reasons[p.protocol] = p.reason;
        }
      }
      protocolReasons = reasons;
      // NOTE: we no longer emit onchange here. The Player Orchestrator owns the
      // initial protocol selection (it built the candidate chain from this same
      // /protocols response + browser caps). This component only emits onchange
      // on an explicit user click in selectProtocol() — so the dropdown reflects
      // the orchestrator's current mode (passed in via `selected`), and a click
      // pins/clears the override. Emitting onchange on mount would re-pin the
      // backend default and disable the orchestrator's adaptive degrade/upgrade.
    } catch (e) {
      console.warn('Failed to load protocols:', e);
      const encoding = (cameraEncoding || '').toLowerCase();
      availableProtocols = ['hls'];
      protocolReasons = {};
      if (encoding === 'h264') {
        availableProtocols.push('webrtc');
      }
      if (encoding === 'h264' || encoding === 'h265') {
        availableProtocols.push('flv');
        availableProtocols.push('ll-hls');
      }
      if (browserSupportsWasm) {
        availableProtocols.push('wasm');
      }
    } finally {
      loading = false;
      // The H.265-on-WebRTC guard previously fired onchange here to demote an
      // invalid selection. With the orchestrator owning selection, that demote
      // is handled at chain-build time (WebRTC is never in an H.265 chain), so
      // there's nothing to do here.
    }
  }

  function selectProtocol(protocol: StreamingProtocol) {
    // 'auto' clears any per-camera override → back to orchestrator auto-selection.
    if (protocol === 'auto') {
      open = false;
      clearCameraProtocolOverride(cameraId);
      onchange?.(null);
      return;
    }
    if (!isAvailable(protocol)) {
      // Show H.265 fallback info if user clicks WebRTC on H.265 camera
      if (protocol === 'webrtc' && isH265) {
        showToast(t('live.h265.webrtcUnavailable'), 'warning');
      }
      return;
    }
    open = false;
    // Persist the user's explicit manual choice so it survives navigation and
    // is honored by the orchestrator (pins the chain — disables auto-degrade/
    // upgrade to respect the explicit selection). This is the ONLY writer.
    setCameraProtocolOverride(cameraId, protocol);
    onchange?.(protocol);
  }

  function toggleDropdown() {
    open = !open;
  }

  function handleClickOutside(e: MouseEvent) {
    if (dropdownEl && !dropdownEl.contains(e.target as Node)) {
      open = false;
      tooltipId = null;
    }
  }

  function showTooltip(id: string) {
    tooltipId = tooltipId === id ? null : id;
  }

  onMount(() => {
    browserSupportsWasm = detectWebCodecs();
    browserSupportsWasmH265 = detectWasmH265();
    if (!browserSupportsWasm) {
      wasmUnavailableReason = getWebCodecsUnavailableReason();
    }
    loadProtocols();
    document.addEventListener('click', handleClickOutside);
    return () => {
      document.removeEventListener('click', handleClickOutside);
    };
  });
</script>

<div class="relative inline-block" bind:this={dropdownEl}>
  <!-- Trigger button -->
  <button
    onclick={toggleDropdown}
    class="flex items-center gap-1.5 px-2.5 py-1 rounded-md text-xs font-medium transition-colors th-text-primary th-bg-tertiary hover:th-bg-hover border th-border"
    title={t('live.protocol.select')}
    disabled={loading}
  >
    {#if loading}
      <div class="w-3 h-3 border-2 border-white/20 border-t-white/60 rounded-full animate-spin"></div>
    {:else}
      {#if isH265}
        <span class="px-1 py-0.5 text-[10px] font-semibold rounded bg-[var(--color-warning)]/20 text-[var(--color-warning-light)] border border-[var(--color-warning)]/30">{t('live.h265.badge')}</span>
      {/if}
      <span>{currentOption.label}</span>
      <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="transition-transform {open ? 'rotate-180' : ''}"><polyline points="6 9 12 15 18 9"></polyline></svg>
    {/if}
  </button>

  <!-- Dropdown -->
  {#if open && !loading}
    <div class="absolute top-full left-0 mt-1 w-60 rounded-lg shadow-lg border th-border th-bg-elevated z-50 overflow-hidden">
      {#each visibleOptions as option (option.id)}
        {@const available = isAvailable(option.id)}
        {@const isActive = selected === option.id}
        {@const reason = getUnavailableReason(option.id)}
        <div
          role="button"
          tabindex="0"
          onclick={() => selectProtocol(option.id)}
          onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); selectProtocol(option.id); } }}
          class="w-full px-3 py-2.5 text-left transition-colors {isActive ? 'bg-[var(--color-primary)]/10' : available ? 'hover:th-bg-hover' : 'opacity-40 cursor-not-allowed'}"
        >
          <div class="flex items-center justify-between">
            <div class="flex items-center gap-2">
              <span class="text-sm font-medium th-text-primary">{option.label}</span>
              {#if isActive}
                <div class="w-1.5 h-1.5 rounded-full bg-[var(--color-primary)]"></div>
              {/if}
              <!-- Info tooltip trigger -->
              <button
                type="button"
                onclick={(e: MouseEvent) => { e.stopPropagation(); showTooltip(option.id); }}
                class="text-[var(--text-tertiary)] hover:text-[var(--text-secondary)] transition-colors"
                title="{t('live.protocol.tooltip.latency')}: {option.latency} | {t('live.protocol.tooltip.viewers')}: {option.viewers} | {t('live.protocol.tooltip.cpu')}: {option.resource}"
              >
                <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"></circle><line x1="12" y1="16" x2="12" y2="12"></line><line x1="12" y1="8" x2="12.01" y2="8"></line></svg>
              </button>
            </div>
            {#if reason}
              <span class="text-[10px] th-text-tertiary">{reason}</span>
            {/if}
          </div>
          <div class="flex items-center gap-3 mt-1">
            <span class="text-[10px] th-text-tertiary">{option.latency}</span>
            <span class="text-[10px] th-text-tertiary">{option.resource}</span>
          </div>
          <!-- Expanded tooltip panel -->
          {#if tooltipId === option.id}
            <div class="mt-2 pt-2 border-t th-border text-[10px] th-text-tertiary space-y-1">
              <div class="flex items-center gap-1.5">
                <span class="font-medium">{t('live.protocol.tooltip.latency')}:</span>
                <span>{option.latency}</span>
              </div>
              <div class="flex items-center gap-1.5">
                <span class="font-medium">{t('live.protocol.tooltip.viewers')}:</span>
                <span>{option.viewers}</span>
              </div>
              <div class="flex items-center gap-1.5">
                <span class="font-medium">{t('live.protocol.tooltip.cpu')}:</span>
                <span>{option.resource}</span>
              </div>
              {#if option.id === 'webrtc' && isH265}
                <div class="flex items-center gap-1.5 text-[var(--color-warning)]">
                  <svg xmlns="http://www.w3.org/2000/svg" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z"></path><line x1="12" y1="9" x2="12" y2="13"></line><line x1="12" y1="17" x2="12.01" y2="17"></line></svg>
                  <span>{t('live.protocol.tooltip.h265Note')}</span>
                </div>
              {/if}
              {#if option.id === 'flv' && isH265}
                <div class="flex items-center gap-1.5 text-[var(--color-warning)]">
                  <svg xmlns="http://www.w3.org/2000/svg" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z"></path><line x1="12" y1="9" x2="12" y2="13"></line><line x1="12" y1="17" x2="12.01" y2="17"></line></svg>
                  <span>{t('live.protocol.tooltip.flvH265Note')}</span>
                </div>
              {/if}
              {#if !available && reason}
                <div class="flex items-center gap-1.5 text-[var(--color-warning)]">
                  <svg xmlns="http://www.w3.org/2000/svg" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z"></path><line x1="12" y1="9" x2="12" y2="13"></line><line x1="12" y1="17" x2="12.01" y2="17"></line></svg>
                  <span>{reason}</span>
                </div>
              {/if}
            </div>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</div>
