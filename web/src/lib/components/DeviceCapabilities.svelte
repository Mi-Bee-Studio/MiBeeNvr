<script lang="ts">
  import { getDeviceCapabilities } from '$lib/api';
  import type { DeviceCapabilitiesInfo } from '$lib/api';
  import { Move, Image, Bell, Camera, Radio } from 'lucide-svelte';
  import { t } from '$lib/i18n';
  let { cameraId }: { cameraId: string } = $props();

  let caps = $state<DeviceCapabilitiesInfo | null>(null);
  let loading = $state(true);
  let error = $state('');

  $effect(() => {
    cameraId;
    loadCapabilities();
  });

  async function loadCapabilities() {
    loading = true;
    error = '';
    try {
      caps = await getDeviceCapabilities(cameraId);
    } catch (e: any) {
      error = e.message || t('onvif.capabilities.loadError');
    } finally {
      loading = false;
    }
  }

  interface CapabilityBadge {
    key: string;
    label: string;
    icon: any;
    enabled: boolean;
  }

  let badges = $derived<CapabilityBadge[]>(caps ? [
    { key: 'ptz', label: t('onvif.capabilities.ptz'), icon: Move, enabled: caps.ptz },
    { key: 'imaging', label: t('onvif.capabilities.imaging'), icon: Image, enabled: caps.imaging },
    { key: 'events', label: t('onvif.capabilities.events'), icon: Bell, enabled: caps.events },
    { key: 'snapshot', label: t('onvif.capabilities.snapshot'), icon: Camera, enabled: caps.snapshot },
    { key: 'streaming', label: t('onvif.capabilities.streaming'), icon: Radio, enabled: caps.streaming },
  ] : []);
</script>

<div class="caps-panel">
  {#if loading}
    <div class="caps-loading">
      <span class="spinner"></span>
    </div>
  {:else if error}
    <div class="caps-error">{error}</div>
  {:else if caps}
    <!-- Device info -->
    {#if caps.device_info}
      <div class="caps-device-info">
        {#if caps.device_info.manufacturer}
          <span class="caps-info-item">{caps.device_info.manufacturer}</span>
        {/if}
        {#if caps.device_info.model}
          <span class="caps-info-sep">·</span>
          <span class="caps-info-item">{caps.device_info.model}</span>
        {/if}
        {#if caps.device_info.firmware}
          <span class="caps-info-sep">·</span>
          <span class="caps-info-item caps-info-fw">FW: {caps.device_info.firmware}</span>
        {/if}
      </div>
    {/if}

    <!-- Capability badges -->
    <div class="caps-badges">
      {#each badges as badge (badge.key)}
        <span class="caps-badge" class:caps-badge-on={badge.enabled} class:caps-badge-off={!badge.enabled}>
          <badge.icon size={12} />
          {badge.label}
        </span>
      {/each}
    </div>
  {/if}
</div>

<style>
  .caps-panel {
    padding: 0.75rem;
    background-color: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
  }

  .caps-loading {
    display: flex;
    justify-content: center;
    padding: 0.75rem 0;
  }

  .caps-error {
    font-size: 0.75rem;
    color: var(--color-danger);
    text-align: center;
    padding: 0.25rem 0.5rem;
    background-color: rgba(239, 68, 68, 0.1);
    border-radius: var(--radius-sm);
  }

  .caps-device-info {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 0.25rem;
    margin-bottom: 0.625rem;
    font-size: 0.75rem;
    color: var(--text-primary);
    font-weight: 500;
  }

  .caps-info-sep {
    color: var(--text-tertiary);
  }

  .caps-info-fw {
    color: var(--text-tertiary);
    font-weight: 400;
    font-size: 0.6875rem;
  }

  .caps-badges {
    display: flex;
    flex-wrap: wrap;
    gap: 0.375rem;
  }

  .caps-badge {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    padding: 0.25rem 0.5rem;
    border-radius: 9999px;
    font-size: 0.6875rem;
    font-weight: 500;
    border: 1px solid transparent;
    transition: all var(--duration-fast) var(--ease-out);
  }

  .caps-badge-on {
    background-color: rgba(16, 185, 129, 0.1);
    color: var(--color-success-light);
    border-color: rgba(16, 185, 129, 0.2);
  }

  .caps-badge-off {
    background-color: var(--bg-tertiary);
    color: var(--text-tertiary);
    border-color: var(--border);
    opacity: 0.6;
  }
</style>
