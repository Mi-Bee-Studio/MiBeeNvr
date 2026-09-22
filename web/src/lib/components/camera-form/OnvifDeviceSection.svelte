<script lang="ts">
    import { t } from '$lib/i18n';
    import { getDeviceCapabilities, normalizeProtocol } from '$lib/api';
    import type { Camera, DeviceCapabilitiesInfo } from '$lib/api';
    import DeviceCapabilities from '$lib/components/DeviceCapabilities.svelte';
    import ImagingPanel from '$lib/components/ImagingPanel.svelte';
    import PresetManager from '$lib/components/PresetManager.svelte';
    import ONVIFEvents from '$lib/components/ONVIFEvents.svelte';
    import DeviceManagement from '$lib/components/DeviceManagement.svelte';

  interface Props {
    camera: Camera;
    /** Subnet-hints textarea content (one CIDR per line) — part of the save payload. */
    formSubnetHints?: string;
  }

  let { camera, formSubnetHints = $bindable('') }: Props = $props();

  // ONVIF capabilities
  let deviceCaps = $state<DeviceCapabilitiesInfo | null>(null);
  let capsLoading = $state(true);

  $effect(() => {
    loadCapabilities(camera);
  });

  async function loadCapabilities(cam: Camera) {
    if (normalizeProtocol(cam.protocol) !== 'onvif') {
      deviceCaps = null;
      capsLoading = false;
      return;
    }
    capsLoading = true;
    try {
      deviceCaps = await getDeviceCapabilities(cam.id);
    } catch (e) {
      console.warn('Failed to load device capabilities:', e);
      deviceCaps = null;
    } finally {
      capsLoading = false;
    }
  }
</script>

{#if normalizeProtocol(camera.protocol) === 'onvif' && !capsLoading}
  <div class="mt-6 space-y-4">
    <h4 class="text-sm font-semibold th-text-secondary uppercase tracking-wide">ONVIF</h4>

    <!-- Device Capabilities -->
    <DeviceCapabilities cameraId={camera.id} />

    <!-- IP self-healing: subnet hints (where to look when this camera's IP changes) -->
    <details class="border th-border rounded-lg">
      <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
        {t('cameras.subnetHintsTitle')}
      </summary>
      <div class="px-4 pb-4 space-y-2">
        <p class="text-xs th-text-muted">{t('cameras.subnetHintsHint')}</p>
        <textarea
          bind:value={formSubnetHints}
          rows="3"
          class="input font-mono text-xs"
          placeholder="192.168.1.0/24&#10;10.0.0.0/24"
        ></textarea>
        {#if camera.stable_id}
          <p class="text-xs th-text-muted">{t('cameras.subnetHintsStableId', { id: camera.stable_id })}</p>
        {:else}
          <p class="text-xs th-color-warning">{t('cameras.subnetHintsNoStableId')}</p>
        {/if}
      </div>
    </details>

    <!-- Imaging Panel (if supported) -->
    {#if deviceCaps?.imaging}
      <details class="border th-border rounded-lg" open>
        <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
          {t('onvif.imaging.title')}
        </summary>
        <div class="px-4 pb-4">
          <ImagingPanel cameraId={camera.id} />
        </div>
      </details>
    {/if}

    <!-- Preset Manager (if PTZ supported) -->
    {#if deviceCaps?.ptz}
      <details class="border th-border rounded-lg" open>
        <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
          {t('onvif.presets.title')}
        </summary>
        <div class="px-4 pb-4">
          <PresetManager cameraId={camera.id} />
        </div>
      </details>
    {/if}

    <!-- ONVIF Events (if supported) -->
    {#if deviceCaps?.events}
      <details class="border th-border rounded-lg">
        <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
          {t('onvif.events.title')}
        </summary>
        <div class="px-4 pb-4">
          <ONVIFEvents cameraId={camera.id} maxEvents={50} />
        </div>
      </details>
    {/if}

    <!-- Device Management -->
    <details class="border th-border rounded-lg">
      <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
        {t('onvif.device.title')}
      </summary>
      <div class="px-4 pb-4">
        <DeviceManagement cameraId={camera.id} cameraName={camera.name} />
      </div>
    </details>
  </div>
{/if}
