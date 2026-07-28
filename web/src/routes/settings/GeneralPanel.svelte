<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getSettings, updateSettings } from '$lib/api';
  import type { SettingsConfig } from '$lib/api';
  import { getItemsPerPage, setItemsPerPage, getAutoRefresh, setAutoRefresh } from '$lib/preferences';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { settingsForm } from '$lib/settings/settings-form.svelte';
  import { AlertCircle } from 'lucide-svelte';

  // --- Form state ---
  // `selectedTimezone` flows through the unified save pipeline (server-persisted).
  // `itemsPerPage` / `autoRefresh` are pure localStorage prefs — they persist
  // instantly on change, so they are NOT part of dirty tracking or the save flow.
  let selectedTimezone = $state('Local');
  let itemsPerPage = $state(getItemsPerPage());
  let autoRefresh = $state(getAutoRefresh());

  let loading = $state(true);
  let error = $state('');

  // Last-saved timezone snapshot — drives isDirty and reset.
  let savedTimezone = $state('Local');

  // Timezone options (expanded set — Hong_Kong, Singapore, Chicago, Paris, Auckland).
  const timezoneOptions = [
    { value: 'Local', label: t('settings.timezoneLocal') },
    { value: 'UTC', label: 'UTC' },
    { value: 'Asia/Shanghai', label: 'Asia/Shanghai' },
    { value: 'Asia/Hong_Kong', label: 'Asia/Hong_Kong' },
    { value: 'Asia/Singapore', label: 'Asia/Singapore' },
    { value: 'Asia/Tokyo', label: 'Asia/Tokyo' },
    { value: 'America/New_York', label: 'America/New_York' },
    { value: 'America/Los_Angeles', label: 'America/Los_Angeles' },
    { value: 'America/Chicago', label: 'America/Chicago' },
    { value: 'Europe/London', label: 'Europe/London' },
    { value: 'Europe/Berlin', label: 'Europe/Berlin' },
    { value: 'Europe/Paris', label: 'Europe/Paris' },
    { value: 'Australia/Sydney', label: 'Australia/Sydney' },
    { value: 'Pacific/Auckland', label: 'Pacific/Auckland' },
  ];

  // Dirty only tracks the server-persisted timezone; prefs are instant-save.
  let isDirty = $derived.by(() => {
    if (loading) return false;
    return selectedTimezone !== savedTimezone;
  });

  // --- Load / save / reset ---

  async function loadSettings() {
    loading = true;
    error = '';
    try {
      const settings = await getSettings();
      selectedTimezone = settings.timezone || 'Local';
      savedTimezone = selectedTimezone; // snapshot
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedLoadSettings');
    } finally {
      loading = false;
    }
  }

  async function performSave() {
    try {
      await updateSettings({ timezone: selectedTimezone } as SettingsConfig);
      savedTimezone = selectedTimezone; // re-snapshot after success
      showToast(t('settings.saved'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.failedSaveSettings'), 'error');
      throw e; // let the shell surface save failures
    }
  }

  function resetForm() {
    selectedTimezone = savedTimezone;
  }

  // --- localStorage prefs (instant-save) ---

  function handleItemsPerPageChange() {
    setItemsPerPage(itemsPerPage);
  }

  function handleAutoRefreshChange(event: Event) {
    const select = event.target as HTMLSelectElement;
    setAutoRefresh(select.value);
  }

  // --- Register with the unified settings form ---

  let unregister: (() => void) | undefined;

  onMount(() => {
    loadSettings();
    unregister = settingsForm.register('general', {
      isDirty: () => isDirty,
      save: performSave,
      reset: resetForm,
    });
  });

  onDestroy(() => unregister?.());
</script>

<!-- Error -->
{#if error}
  <div class="card border th-border-danger p-8 text-center">
    <div class="flex justify-center mb-4 th-color-danger">
      <AlertCircle size={48} />
    </div>
    <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
    <p class="th-text-secondary mb-4">{error}</p>
    <button onclick={loadSettings} class="btn btn-primary btn-sm">{t('common.retry')}</button>
  </div>
{:else if loading}
  <div class="card border th-border">
    <div class="p-6 space-y-4">
      <div class="h-6 w-40 th-bg-tertiary rounded animate-pulse"></div>
      <div class="h-4 w-64 th-bg-tertiary rounded animate-pulse"></div>
      <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
        <div class="space-y-2">
          <div class="h-4 w-24 th-bg-tertiary rounded animate-pulse"></div>
          <div class="h-10 th-bg-tertiary rounded animate-pulse"></div>
        </div>
        <div class="space-y-2">
          <div class="h-4 w-28 th-bg-tertiary rounded animate-pulse"></div>
          <div class="h-10 th-bg-tertiary rounded animate-pulse"></div>
        </div>
      </div>
    </div>
  </div>
{:else}
  <!-- Timezone (server-persisted; flows through unified save) -->
  <div class="card p-8 border th-border">
    <h3 class="text-lg font-semibold th-text-primary mb-1">{t('settings.timezone')}</h3>
    <p class="text-sm th-text-tertiary mb-8">{t('settings.timezoneDesc')}</p>
    <div class="max-w-sm">
      <label for="timezone" class="input-label">{t('settings.timezone')}</label>
      <select id="timezone" class="input" bind:value={selectedTimezone}>
        {#each timezoneOptions as opt}
          <option value={opt.value}>{opt.label}</option>
        {/each}
      </select>
    </div>
  </div>

  <!-- Frontend Preferences (localStorage, instant-save) -->
  <div class="card p-8 border th-border">
    <h3 class="text-lg font-semibold th-text-primary mb-1">{t('settings.frontendPrefs')}</h3>
    <p class="text-sm th-text-tertiary mb-8">{t('settings.frontendPrefsDesc')}</p>

    <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
      <div>
        <label for="itemsPerPage" class="input-label">{t('settings.itemsPerPage')}</label>
        <select id="itemsPerPage" class="input" bind:value={itemsPerPage} onchange={handleItemsPerPageChange}>
          <option value={20}>20</option>
          <option value={50}>50</option>
          <option value={100}>100</option>
        </select>
      </div>

      <div>
        <label for="autoRefresh" class="input-label">{t('settings.autoRefresh')}</label>
        <select id="autoRefresh" class="input" bind:value={autoRefresh} onchange={handleAutoRefreshChange}>
          <option value="30s">{t('settings.every30s')}</option>
          <option value="60s">{t('settings.every60s')}</option>
          <option value="120s">{t('settings.every2m')}</option>
          <option value="off">{t('settings.off')}</option>
        </select>
      </div>
    </div>
  </div>
{/if}
