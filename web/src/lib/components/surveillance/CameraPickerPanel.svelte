<script lang="ts">
  // Camera picker / wall configuration panel for the surveillance wall —
  // search + online-only filter + grouped section list (mirrors the
  // management page's grouping), selection chips, and the wall capacity /
  // photo-wall / rotation / layout-template chip groups. Extracted from
  // Surveillance.svelte.
  //
  // Selection state is bindable (the parent syncs it from the applied grid
  // and reads it back on apply); wall preferences persist here and mirror
  // into the parent through bindables. Grid-capacity changes stay in the
  // parent (they grow/trim the applied grid), surfaced as a callback.
  import { Search, X, Plus, Settings as SettingsIcon, Save, Trash2 } from 'lucide-svelte';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast.svelte';
  import {
    setMaxGridCameras, setPhotoWallEnabled, setWallRotationSecs, setWallTemplateId,
    WALL_ROTATION_OPTIONS, MAX_GRID_CAMERAS_OPTIONS,
  } from '$lib/preferences';
  import { wallGeometry, WALL_TEMPLATES, CLASSIC_GRID_SIZE, type WallTemplate } from '$lib/wall-layouts';
  import {
    listWallPresets, saveWallPreset, deleteWallPreset, MAX_WALL_PRESETS,
    type WallPreset,
  } from '$lib/wall-presets';
  import type { Camera } from '$lib/api';

  interface Props {
    allCameras: Camera[];
    registryGroups: string[];
    effectiveMax: number;
    serverBudget: number;
    gridCapacityOptions: number[];
    maxGridCameras: number;
    photoWall: boolean;
    rotationSecs: number;
    wallTemplateId: string;
    pendingCameraIds: string[];
    /** Fired on Apply — the parent rebuilds the grid from pendingCameraIds. */
    onapply: () => void;
    /** Fired on a capacity chip (parent grows/trims the applied grid). */
    onchangecapacity: (n: number) => void;
    /** Fired when a saved wall preset is loaded — the parent applies it. */
    onloadpreset: (preset: WallPreset) => void;
    /** Fired on Dismiss — the parent closes the panel. */
    ondismiss: () => void;
  }

  let {
    allCameras,
    registryGroups,
    effectiveMax,
    serverBudget,
    gridCapacityOptions,
    maxGridCameras = $bindable(),
    photoWall = $bindable(),
    rotationSecs = $bindable(),
    wallTemplateId = $bindable(),
    pendingCameraIds = $bindable(),
    onapply,
    onchangecapacity,
    onloadpreset,
    ondismiss,
  } = $props();

  let configSearch = $state('');
  let onlineOnly = $state(true);

  // Saved wall combinations (max 3). Loaded on mount and refreshed after
  // every save/delete so the chips always mirror localStorage.
  let wallPresets = $state<WallPreset[]>(listWallPresets());
  let presetName = $state('');

  function refreshPresets() {
    wallPresets = listWallPresets();
  }

  function handleSavePreset() {
    if (!presetName.trim()) {
      showToast(t('dashboard.presetNameRequired'), 'error');
      return;
    }
    const saved = saveWallPreset({
      name: presetName,
      cameraIds: pendingCameraIds,
      maxGridCameras,
      photoWall,
      rotationSecs,
      wallTemplateId,
    });
    if (!saved) {
      showToast(t('dashboard.presetFull'), 'error');
      return;
    }
    presetName = '';
    refreshPresets();
    showToast(t('dashboard.presetSaved'), 'success');
  }

  function handleDeletePreset(preset: WallPreset) {
    wallPresets = deleteWallPreset(preset.id);
    showToast(t('dashboard.presetDeleted'), 'success');
  }

  function handleLoadPreset(preset: WallPreset) {
    onloadpreset(preset);
    showToast(t('dashboard.presetLoaded', { name: preset.name }), 'success');
  }

  function changeWallTemplate(id: string) {
    wallTemplateId = id;
    setWallTemplateId(id);
  }

  function changeWallRotation(secs: number) {
    rotationSecs = secs;
    setWallRotationSecs(secs);
  }

  function togglePhotoWall() {
    photoWall = !photoWall;
    setPhotoWallEnabled(photoWall);
  }

  function isCameraOnline(c: Camera): boolean {
    const st = (c.status || '').toLowerCase();
    return st === 'recording' || st === 'active';
  }

  const pickerSections = $derived.by(() => {
    const q = configSearch.trim().toLowerCase();
    const filtered = allCameras.filter(c => {
      if (q) {
        const name = (c.name || '').toLowerCase();
        const group = (c.group || '').trim().toLowerCase();
        if (!name.includes(q) && !group.includes(q)) return false;
      }
      // Online-only hides OFFLINE cameras, but a selected one stays visible
      // (so the user can always see and uncheck what's already in the grid).
      if (onlineOnly && !isCameraOnline(c) && !pendingCameraIds.includes(c.id)) return false;
      return true;
    });
    const byGroup = new Map<string, Camera[]>();
    for (const c of filtered) {
      const g = (c.group || '').trim();
      if (!byGroup.has(g)) byGroup.set(g, []);
      byGroup.get(g)!.push(c);
    }
    const inRegistry = new Set(registryGroups);
    const derived = [...byGroup.keys()]
      .filter(g => g !== '' && !inRegistry.has(g))
      .sort((a, b) => a.localeCompare(b, 'zh'));
    const sections: { name: string; cameras: Camera[] }[] = [];
    for (const n of registryGroups) {
      const cams = byGroup.get(n);
      if (cams && cams.length > 0) sections.push({ name: n, cameras: cams });
    }
    for (const n of derived) sections.push({ name: n, cameras: byGroup.get(n)! });
    const ungrouped = byGroup.get('');
    if (ungrouped && ungrouped.length > 0) sections.push({ name: '', cameras: ungrouped });
    return sections;
  });

  function addGroupToSelection(name: string): void {
    const sec = pickerSections.find(s => s.name === name);
    if (!sec) return;
    for (const c of sec.cameras) {
      if (pendingCameraIds.length >= effectiveMax) break;
      if (!pendingCameraIds.includes(c.id)) pendingCameraIds = [...pendingCameraIds, c.id];
    }
  }

  function toggleCameraSelection(cameraId: string) {
    if (pendingCameraIds.includes(cameraId)) {
      pendingCameraIds = pendingCameraIds.filter(id => id !== cameraId);
    } else if (pendingCameraIds.length < effectiveMax) {
      pendingCameraIds = [...pendingCameraIds, cameraId];
    }
  }

  function clearPendingSelection(): void {
    pendingCameraIds = [];
  }
