<script lang="ts">
  // GB/T 28181 platform server settings.
  // Part of the unified settings shell (#153): no save button here; the
  // shell drives save/reset via the settingsForm coordinator.
  import { onMount, onDestroy } from 'svelte';
  import { getSettings, updateSettings } from '$lib/api';
  import type { GB28181Config } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { settingsForm } from '$lib/settings/settings-form.svelte';
  import SettingsCard from '$lib/components/SettingsCard.svelte';
  import Toggle from '$lib/components/Toggle.svelte';
  import { Plus, X } from 'lucide-svelte';

  let loading = $state(true);
  let error = $state('');

  // GB28181 server config state — mirrors internal/config/config_gb28181.go
  // defaults (applyConfigDefaults).
  let gb28181Enabled = $state(false);
  let sipListen = $state(':5060');
  let serverId = $state('');
  let realm = $state('34020000002000000001');
  // The API never returns the password (password_configured only), so this
  // field stays blank on load; leaving it blank keeps the current password.
  let password = $state('');
  let passwordConfigured = $state(false);
  let portRange = $state('30000-30050');
  let heartbeatInterval = $state('60s');
  let catalogInterval = $state('30m');
  let mediaTransport = $state('udp');
  let tcpFraming = $state('auto');
  let allowedDeviceIds = $state<string[]>([]);
  let newDeviceId = $state('');

  // Original values for dirty tracking + destructive detection
  let originalSnapshot = $state('');
  let originalEnabled = $state(false);

  const TCP_FRAMING_OPTIONS = ['auto', 'rfc4571', '0x24'];

  // Derived: is any GB28181 setting dirty?
  let isDirty = $derived.by(() => {
    if (loading) return false;
    const current = JSON.stringify({
      gb28181Enabled,
      sipListen,
      serverId,
      realm,
      password,
      portRange,
      heartbeatInterval,
      catalogInterval,
      mediaTransport,
      tcpFraming,
      allowedDeviceIds,
    });
    return current !== originalSnapshot;
  });

  function captureSnapshot() {
    originalSnapshot = JSON.stringify({
      gb28181Enabled,
      sipListen,
      serverId,
      realm,
      password,
      portRange,
      heartbeatInterval,
      catalogInterval,
      mediaTransport,
      tcpFraming,
      allowedDeviceIds,
    });
    originalEnabled = gb28181Enabled;
  }

  async function loadConfig() {
    loading = true;
    error = '';
    try {
      const settings = await getSettings();
      const cfg: GB28181Config | undefined = settings.gb28181;
      gb28181Enabled = cfg?.enabled ?? false;
      sipListen = cfg?.sip_listen ?? ':5060';
      serverId = cfg?.server_id ?? '';
      realm = cfg?.realm ?? '34020000002000000001';
      password = '';
      passwordConfigured = cfg?.password_configured ?? false;
      portRange = cfg?.port_range ?? '30000-30050';
      heartbeatInterval = cfg?.heartbeat_interval ?? '60s';
      catalogInterval = cfg?.catalog_interval ?? '30m';
      mediaTransport = cfg?.media_transport ?? (cfg?.tcp_mode ? 'tcp-passive' : 'udp');
      tcpFraming = cfg?.tcp_framing ?? 'auto';
      allowedDeviceIds = cfg?.allowed_device_ids ? [...cfg.allowed_device_ids] : [];
      captureSnapshot();
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedLoadSettings');
    } finally {
      loading = false;
    }
  }

  async function performSave() {
    // Client-side validation — mirrors backend Validate(): an enabled server
    // requires server_id + sip_listen.
    if (gb28181Enabled) {
      if (!serverId.trim()) {
        const msg = t('settings.gb28181.validationServerId');
        showToast(msg, 'error');
        throw new Error(msg);
      }
      if (!sipListen.trim()) {
        const msg = t('settings.gb28181.validationSipListen');
        showToast(msg, 'error');
        throw new Error(msg);
      }
    }
    try {
      await updateSettings({
        gb28181: {
          enabled: gb28181Enabled,
          sip_listen: sipListen.trim(),
          server_id: serverId.trim(),
          realm: realm.trim(),
          password,
          port_range: portRange.trim(),
          heartbeat_interval: heartbeatInterval.trim(),
          catalog_interval: catalogInterval.trim(),
          media_transport: mediaTransport,
          tcp_framing: tcpFraming,
          allowed_device_ids: allowedDeviceIds,
        },
      });
      captureSnapshot();
      showToast(t('settings.saved'), 'success');
    } catch (e) {
      // Re-throw so the unified shell keeps the dirty bar visible and reports
      // the failure (#160).
      showToast(e instanceof Error ? e.message : t('common.failedSaveSettings'), 'error');
      throw e;
    }
  }

  function resetForm() {
    // Restore from the last captured snapshot.
    try {
      const snap = JSON.parse(originalSnapshot);
      gb28181Enabled = snap.gb28181Enabled;
      sipListen = snap.sipListen;
      serverId = snap.serverId;
      realm = snap.realm;
      password = snap.password;
      portRange = snap.portRange;
      heartbeatInterval = snap.heartbeatInterval;
      catalogInterval = snap.catalogInterval;
      mediaTransport = snap.mediaTransport;
      tcpFraming = snap.tcpFraming;
      allowedDeviceIds = snap.allowedDeviceIds;
    } catch { /* ignore */ }
  }

  // --- Allowed device list editor (tag-style) ---
  function addDeviceId() {
    const ids = newDeviceId
      .split(',')
      .map((s) => s.trim())
      .filter((s) => s.length > 0);
    if (ids.length === 0) return;
    const existing = new Set(allowedDeviceIds);
    for (const id of ids) {
      if (!existing.has(id)) {
        existing.add(id);
        allowedDeviceIds = [...existing];
      }
    }
    newDeviceId = '';
  }

  function removeDeviceId(id: string) {
    allowedDeviceIds = allowedDeviceIds.filter((d) => d !== id);
  }

  let unregister: (() => void) | undefined;
  onMount(() => {
    loadConfig();
    unregister = settingsForm.register('gb28181', {
      isDirty: () => isDirty,
      save: performSave,
      reset: resetForm,
      getDestructiveWarning: () => {
        // Destructive if: turning OFF the GB28181 server.
        if (originalEnabled && !gb28181Enabled) {
          return t('settings.gb28181.destructiveOff');
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
    title={t('settings.gb28181.title')}
    subtitle={t('settings.gb28181.description')}
    badge={gb28181Enabled
      ? { text: t('settings.featureToggles.enabled'), color: 'success' as const }
      : { text: t('settings.featureToggles.disabled'), color: 'warning' as const }}
  >
    <div class="flex items-center justify-between mb-6">
      <span class="text-sm th-text-secondary">{t('settings.gb28181.enabled')}</span>
      <Toggle checked={gb28181Enabled} onChange={(v) => { gb28181Enabled = v; }} label={t('settings.gb28181.enabled')} />
    </div>

    {#if gb28181Enabled}
      <div class="space-y-6">
        <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
          <!-- SIP listen address -->
          <div>
            <label class="input-label" for="gb28181-sip-listen">{t('settings.gb28181.sipListen')}</label>
            <input
              id="gb28181-sip-listen"
              class="input"
              type="text"
              bind:value={sipListen}
              placeholder=":5060"
            />
            <p class="text-xs th-text-tertiary mt-1">{t('settings.gb28181.sipListenHint')}</p>
          </div>

          <!-- Server ID -->
          <div>
            <label class="input-label" for="gb28181-server-id">{t('settings.gb28181.serverId')}</label>
            <input
              id="gb28181-server-id"
              class="input"
              type="text"
              bind:value={serverId}
              placeholder="34020000002000000001"
              maxlength="20"
            />
            <p class="text-xs th-text-tertiary mt-1">{t('settings.gb28181.serverIdHint')}</p>
          </div>

          <!-- Realm -->
          <div>
            <label class="input-label" for="gb28181-realm">{t('settings.gb28181.realm')}</label>
            <input
              id="gb28181-realm"
              class="input"
              type="text"
              bind:value={realm}
              placeholder="34020000002000000001"
            />
            <p class="text-xs th-text-tertiary mt-1">{t('settings.gb28181.realmHint')}</p>
          </div>

          <!-- SIP digest password -->
          <div>
            <label class="input-label" for="gb28181-password">{t('settings.gb28181.password')}</label>
            <input
              id="gb28181-password"
              class="input"
              type="password"
              bind:value={password}
              autocomplete="new-password"
              placeholder={passwordConfigured ? '********' : ''}
            />
            <p class="text-xs th-text-tertiary mt-1">
              {passwordConfigured ? t('settings.gb28181.passwordConfigured') : t('settings.gb28181.passwordHint')}
            </p>
          </div>

          <!-- RTP port range -->
          <div>
            <label class="input-label" for="gb28181-port-range">{t('settings.gb28181.portRange')}</label>
            <input
              id="gb28181-port-range"
              class="input"
              type="text"
              bind:value={portRange}
              placeholder="30000-30050"
            />
            <p class="text-xs th-text-tertiary mt-1">{t('settings.gb28181.portRangeHint')}</p>
          </div>

          <!-- Heartbeat interval -->
          <div>
            <label class="input-label" for="gb28181-heartbeat">{t('settings.gb28181.heartbeatInterval')}</label>
            <input
              id="gb28181-heartbeat"
              class="input"
              type="text"
              bind:value={heartbeatInterval}
              placeholder="60s"
            />
            <p class="text-xs th-text-tertiary mt-1">{t('settings.gb28181.heartbeatIntervalHint')}</p>
          </div>

          <!-- Catalog interval -->
          <div>
            <label class="input-label" for="gb28181-catalog">{t('settings.gb28181.catalogInterval')}</label>
            <input
              id="gb28181-catalog"
              class="input"
              type="text"
              bind:value={catalogInterval}
              placeholder="30m"
            />
            <p class="text-xs th-text-tertiary mt-1">{t('settings.gb28181.catalogIntervalHint')}</p>
          </div>

          <!-- TCP framing -->
          <div>
            <label class="input-label" for="gb28181-tcp-framing">{t('settings.gb28181.tcpFraming')}</label>
            <select id="gb28181-tcp-framing" class="input" bind:value={tcpFraming}>
              {#each TCP_FRAMING_OPTIONS as opt}
                <option value={opt}>{t(`settings.gb28181.tcpFraming${opt === '0x24' ? '0x24' : opt === 'rfc4571' ? 'Rfc4571' : 'Auto'}`)}</option>
              {/each}
            </select>
            <p class="text-xs th-text-tertiary mt-1">{t('settings.gb28181.tcpFramingHint')}</p>
          </div>
        </div>

        <!-- Media transport -->
        <div class="flex items-center justify-between p-3 rounded-md border th-border th-bg-hover">
          <div>
            <span class="text-sm font-medium th-text-primary">{t('settings.gb28181.mediaTransport')}</span>
            <p class="text-xs th-text-tertiary mt-1">{t('settings.gb28181.mediaTransportHint')}</p>
          </div>
          <select
            class="input w-44"
            aria-label={t('settings.gb28181.mediaTransport')}
            bind:value={mediaTransport}
          >
            <option value="udp">{t('settings.gb28181.mediaTransportUdp')}</option>
            <option value="tcp-passive">{t('settings.gb28181.mediaTransportTcpPassive')}</option>
            <option value="tcp-active">{t('settings.gb28181.mediaTransportTcpActive')}</option>
          </select>
        </div>

        <!-- Allowed device IDs -->
        <div>
          <label class="input-label" for="gb28181-allowed-devices">{t('settings.gb28181.allowedDevices')}</label>
          <p class="text-xs th-text-tertiary mb-2">{t('settings.gb28181.allowedDevicesHint')}</p>
          {#if allowedDeviceIds.length > 0}
            <div class="flex flex-wrap gap-2 mb-2">
              {#each allowedDeviceIds as id (id)}
                <span class="inline-flex items-center gap-1 px-2 py-1 text-xs rounded-md border th-border th-bg-hover th-text-primary">
                  {id}
                  <button
                    type="button"
                    class="th-text-tertiary hover:th-text-primary"
                    aria-label={t('settings.gb28181.allowedDevicesRemove')}
                    onclick={() => removeDeviceId(id)}
                  >
                    <X size={12} />
                  </button>
                </span>
              {/each}
            </div>
          {/if}
          <div class="flex gap-2">
            <input
              id="gb28181-allowed-devices"
              class="input flex-1"
              type="text"
              bind:value={newDeviceId}
              placeholder={t('settings.gb28181.allowedDevicesPlaceholder')}
              onkeydown={(e: KeyboardEvent) => {
                if (e.key === 'Enter') {
                  e.preventDefault();
                  addDeviceId();
                }
              }}
            />
            <button type="button" class="btn btn-ghost" onclick={addDeviceId} disabled={!newDeviceId.trim()}>
              <Plus size={14} />
              {t('settings.gb28181.allowedDevicesAdd')}
            </button>
          </div>
        </div>
      </div>
    {/if}
  </SettingsCard>
{/if}