<script lang="ts">
  import { ptzMove, ptzStop } from '$lib/api';
  import { t } from '$lib/i18n';
  import { ChevronUp, ChevronDown, ChevronLeft, ChevronRight, ZoomIn, ZoomOut } from 'lucide-svelte';

  let { cameraId, enabled = false }: { cameraId: string; enabled?: boolean } = $props();

  let moving = $state<string | null>(null);
  let error = $state('');

  async function handleMoveStart(direction: string, speed: number = 0.5) {
    if (moving) return;
    error = '';
    moving = direction;

    try {
      await ptzMove(cameraId, { speed, direction });
    } catch (e) {
      error = e instanceof Error ? e.message : 'PTZ move failed';
      moving = null;
    }
  }

  async function handleMoveStop() {
    if (!moving) return;
    const wasMoving = moving;
    moving = null;

    try {
      await ptzStop(cameraId);
    } catch (e) {
      error = e instanceof Error ? e.message : 'PTZ stop failed';
    }
  }

  function onPointerDown(direction: string, speed?: number) {
    return (e: PointerEvent) => {
      e.preventDefault();
      (e.target as HTMLElement).setPointerCapture(e.pointerId);
      handleMoveStart(direction, speed);
    };
  }

  function onPointerUp(_e: PointerEvent) {
    handleMoveStop();
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
    width: 2.25rem;
    height: 2.25rem;
  }

  .ptz-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 2.25rem;
    height: 2.25rem;
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
    width: 2.25rem;
    height: 2.25rem;
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
</style>
