<script>
  import { ChevronLeft, ChevronRight } from 'lucide-svelte';
  export let currentPage = 1;
  export let totalPages = 1;
  export let onPageChange = () => {};

  $: canGoPrev = currentPage > 1;
  $: canGoNext = currentPage < totalPages;
  $: pages = generatePageNumbers(currentPage, totalPages);

  function generatePageNumbers(current, total) {
    if (total <= 7) return Array.from({ length: total }, (_, i) => i + 1);
    const pages = [];
    pages.push(1);
    for (let i = Math.max(2, current - 1); i <= Math.min(total - 1, current + 1); i++) {
      pages.push(i);
    }
    if (total > 1) pages.push(total);
    return [...new Set(pages)].sort((a, b) => a - b);
  }
</script>

<div class="flex items-center justify-between px-4 py-3 border-t border-[var(--border)]">
  <span class="text-sm text-[var(--text-muted)]">
    <!-- page info set by parent -->
  </span>
  <div class="flex items-center gap-1">
    <button
      on:click={() => onPageChange(currentPage - 1)}
      disabled={!canGoPrev}
      class="px-3 py-1 text-sm rounded border border-[var(--border)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] disabled:opacity-40 disabled:cursor-not-allowed"
    >
      <ChevronLeft size={16} />
    </button>
    {#each pages as page (page)}
      {#if page === currentPage}
        <span class="px-3 py-1 text-sm rounded bg-[var(--color-accent)] text-white font-medium">
          {page}
        </span>
      {:else}
        <button
          on:click={() => onPageChange(page)}
          class="px-3 py-1 text-sm rounded border border-[var(--border)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)]"
        >
          {page}
        </button>
      {/if}
    {/each}
    <button
      on:click={() => onPageChange(currentPage + 1)}
      disabled={!canGoNext}
      class="px-3 py-1 text-sm rounded border border-[var(--border)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] disabled:opacity-40 disabled:cursor-not-allowed"
    >
      <ChevronRight size={16} />
    </button>
  </div>
</div>
