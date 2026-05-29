<script lang="ts">
  import { t } from '$lib/i18n';
  import { Video, Film } from 'lucide-svelte';
  import Tab from '$lib/components/Tab.svelte';
  import Cameras from './Cameras.svelte';
  import Recordings from './Recordings.svelte';

  let { initialTab = 'cameras' }: { initialTab?: string } = $props();
  let activeTab = $state('cameras');

  $effect(() => {
    activeTab = initialTab;
  });

  let tabs = $derived([
    { id: 'cameras', label: t('nav.cameras'), icon: Video },
    { id: 'recordings', label: t('nav.recordings'), icon: Film },
  ]);

  function handleTabChange(tabId: string) {
    activeTab = tabId;
    window.location.hash = tabId === 'recordings' ? '#/surveillance/recordings' : '#/surveillance';
  }
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">
  <main class="mx-auto px-3 sm:px-4 lg:px-6 py-4 sm:py-6" style="max-width: 100%;">
    <Tab {tabs} {activeTab} onchange={handleTabChange} />
    {#if activeTab === 'cameras'}
      <Cameras />
    {:else if activeTab === 'recordings'}
      <Recordings />
    {/if}
  </main>
</div>
