<script lang="ts">
  import { onMount } from 'svelte';
  import { t } from '$lib/i18n';
  import { apiRequest } from '$lib/api';
  import { showToast } from '$lib/toast';

  export type StreamingProtocol = 'hls' | 'll-hls' | 'webrtc' | 'flv';

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
    selected = 'hls',
    onchange,
  }: {
    cameraId: string;
    cameraEncoding?: string;
    selected?: StreamingProtocol;
    onchange?: (protocol: StreamingProtocol) => void;
  } = $props();

  let availableProtocols = $state<string[]>([]);
  let loading = $state(true);
  let open = $state(false);
  let tooltipId = $state<string | null>(null);
  let dropdownEl: HTMLDivElement | undefined = $state();

  let isH265 = $derived((cameraEncoding || '').toLowerCase() === 'h265');

  const protocolOptions: ProtocolOption[] = [
    { id: 'webrtc', label: 'WebRTC', latency: t('live.protocol.latency.webrtc'), viewers: t('live.protocol.viewers.webrtc'), resource: t('live.protocol.resource.webrtc') },
    { id: 'flv', label: 'HTTP-FLV', latency: t('live.protocol.latency.flv'), viewers: t('live.protocol.viewers.flv'), resource: t('live.protocol.resource.flv') },
    { id: 'hls', label: 'HLS', latency: t('live.protocol.latency.hls'), viewers: t('live.protocol.viewers.hls'), resource: t('live.protocol.resource.hls') },
    { id: 'll-hls', label: 'LL-HLS', latency: t('live.protocol.latency.llHls'), viewers: t('live.protocol.viewers.hls'), resource: t('live.protocol.resource.hls') },
  ];

  let currentOption = $derived(
    protocolOptions.find(p => p.id === selected) || protocolOptions[2],
  );

  function isAvailable(protocol: StreamingProtocol): boolean {
    // H.265 cameras cannot use WebRTC
    if (protocol === 'webrtc' && isH265) return false;
    return availableProtocols.includes(protocol);
  }

  /**
   * Returns the reason a protocol is unavailable, or null if available.
   */
  function getUnavailableReason(protocol: StreamingProtocol): string | null {
    if (protocol === 'webrtc' && isH265) {
      return t('live.protocol.tooltip.h265Note');
    }
    if (!availableProtocols.includes(protocol)) {
      return t('live.protocol.unavailable');
    }
    return null;
  }

  async function loadProtocols() {
    loading = true;
    try {
      const result = await apiRequest<ProtocolsResponse>(`/cameras/${cameraId}/protocols`);
      availableProtocols = result.protocols
        .filter(p => p.Available)
        .map(p => p.Protocol);
      // Auto-select the server's default if different from current selection
      if (result.default && result.default !== selected && isAvailable(result.default as StreamingProtocol)) {
        onchange?.(result.default as StreamingProtocol);
      }
    } catch (e) {
      console.warn('Failed to load protocols:', e);
      const encoding = (cameraEncoding || '').toLowerCase();
      availableProtocols = ['hls'];
      if (encoding === 'h264') {
        availableProtocols.push('webrtc');
      }
      if (encoding === 'h264' || encoding === 'h265') {
        availableProtocols.push('flv');
        availableProtocols.push('ll-hls');
      }
    } finally {
      loading = false;
      // Auto-fallback: if H.265 and selected is WebRTC, switch to FLV or HLS
      if (isH265 && selected === 'webrtc') {
        handleH265Fallback();
      }
    }
  }

  function handleH265Fallback() {
    const fallbackProtocol: StreamingProtocol = availableProtocols.includes('flv') ? 'flv' : 'hls';
    const protocolLabel = fallbackProtocol === 'flv' ? 'HTTP-FLV' : 'HLS';
    showToast(
      t('live.h265.fallbackMessage', { protocol: protocolLabel }),
      'warning',
    );
    onchange?.(fallbackProtocol);
  }

  function selectProtocol(protocol: StreamingProtocol) {
    if (!isAvailable(protocol)) {
      // Show H.265 fallback info if user clicks WebRTC on H.265 camera
      if (protocol === 'webrtc' && isH265) {
        showToast(t('live.h265.webrtcUnavailable'), 'warning');
      }
      return;
    }
    open = false;
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
      {#each protocolOptions as option (option.id)}
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
            </div>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</div>
