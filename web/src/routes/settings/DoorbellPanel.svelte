<script lang="ts">
  // SIP doorbell / intercom ingress settings (#971).
  // Part of the unified settings shell (#153): no save button here; the
  // shell drives save/reset via the settingsForm coordinator. Section
  // changes apply on the next start — performSave surfaces the
  // restart_required the API answers with.
  import { onMount, onDestroy } from 'svelte';
  import { getSettings, updateSettings, getDoorbellStatus, listCamerasSummary } from '$lib/api';
  import type { DoorbellStatus } from '$lib/api';
  import type { CameraSummary } from '$lib/api/cameras';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast.svelte';
  import { settingsForm } from '$lib/settings/settings-form.svelte';
  import SettingsCard from '$lib/components/SettingsCard.svelte';
  import Toggle from '$lib/components/Toggle.svelte';
  import { Plus, Trash2 } from 'lucide-svelte';

  let loading = $state(true);
  let error = $state('');

  // Runtime diagnostics — enabled-vs-running divergence is the
  // pending-restart state; never blocks the panel.
  let status = $state<DoorbellStatus | null>(null);
  let cameras = $state<CameraSummary[]>([]);
  // Set when THIS session saved a change the running service has not picked
  // up yet (restart_required). runtime-only divergence (enabled config, no
  // service) covers the same state across reloads; the flag also covers the
  // reverse direction — disabled config, service still listening until the
  // restart.
  let pendingRestart = $state(false);

  // Config state — mirrors internal/config/config_doorbell.go defaults
  // (apply_defaults.go).
  let doorbellEnabled = $state(false);
  let listen = $state(':5070');
  let realm = $state('mibee-nvr');
  let transport = $state('udp');
  let tlsCert = $state('');
  let tlsKey = $state('');
  let lanOnly = $state(true);
  let ringTimeout = $state('60s');

  interface UserRow {
    key: string;
    username: string;
    name: string;
    cameraId: string;
    secret: string;
    secretConfigured: boolean;
  }
  let users = $state<UserRow[]>([]);

  let outboundEnabled = $state(false);
  let outboundServer = $state('');
  let outboundUsername = $state('');
  let outboundSecret = $state('');
  let outboundSecretConfigured = $state(false);
  let outboundExpires = $state('300s');

  // Original values for dirty tracking + destructive detection.
  let originalSnapshot = $state('');
  let originalEnabled = $state(false);

  const TRANSPORT_OPTIONS = ['udp', 'tcp', 'tls'];

  let isDirty = $derived.by(() => {
    if (loading) return false;
    return JSON.stringify(currentForm()) !== originalSnapshot;
  });

  function currentForm() {
    return {
      doorbellEnabled,
      listen,
      realm,
      transport,
      tlsCert,
      tlsKey,
      lanOnly,
      ringTimeout,
      users: users.map(({ key, secretConfigured, ...rest }) => rest),
      outboundEnabled,
      outboundServer,
      outboundUsername,
      outboundSecret,
      outboundExpires,
    };
  }

  function captureSnapshot() {
    originalSnapshot = JSON.stringify(currentForm());
    originalEnabled = doorbellEnabled;
  }

  async function loadConfig() {
    loading = true;
    error = '';
    pendingRestart = false;
    getDoorbellStatus()
      .then((st) => (status = st))
      .catch(() => (status = null));
    listCamerasSummary()
      .then((list) => (cameras = list ?? []))
      .catch(() => (cameras = []));
    try {
      const settings = await getSettings();
      const cfg = settings.doorbell;
      doorbellEnabled = cfg?.enabled ?? false;
      listen = cfg?.listen ?? ':5070';
      realm = cfg?.realm ?? 'mibee-nvr';
      transport = cfg?.transport ?? 'udp';
      tlsCert = cfg?.tls_cert ?? '';
      tlsKey = cfg?.tls_key ?? '';
      lanOnly = cfg?.lan_only ?? true;
      ringTimeout = cfg?.ring_timeout ?? '60s';
      users = (cfg?.users ?? []).map((u) => ({
        key: 'u' + Math.random().toString(36).slice(2, 8),
        username: u.username,
        name: u.name,
        cameraId: u.camera_id,
        // The API never returns secrets — blank keeps the stored one.
        secret: '',
        secretConfigured: u.secret_configured ?? false,
      }));
      outboundEnabled = !!cfg?.outbound;
      outboundServer = cfg?.outbound?.server ?? '';
      outboundUsername = cfg?.outbound?.username ?? '';
      outboundSecret = '';
      outboundSecretConfigured = cfg?.outbound?.secret_configured ?? false;
      outboundExpires = cfg?.outbound?.expires ?? '300s';
      captureSnapshot();
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedLoadSettings');
    } finally {
      loading = false;
    }
  }

  async function performSave() {
    // Client-side validation — mirrors backend Validate(): an enabled
    // ingress needs at least one door station or the PBX outbound;
    // usernames must be non-empty and unique; TLS transport needs the cert
    // pair; durations must parse.
    if (doorbellEnabled) {
      if (users.length === 0 && !outboundEnabled) {
        throwValidation('settings.doorbell.validationUsersRequired');
      }
      if (users.some((u) => !u.username.trim())) {
        throwValidation('settings.doorbell.validationUsername');
      }
      const names = new Set(users.map((u) => u.username.trim()));
      if (names.size !== users.length) {
        throwValidation('settings.doorbell.validationUsernameDuplicate');
      }
      if (transport === 'tls' && (!tlsCert.trim() || !tlsKey.trim())) {
        throwValidation('settings.doorbell.validationTls');
      }
      if (ringTimeout.trim() && !/^\d+(\.\d+)?(ns|us|µs|ms|s|m|h)$/.test(ringTimeout.trim())) {
        throwValidation('settings.doorbell.validationDuration');
      }
      if (outboundEnabled) {
        if (!outboundServer.trim() || !outboundUsername.trim()) {
          throwValidation('settings.doorbell.validationOutbound');
        }
        if (!outboundSecret.trim() && !outboundSecretConfigured) {
          throwValidation('settings.doorbell.validationOutboundSecret');
        }
      }
    }
    try {
      const res = await updateSettings({
        doorbell: {
          enabled: doorbellEnabled,
          listen: listen.trim(),
          realm: realm.trim(),
          transport,
          tls_cert: tlsCert.trim(),
          tls_key: tlsKey.trim(),
          lan_only: lanOnly,
          ring_timeout: ringTimeout.trim(),
          users: users.map((u) => ({
            username: u.username.trim(),
            name: u.name.trim(),
            camera_id: u.cameraId,
            secret: u.secret,
          })),
          outbound: outboundEnabled
            ? {
                enabled: true,
                server: outboundServer.trim(),
                username: outboundUsername.trim(),
                secret: outboundSecret,
                expires: outboundExpires.trim(),
              }
            : { enabled: false },
        },
      });
      // Blank secrets round-trip as "keep"; typed ones now count as
      // configured. Clear the fields so a re-save is a no-op dirty-wise.
      users.forEach((u) => {
        if (u.secret) u.secretConfigured = true;
        u.secret = '';
      });
      if (outboundSecret) outboundSecretConfigured = true;
      outboundSecret = '';
      captureSnapshot();
      showToast(t('settings.saved'), 'success');
      if (res.restart_required) {
        showToast(t('settings.doorbell.restartRequired'), 'info');
        pendingRestart = true;
        getDoorbellStatus()
          .then((st) => (status = st))
          .catch(() => {});
      }
    } catch (e) {
      // Re-throw so the unified shell keeps the dirty bar visible (#160).
      showToast(e instanceof Error ? e.message : t('common.failedSaveSettings'), 'error');
      throw e;
    }
  }

  function throwValidation(key: string): never {
    const msg = t(key);
    showToast(msg, 'error');
    throw new Error(msg);
  }

  function resetForm() {
    try {
      const snap = JSON.parse(originalSnapshot);
      doorbellEnabled = snap.doorbellEnabled;
      listen = snap.listen;
      realm = snap.realm;
      transport = snap.transport;
      tlsCert = snap.tlsCert;
      tlsKey = snap.tlsKey;
      lanOnly = snap.lanOnly;
      ringTimeout = snap.ringTimeout;
      users = snap.users.map((u: Omit<UserRow, 'key' | 'secretConfigured'>) => ({
        ...u,
        key: 'u' + Math.random().toString(36).slice(2, 8),
        secret: '',
        secretConfigured: users.find((old) => old.username === u.username)?.secretConfigured ?? false,
      }));
      outboundEnabled = snap.outboundEnabled;
      outboundServer = snap.outboundServer;
      outboundUsername = snap.outboundUsername;
      outboundSecret = '';
      outboundExpires = snap.outboundExpires;
    } catch { /* ignore */ }
  }

  // --- Door station row editor ---
  function addUser() {
    users = [
      ...users,
      {
        key: 'u' + Math.random().toString(36).slice(2, 8),
        username: '',
        name: '',
        cameraId: '',
        secret: '',
        secretConfigured: false,
      },
    ];
  }

  function removeUser(key: string) {
    users = users.filter((u) => u.key !== key);
  }

  let unregister: (() => void) | undefined;
  onMount(() => {
    loadConfig();
    unregister = settingsForm.register('doorbell', {
      isDirty: () => isDirty,
      save: performSave,
      reset: resetForm,
      getDestructiveWarning: () => {
        if (originalEnabled && !doorbellEnabled) {
          return t('settings.doorbell.destructiveOff');
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
    title={t('settings.doorbell.title')}
    subtitle={t('settings.doorbell.description')}
    badge={doorbellEnabled
      ? { text: t('settings.featureToggles.enabled'), color: 'success' as const }
      : { text: t('settings.featureToggles.disabled'), color: 'warning' as const }}
  >
    <div class="flex items-center justify-between mb-6">
      <span class="text-sm th-text-secondary">{t('settings.doorbell.enabled')}</span>
      <Toggle checked={doorbellEnabled} onChange={(v) => { doorbellEnabled = v; }} label={t('settings.doorbell.enabled')} />
    </div>

    {#if doorbellEnabled}
      <div class="space-y-6">
        <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
          <!-- SIP listen address -->
          <div>
            <label class="input-label" for="doorbell-listen">{t('settings.doorbell.listen')}</label>
            <input id="doorbell-listen" class="input" type="text" bind:value={listen} placeholder=":5070" />
            <p class="text-xs th-text-tertiary mt-1">{t('settings.doorbell.listenHint')}</p>
          </div>

          <!-- Digest realm -->
          <div>
            <label class="input-label" for="doorbell-realm">{t('settings.doorbell.realm')}</label>
            <input id="doorbell-realm" class="input" type="text" bind:value={realm} placeholder="mibee-nvr" />
            <p class="text-xs th-text-tertiary mt-1">{t('settings.doorbell.realmHint')}</p>
          </div>

          <!-- SIP transport -->
          <div>
            <label class="input-label" for="doorbell-transport">{t('settings.doorbell.transport')}</label>
            <select id="doorbell-transport" class="input" bind:value={transport}>
              <option value="udp">{t('settings.doorbell.transportUdp')}</option>
              <option value="tcp">{t('settings.doorbell.transportTcp')}</option>
              <option value="tls">{t('settings.doorbell.transportTls')}</option>
            </select>
            <p class="text-xs th-text-tertiary mt-1">{t('settings.doorbell.transportHint')}</p>
          </div>

          <!-- Ring timeout -->
          <div>
            <label class="input-label" for="doorbell-ring-timeout">{t('settings.doorbell.ringTimeout')}</label>
            <input id="doorbell-ring-timeout" class="input" type="text" bind:value={ringTimeout} placeholder="60s" />
            <p class="text-xs th-text-tertiary mt-1">{t('settings.doorbell.ringTimeoutHint')}</p>
          </div>

          {#if transport === 'tls'}
            <!-- TLS certificate pair (transport=tls) -->
            <div>
              <label class="input-label" for="doorbell-tls-cert">{t('settings.doorbell.tlsCert')}</label>
              <input id="doorbell-tls-cert" class="input" type="text" bind:value={tlsCert} placeholder="/etc/ssl/certs/doorbell.pem" />
              <p class="text-xs th-text-tertiary mt-1">{t('settings.doorbell.tlsCertHint')}</p>
            </div>
            <div>
              <label class="input-label" for="doorbell-tls-key">{t('settings.doorbell.tlsKey')}</label>
              <input id="doorbell-tls-key" class="input" type="text" bind:value={tlsKey} placeholder="/etc/ssl/private/doorbell.key" />
              <p class="text-xs th-text-tertiary mt-1">{t('settings.doorbell.tlsKeyHint')}</p>
            </div>
          {/if}
        </div>

        <!-- LAN-only guard -->
        <div class="flex items-center justify-between p-3 rounded-md border th-border th-bg-hover">
          <div>
            <span class="text-sm font-medium th-text-primary">{t('settings.doorbell.lanOnly')}</span>
            <p class="text-xs th-text-tertiary mt-1">{t('settings.doorbell.lanOnlyHint')}</p>
          </div>
          <Toggle checked={lanOnly} onChange={(v) => { lanOnly = v; }} label={t('settings.doorbell.lanOnly')} />
        </div>

        <!-- Door stations (mode A) -->
        <div>
          <div class="flex items-center justify-between mb-2">
            <div>
              <span class="text-sm font-medium th-text-primary">{t('settings.doorbell.usersTitle')}</span>
              <p class="text-xs th-text-tertiary mt-1">{t('settings.doorbell.usersHint')}</p>
            </div>
            <button type="button" class="btn btn-ghost btn-sm" onclick={addUser}>
              <Plus size={14} />
              {t('settings.doorbell.userAdd')}
            </button>
          </div>
          <div class="space-y-3">
            {#each users as u (u.key)}
              <div class="grid grid-cols-1 md:grid-cols-5 gap-3 items-end p-3 rounded-md border th-border th-bg-hover">
                <div>
                  <label class="input-label" for="doorbell-user-{u.key}-username">{t('settings.doorbell.userUsername')}</label>
                  <input id="doorbell-user-{u.key}-username" class="input" type="text" bind:value={u.username} placeholder="door1" autocomplete="off" />
                </div>
                <div>
                  <label class="input-label" for="doorbell-user-{u.key}-name">{t('settings.doorbell.userName')}</label>
                  <input id="doorbell-user-{u.key}-name" class="input" type="text" bind:value={u.name} placeholder={t('settings.doorbell.userNamePlaceholder')} />
                </div>
                <div>
                  <label class="input-label" for="doorbell-user-{u.key}-camera">{t('settings.doorbell.userCamera')}</label>
                  <select id="doorbell-user-{u.key}-camera" class="input" bind:value={u.cameraId}>
                    <option value="">{t('settings.doorbell.userNoCamera')}</option>
                    {#each cameras as cam (cam.id)}
                      <option value={cam.id}>{cam.name}</option>
                    {/each}
                  </select>
                </div>
                <div>
                  <label class="input-label" for="doorbell-user-{u.key}-secret">{t('settings.doorbell.userSecret')}</label>
                  <input
                    id="doorbell-user-{u.key}-secret"
                    class="input"
                    type="password"
                    bind:value={u.secret}
                    autocomplete="new-password"
                    placeholder={u.secretConfigured ? '********' : ''}
                  />
                  <p class="text-xs th-text-tertiary mt-1">
                    {u.secretConfigured ? t('settings.doorbell.userSecretConfigured') : t('settings.doorbell.userSecretHint')}
                  </p>
                </div>
                <button
                  type="button"
                  class="btn btn-ghost btn-sm justify-self-end"
                  aria-label={t('settings.doorbell.userRemove')}
                  onclick={() => removeUser(u.key)}
                >
                  <Trash2 size={14} />
                </button>
              </div>
            {/each}
            {#if users.length === 0}
              <p class="text-xs th-text-tertiary">{t('settings.doorbell.usersEmpty')}</p>
            {/if}
          </div>
        </div>

        <!-- PBX outbound (mode B) -->
        <div class="space-y-3">
          <div class="flex items-center justify-between p-3 rounded-md border th-border th-bg-hover">
            <div>
              <span class="text-sm font-medium th-text-primary">{t('settings.doorbell.outboundTitle')}</span>
              <p class="text-xs th-text-tertiary mt-1">{t('settings.doorbell.outboundHint')}</p>
            </div>
            <Toggle checked={outboundEnabled} onChange={(v) => { outboundEnabled = v; }} label={t('settings.doorbell.outboundTitle')} />
          </div>
          {#if outboundEnabled}
            <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
              <div>
                <label class="input-label" for="doorbell-outbound-server">{t('settings.doorbell.outboundServer')}</label>
                <input id="doorbell-outbound-server" class="input" type="text" bind:value={outboundServer} placeholder="pbx.example.com:5060" />
                <p class="text-xs th-text-tertiary mt-1">{t('settings.doorbell.outboundServerHint')}</p>
              </div>
              <div>
                <label class="input-label" for="doorbell-outbound-username">{t('settings.doorbell.outboundUsername')}</label>
                <input id="doorbell-outbound-username" class="input" type="text" bind:value={outboundUsername} autocomplete="off" />
              </div>
              <div>
                <label class="input-label" for="doorbell-outbound-secret">{t('settings.doorbell.outboundSecret')}</label>
                <input
                  id="doorbell-outbound-secret"
                  class="input"
                  type="password"
                  bind:value={outboundSecret}
                  autocomplete="new-password"
                  placeholder={outboundSecretConfigured ? '********' : ''}
                />
                <p class="text-xs th-text-tertiary mt-1">
                  {outboundSecretConfigured ? t('settings.doorbell.userSecretConfigured') : t('settings.doorbell.userSecretHint')}
                </p>
              </div>
              <div>
                <label class="input-label" for="doorbell-outbound-expires">{t('settings.doorbell.outboundExpires')}</label>
                <input id="doorbell-outbound-expires" class="input" type="text" bind:value={outboundExpires} placeholder="300s" />
                <p class="text-xs th-text-tertiary mt-1">{t('settings.doorbell.outboundExpiresHint')}</p>
              </div>
            </div>
          {/if}
        </div>
      </div>
    {/if}

    <!-- Runtime status — read-only diagnostics (cascade-style row). -->
    <div class="flex items-center justify-between p-3 rounded-md border th-border th-bg-hover">
      <div>
        <span class="text-sm font-medium th-text-primary">{t('settings.doorbell.statusTitle')}</span>
        <p class="text-xs th-text-tertiary mt-1">{t('settings.doorbell.statusHint')}</p>
      </div>
      <div class="flex flex-col gap-1 items-end text-xs">
        {#if pendingRestart || (doorbellEnabled && status !== null && !status.running)}
          <span class="inline-flex items-center gap-1.5 text-yellow-600 dark:text-yellow-400">
            <span class="w-2 h-2 rounded-full bg-yellow-500 animate-pulse"></span>
            {t('settings.doorbell.statusRestartPending')}
          </span>
        {:else if status !== null && status.running}
          <span class="inline-flex items-center gap-1.5 text-green-600 dark:text-green-400">
            <span class="w-2 h-2 rounded-full bg-green-500"></span>
            {t('settings.doorbell.statusRunning')}
          </span>
        {:else}
          <span class="th-text-tertiary">{t('settings.doorbell.statusStopped')}</span>
        {/if}
        {#if status && status.registered.length > 0}
          <span class="th-text-tertiary">
            {t('settings.doorbell.statusRegistered', { count: status.registered.length })}: {status.registered.map((r) => r.user).join(', ')}
          </span>
        {/if}
      </div>
    </div>
  </SettingsCard>
{/if}
