<script lang="ts">
  import { t } from '$lib/i18n';

  interface Props {
    selectedFormat?: string;
    onchange?: (format: string) => void;
  }

  let { selectedFormat = $bindable('All'), onchange = (f: string) => {} }: Props = $props();

  // Labels are resolved via i18n; ids stay locale-independent for state.
  const formats = [
    { id: 'All', labelKey: 'recordings.formatAll', icon: '' },
    { id: 'Video', labelKey: 'recordings.formatVideo', icon: '📹' },
    { id: 'Timelapse', labelKey: 'recordings.formatTimelapse', icon: '⏱' },
    { id: 'MJPEG', labelKey: 'recordings.formatMjpeg', icon: '🎞' },
  ] as const;

  function handleClick(format: string) {
    selectedFormat = format;
    onchange(format);
  }
</script>

<div class="flex gap-1">
  {#each formats as { id, labelKey, icon }}
    <button
      class="flex items-center gap-1 px-2.5 py-1 text-xs font-medium rounded transition-colors whitespace-nowrap
        {selectedFormat === id
          ? 'bg-[var(--color-primary)] text-white'
          : 'th-bg-tertiary th-text-secondary hover:th-bg-hover'}"
      onclick={() => handleClick(id)}
    >
      {#if icon}
        <span>{icon}</span>
        <span class="hidden sm:inline">{t(labelKey)}</span>
      {:else}
        <span>{t(labelKey)}</span>
      {/if}
    </button>
  {/each}
</div>
