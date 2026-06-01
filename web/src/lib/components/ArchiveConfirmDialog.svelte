<script lang="ts">
  import { t } from '$lib/i18n';
  import { AlertTriangle } from 'lucide-svelte';

  interface Props {
    cameraName: string;
    recordingCount: number;
    totalSize: string;
    loading?: boolean;
    onconfirm: () => void;
    oncancel: () => void;
  }

  let { cameraName, recordingCount, totalSize, loading = false, onconfirm, oncancel }: Props = $props();

  let step = $state(1);
  let confirmInput = $state('');
  let canConfirm = $derived(confirmInput === 'DELETE');

  function handleKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault();
      oncancel();
    }
  }
</script>

<svelte:window onkeydown={handleKeydown} />

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  class="fixed inset-0 z-50 flex items-center justify-center"
  role="presentation"
  onmousedown={(e) => { if (e.target === e.currentTarget) oncancel(); }}
>
  <!-- Backdrop -->
  <div class="fixed inset-0 bg-black/60 backdrop-blur-sm" aria-hidden="true"></div>

  <!-- Dialog -->
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div
    role="dialog"
    tabindex="-1"
    aria-modal="true"
    aria-label={t('cameras.archive.confirm.title')}
    class="relative card p-6 border th-border max-w-md w-full mx-4"
    onmousedown={(e) => e.stopPropagation()}
  >
    <div class="flex items-center gap-2 mb-4">
      <span class="text-sm th-text-tertiary font-medium">
        {t('cameras.archive.confirm.title')}
      </span>
      <span class="text-xs th-text-tertiary ml-auto">
        Step {step} of 2
      </span>
    </div>

    {#if step === 1}
      <div class="flex items-start gap-4">
        <div class="flex-shrink-0 mt-0.5">
          <AlertTriangle size={20} class="th-color-danger" />
        </div>
        <div class="flex-1 min-w-0">
          <p class="text-sm th-text-secondary leading-relaxed">
            {t('cameras.archive.confirm.step1.message', {
              name: cameraName,
              count: recordingCount,
              size: totalSize,
            })}
          </p>
        </div>
      </div>

      <div class="flex items-center justify-end gap-3 mt-6">
        <button
          class="btn btn-ghost"
          onclick={oncancel}
        >
          {t('common.cancel')}
        </button>
        <button
          class="px-4 py-2 th-bg-danger hover:th-bg-danger-light text-white rounded-md transition-colors text-sm font-medium"
          onclick={() => { step = 2; }}
        >
          {t('cameras.archive.confirm.step1.continue')}
        </button>
      </div>
    {:else}
      <div class="flex items-start gap-4">
        <div class="flex-shrink-0 mt-0.5">
          <AlertTriangle size={20} class="th-color-danger" />
        </div>
        <div class="flex-1 min-w-0">
          <label for="archive-confirm-input" class="input-label">
            {t('cameras.archive.confirm.step2.label')}
          </label>
          <input
            id="archive-confirm-input"
            type="text"
            class="input"
            bind:value={confirmInput}
            placeholder="DELETE"
            autocomplete="off"
          />
        </div>
      </div>

      <div class="flex items-center justify-end gap-3 mt-6">
        <button
          class="btn btn-ghost"
          onclick={oncancel}
          disabled={loading}
        >
          {t('common.cancel')}
        </button>
        <button
          class="px-4 py-2 th-bg-danger hover:th-bg-danger-light text-white rounded-md transition-colors text-sm font-medium"
          disabled={!canConfirm || loading}
          onclick={onconfirm}
        >
          {loading ? t('common.loading') : t('cameras.archive.confirm.step2.confirm')}
        </button>
      </div>
    {/if}
  </div>
</div>
