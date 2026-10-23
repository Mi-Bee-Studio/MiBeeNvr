<script lang="ts">
  // UPnP AV / DLNA media server settings (#970).
  // Part of the unified settings shell (#153): no save button here; the
  // shell drives save/reset via the settingsForm coordinator. Section
  // changes apply on the next start — performSave surfaces the
  // restart_required the API answers with.
  import { onMount, onDestroy } from 'svelte';
  import { getSettings, updateSettings } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast.svelte';
  import { settingsForm } from '$lib/settings/settings-form.svelte';
  import SettingsCard from '$lib/components/SettingsCard.svelte';
  import Toggle from '$lib/components/Toggle.svelte';
  import { Plus, X } from 'lucide-svelte';

  let loading = $state(true);
  let error = $state('');

  // Config state — mirrors internal/config/config_dlna.go defaults
  // (apply_defaults.go fills port 49152).
  let dlnaEnabled = $state(false);
  let port = $state(49152);
  let interfaces = $state<string[]>([]);
  let newInterface = $state('');
  let friendlyName = $state('');

  // Original values for dirty tracking + destructive detection.
  let originalSnapshot = $state('');
  let originalEnabled = $state(false);

  let isDirty = $derived.by(() => {
    if (loading) return false;
    const current = JSON.stringify({ dlnaEnabled, port, interfaces, friendlyName });
    return current !== originalSnapshot;
  });

  function captureSnapshot() {
    originalSnapshot = JSON.stringify({ dlnaEnabled, port, interfaces, friendlyName });
    originalEnabled = dlnaEnabled;
  }

  async function loadConfig() {
    loading = true;
    error = '';
    try {
      const settings = await getSettings();
      const cfg = settings.dlna;
      dlnaEnabled = cfg?.enabled ?? false;
      port = cfg?.port ?? 49152;
      interfaces = cfg?.interfaces ? [...cfg.interfaces] : [];
      friendlyName = cfg?.friendly_name ?? '';
      captureSnapshot();
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedLoadSettings');
    } finally {
      loading = false;
    }
  }

  async function performSave() {
    // Client-side validation — mirrors backend Validate(): enabled DLNA
    // needs a valid port and non-empty interface names.
    if (dlnaEnabled) {
      const p = Number(port);
      if (!Number.isInteger(p) || p < 1 || p > 65535) {
        const msg = t('settings.dlna.validationPort');
        showToast(msg, 'error');
        throw new Error(msg);
      }
      if (interfaces.some((n) => !n.trim())) {
        const msg = t('settings.dlna.validationInterface');
        showToast(msg, 'error');
        throw new Error(msg);
      }
    }
    try {
      const res = await updateSettings({
        dlna: {
          enabled: dlnaEnabled,
          port: Number(port) || 0,
          interfaces,
          friendly_name: friendlyName.trim(),
        },
      });
      captureSnapshot();
      showToast(t('settings.saved'), 'success');
      if (res.restart_required) {
        showToast(t('settings.dlna.restartRequired'), 'info');
      }
    } catch (e) {
      // Re-throw so the unified shell keeps the dirty bar visible (#160).
      showToast(e instanceof Error ? e.message : t('common.failedSaveSettings'), 'error');
      throw e;
    }
  }

  function resetForm() {
    try {
      const snap = JSON.parse(originalSnapshot);
      dlnaEnabled = snap.dlnaEnabled;
      port = snap.port;
      interfaces = snap.interfaces;
      friendlyName = snap.friendlyName;
    } catch { /* ignore */ }
  }

  // --- Interface list editor (tag-style) ---
  function addInterface() {
    const names = newInterface
      .split(',')
      .map((s) => s.trim())
      .filter((s) => s.length > 0);
    if (names.length === 0) return;
    const existing = new Set(interfaces);
    for (const name of names) {
      if (!existing.has(name)) {
        existing.add(name);
        interfaces = [...existing];
      }
    }
    newInterface = '';
  }

  function removeInterface(name: string) {
    interfaces = interfaces.filter((n) => n !== name);
  }

  let unregister: (() => void) | undefined;
  onMount(() => {
    loadConfig();
    unregister = settingsForm.register('dlna', {
      isDirty: () => isDirty,
      save: performSave,
      reset: resetForm,
      getDestructiveWarning: () => {
        if (originalEnabled && !dlnaEnabled) {
          return t('settings.dlna.destructiveOff');
        }
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
      <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
        <div class="space-y-2"><div class="h-4 w-24 th-bg-tertiary rounded animate-pulse"></div><div class="h-10 th-bg-tertiary rounded animate-pulse"></div></div>
        <div class="space-y-2"><div class="h-4 w-28 th-bg-tertiary rounded animate-pulse"></div><div class="h-10 th-bg-tertiary rounded animate-pulse"></div></div>
      </div>
    </div>
  </div>
{:else if error}
  <div class="card border th-border-danger p-8 text-center">
    <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
    <p class="th-text-secondary mb-4">{error}</p>
    <button onclick={loadConfig} class="btn btn-primary btn-sm">{t('common.retry')}</button>
  </div>
{:else}
  <SettingsCard
    title={t('settings.dlna.title')}
    subtitle={t('settings.dlna.description')}
    badge={dlnaEnabled
      ? { text: t('settings.featureToggles.enabled'), color: 'success' as const }
      : { text: t('settings.featureToggles.disabled'), color: 'warning' as const }}
  >
    <div class="flex items-center justify-between mb-6">
      <span class="text-sm th-text-secondary">{t('settings.dlna.enabled')}</span>
      <Toggle checked={dlnaEnabled} onChange={(v) => { dlnaEnabled = v; }} label={t('settings.dlna.enabled')} />
    </div>

    {#if dlnaEnabled}
      <div class="space-y-6">
        <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
          <!-- HTTP listen port -->
          <div>
            <label class="input-label" for="dlna-port">{t('settings.dlna.port')}</label>
            <input id="dlna-port" class="input" type="number" min="1" max="65535" bind:value={port} />
            <p class="text-xs th-text-tertiary mt-1">{t('settings.dlna.portHint')}</p>
          </div>

          <!-- Friendly name (SSDP device name on the TV) -->
          <div>
            <label class="input-label" for="dlna-friendly-name">{t('settings.dlna.friendlyName')}</label>
            <input id="dlna-friendly-name" class="input" type="text" bind:value={friendlyName} placeholder="MiBee NVR" />
            <p class="text-xs th-text-tertiary mt-1">{t('settings.dlna.friendlyNameHint')}</p>
          </div>
        </div>

        <!-- Multicast interfaces -->
        <div>
          <label class="input-label" for="dlna-interfaces">{t('settings.dlna.interfaces')}</label>
          <p class="text-xs th-text-tertiary mb-2">{t('settings.dlna.interfacesHint')}</p>
          {#if interfaces.length > 0}
            <div class="flex flex-wrap gap-2 mb-2">
              {#each interfaces as name (name)}
                <span class="inline-flex items-center gap-1 px-2 py-1 text-xs rounded-md border th-border th-bg-hover th-text-primary">
                  {name}
                  <button
                    type="button"
                    class="th-text-tertiary hover:th-text-primary"
                    aria-label={t('settings.dlna.interfacesRemove')}
                    onclick={() => removeInterface(name)}
                  >
                    <X size={12} />
                  </button>
                </span>
              {/each}
            </div>
          {/if}
          <div class="flex gap-2">
            <input
              id="dlna-interfaces"
              class="input flex-1"
              type="text"
              bind:value={newInterface}
              placeholder="eth0"
              onkeydown={(e: KeyboardEvent) => {
                if (e.key === 'Enter') {
                  e.preventDefault();
                  addInterface();
                }
              }}
            />
            <button type="button" class="btn btn-ghost" onclick={addInterface} disabled={!newInterface.trim()}>
              <Plus size={14} />
              {t('settings.dlna.interfacesAdd')}
            </button>
          </div>
        </div>

        <!-- What the server exposes — read-only explainer -->
        <div class="p-3 rounded-md border th-border th-bg-hover">
          <span class="text-sm font-medium th-text-primary">{t('settings.dlna.contentTitle')}</span>
          <p class="text-xs th-text-tertiary mt-1">{t('settings.dlna.contentHint')}</p>
          <p class="text-xs th-text-tertiary mt-1">{t('settings.dlna.lanOnlyHint')}</p>
        </div>
      </div>
    {/if}
  </SettingsCard>
{/if}
