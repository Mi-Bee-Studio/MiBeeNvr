<script lang="ts">
  import { ChevronLeft, ChevronRight } from 'lucide-svelte';
  import { t } from '$lib/i18n';

  interface Props {
    currentPage?: number;
    totalPages?: number;
    onPageChange?: (page: number) => void;
    // Optional sequential callbacks that use keyset cursor pagination (O(1) deep pages).
    // When provided, the prev/next arrow buttons call these instead of onPageChange,
    // avoiding the O(N) OFFSET scan-skip on deep pages.
    onNext?: () => void;
    onPrev?: () => void;
  }

  let {
    currentPage = 1,
    totalPages = 1,
    onPageChange = () => {},
    onNext,
    onPrev
  }: Props = $props();

  let canGoPrev = $derived(currentPage > 1);
  let canGoNext = $derived(currentPage < totalPages);
  let pages = $derived(generatePageNumbers(currentPage, totalPages));

  function generatePageNumbers(current: number, total: number): number[] {
    if (total <= 7) return Array.from({ length: total }, (_, i) => i + 1);
    const pages: number[] = [];
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
      onclick={() => (onPrev ? onPrev() : onPageChange(currentPage - 1))}
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
          onclick={() => onPageChange(page)}
          class="px-3 py-1 text-sm rounded border border-[var(--border)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)]"
        >
          {page}
        </button>
      {/if}
    {/each}
    <button
      onclick={() => (onNext ? onNext() : onPageChange(currentPage + 1))}
      disabled={!canGoNext}
      class="px-3 py-1 text-sm rounded border border-[var(--border)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] disabled:opacity-40 disabled:cursor-not-allowed"
    >
      <ChevronRight size={16} />
    </button>
    <div class="flex items-center gap-2 text-sm text-[var(--text-secondary)] ml-2">
      <span>{t('pagination.jumpToPage')}</span>
      <input
        type="number"
        min="1"
        max={totalPages}
        value={currentPage}
        onkeydown={(e: KeyboardEvent) => {
          if (e.key === 'Enter') {
            const target = e.target as HTMLInputElement;
            const page = parseInt(target.value);
            if (page >= 1 && page <= totalPages) {
              onPageChange(page);
            }
          }
        }}
        class="w-16 text-center py-1 px-2 border border-[var(--border)] rounded focus:outline-none focus:ring-2 focus:ring-[var(--color-accent)] text-sm"
      />
      <span>{t('pagination.of')} {totalPages}</span>
    </div>
  </div>
</div>
