<script lang="ts">
  import { t } from '$lib/i18n';
  import { Activity, Film } from 'lucide-svelte';
  import Tab from '$lib/components/Tab.svelte';
  import HealthHistory from './HealthHistory.svelte';
  import TranscodingHistory from './TranscodingHistory.svelte';

  let { initialTab = 'health' }: { initialTab?: string } = $props();
  let activeTab = $state('health');

  $effect(() => {
    activeTab = initialTab;
  });

  let tabs = $derived([
    { id: 'health', label: t('health.title'), icon: Activity },
    { id: 'transcoding', label: t('transcoding.history.nav'), icon: Film },
  ]);

  function handleTabChange(tabId: string) {
    activeTab = tabId;
    window.location.hash = tabId === 'transcoding' ? '#/status/transcoding' : '#/status';
  }
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">
  <main class="mx-auto px-3 sm:px-4 lg:px-6 py-4 sm:py-6" style="max-width: 100%;">
    <Tab {tabs} {activeTab} onchange={handleTabChange} />
    {#if activeTab === 'health'}
      <div class="health-tab-content">
        <HealthHistory />
      </div>
    {:else if activeTab === 'transcoding'}
      <TranscodingHistory />
    {/if}
  </main>
</div>

<style>
  .health-tab-content > :global(:first-child) {
    padding-top: 0 !important;
  }
</style>
