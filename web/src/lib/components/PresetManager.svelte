<script lang="ts">
  import { getPTZPresets, createPTZPreset, goToPTZPreset, deletePTZPreset } from '$lib/api';
  import type { PTZPreset } from '$lib/api';
  import { showToast } from '$lib/toast';
  import { t } from '$lib/i18n';
  let { cameraId }: { cameraId: string } = $props();

  let presets = $state<PTZPreset[]>([]);
  let loading = $state(true);
  let error = $state('');
  let newPresetName = $state('');
  let adding = $state(false);
  let goingTo = $state<string | null>(null);
  let deleting = $state<string | null>(null);

  $effect(() => {
    cameraId;
    loadPresets();
  });

  async function loadPresets() {
    loading = true;
    error = '';
    try {
      presets = await getPTZPresets(cameraId);
    } catch (e: any) {
      error = e.message || t('onvif.presets.loadError');
    } finally {
      loading = false;
    }
  }

  async function handleAdd() {
    const name = newPresetName.trim();
    if (!name) return;
    adding = true;
    try {
      await createPTZPreset(cameraId, name);
      newPresetName = '';
      await loadPresets();
      showToast(t('onvif.presets.created'), 'success');
    } catch (e: any) {
      showToast(e.message || t('onvif.presets.failed'), 'error');
    } finally {
      adding = false;
    }
  }

  async function handleGoTo(token: string) {
    goingTo = token;
    try {
      await goToPTZPreset(cameraId, token);
    } catch (e: any) {
      showToast(e.message || 'Failed to go to preset', 'error');
    } finally {
      goingTo = null;
    }
  }

  async function handleDelete(token: string, name: string) {
    if (!confirm(t('onvif.presets.confirmDelete', { name }))) return;
    deleting = token;
    try {
      await deletePTZPreset(cameraId, token);
      await loadPresets();
      showToast(t('onvif.presets.deleted'), 'success');
    } catch (e: any) {
      showToast(e.message || 'Failed to delete preset', 'error');
    } finally {
      deleting = null;
    }
  }
</script>

<div class="preset-panel">
  <div class="preset-header">
    <span class="preset-title">{t('onvif.presets.title')}</span>
  </div>

  {#if loading}
    <div class="preset-loading">
      <span class="spinner"></span>
    </div>
  {:else if error}
    <div class="preset-error">{error}</div>
  {:else}
    <!-- Add preset -->
    <div class="preset-add">
      <input
        type="text"
        class="input preset-input"
        placeholder={t('onvif.presets.name')}
        bind:value={newPresetName}
        onkeydown={(e) => { if (e.key === 'Enter') handleAdd(); }}
      />
      <button
        class="btn btn-secondary preset-add-btn"
        onclick={handleAdd}
        disabled={adding || !newPresetName.trim()}
      >
        {#if adding}
          <span class="spinner"></span>
        {:else}
          {t('onvif.presets.add')}
        {/if}
      </button>
    </div>

    <!-- Preset list -->
    {#if presets.length === 0}
      <div class="preset-empty">{t('onvif.presets.noPresets')}</div>
    {:else}
      <div class="preset-list">
        {#each presets as preset (preset.token)}
          <div class="preset-row">
            <div class="preset-info">
              <span class="preset-name">{preset.name}</span>
              <span class="preset-token">{preset.token}</span>
            </div>
            <div class="preset-actions">
              <button
                class="btn btn-ghost preset-action-btn"
                onclick={() => handleGoTo(preset.token)}
                disabled={goingTo === preset.token}
              >
                {#if goingTo === preset.token}
                  <span class="spinner"></span>
                {:else}
                  {t('onvif.presets.goTo')}
                {/if}
              </button>
              <button
                class="btn btn-ghost preset-action-btn preset-delete-btn"
                onclick={() => handleDelete(preset.token, preset.name)}
                disabled={deleting === preset.token}
              >
                {#if deleting === preset.token}
                  <span class="spinner"></span>
                {:else}
                  {t('onvif.presets.delete')}
                {/if}
              </button>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
</div>

<style>
  .preset-panel {
    padding: 0.75rem;
    background-color: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
  }

  .preset-header {
    margin-bottom: 0.75rem;
  }

  .preset-title {
    font-size: 0.75rem;
    font-weight: 500;
    color: var(--text-secondary);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }

  .preset-loading {
    display: flex;
    justify-content: center;
    padding: 1rem 0;
  }

  .preset-error {
    font-size: 0.75rem;
    color: var(--color-danger);
    text-align: center;
    padding: 0.25rem 0.5rem;
    background-color: rgba(239, 68, 68, 0.1);
    border-radius: var(--radius-sm);
  }

  .preset-add {
    display: flex;
    gap: 0.375rem;
    margin-bottom: 0.75rem;
  }

  .preset-input {
    font-size: 0.75rem;
    padding: 0.375rem 0.5rem;
  }

  .preset-add-btn {
    padding: 0.375rem 0.625rem;
    font-size: 0.75rem;
    white-space: nowrap;
  }

  .preset-empty {
    font-size: 0.75rem;
    color: var(--text-tertiary);
    text-align: center;
    padding: 0.5rem 0;
  }

  .preset-list {
    display: flex;
    flex-direction: column;
    gap: 2px;
    max-height: 12rem;
    overflow-y: auto;
  }

  .preset-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.375rem 0.5rem;
    border-radius: var(--radius-sm);
    transition: background-color var(--duration-fast) var(--ease-out);
  }

  .preset-row:hover {
    background-color: var(--bg-hover);
  }

  .preset-info {
    display: flex;
    flex-direction: column;
    min-width: 0;
    flex: 1;
  }

  .preset-name {
    font-size: 0.75rem;
    font-weight: 500;
    color: var(--text-primary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .preset-token {
    font-size: 0.625rem;
    color: var(--text-tertiary);
    font-family: monospace;
  }

  .preset-actions {
    display: flex;
    gap: 2px;
    flex-shrink: 0;
  }

  .preset-action-btn {
    padding: 0.25rem 0.5rem;
    font-size: 0.6875rem;
    min-height: auto;
  }

  .preset-delete-btn:hover {
    color: var(--color-danger);
  }
</style>
