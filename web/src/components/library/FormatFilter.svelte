<script lang="ts">
  interface Props {
    selectedFormat?: string;
    onchange?: (format: string) => void;
  }

  let { selectedFormat = $bindable('All'), onchange = (f: string) => {} }: Props = $props();

  const formats = [
    { id: 'All', label: 'All', icon: '' },
    { id: 'Video', label: 'Video', icon: '📹' },
    { id: 'Timelapse', label: 'Timelapse', icon: '⏱' },
    { id: 'MJPEG', label: 'MJPEG', icon: '🎞' },
  ] as const;

  function handleClick(format: string) {
    selectedFormat = format;
    onchange(format);
  }
</script>

<div class="flex gap-1">
  {#each formats as { id, label, icon }}
    <button
      class="flex items-center gap-1 px-2.5 py-1 text-xs font-medium rounded transition-colors whitespace-nowrap
        {selectedFormat === id
          ? 'bg-[var(--color-primary)] text-white'
          : 'th-bg-tertiary th-text-secondary hover:th-bg-hover'}"
      onclick={() => handleClick(id)}
    >
      {#if icon}
        <span>{icon}</span>
        <span class="hidden sm:inline">{label}</span>
      {:else}
        <span>All</span>
      {/if}
    </button>
  {/each}
</div>
