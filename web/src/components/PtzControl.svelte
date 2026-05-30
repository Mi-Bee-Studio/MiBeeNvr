<script lang="ts">
  import { ptzMove, ptzStop, getPTZPresets, goToPTZPreset } from '$lib/api';
  import type { PTZPreset } from '$lib/api';
  import { t } from '$lib/i18n';
  import { ChevronUp, ChevronDown, ChevronLeft, ChevronRight, ZoomIn, ZoomOut } from 'lucide-svelte';

  let { cameraId, enabled = false }: { cameraId: string; enabled?: boolean } = $props();

  let moving = $state<string | null>(null);
  let error = $state('');
  let presets = $state<PTZPreset[]>([]);
  let selectedPreset = $state('');
  let goingToPreset = $state(false);

  // Load presets when enabled
  $effect(() => {
    if (enabled && cameraId) {
      loadPresets();
    }
  });

  async function loadPresets() {
    try {
      presets = await getPTZPresets(cameraId);
    } catch {
      presets = [];
    }
  }

  async function handleGoToPreset() {
    if (!selectedPreset) return;
    goingToPreset = true;
    try {
      await goToPTZPreset(cameraId, selectedPreset);
    } catch (e) {
      error = e instanceof Error ? e.message : 'Preset goto failed';
    } finally {
      goingToPreset = false;
      selectedPreset = '';
    }
  }
</script>

