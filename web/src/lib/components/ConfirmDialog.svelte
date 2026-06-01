<script lang="ts">
  import { onMount } from 'svelte';
  import { t } from '$lib/i18n';
  import { AlertTriangle } from 'lucide-svelte';

  interface Props {
    title: string;
    message: string;
    onconfirm: () => void;
    oncancel: () => void;
    confirmText?: string;
    cancelText?: string;
    variant?: 'danger' | 'primary';
    loading?: boolean;
  }

  let {
    title,
    message,
    onconfirm,
    oncancel,
    confirmText,
    cancelText,
    variant = 'danger',
    loading = false,
  }: Props = $props();

  // Use i18n defaults if not provided
  const _confirmText = $derived(confirmText ?? t('common.confirm'));
  const _cancelText = $derived(cancelText ?? t('common.cancel'));

  let dialogEl: HTMLDialogElement | undefined = $state();

  function handleKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault();
      oncancel();
    }
  }

  onMount(() => {
    // Focus the cancel button on open for safety (destructive action should not be default)
    dialogEl?.querySelector<HTMLButtonElement>('.confirm-dialog-cancel')?.focus();
  });
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
    bind:this={dialogEl}
    role="dialog"
    tabindex="-1"
    aria-modal="true"
    aria-labelledby="confirm-dialog-title"
    aria-describedby="confirm-dialog-desc"
    class="relative card p-6 border th-border max-w-md w-full mx-4"
    onmousedown={(e) => e.stopPropagation()}
  >
    <div class="flex items-start gap-4">
      <div class="flex-shrink-0 mt-0.5">
        <AlertTriangle size={20} class={variant === 'danger' ? 'th-color-danger' : 'th-color-primary'} />
      </div>
      <div class="flex-1 min-w-0">
        <h3 id="confirm-dialog-title" class="text-lg font-semibold th-text-primary mb-2">
          {title}
        </h3>
        <p id="confirm-dialog-desc" class="text-sm th-text-secondary">
          {message}
        </p>
      </div>
    </div>

    <div class="flex items-center justify-end gap-3 mt-6">
      <button
        class="confirm-dialog-cancel btn btn-ghost"
        onclick={oncancel}
        disabled={loading}
      >
        {_cancelText}
      </button>
      <button
        class={variant === 'danger'
          ? 'px-4 py-2 th-bg-danger hover:th-bg-danger-light text-white rounded-md transition-colors text-sm font-medium'
          : 'btn btn-primary'
        }
        onclick={onconfirm}
        disabled={loading}
      >
        {loading ? t('common.loading') : _confirmText}
      </button>
    </div>
  </div>
</div>
