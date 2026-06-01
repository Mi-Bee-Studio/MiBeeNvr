<script lang="ts">
  import type { Component } from 'svelte';

  interface TabItem {
    id: string;
    label: string;
    icon?: Component;
    count?: number;
  }

  interface Props {
    tabs: TabItem[];
    activeTab: string;
    onchange: (tabId: string) => void;
  }

  let { tabs, activeTab, onchange }: Props = $props();

  let activeIndex = $derived(tabs.findIndex(t => t.id === activeTab));
  let indicatorStyle = $derived({
    left: `${(activeIndex * 100) / tabs.length}%`,
    width: `${100 / tabs.length}%`
  });
</script>

<div class="tab-bar flex" role="tablist">
  {#each tabs as tab}
    <button
      class="tab-btn flex-1 flex items-center justify-center gap-1.5 px-4 py-3 text-sm font-medium th-text-secondary"
      class:active={tab.id === activeTab}
      onclick={() => onchange(tab.id)}
      role="tab"
      aria-selected={tab.id === activeTab}
    >
      {#if tab.icon}
        {@const Icon = tab.icon}
        <Icon size={16} />
      {/if}
      <span>{tab.label}</span>
      {#if tab.count !== undefined}
        <span class="tab-count">{tab.count}</span>
      {/if}
    </button>
  {/each}
  {#if tabs.length > 0}
    <div class="tab-indicator" style={indicatorStyle}></div>
  {/if}
</div>

<style>
  .tab-bar {
    position: relative;
    border-bottom: 1px solid var(--border);
  }

  .tab-btn {
    position: relative;
    z-index: 1;
    background: transparent;
    border: none;
    cursor: pointer;
    white-space: nowrap;
    transition:
      color var(--duration-fast) var(--ease-out),
      background-color var(--duration-fast) var(--ease-out);
  }

  .tab-btn:hover {
    color: var(--text-primary);
    background-color: var(--bg-hover);
  }

  .tab-btn.active {
    color: var(--text-primary);
  }

  .tab-count {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 1.25rem;
    height: 1.25rem;
    padding: 0 0.375rem;
    border-radius: 9999px;
    font-size: 0.75rem;
    font-weight: 600;
    background: var(--color-primary);
    color: #ffffff;
  }

  .tab-indicator {
    position: absolute;
    bottom: 0;
    height: 2px;
    background: var(--color-primary);
    border-radius: 1px;
    transition: left var(--duration-fast) var(--ease-out);
    pointer-events: none;
  }
</style>
