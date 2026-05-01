<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getSettings, updateSettings, logout } from '$lib/api';
  import type { SettingsConfig } from '$lib/api';
  import LanguageSwitcher from '../components/LanguageSwitcher.svelte';
  import { t, onLangChange, getCurrentLang } from '$lib/i18n';

  // Re-render on language change
  let lang = getCurrentLang();
  const unsubscribe = onLangChange(() => { lang = getCurrentLang(); });

  onDestroy(() => { unsubscribe(); });

  let settings: SettingsConfig | null = null;
  let loading = true;
  let error = '';
  let saveError = '';
  let saveSuccess = false;
  let saving = false;

  // Form state
  let retentionDays = 30;
  let diskThresholdPercent = 90;
  let checkInterval = '1h';
  let itemsPerPage = 50;
  let autoRefresh = '30s';

  // Validation
  let validationErrors: Record<string, string> = {};

  function validate(): boolean {
    validationErrors = {};

    if (retentionDays < 1) {
      validationErrors['retention_days'] = t('settings.validationRetention');
    }

    if (diskThresholdPercent < 0 || diskThresholdPercent > 100) {
      validationErrors['disk_threshold'] = t('settings.validationThreshold');
    }

    return Object.keys(validationErrors).length === 0;
  }

  async function loadSettings() {
    loading = true;
    error = '';

    try {
      settings = await getSettings();
      retentionDays = settings.cleanup.retention_days;
      diskThresholdPercent = settings.cleanup.disk_threshold_percent;
      checkInterval = settings.cleanup.check_interval;
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedLoadSettings');
    } finally {
      loading = false;
    }
  }

  async function save() {
    if (!validate()) return;

    saving = true;
    saveError = '';
    saveSuccess = false;

    try {
      const payload: SettingsConfig = {
        cleanup: {
          retention_days: retentionDays,
          disk_threshold_percent: diskThresholdPercent,
          check_interval: checkInterval,
        },
      };

      const result = await updateSettings(payload);
      settings = await getSettings();
      saveSuccess = true;

      setTimeout(() => {
        saveSuccess = false;
      }, 3000);
    } catch (e) {
      saveError = e instanceof Error ? e.message : t('common.failedSaveSettings');
    } finally {
      saving = false;
    }
  }


  onMount(() => {
    loadSettings();
  });
</script>

<div class="min-h-screen bg-slate-900">
  <!-- Header -->
  <header class="bg-slate-800 border-b border-slate-700">
    <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
      <div class="flex items-center justify-between h-16">
        <div class="flex items-center gap-4">
          <h1 class="text-xl font-bold text-slate-100">MiBee NVR</h1>
          <nav class="flex gap-4">
            <a href="#/recordings" class="text-slate-300 hover:text-slate-100 transition-colors">
              {t('nav.recordings')}
            </a>
            <a href="#/cameras" class="text-slate-300 hover:text-slate-100 transition-colors">
              {t('nav.cameras')}
            </a>
            <a href="#/stats" class="text-slate-300 hover:text-slate-100 transition-colors">{t('nav.stats')}</a>
          </nav>
        </div>
        <div class="flex items-center gap-3">
          <LanguageSwitcher />
          <button on:click={logout} class="btn btn-ghost">
            {t('nav.logout')}
          </button>
        </div>
      </div>
    </div>
  </header>

  <!-- Main content -->
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="mb-6">
      <h2 class="text-2xl font-bold text-slate-100">{t('settings.title')}</h2>
    </div>

    <!-- Error message -->
    {#if error}
      <div class="mb-4 p-4 bg-red-900/30 border border-red-700 rounded-md text-red-300">
        {error}
      </div>
    {/if}

    <!-- Loading state -->
    {#if loading}
      <div class="flex justify-center items-center h-64">
        <div class="spinner spinner-lg"></div>
      </div>
    {:else}
      <div class="space-y-6">

        <!-- Cleanup Policy -->
        <div class="card p-6 border border-slate-700/60">
          <h3 class="text-lg font-semibold text-slate-100 mb-1">{t('settings.cleanup')}</h3>
          <p class="text-sm text-slate-400 mb-6">{t('settings.cleanupDesc')}</p>

          <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
            <!-- Retention Days -->
            <div>
              <label for="retention" class="input-label">{t('settings.retentionDays')}</label>
              <input
                id="retention"
                type="number"
                class="input"
                bind:value={retentionDays}
                min="1"
              />
              {#if validationErrors['retention_days']}
                <p class="text-red-400 text-xs mt-1">{validationErrors['retention_days']}</p>
              {/if}
            </div>

            <!-- Disk Threshold -->
            <div>
              <label for="threshold" class="input-label">{t('settings.diskThreshold', { percent: String(diskThresholdPercent) })}</label>
              <input
                id="threshold"
                type="range"
                class="w-full h-2 bg-slate-700 rounded-lg appearance-none cursor-pointer accent-cyan-500 mt-2"
                bind:value={diskThresholdPercent}
                min="0"
                max="100"
              />
              <div class="flex justify-between text-xs text-slate-500 mt-1">
                <span>0%</span>
                <span>50%</span>
                <span>100%</span>
              </div>
              {#if validationErrors['disk_threshold']}
                <p class="text-red-400 text-xs mt-1">{validationErrors['disk_threshold']}</p>
              {/if}
            </div>

            <!-- Check Interval -->
            <div>
              <label for="interval" class="input-label">{t('settings.checkInterval')}</label>
              <select id="interval" class="input" bind:value={checkInterval}>
                <option value="30m">{t('settings.every30m')}</option>
                <option value="1h">{t('settings.every1h')}</option>
                <option value="6h">{t('settings.every6h')}</option>
                <option value="24h">{t('settings.every24h')}</option>
              </select>
            </div>
          </div>
        </div>


        <!-- Frontend Preferences -->
        <div class="card p-6 border border-slate-700/60">
          <h3 class="text-lg font-semibold text-slate-100 mb-1">{t('settings.frontendPrefs')}</h3>
          <p class="text-sm text-slate-400 mb-6">{t('settings.frontendPrefsDesc')}</p>

          <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
            <!-- Items Per Page -->
            <div>
              <label for="itemsPerPage" class="input-label">{t('settings.itemsPerPage')}</label>
              <select id="itemsPerPage" class="input" bind:value={itemsPerPage}>
                <option value="20">20</option>
                <option value="50">50</option>
                <option value="100">100</option>
              </select>
            </div>

            <!-- Auto Refresh -->
            <div>
              <label for="autoRefresh" class="input-label">{t('settings.autoRefresh')}</label>
              <select id="autoRefresh" class="input" bind:value={autoRefresh}>
                <option value="10s">{t('settings.every10s')}</option>
                <option value="30s">{t('settings.every30s')}</option>
                <option value="60s">{t('settings.every60s')}</option>
                <option value="off">{t('settings.off')}</option>
              </select>
            </div>
          </div>
        </div>

        <!-- Save button & feedback -->
        <div class="flex items-center gap-4">
          <button
            on:click={save}
            class="btn btn-primary"
            disabled={saving}
          >
            {#if saving}
              <span class="spinner mr-2"></span>
              {t('settings.saving')}
            {:else}
              {t('settings.save')}
            {/if}
          </button>

          {#if saveSuccess}
            <span class="text-emerald-400 text-sm">{t('settings.saved')}</span>
          {/if}

          {#if saveError}
            <span class="text-red-400 text-sm">{saveError}</span>
          {/if}
        </div>

      </div>
    {/if}
  </main>
</div>
