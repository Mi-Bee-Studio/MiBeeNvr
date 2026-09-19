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
  // Fragment batching window in seconds (#852): null until loaded; 0 = off.
  let fragmentHoldS = $state<number | null>(null);
  let originalFragmentHoldS = $state<number | null>(null);
  let loading = $state(true);

  let isDirty = $derived(
    !loading && (mergeEnabled !== originalMergeEnabled || fragmentHoldS !== originalFragmentHoldS),
  );

  let unregister: (() => void) | undefined;

  async function loadMergeSettings() {
    loading = true;
    try {
      const mergeSettings = await getMergeSettings();
      mergeEnabled = mergeSettings.enabled ?? true;
      fragmentHoldS = mergeSettings.rolling_fragment_hold_s ?? 300;
    } catch (e) {
      console.warn('Failed to load merge settings:', e);
    } finally {
      originalMergeEnabled = mergeEnabled;
      originalFragmentHoldS = fragmentHoldS;
      loading = false;
    }
  }

  async function performSave() {
    const hold = Math.min(3600, Math.max(0, Math.trunc(fragmentHoldS ?? 300)));
    await updateMergeSettings({
      enabled: mergeEnabled,
      rolling_fragment_hold_s: hold,
    });
    fragmentHoldS = hold;
    originalMergeEnabled = mergeEnabled;
    originalFragmentHoldS = hold;
    showToast(t('settings.saved'), 'success');
  }

  function resetForm() {
    mergeEnabled = originalMergeEnabled;
    fragmentHoldS = originalFragmentHoldS;
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
    <div class="pt-4 mt-4 border-t th-border">
      <label for="fragmentHoldS" class="text-sm font-medium th-text-primary">{t('settings.merge.fragmentHold')}</label>
      <input
        id="fragmentHoldS"
        type="number"
        class="input mt-2"
        value={fragmentHoldS ?? 300}
        min="0"
        max="3600"
        step="10"
        oninput={(e) => {
          const v = parseInt((e.target as HTMLInputElement).value, 10);
          fragmentHoldS = Number.isNaN(v) ? 0 : v;
        }}
      />
      <p class="text-xs th-text-tertiary mt-1">{t('settings.merge.fragmentHoldHint')}</p>
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
