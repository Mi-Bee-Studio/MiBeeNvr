<script lang="ts">
  // 高级 (Advanced) — downgraded tuning parameters.
  // This is an informational/read-only panel: it hosts the resource estimate
  // table (and future advanced-only tunables). There is no editable state, so
  // it registers with the settingsForm coordinator using a no-op save/reset
  // and isDirty() => false — it never contributes to dirty tracking or saves.
  import { onMount, onDestroy } from 'svelte';
  import { t } from '$lib/i18n';
  import { settingsForm } from '$lib/settings/settings-form.svelte';
  import SettingsCard from '$lib/components/SettingsCard.svelte';

  // No editable state on this panel — everything below is static reference
  // material. Registering keeps the panel consistent with the shell contract
  // (and lets future advanced tunables opt in without changing the shell).
  let unregister: (() => void) | undefined;
  onMount(() => {
    unregister = settingsForm.register('advanced', {
      isDirty: () => false,
      save: async () => {
        /* no-op: read-only panel */
      },
      reset: () => {
        /* no-op: read-only panel */
      },
    });
  });

  onDestroy(() => unregister?.());
</script>

<!-- Resource Usage Estimates -->
<SettingsCard
  title={t('settings.streaming.resourceEstimate')}
  subtitle={t('settings.streaming.resourceEstimateDesc')}
>
  <div class="space-y-2">
    <div class="flex items-center gap-2 text-xs th-text-secondary">
      <span class="w-2 h-2 rounded-full bg-[var(--color-danger)]"></span>
      <span>{t('settings.streaming.resource.webrtc')}</span>
    </div>
    <div class="flex items-center gap-2 text-xs th-text-secondary">
      <span class="w-2 h-2 rounded-full bg-[var(--color-warning)]"></span>
      <span>{t('settings.streaming.resource.flv')}</span>
    </div>
    <div class="flex items-center gap-2 text-xs th-text-secondary">
      <span class="w-2 h-2 rounded-full bg-[var(--color-success)]"></span>
      <span>{t('settings.streaming.resource.hls')}</span>
    </div>
  </div>
</SettingsCard>