{#if enabled}
  <div class="ptz-panel">
    <div class="ptz-label">{t('ptz.control')}</div>

    {#if error}
      <div class="ptz-error">{error}</div>
    {/if}

    <!-- Direction pad: 3x3 grid -->
    <div class="ptz-grid">
      <div class="ptz-cell"></div>
      <button
        class="ptz-btn"
        class:ptz-btn-active={moving === 'up'}
        onpointerdown={onPointerDown('up')}
        onpointerup={onPointerUp}
        onpointerleave={onPointerUp}
        aria-label={t('ptz.up')}
      >
        <ChevronUp size={18} />
      </button>
      <div class="ptz-cell"></div>

      <button
        class="ptz-btn"
        class:ptz-btn-active={moving === 'left'}
        onpointerdown={onPointerDown('left')}
        onpointerup={onPointerUp}
        onpointerleave={onPointerUp}
        aria-label={t('ptz.left')}
      >
        <ChevronLeft size={18} />
      </button>
      <div class="ptz-center">
        {#if moving}
          <span class="ptz-dot"></span>
        {/if}
      </div>
      <button
        class="ptz-btn"
        class:ptz-btn-active={moving === 'right'}
        onpointerdown={onPointerDown('right')}
        onpointerup={onPointerUp}
        onpointerleave={onPointerUp}
        aria-label={t('ptz.right')}
      >
        <ChevronRight size={18} />
      </button>

      <div class="ptz-cell"></div>
      <button
        class="ptz-btn"
        class:ptz-btn-active={moving === 'down'}
        onpointerdown={onPointerDown('down')}
        onpointerup={onPointerUp}
        onpointerleave={onPointerUp}
        aria-label={t('ptz.down')}
      >
        <ChevronDown size={18} />
      </button>
      <div class="ptz-cell"></div>
    </div>

    <!-- Zoom controls -->
    <div class="ptz-zoom-row">
      <button
        class="ptz-btn ptz-btn-zoom"
        class:ptz-btn-active={moving === 'zoom_in'}
        onpointerdown={onPointerDown('zoom_in', 0.5)}
        onpointerup={onPointerUp}
        onpointerleave={onPointerUp}
        aria-label={t('ptz.zoomIn')}
      >
        <ZoomIn size={16} />
        <span class="ptz-btn-label">{t('ptz.zoomIn')}</span>
      </button>
      <button
        class="ptz-btn ptz-btn-zoom"
        class:ptz-btn-active={moving === 'zoom_out'}
        onpointerdown={onPointerDown('zoom_out', 0.5)}
        onpointerup={onPointerUp}
        onpointerleave={onPointerUp}
        aria-label={t('ptz.zoomOut')}
      >
        <ZoomOut size={16} />
        <span class="ptz-btn-label">{t('ptz.zoomOut')}</span>
      </button>
    </div>
    <!-- Preset quick-access -->
    {#if presets.length > 0}
      <div class="ptz-presets">
        <select
          class="ptz-preset-select"
          bind:value={selectedPreset}
          onchange={handleGoToPreset}
          disabled={goingToPreset}
        >
          <option value="">Go to preset...</option>
          {#each presets as preset (preset.token)}
            <option value={preset.token}>{preset.name}</option>
          {/each}
        </select>
      </div>
    {/if}
  </div>
{/if}

<style>
  .ptz-panel {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.5rem;
    padding: 0.75rem;
    background-color: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
  }

  .ptz-label {
    font-size: 0.75rem;
    font-weight: 500;
    color: var(--text-secondary);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }

  .ptz-error {
    font-size: 0.75rem;
    color: var(--color-danger);
    text-align: center;
    padding: 0.25rem 0.5rem;
    background-color: rgba(239, 68, 68, 0.1);
    border-radius: var(--radius-sm);
    width: 100%;
  }

  .ptz-grid {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 4px;
    width: fit-content;
  }

  .ptz-cell {
    width: 2.75rem;
    height: 2.75rem;
  }

  .ptz-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    min-width: 2.75rem;
    min-height: 2.75rem;
    width: 2.75rem;
    height: 2.75rem;
    border-radius: var(--radius-sm);
    background-color: var(--bg-tertiary);
    border: 1px solid var(--border);
    color: var(--text-secondary);
    cursor: pointer;
    transition: all var(--duration-fast) var(--ease-out);
    user-select: none;
    touch-action: none;
  }

  .ptz-btn:hover {
    background-color: var(--bg-hover);
    color: var(--text-primary);
    border-color: var(--border-hover);
  }

  .ptz-btn-active {
    background-color: var(--color-primary);
    color: #ffffff;
    border-color: var(--color-primary);
  }

  .ptz-center {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 2.75rem;
    height: 2.75rem;
    border-radius: 50%;
    background-color: var(--bg-tertiary);
    border: 1px solid var(--border);
  }

  .ptz-dot {
    width: 0.5rem;
    height: 0.5rem;
    border-radius: 50%;
    background-color: var(--color-primary);
    animation: pulse 0.8s ease-in-out infinite alternate;
  }

  @keyframes pulse {
    from { opacity: 0.5; }
    to { opacity: 1; }
  }

  .ptz-zoom-row {
    display: flex;
    gap: 0.5rem;
    width: 100%;
    justify-content: center;
  }

  .ptz-btn-zoom {
    width: auto;
    padding: 0 0.625rem;
    gap: 0.25rem;
  }

  .ptz-btn-label {
    font-size: 0.6875rem;
    font-weight: 500;
  }

  .ptz-presets {
    width: 100%;
    padding-top: 0.375rem;
    border-top: 1px solid var(--border);
  }

  .ptz-preset-select {
    width: 100%;
    padding: 0.375rem 0.5rem;
    background-color: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    color: var(--text-secondary);
    font-size: 0.6875rem;
    cursor: pointer;
    transition: all var(--duration-fast) var(--ease-out);
    appearance: none;
    -webkit-appearance: none;
    background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='12' viewBox='0 0 24 24' fill='none' stroke='%23737373' stroke-width='2'%3E%3Cpath d='m6 9 6 6 6-6'/%3E%3C/svg%3E");
    background-repeat: no-repeat;
    background-position: right 0.5rem center;
    padding-right: 1.5rem;
  }

  .ptz-preset-select:hover {
    border-color: var(--border-hover);
    color: var(--text-primary);
  }

  .ptz-preset-select:focus {
    outline: none;
    border-color: var(--color-primary);
    box-shadow: var(--focus-ring);
  }
</style>
