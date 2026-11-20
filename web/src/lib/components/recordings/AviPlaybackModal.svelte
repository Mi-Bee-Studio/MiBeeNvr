<script lang="ts">
  // AVI realtime-playback modal shell — extracted verbatim from
  // Recordings.svelte. The player itself is AviPlayback.
  import { t } from '$lib/i18n';
  import AviPlayback from '$lib/components/AviPlayback.svelte';

  interface Props {
    recordingId: string;
    open: boolean;
  }

  let { recordingId, open = $bindable(false) }: Props = $props();
</script>

{#if open}
  <div
    class="fixed inset-0 bg-black/70 flex items-center justify-center p-4 z-50"
    onclick={() => open = false}
    role="dialog"
    aria-modal="true"
  >
    <div
      class="card max-w-3xl w-full p-4"
      onclick={(e) => e.stopPropagation()}
    >
      <div class="flex items-center justify-between mb-3">
        <h3 class="text-lg font-semibold th-text-primary">{t('avi.title')}</h3>
        <button
          onclick={() => open = false}
          class="btn btn-ghost btn-sm"
        >
          ✕
        </button>
      </div>
      <AviPlayback recordingId={recordingId} />
    </div>
  </div>
{/if}
