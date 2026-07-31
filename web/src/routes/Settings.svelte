<script lang="ts">
  import { onDestroy } from 'svelte';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { settingsForm } from '$lib/settings/settings-form.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import {
    Settings,
    HardDrive,
    Camera,
    Radio,
    BrainCircuit,
    Film,
    SlidersHorizontal,
    Info,
    Save,
    RotateCcw,
  } from 'lucide-svelte';

  import GeneralPanel from './settings/GeneralPanel.svelte';
  import StoragePanel from './settings/StoragePanel.svelte';
  import CameraAccessPanel from './settings/CameraAccessPanel.svelte';
  import StreamingPanel from './settings/StreamingPanel.svelte';
  import AIPanel from './settings/AIPanel.svelte';
  import ProcessingPanel from './settings/ProcessingPanel.svelte';
  import AdvancedPanel from './settings/AdvancedPanel.svelte';
  import UpdatePanel from './settings/UpdatePanel.svelte';

  let activeCategory = $state('general');

  let categories = $derived([
    { id: 'general', label: t('settings.sidebar.general'), icon: Settings },
    { id: 'storage', label: t('settings.sidebar.storage'), icon: HardDrive },
    { id: 'cameras', label: t('settings.sidebar.cameras'), icon: Camera },
    { id: 'streaming', label: t('settings.sidebar.streaming'), icon: Radio },
    { id: 'ai', label: t('settings.sidebar.ai'), icon: BrainCircuit },
    { id: 'processing', label: t('settings.sidebar.processing'), icon: Film },
    { id: 'advanced', label: t('settings.sidebar.advanced'), icon: SlidersHorizontal },
    { id: 'about', label: t('settings.sidebar.about'), icon: Info },
  ]);

  let saving = $state(false);
  let showDestructiveConfirm = $state(false);
  let destructiveMessages = $state<string[]>([]);
  let showNavGuard = $state(false);
  let pendingHash = $state('');

  // Reactive dirty state from coordinator
  let isDirty = $derived(settingsForm.isAnyDirty);

  async function handleSave() {
    // Check for destructive changes first
    const warnings = settingsForm.getDestructiveWarnings();
    if (warnings.length > 0) {
      destructiveMessages = warnings;
      showDestructiveConfirm = true;
      return;
    }
    await performSave();
  }

  async function performSave() {
    saving = true;
    try {
      await settingsForm.saveAll();
      showToast(t('settings.saved'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.failedSaveSettings'), 'error');
    } finally {
      saving = false;
    }
  }

  function confirmDestructiveSave() {
    showDestructiveConfirm = false;
    performSave();
  }

  function cancelDestructiveSave() {
    showDestructiveConfirm = false;
  }

  function handleReset() {
    settingsForm.resetAll();
  }

  // Navigation guard — intercept hash change when dirty
  function handleHashChange(e: HashChangeEvent) {
    if (isDirty && !showNavGuard) {
      e.preventDefault();
      pendingHash = window.location.hash;
      showNavGuard = true;
    }
  }

  function confirmNavigation() {
    showNavGuard = false;
    window.removeEventListener('hashchange', handleHashChange);
    if (pendingHash) window.location.hash = pendingHash;
    window.addEventListener('hashchange', handleHashChange);
  }

  function cancelNavigation() {
    showNavGuard = false;
    pendingHash = '';
  }

  window.addEventListener('hashchange', handleHashChange);
  onDestroy(() => {
    window.removeEventListener('hashchange', handleHashChange);
  });
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="mb-6">
      <h2 class="text-2xl font-bold th-text-primary">{t('settings.title')}</h2>
    </div>

    <div class="flex flex-col md:flex-row gap-6">
      <!-- Left sidebar (desktop) / horizontal scroll (mobile) -->
      <nav class="md:w-56 shrink-0" role="navigation" aria-label={t('settings.title')}>
        <!-- Mobile: horizontal scroll tabs -->
        <div class="md:hidden flex gap-2 overflow-x-auto pb-2 mb-2">
          {#each categories as cat (cat.id)}
            {@const Icon = cat.icon}
            <button
              class="flex items-center gap-1.5 px-3 py-2 rounded-lg text-sm font-medium whitespace-nowrap transition-colors {activeCategory === cat.id ? 'bg-blue-600 text-white' : 'th-bg-hover th-text-secondary'}"
              onclick={() => (activeCategory = cat.id)}
            >
              <Icon size={16} />
              {cat.label}
            </button>
          {/each}
        </div>
        <!-- Desktop: vertical sidebar -->
        <div class="hidden md:flex flex-col gap-1">
          {#each categories as cat (cat.id)}
            {@const Icon = cat.icon}
            <button
              class="flex items-center gap-3 px-4 py-2.5 rounded-lg text-sm font-medium transition-colors text-left {activeCategory === cat.id ? 'bg-blue-50 dark:bg-blue-950/40 text-blue-600 dark:text-blue-400' : 'th-text-secondary hover:th-bg-hover'}"
              onclick={() => (activeCategory = cat.id)}
            >
              <Icon size={18} />
              {cat.label}
            </button>
          {/each}
        </div>
      </nav>

      <!--
        Right content area (#160 fix).

        All 7 panels are MOUNTED SIMULTANEOUSLY and toggled via CSS
        `hidden`, NOT conditionally rendered with `{#if}`. This is deliberate:

          - Conditional render (`{#if activeCategory === 'general'}`) unmounts
            the inactive panel → fires its `onDestroy` → calls
            `settingsForm.unregister(panelId)` → the panel's dirty predicate,
            unsaved form values, and last-saved snapshot are dropped from the
            coordinator. Switching back remounts it from scratch (fresh
            `loadSettings()`), so any edits made before the switch are silently
            lost. This broke the unified-save promise (#159): users expect a
            single Save at the bottom to persist every category's edits.

          - Keeping every panel mounted means `onDestroy` never fires while the
            user navigates between categories, so each panel's registration —
            and its in-progress edits — survive the switch. The unified Save bar
            then sees the accumulated dirty state across all panels at once.

        Trade-off: all panels load their API data on first mount. This is a few
        extra GETs on the Settings page entry (one-time, idempotent), well
        within the RPi-3B budget. Panels already guard their fetches with
        loading states, and these are read-only config reads (no per-frame or
        streaming cost). The alternative — lifting form state into the
        coordinator — is a larger rewrite for no user-facing benefit.
      -->
      <div class="flex-1 min-w-0 space-y-6 pb-24">
        <div class={activeCategory === 'general' ? '' : 'hidden'}>
          <GeneralPanel />
        </div>
        <div class={activeCategory === 'storage' ? '' : 'hidden'}>
          <StoragePanel />
        </div>
        <div class={activeCategory === 'cameras' ? '' : 'hidden'}>
          <CameraAccessPanel />
        </div>
        <div class={activeCategory === 'streaming' ? '' : 'hidden'}>
          <StreamingPanel />
        </div>
        <div class={activeCategory === 'ai' ? '' : 'hidden'}>
          <AIPanel />
        </div>
        <div class={activeCategory === 'processing' ? '' : 'hidden'}>
          <ProcessingPanel />
        </div>
        <div class={activeCategory === 'advanced' ? '' : 'hidden'}>
          <AdvancedPanel />
        </div>
        <div class={activeCategory === 'about' ? '' : 'hidden'}>
          <UpdatePanel />
        </div>
      </div>
    </div>
  </main>

  <!-- Sticky bottom save bar -->
  {#if isDirty || saving}
    <div class="fixed bottom-0 left-0 right-0 z-40 border-t th-border th-bg-primary shadow-lg">
      <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-3 flex items-center justify-between gap-4">
        <span class="text-sm th-text-secondary">
          {#if saving}
            {t('settings.saving')}
          {:else}
            {t('settings.unsavedChanges')}
          {/if}
        </span>
        <div class="flex items-center gap-3">
          <button class="btn btn-ghost btn-sm" onclick={handleReset} disabled={saving}>
            <RotateCcw size={14} />
            {t('settings.reset')}
          </button>
          <button class="btn btn-primary btn-sm" onclick={handleSave} disabled={saving}>
            {#if saving}
              <span class="spinner mr-1"></span>
            {:else}
              <Save size={14} />
            {/if}
            {t('settings.save')}
          </button>
        </div>
      </div>
    </div>
  {/if}
</div>

<!-- Destructive change confirmation -->
{#if showDestructiveConfirm}
  <ConfirmDialog
    title={t('settings.confirmSaveTitle')}
    message={destructiveMessages.join('\n\n')}
    confirmText={t('settings.confirmSave')}
    onconfirm={confirmDestructiveSave}
    oncancel={cancelDestructiveSave}
    variant="danger"
  />
{/if}

<!-- Unsaved changes navigation guard -->
{#if showNavGuard}
  <ConfirmDialog
    title={t('settings.unsavedTitle')}
    message={t('settings.unsavedMessage')}
    confirmText={t('settings.unsavedDiscard')}
    onconfirm={confirmNavigation}
    oncancel={cancelNavigation}
    variant="danger"
  />
{/if}
