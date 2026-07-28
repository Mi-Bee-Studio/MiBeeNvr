<script lang="ts">
  // 摄像头接入 (Camera Access) — auto-discover settings only.
  // Part of the unified settings shell (#153): no save button here; the
  // shell drives save/reset via the settingsForm coordinator.
  import { onMount, onDestroy } from 'svelte';
  import { getAutoDiscoverSettings, updateAutoDiscoverSettings, type AutoDiscoverSettings } from '$lib/api/settings';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { friendlyError } from '$lib/errors';
  import { settingsForm } from '$lib/settings/settings-form.svelte';
  import SettingsCard from '$lib/components/SettingsCard.svelte';
  import Toggle from '$lib/components/Toggle.svelte';

  let loading = $state(true);
  let error = $state('');

  // --- Auto-Discover ---
  let adSettings = $state<AutoDiscoverSettings | null>(null);
  // Local form fields (so the toggle/inputs are reactive before save).
  let adEnabled = $state(false);
  let adNetworkInterface = $state('');
  let adDefaultUsername = $state('');
  let adDefaultPassword = $state(''); // only sent when the user types a new one
  let adIgnoreScopes = $state(''); // comma-separated in the UI
  let adHasPassword = $state(false);

  // Original values for dirty tracking + destructive detection.
  let originalEnabled = $state(false);

  // Dirty when any field diverges from the loaded config.
  let isDirty = $derived.by(() => {
    if (loading || !adSettings) return false;
    return (
      adEnabled !== adSettings.enabled ||
      adNetworkInterface !== adSettings.network_interface ||
      adDefaultUsername !== adSettings.default_username ||
      adIgnoreScopes !== (adSettings.ignore_scopes ?? []).join(', ') ||
      adDefaultPassword !== ''
    );
  });

  async function loadAutoDiscover() {
    loading = true;
    error = '';
    try {
      const cfg = await getAutoDiscoverSettings();
      adSettings = cfg;
      adEnabled = cfg.enabled;
      originalEnabled = cfg.enabled;
      adNetworkInterface = cfg.network_interface;
      adDefaultUsername = cfg.default_username;
      adHasPassword = cfg.has_default_password;
      adIgnoreScopes = (cfg.ignore_scopes ?? []).join(', ');
      adDefaultPassword = ''; // never pre-fill; only sent when the user types
    } catch (e: any) {
      error = friendlyError(e, 'settings.autoDiscover.loadFailed');
    } finally {
      loading = false;
    }
  }

  async function performSave() {
    try {
      const payload: Record<string, unknown> = {
        enabled: adEnabled,
        network_interface: adNetworkInterface,
        default_username: adDefaultUsername,
        ignore_scopes: adIgnoreScopes.split(',').map((s) => s.trim()).filter(Boolean),
      };
      // Only send the password when the user typed a new one (empty = unchanged).
      if (adDefaultPassword) payload.default_password = adDefaultPassword;
      await updateAutoDiscoverSettings(payload);
      adDefaultPassword = '';
      await loadAutoDiscover(); // refresh has_default_password
      showToast(t('settings.autoDiscover.saved'), 'success');
    } catch (e: any) {
      // Re-throw so the unified shell keeps the dirty bar visible and reports
      // the failure (#160). Without this, saveAll treats the panel as saved
      // and the user's backend failure is hidden behind a one-shot toast.
      showToast(friendlyError(e, 'settings.autoDiscover.saveFailed'), 'error');
      throw e;
    }
  }

  function resetForm() {
    // Restore from the last loaded config snapshot.
    if (!adSettings) return;
    adEnabled = adSettings.enabled;
    adNetworkInterface = adSettings.network_interface;
    adDefaultUsername = adSettings.default_username;
    adIgnoreScopes = (adSettings.ignore_scopes ?? []).join(', ');
    adDefaultPassword = '';
  }

  let unregister: (() => void) | undefined;
  onMount(() => {
    loadAutoDiscover();
    unregister = settingsForm.register('cameras', {
      isDirty: () => isDirty,
      save: performSave,
      reset: resetForm,
      getDestructiveWarning: () => {
        // Destructive if: turning OFF auto-discover.
        if (originalEnabled && !adEnabled)
          return t('settings.destructive.autodiscoverOff');
        return null;
      },
    });
  });

  onDestroy(() => unregister?.());
</script>

{#if loading}
  <div class="card border th-border">
    <div class="p-6 space-y-4">
      <div class="h-6 w-40 th-bg-tertiary rounded animate-pulse"></div>
      <div class="h-4 w-64 th-bg-tertiary rounded animate-pulse"></div>
      <div class="h-10 w-20 th-bg-tertiary rounded animate-pulse"></div>
    </div>
  </div>
{:else if error}
  <div class="card border th-border-danger p-8 text-center">
    <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
    <p class="th-text-secondary mb-4">{error}</p>
    <button onclick={loadAutoDiscover} class="btn btn-primary btn-sm">{t('common.retry')}</button>
  </div>
{:else}
  <!-- Auto-Discover Cameras -->
  <SettingsCard
    title={t('settings.autoDiscover.title')}
    subtitle={t('settings.autoDiscover.description')}
    badge={adEnabled
      ? { text: t('settings.featureToggles.enabled'), color: 'success' as const }
      : { text: t('settings.featureToggles.disabled'), color: 'warning' as const }}
  >
    <div class="flex items-center justify-between mb-4">
      <span class="text-sm th-text-secondary">{t('settings.autoDiscover.enabled')}</span>
      <Toggle checked={adEnabled} onChange={(v) => { adEnabled = v; }} label={t('settings.autoDiscover.enabled')} />
    </div>

    {#if adEnabled}
      <div class="space-y-6">
        <!-- Network interface -->
        <div>
          <label class="input-label" for="ad-iface">{t('settings.autoDiscover.networkInterface')}</label>
          <input id="ad-iface" type="text" class="input mt-1 w-full" bind:value={adNetworkInterface} placeholder="eth0" />
          <p class="text-xs th-text-tertiary mt-1">{t('settings.autoDiscover.networkInterfaceHint')}</p>
        </div>

        <!-- Default credentials -->
        <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div>
            <label class="input-label" for="ad-user">{t('settings.autoDiscover.defaultUsername')}</label>
            <input id="ad-user" type="text" class="input mt-1 w-full" bind:value={adDefaultUsername} autocomplete="username" />
            <p class="text-xs th-text-tertiary mt-1">{t('settings.autoDiscover.defaultUsernameHint')}</p>
          </div>
          <div>
            <label class="input-label" for="ad-pass">{t('settings.autoDiscover.defaultPassword')}</label>
            <input id="ad-pass" type="password" class="input mt-1 w-full" bind:value={adDefaultPassword} autocomplete="new-password" placeholder={adHasPassword ? '••••••••' : ''} />
            <p class="text-xs th-text-tertiary mt-1">{adHasPassword ? t('settings.autoDiscover.passwordSet') : t('settings.autoDiscover.defaultPasswordHint')}</p>
          </div>
        </div>

        <!-- Ignore scopes -->
        <div>
          <label class="input-label" for="ad-scopes">{t('settings.autoDiscover.ignoreScopes')}</label>
          <input id="ad-scopes" type="text" class="input mt-1 w-full" bind:value={adIgnoreScopes} placeholder="hardware/LegacyCam" />
          <p class="text-xs th-text-tertiary mt-1">{t('settings.autoDiscover.ignoreScopesHint')}</p>
        </div>
      </div>
    {/if}
  </SettingsCard>
{/if}
