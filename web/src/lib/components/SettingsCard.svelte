<script lang="ts">
  import { ChevronDown } from 'lucide-svelte';

  interface Badge {
    text: string;
    color: 'success' | 'warning' | 'danger' | 'info';
  }

  interface Props {
    title: string;
    subtitle?: string;
    badge?: Badge;
    defaultOpen?: boolean;
    onBadgeClick?: () => void;
    children?: import('svelte').Snippet;
  }

  let { title, subtitle, badge, onBadgeClick, children, ...rest }: Props = $props();

  let open = $state(rest.defaultOpen === true);

  let badgeClass = $derived.by(() => {
    if (!badge) return '';
    const map: Record<string, string> = {
      success: 'badge-success',
      warning: 'badge-warning',
      danger: 'badge-error',
      info: 'badge-info',
    };
    return map[badge.color] || 'badge-neutral';
  });

  function toggle() {
    open = !open;
  }
</script>

<div class="card border th-border overflow-hidden">
  <button
    onclick={toggle}
    class="w-full flex items-start gap-3 p-6 text-left hover:th-bg-hover transition-colors"
    aria-expanded={open}
  >
    <div class="flex-1 min-w-0">
      <div class="flex items-center gap-3 flex-wrap">
        <h3 class="text-lg font-semibold th-text-primary m-0">{title}</h3>
        {#if badge}
          <span
            class="badge {badgeClass} cursor-pointer"
            onclick={(e) => { e.stopPropagation(); onBadgeClick?.(); }}
            role="button"
            tabindex="0"
            onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onBadgeClick?.(); } }}
          >
            {badge.text}
          </span>
        {/if}
      </div>
      {#if subtitle}
        <p class="text-sm th-text-secondary mt-1">{subtitle}</p>
      {/if}
    </div>
    <ChevronDown
      size={18}
      class="th-text-tertiary mt-1 transition-transform duration-200 {open ? 'rotate-180' : ''}"
    />
  </button>

  {#if open}
    <div class="px-6 pb-6">
      <div class="border-t th-border pt-6">
        {@render children?.()}
      </div>
    </div>
  {/if}
</div>
