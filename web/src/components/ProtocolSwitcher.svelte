<script lang="ts">
  import { onMount } from 'svelte';
  import { t } from '$lib/i18n';
  import { apiRequest } from '$lib/api';

  export type StreamingProtocol = 'hls' | 'll-hls' | 'webrtc' | 'flv';

  interface ProtocolOption {
    id: StreamingProtocol;
    label: string;
    latency: string;
    viewers: string;
    resource: string;
  }

  interface CameraProtocol {
    protocol: string;
    available: boolean;
    reason?: string;
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
  let dropdownEl: HTMLDivElement | undefined = $state();

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
    return availableProtocols.includes(protocol);
  }

  async function loadProtocols() {
    loading = true;
    try {
      const result = await apiRequest<{ protocols: CameraProtocol[] }>(`/cameras/${cameraId}/protocols`);
      availableProtocols = result.protocols
        .filter(p => p.available)
        .map(p => p.protocol);
    } catch (e) {
      console.warn('Failed to load protocols:', e);
      // Fallback: determine from encoding
      const encoding = (cameraEncoding || '').toLowerCase();
      availableProtocols = ['hls']; // HLS always available for H.264/H.265
      if (encoding === 'h264') {
        availableProtocols.push('webrtc');
      }
      if (encoding === 'h264' || encoding === 'h265') {
        availableProtocols.push('flv');
        availableProtocols.push('ll-hls');
      }
    } finally {
      loading = false;
    }
  }

  function selectProtocol(protocol: StreamingProtocol) {
    if (!isAvailable(protocol)) return;
    open = false;
    onchange?.(protocol);
  }

  function toggleDropdown() {
    open = !open;
  }

  function handleClickOutside(e: MouseEvent) {
    if (dropdownEl && !dropdownEl.contains(e.target as Node)) {
      open = false;
    }
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
      <span>{currentOption.label}</span>
      <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="transition-transform {open ? 'rotate-180' : ''}"><polyline points="6 9 12 15 18 9"></polyline></svg>
    {/if}
  </button>

  <!-- Dropdown -->
  {#if open && !loading}
    <div class="absolute top-full left-0 mt-1 w-56 rounded-lg shadow-lg border th-border th-bg-elevated z-50 overflow-hidden">
      {#each protocolOptions as option (option.id)}
        {@const available = isAvailable(option.id)}
        {@const isActive = selected === option.id}
        <button
          onclick={() => selectProtocol(option.id)}
          disabled={!available}
          class="w-full px-3 py-2.5 text-left transition-colors {isActive ? 'bg-[var(--color-primary)]/10' : available ? 'hover:th-bg-hover' : 'opacity-40 cursor-not-allowed'}"
        >
          <div class="flex items-center justify-between">
            <div class="flex items-center gap-2">
              <span class="text-sm font-medium th-text-primary">{option.label}</span>
              {#if isActive}
                <div class="w-1.5 h-1.5 rounded-full bg-[var(--color-primary)]"></div>
              {/if}
            </div>
            {#if !available}
              <span class="text-[10px] th-text-tertiary">{t('live.protocol.unavailable')}</span>
            {/if}
          </div>
          <div class="flex items-center gap-3 mt-1">
            <span class="text-[10px] th-text-tertiary">{option.latency}</span>
            <span class="text-[10px] th-text-tertiary">{option.resource}</span>
          </div>
        </button>
      {/each}
    </div>
  {/if}
</div>