</script>

<div class="card p-4 mb-4">
  <div class="flex items-center justify-between mb-3">
    <h3 class="text-sm font-semibold th-text-primary">{t('dashboard.selectCameras')}</h3>
    <span class="text-xs th-text-muted">{pendingCameraIds.length} / {effectiveMax} · {t('dashboard.maxCameras')}</span>
  </div>

  <!-- Toolbar: search + online-only filter + clear -->
  <div class="flex flex-wrap items-center gap-2 mb-3">
    <div class="relative flex-1 min-w-[180px]">
      <Search size={14} class="absolute left-2.5 top-1/2 -translate-y-1/2 th-text-muted pointer-events-none" />
      <input
        type="text"
        class="input pl-8 py-1.5 text-sm"
        placeholder={t('dashboard.searchCameras')}
        bind:value={configSearch}
      />
    </div>
    <label class="flex items-center gap-1.5 text-xs th-text-secondary cursor-pointer whitespace-nowrap select-none">
      <input type="checkbox" bind:checked={onlineOnly} class="accent-[var(--color-primary)]" />
      {t('dashboard.onlineOnly')}
    </label>
    <!-- Wall capacity + photo-wall toggle (#1072): capacity is clamped by
         the server's HLS preview budget; the photo wall only exists
         above 4 tiles. -->
    <!-- Capacity + rotation are in-page chip groups, NOT native <select>:
         embedded-webview panes render the native popup outside the
         page's control — it can drift past the window edge and its
         click landing points shift, accidentally toggling neighboring
         controls. -->
    <div
      class="flex items-center gap-1.5 text-xs th-text-secondary whitespace-nowrap select-none"
      role="group"
      aria-label={t('dashboard.gridCapacity')}
    >
      <span>{t('dashboard.gridCapacity')}</span>
      <div class="flex items-center gap-1">
        {#each MAX_GRID_CAMERAS_OPTIONS as n (n)}
          {@const overBudget = n > Math.max(serverBudget, CLASSIC_GRID_SIZE)}
          <button
            type="button"
            class="opt-chip {maxGridCameras === n ? 'opt-chip-active' : ''}"
            disabled={overBudget}
            aria-pressed={maxGridCameras === n}
            title={overBudget ? t('dashboard.budgetHint', { budget: serverBudget }) : `${n}`}
            onclick={() => onchangecapacity(n)}
          >{n}</button>
        {/each}
      </div>
    </div>
    <label
      class="flex items-center gap-1.5 text-xs th-text-secondary cursor-pointer whitespace-nowrap select-none {maxGridCameras > 4 ? '' : 'opacity-50 pointer-events-none'}"
      title={t('dashboard.photoWallHint')}
    >
      <input type="checkbox" checked={photoWall} onchange={togglePhotoWall} disabled={maxGridCameras <= 4} class="accent-[var(--color-primary)]" />
      {t('dashboard.photoWall')}
    </label>
    <div
      class="flex items-center gap-1.5 text-xs th-text-secondary whitespace-nowrap select-none {photoWall && maxGridCameras > 4 ? '' : 'opacity-50 pointer-events-none'}"
      role="group"
      aria-label={t('dashboard.wallRotation')}
      title={t('dashboard.wallRotationHint')}
    >
      <span>{t('dashboard.wallRotation')}</span>
      <div class="flex items-center gap-1">
        {#each WALL_ROTATION_OPTIONS as s (s)}
          <button
            type="button"
            class="opt-chip {rotationSecs === s ? 'opt-chip-active' : ''}"
            disabled={!(photoWall && maxGridCameras > 4)}
            aria-pressed={rotationSecs === s}
            onclick={() => changeWallRotation(s)}
          >{s === 0 ? t('dashboard.wallRotation.off') : s === 300 ? '5min' : `${s}s`}</button>
        {/each}
      </div>
    </div>
    {#if photoWall && maxGridCameras > 4}
      <!-- Wall layout templates (#1072): chips render live mini
           previews of each template at the CURRENT camera count, so
           what you pick is what you get. -->
      <div class="w-full flex items-center gap-2 flex-wrap">
        <span class="text-xs th-text-secondary whitespace-nowrap" title={t('dashboard.wallLayoutHint')}>{t('dashboard.wallLayout')}</span>
        {#each WALL_TEMPLATES as tpl (tpl.id)}
          {@const previewCount = Math.max(allCameras.length, 6)}
          {@const geom = wallGeometry(tpl as WallTemplate, previewCount)}
          {@const tooFew = previewCount < tpl.minCameras}
          <button
            type="button"
            class="wall-tpl-chip {wallTemplateId === tpl.id ? 'wall-tpl-chip-active' : ''}"
            title={tooFew
              ? `${t(`dashboard.layout.${tpl.id}`)} — ${t('dashboard.layoutMinHint', { count: tpl.minCameras })}`
              : t(`dashboard.layout.${tpl.id}`)}
            aria-label={t(`dashboard.layout.${tpl.id}`)}
            aria-pressed={wallTemplateId === tpl.id}
            disabled={tooFew}
            onclick={() => changeWallTemplate(tpl.id)}
          >
            <span class="wall-tpl-preview" style="grid-template-columns: repeat({geom.cols}, minmax(0, 1fr));">
              {#each geom.placements as p (p.col * 100 + p.row)}
                <span class="wall-tpl-cell wall-tpl-featured" style="grid-column: {p.col} / span 2; grid-row: {p.row} / span 2;"></span>
              {/each}
              {#each Array(Math.max(previewCount - geom.featured, 0)) as _, si (si)}
                <span class="wall-tpl-cell"></span>
              {/each}
            </span>
          </button>
        {/each}
      </div>
    {/if}
    {#if gridCapacityOptions.length < MAX_GRID_CAMERAS_OPTIONS.length}
      <p class="w-full text-[11px] th-text-muted">{t('dashboard.budgetHint', { budget: serverBudget })}</p>
    {/if}
    <button
      class="btn btn-ghost text-xs px-2 py-1"
      onclick={clearPendingSelection}
      disabled={pendingCameraIds.length === 0}
    >
      {t('dashboard.clearSelection')}
    </button>
  </div>

  <!-- Saved wall combinations (max 3): one-click switch between named
       presets. Saving captures the pending selection + the current wall
       settings; loading hands the snapshot to the parent to apply. -->
  <div class="rounded-md border th-border px-3 py-2 mb-3">
    <div class="flex items-center gap-2 mb-2 flex-wrap">
      <span class="text-xs font-semibold th-text-primary">{t('dashboard.presetsTitle')}</span>
      <span class="text-[11px] th-text-muted">{wallPresets.length} / {MAX_WALL_PRESETS}</span>
      <div class="flex-1"></div>
      <input
        type="text"
        class="input py-1 text-xs w-36"
        placeholder={t('dashboard.presetNamePlaceholder')}
        bind:value={presetName}
        maxlength={32}
        onkeydown={(e) => { if (e.key === 'Enter') handleSavePreset(); }}
      />
      <button
        type="button"
        class="btn btn-ghost text-xs px-2 py-1 inline-flex items-center gap-1"
        onclick={handleSavePreset}
        title={t('dashboard.presetFull')}
      >
        <Save size={13} />
        {t('dashboard.presetSave')}
      </button>
    </div>
    {#if wallPresets.length === 0}
      <p class="text-[11px] th-text-muted">{t('dashboard.presetEmpty')}</p>
    {:else}
      <div class="flex flex-wrap gap-1.5">
        {#each wallPresets as preset (preset.id)}
          <span class="inline-flex items-center gap-1 pl-2 pr-1 py-0.5 rounded-full bg-[var(--bg-tertiary)] text-xs">
            <button
              type="button"
              class="hover:underline"
              onclick={() => handleLoadPreset(preset)}
              title={t('dashboard.presetLoad')}
            >{preset.name}</button>
            <button
              type="button"
              class="p-0.5 rounded-full hover:bg-[var(--bg-elevated)] transition-colors th-text-muted"
              onclick={() => handleDeletePreset(preset)}
              aria-label={t('dashboard.presetDelete')}
              title={t('dashboard.presetDelete')}
            >
              <Trash2 size={12} />
            </button>
          </span>
        {/each}
      </div>
    {/if}
  </div>

  <!-- Selected chips (ordered — grid position order) -->
  {#if pendingCameraIds.length > 0}
    <div class="flex flex-wrap gap-1.5 mb-3">
      {#each pendingCameraIds as id, i (id)}
        {@const chipCam = allCameras.find(c => c.id === id)}
        <span class="inline-flex items-center gap-1 pl-2 pr-1 py-0.5 rounded-full bg-[var(--color-primary)] text-white text-xs">
          <span class="opacity-70">{i + 1}.</span>
          {chipCam?.name || id}
          <button
            type="button"
            class="p-0.5 rounded-full hover:bg-white/20 transition-colors"
            onclick={() => toggleCameraSelection(id)}
            aria-label={t('dashboard.removeCamera')}
            title={t('dashboard.removeCamera')}
          >
            <X size={12} />
          </button>
        </span>
      {/each}
    </div>
  {/if}

  <!-- Grouped, filtered camera list -->
  <div class="space-y-0.5 max-h-72 overflow-y-auto mb-4">
    {#each pickerSections as sec (sec.name)}
      <div class="pt-1.5">
        <div class="flex items-center gap-2 px-2 py-1 sticky top-0 bg-[var(--bg-elevated)] z-[1]">
          <span class="text-[11px] font-semibold th-text-secondary tracking-wide">{sec.name || t('cameras.groupUngrouped')}</span>
          <span class="text-[11px] th-text-muted">{sec.cameras.length}</span>
          <div class="flex-1 border-t th-border opacity-50"></div>
          {#if pendingCameraIds.length < effectiveMax}
            <button
              type="button"
              class="inline-flex items-center gap-0.5 text-[11px] hover:underline"
              style="color: var(--color-primary);"
              onclick={() => addGroupToSelection(sec.name)}
              title={t('dashboard.addGroupHint')}
            >
              <Plus size={11} />
              {t('dashboard.addGroup')}
            </button>
          {/if}
        </div>
        {#each sec.cameras as camera (camera.id)}
          {@const online = isCameraOnline(camera)}
          <label class="flex items-center gap-2 px-2 py-1.5 rounded-md hover:bg-[var(--bg-tertiary)] cursor-pointer transition-colors">
            <input
              type="checkbox"
              checked={pendingCameraIds.includes(camera.id)}
              onchange={() => toggleCameraSelection(camera.id)}
              disabled={!pendingCameraIds.includes(camera.id) && pendingCameraIds.length >= effectiveMax}
              class="accent-[var(--color-primary)]"
            />
            <span class="w-1.5 h-1.5 rounded-full shrink-0 {online ? 'bg-emerald-500' : 'bg-gray-400'}"
              title={online ? t('cameras.statusRecording') : t('cameras.statusStopped')}></span>
            <span class="text-sm th-text-primary truncate">{camera.name || camera.id}</span>
            <span class="text-xs th-text-muted ml-auto shrink-0">{camera.protocol}</span>
          </label>
        {/each}
      </div>
    {:else}
      <p class="text-xs th-text-muted px-2 py-6 text-center">{t('dashboard.noMatch')}</p>
    {/each}
  </div>

  <div class="flex justify-end gap-2">
    <button
      class="btn btn-ghost text-sm px-3 py-1.5"
      onclick={ondismiss}
    >
      {t('common.dismiss')}
    </button>
    <button
      class="btn btn-primary text-sm px-3 py-1.5"
      onclick={onapply}
    >
      {t('dashboard.apply')}
    </button>
  </div>
</div>
