<script lang="ts">
  import { getSnapshotUri, API_BASE, getAuthHeader } from '$lib/api';
  import { Camera } from 'lucide-svelte';
  import { t } from '$lib/i18n';
  let { cameraId }: { cameraId: string } = $props();

  let loading = $state(false);
  let supported = $state(true);
  let snapshotUri = $state('');

  $effect(() => {
    cameraId;
    checkSnapshot();
  });

  async function checkSnapshot() {
    loading = true;
    try {
      const resp = await getSnapshotUri(cameraId);
      snapshotUri = resp.uri;
      supported = true;
    } catch {
      supported = false;
      snapshotUri = '';
    } finally {
      loading = false;
    }
  }

  function handleSnapshot() {
    if (!snapshotUri) return;
    // Build URL with auth for ONVIF snapshot URIs that require it
    const url = snapshotUri.startsWith('http')
      ? snapshotUri
      : `${API_BASE}/cameras/${cameraId}/snapshot`;

    // Open in new tab with auth embedded if needed
    const auth = getAuthHeader();
    if (auth && url.startsWith('/')) {
      // Relative URL — just open, browser has session
      window.open(url, '_blank');
    } else {
      window.open(url, '_blank');
    }
  }
</script>

{#if supported}
  <button
    class="btn btn-ghost snapshot-btn"
    onclick={handleSnapshot}
    disabled={loading || !snapshotUri}
    title={loading ? t('onvif.snapshot.loading') : t('onvif.snapshot.takeSnapshot')}
  >
    <Camera size={14} />
    <span class="snapshot-btn-label">{t('onvif.snapshot.title')}</span>
  </button>
{:else}
  <button
    class="btn btn-ghost snapshot-btn"
    disabled
    title="Snapshot not supported by this camera"
  >
    <Camera size={14} />
    <span class="snapshot-btn-label">{t('onvif.snapshot.title')}</span>
  </button>
{/if}

<style>
  .snapshot-btn {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    padding: 0.375rem 0.625rem;
    font-size: 0.75rem;
  }

  .snapshot-btn-label {
    font-weight: 500;
  }
</style>
