<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getMergeSettings, updateMergeSettings } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { settingsForm } from '$lib/settings/settings-form.svelte';
  import SettingsCard from '$lib/components/SettingsCard.svelte';
  import Toggle from '$lib/components/Toggle.svelte';
  import SettingsTranscodingCard from './SettingsTranscodingCard.svelte';

  // Merge state
  let mergeEnabled = $state(true);
  let originalMergeEnabled = $state(true);
  let loading = $state(true);

  let isDirty = $derived(!loading && mergeEnabled !== originalMergeEnabled);

  let unregister: (() => void) | undefined;

  async function loadMergeSettings() {
    loading = true;
    try {
      const mergeSettings = await getMergeSettings();
      mergeEnabled = mergeSettings.enabled ?? true;
      originalMergeEnabled = mergeEnabled;
    } catch (e) {
      console.warn('Failed to load merge settings:', e);
    } finally {
      loading = false;
    }
  }

  async function performSave() {
    await updateMergeSettings({ enabled: mergeEnabled });
    originalMergeEnabled = mergeEnabled;
    showToast(t('settings.saved'), 'success');
  }

  function resetForm() {
    mergeEnabled = originalMergeEnabled;
  }

  onMount(() => {
    loadMergeSettings();
    unregister = settingsForm.register('processing', {
      isDirty: () => isDirty,
      save: performSave,
      reset: resetForm,
      getDestructiveWarning: () => {
        if (originalMergeEnabled && !mergeEnabled) {
          return t('settings.destructive.mergeOff');
        }
        return null;
      },
    });
  });
  onDestroy(() => unregister?.());
</script>

{#if loading}
  <div class="card border th-border p-6">
    <div class="space-y-3">
      <div class="h-6 w-40 th-bg-tertiary rounded animate-pulse"></div>
      <div class="h-4 w-64 th-bg-tertiary rounded animate-pulse"></div>
    </div>
  </div>
{:else}
  <!-- Segment Merging -->
  <SettingsCard
    title={t('merge.title')}
    subtitle={t('settings.advanced.merge.description')}
    badge={mergeEnabled
      ? { text: t('settings.featureToggles.enabled'), color: 'success' as const }
      : { text: t('settings.featureToggles.disabled'), color: 'warning' as const }}
  >
    <div class="flex items-center justify-between">
      <div>
        <span class="text-sm font-medium th-text-primary">{t('merge.enableMerge')}</span>
        <p class="text-xs th-text-tertiary mt-0.5">{mergeEnabled ? t('merge.enabledState') : t('merge.disabledState')}</p>
      </div>
      <Toggle checked={mergeEnabled} onChange={(v) => { mergeEnabled = v; }} label={t('merge.enableMerge')} />
    </div>
  </SettingsCard>

  <!-- Transcoding -->
  <SettingsCard
    title={t('transcoding.title')}
    subtitle={t('transcoding.description')}
  >
    <SettingsTranscodingCard />
  </SettingsCard>
{/if}
