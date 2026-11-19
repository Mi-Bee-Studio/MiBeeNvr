<script lang="ts">
  /**
   * Filter bar for the recordings page: format pill, AI-marker toggle, camera /
   * AI-class / activity / min-motion selects, search box and the clear button.
   *
   * Extracted from Recordings.svelte — every control is two-way bound so the
   * parent keeps sole ownership of the filter state and the query building.
   */
  import { Search, Brain } from 'lucide-svelte';
  import { t } from '$lib/i18n';
  import type { Camera } from '$lib/api';
  import FormatFilter from '$lib/components/library/FormatFilter.svelte';

  let {
    formatPill = $bindable(),
    showAIMarkers = $bindable(),
    miBeeVisionConnected = false,
    cameraId = $bindable(),
    aiClass = $bindable(),
    activityFilter = $bindable(),
    minMotionScore = $bindable(),
    searchQuery = $bindable(),
    cameras = [],
    onClear = () => {},
  }: {
    formatPill: string;
    showAIMarkers: boolean;
    miBeeVisionConnected?: boolean;
    cameraId: string;
    aiClass: string;
    activityFilter: string;
    minMotionScore: string;
    searchQuery: string;
    cameras?: Camera[];
    onClear?: () => void;
  } = $props();
</script>

<div class="card p-4 mb-6 border th-border">
  <div class="flex flex-wrap items-end gap-3">
    <div class="flex items-center gap-2 pb-[2px]">
      <FormatFilter bind:selectedFormat={formatPill} />
      {#if miBeeVisionConnected}
        <button
          type="button"
          class="ai-marker-toggle {showAIMarkers ? 'is-active' : ''}"
          onclick={() => (showAIMarkers = !showAIMarkers)}
          title={t('library.showAIMarkers')}
          aria-pressed={showAIMarkers}
        >
          <Brain size={14} />
          <span>{t('library.showAIMarkers')}</span>
        </button>
      {/if}
    </div>
    <div class="flex-1 min-w-[160px]">
      <label for="camera" class="input-label">{t('recordings.camera')}</label>
      <select id="camera" class="input" bind:value={cameraId}>
        <option value="">{t('recordings.allCameras')}</option>
        {#each cameras as camera}
          <option value={camera.id}>{camera.name}</option>
        {/each}
      </select>
    </div>
    <div class="flex-1 min-w-[140px]">
      <label for="ai-class" class="input-label" title={t('recordings.aiClassHint')}>
        {t('recordings.aiClass')}
      </label>
      <select id="ai-class" class="input" bind:value={aiClass}>
        <option value="">{t('recordings.aiClassAll')}</option>
        <option value="person">{t('recordings.aiClassPerson')}</option>
        <option value="car">{t('recordings.aiClassCar')}</option>
      </select>
    </div>
    <div class="min-w-[130px]">
      <label for="activity-filter" class="input-label" title={t('recordings.activityHint')}>
        {t('recordings.activityFilter')}
      </label>
      <select id="activity-filter" class="input" bind:value={activityFilter}>
        <option value="">{t('recordings.activityAll')}</option>
        <option value="motion">{t('recordings.activityMotion')}</option>
        <option value="static">{t('recordings.activityStatic')}</option>
        <option value="scene_cut">{t('recordings.activitySceneCut')}</option>
      </select>
    </div>
    <div class="min-w-[120px]">
      <label for="min-motion" class="input-label" title={t('recordings.minMotionHint')}>
        {t('recordings.minMotionScore')}
      </label>
      <select id="min-motion" class="input" bind:value={minMotionScore}>
        <option value="">{t('recordings.minMotionAny')}</option>
        {#each ['0.05', '0.1', '0.2', '0.3', '0.5'] as v}
          <option value={v}>{t('recordings.minMotionAtLeast')} {v}</option>
        {/each}
      </select>
    </div>
    <div class="flex-1 min-w-[180px]">
      <label class="input-label" for="search-input">{t('recordings.search')}</label>
      <div class="relative">
        <Search size={16} class="absolute left-3 top-1/2 -translate-y-1/2 th-text-tertiary" />
        <input
          id="search-input"
          type="text"
          class="input pl-9"
          placeholder={t('recordings.search')}
          bind:value={searchQuery}
        />
      </div>
    </div>
    <div class="flex items-center gap-1 pb-[2px]">
      <button onclick={onClear} class="btn btn-ghost btn-sm">
        {t('recordings.clearFilters')}
      </button>
    </div>
  </div>
</div>
