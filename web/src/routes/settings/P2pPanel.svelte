<script lang="ts">
  import { onMount } from 'svelte';
  import {
    getP2pSettings,
    updateP2pSettings,
    getP2pStatus,
    getLicense,
    installLicense,
    getP2pAssociation,
    confirmP2pAssociation,
    cancelP2pAssociation,
    type P2pSettings,
    type P2pStatus,
    type LicenseStatus,
    type P2pAssociationState,
  } from '$lib/api';
  import SettingsCard from '$lib/components/SettingsCard.svelte';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast.svelte';
  import { settingsForm } from '$lib/settings/settings-form.svelte';

  // --- Load state ---
  let loading = $state(true);
  let error = $state('');
  let settings: P2pSettings | null = $state(null);
  let status: P2pStatus | null = $state(null);
  let license: LicenseStatus | null = $state(null);

  // --- P2P form state ---
  let enabled = $state(false);
  let serverUrl = $state('');
  let peerId = $state('');
  let username = $state('');
  let password = $state('');
  let maxClients = $state(0);
  let saved = $state({ enabled: false, serverUrl: '', peerId: '', username: '', maxClients: 0 });

  // --- License install form ---
  let licenseDoc = $state('');
  let licensePubKey = $state('');
  let licenseRequired = $state(false);
  let installing = $state(false);

  let isDirty = $derived.by(() => {
    if (loading || !settings) return false;
    return (
      enabled !== saved.enabled ||
      serverUrl.trim() !== saved.serverUrl ||
      peerId.trim() !== saved.peerId ||
      username.trim() !== saved.username ||
      maxClients !== saved.maxClients ||
      password !== ''
    );
  });

  function snapshot() {
    saved = {
      enabled,
      serverUrl: serverUrl.trim(),
      peerId: peerId.trim(),
      username: username.trim(),
      maxClients,
    };
  }

  async function load() {
    loading = true;
    error = '';
    try {
      settings = await getP2pSettings();
      enabled = settings.enabled;
      serverUrl = settings.server_url;
      peerId = settings.peer_id;
      username = settings.username;
      password = '';
      maxClients = settings.max_clients;
      snapshot();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
    await Promise.all([refreshStatus(), refreshLicense(), refreshAssociation()]);
  }

  async function refreshStatus() {
    try {
      status = await getP2pStatus();
    } catch {
      status = null;
    }
  }

  async function refreshLicense() {
    try {
      license = await getLicense();
      licenseRequired = license.required;
    } catch {
      license = null;
    }
  }

  // --- Account association (M1 local-confirm pairing) ---

  let assoc: P2pAssociationState | null = $state(null);
  let assocBusy = $state(false);

  async function refreshAssociation() {
    try {
      assoc = await getP2pAssociation();
    } catch {
      assoc = null;
    }
  }

  function fmtTime(iso?: string): string {
    if (!iso) return '-';
    const d = new Date(iso);
    return Number.isNaN(d.getTime())
      ? iso
      : d.toLocaleString(undefined, { hour12: false });
  }

  async function confirmAssociation() {
    const p = assoc?.pending;
    if (!p || assocBusy) return;
    assocBusy = true;
    try {
      assoc = await confirmP2pAssociation(p.request_id);
      showToast(t('settings.p2p.assocConfirmDone'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.failedSaveSettings'), 'error');
    } finally {
      assocBusy = false;
    }
  }

  // 拒绝申请与取消关联同一端点（清空 pending + confirmed）。
  async function clearAssociation() {
    if (assocBusy) return;
    assocBusy = true;
    try {
      await cancelP2pAssociation();
      await refreshAssociation();
      showToast(t('settings.p2p.assocCleared'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.failedSaveSettings'), 'error');
    } finally {
      assocBusy = false;
    }
  }

  async function save() {
    const body: Record<string, unknown> = {
      enabled,
      server_url: serverUrl.trim(),
      peer_id: peerId.trim(),
      username: username.trim(),
      max_clients: maxClients,
    };
    if (password !== '') body.password = password;
    try {
      status = await updateP2pSettings(body);
      if (settings) settings.password_configured = settings.password_configured || password !== '';
      password = '';
      snapshot();
      showToast(t('settings.saved'), 'success');
      await refreshLicense();
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.failedSaveSettings'), 'error');
      throw e;
    }
  }

  function reset() {
    if (!settings) return;
    enabled = saved.enabled;
    serverUrl = saved.serverUrl;
    peerId = saved.peerId;
    username = saved.username;
    maxClients = saved.maxClients;
    password = '';
  }

  async function saveLicense() {
    if (installing) return;
    installing = true;
    try {
      license = await installLicense({
        document: licenseDoc.trim(),
        // 留空 = 沿用已配置公钥（换钥即在此填新钥）。
        public_key: licensePubKey.trim(),
        required: licenseRequired,
      });
      licenseDoc = '';
      licensePubKey = '';
      showToast(t('settings.p2p.licenseInstalled'), 'success');
      await refreshStatus();
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('settings.p2p.licenseInstallFailed'), 'error');
    } finally {
      installing = false;
    }
  }

  // Register with the unified save pipeline（onMount——register 内部会读写
  // coordinator 的 $state，放 $effect 里会形成无限重跑环）。
  let unregister: (() => void) | undefined;
  let pollTimer: ReturnType<typeof setInterval> | undefined;
  let assocPollTimer: ReturnType<typeof setInterval> | undefined;
  onMount(() => {
    load();
    unregister = settingsForm.register('p2p', {
      isDirty: () => isDirty,
      save,
      reset,
    });
    pollTimer = setInterval(() => {
      refreshStatus();
      refreshLicense();
      // 有待确认申请时轮询加密（8s）——确认页面的时效性；无申请时随大轮。
      if (assoc?.pending) refreshAssociation();
    }, 15000);
    assocPollTimer = setInterval(() => {
      if (assoc?.pending) refreshAssociation();
    }, 8000);
    return () => {
      clearInterval(pollTimer);
      clearInterval(assocPollTimer);
      unregister?.();
    };
  });

  const capSourceLabel = $derived(
    status?.cap_source
      ? t(`settings.p2p.capSource.${status.cap_source}`)
      : '',
  );
</script>

{#if loading}
  <div class="animate-pulse space-y-3">
    <div class="h-8 th-bg-secondary rounded"></div>
    <div class="h-32 th-bg-secondary rounded"></div>
  </div>
{:else if error}
  <div class="p-4 rounded-lg bg-red-500/10 text-red-500 text-sm">{error}</div>
{:else if settings}
  <!-- 账号授权关联卡（M1：App 发起 → 此处一键确认；docs/05 方案） -->
  <SettingsCard title={t('settings.p2p.assocTitle')} defaultOpen>
    <div class="space-y-4 text-sm">
      <p class="th-text-secondary text-xs">{t('settings.p2p.assocHint')}</p>

      {#if assoc?.pending}
        <div class="p-4 rounded-lg border border-blue-500/40 bg-blue-500/5 space-y-2">
          <div class="flex items-center gap-2 font-medium th-text-primary">
            <span class="inline-block w-2 h-2 rounded-full bg-blue-500 animate-pulse"></span>
            {t('settings.p2p.assocPendingTitle')}
          </div>
          <div class="th-text-secondary">
            {assoc.pending.display_name || assoc.pending.platform_username}
            <span class="text-xs">({assoc.pending.platform_username || assoc.pending.platform_user_id})</span>
          </div>
          <div class="text-xs th-text-secondary">
            {t('settings.p2p.assocRequestedAt')}: {fmtTime(assoc.pending.at)}
          </div>
          <div class="flex gap-3 pt-1">
            <button
              type="button"
              onclick={confirmAssociation}
              disabled={assocBusy}
              class="px-4 py-2 rounded-lg bg-blue-600 text-white text-sm font-medium disabled:opacity-50"
            >
              {t('settings.p2p.assocConfirm')}
            </button>
            <button
              type="button"
              onclick={clearAssociation}
              disabled={assocBusy}
              class="px-4 py-2 rounded-lg border th-border text-sm th-text-secondary disabled:opacity-50"
            >
              {t('settings.p2p.assocReject')}
            </button>
          </div>
        </div>
      {:else if assoc?.confirmed}
        <div class="p-4 rounded-lg border border-green-500/30 bg-green-500/5 space-y-2">
          <div class="flex items-center gap-2 font-medium text-green-500">
            <span class="inline-block w-2 h-2 rounded-full bg-green-500"></span>
            {t('settings.p2p.assocConfirmedTitle')}
          </div>
          <div class="th-text-secondary">
            {assoc.confirmed.display_name || assoc.confirmed.platform_username}
            <span class="text-xs">({assoc.confirmed.platform_username || assoc.confirmed.platform_user_id})</span>
          </div>
          <div class="text-xs th-text-secondary">
            {t('settings.p2p.assocConfirmedAt')}: {fmtTime(assoc.confirmed.confirmed_at)}
          </div>
          <div class="pt-1">
            <button
              type="button"
              onclick={clearAssociation}
              disabled={assocBusy}
              class="px-4 py-2 rounded-lg border border-red-500/40 text-red-500 text-sm disabled:opacity-50"
            >
              {t('settings.p2p.assocCancel')}
            </button>
          </div>
        </div>
      {:else}
        <p class="th-text-secondary text-xs">{t('settings.p2p.assocEmpty')}</p>
      {/if}
    </div>
  </SettingsCard>

  <!-- 状态卡 -->
  <SettingsCard title={t('settings.p2p.statusTitle')} defaultOpen>
    <div class="space-y-2 text-sm">
      <div class="flex items-center gap-2">
        <span
          class="inline-block w-2 h-2 rounded-full {status?.online
            ? 'bg-green-500'
            : 'bg-gray-400'}"
        ></span>
        <span class="th-text-primary"
          >{status?.online ? t('settings.p2p.online') : t('settings.p2p.offline')}</span
        >
        {#if !settings.enabled}
          <span class="text-xs th-text-secondary">({t('settings.p2p.disabled')})</span>
        {/if}
      </div>
      <div class="th-text-secondary">
        {t('settings.p2p.clients')}:
        <span class="th-text-primary font-medium"
          >{status?.active_clients ?? 0} / {status?.max_clients ?? '-'}</span
        >
        {#if capSourceLabel}
          <span class="text-xs">({capSourceLabel})</span>
        {/if}
        {#if status && status.rejected_total > 0}
          <span class="text-xs text-amber-500">
            · {t('settings.p2p.rejectedTotal')}: {status.rejected_total}</span
          >
        {/if}
      </div>
      {#if license}
        <div class="th-text-secondary">
          {t('settings.p2p.licenseState')}:
          <span
            class="font-medium {license.state === 'valid'
              ? 'text-green-500'
              : license.state === 'expiring'
                ? 'text-amber-500'
                : license.state === 'none'
                  ? 'th-text-secondary'
                  : 'text-red-500'}"
            >{t(`settings.p2p.licenseStateValue.${license.state}`)}</span
          >
          {#if license.document?.plan}
            <span class="text-xs">({license.document.plan})</span>
          {/if}
          {#if license.time_left_sec && license.time_left_sec > 0}
            <span class="text-xs">
              · {t('settings.p2p.licenseTimeLeft')}:
              {Math.floor(license.time_left_sec / 3600)}h{Math.floor((license.time_left_sec % 3600) / 60)}m</span
            >
          {/if}
        </div>
      {/if}
      {#if status?.last_rejection}
        <div class="text-xs text-red-500">
          {t('settings.p2p.lastRejection')}: {status.last_rejection.reason}
          ({status.last_rejection.code}/{status.last_rejection.source})
        </div>
      {/if}
    </div>
  </SettingsCard>

  <!-- P2P 配置卡 -->
  <SettingsCard title={t('settings.p2p.configTitle')} defaultOpen>
    <div class="space-y-4">
      <label class="flex items-center justify-between cursor-pointer">
        <span class="text-sm font-medium th-text-primary">{t('settings.p2p.enableToggle')}</span>
        <input type="checkbox" bind:checked={enabled} class="w-4 h-4 accent-blue-600" />
      </label>
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
        <div>
          <label class="block text-sm th-text-secondary mb-1" for="p2p-server-url"
            >{t('settings.p2p.serverUrl')}</label
          >
          <input
            id="p2p-server-url"
            type="text"
            bind:value={serverUrl}
            placeholder="wss://…/signal"
            class="w-full px-3 py-2 rounded-lg th-bg-secondary th-text-primary text-sm"
          />
        </div>
        <div>
          <label class="block text-sm th-text-secondary mb-1" for="p2p-peer-id"
            >{t('settings.p2p.peerId')}</label
          >
          <input
            id="p2p-peer-id"
            type="text"
            bind:value={peerId}
            class="w-full px-3 py-2 rounded-lg th-bg-secondary th-text-primary text-sm"
          />
        </div>
        <div>
          <label class="block text-sm th-text-secondary mb-1" for="p2p-username"
            >{t('settings.p2p.username')}</label
          >
          <input
            id="p2p-username"
            type="text"
            bind:value={username}
            class="w-full px-3 py-2 rounded-lg th-bg-secondary th-text-primary text-sm"
          />
        </div>
        <div>
          <label class="block text-sm th-text-secondary mb-1" for="p2p-password"
            >{t('settings.p2p.password')}</label
          >
          <input
            id="p2p-password"
            type="password"
            bind:value={password}
            placeholder={settings.password_configured ? t('settings.p2p.passwordKeep') : ''}
            class="w-full px-3 py-2 rounded-lg th-bg-secondary th-text-primary text-sm"
          />
        </div>
        <div>
          <label class="block text-sm th-text-secondary mb-1" for="p2p-max-clients"
            >{t('settings.p2p.maxClients')}</label
          >
          <input
            id="p2p-max-clients"
            type="number"
            min="0"
            max="64"
            bind:value={maxClients}
            class="w-full px-3 py-2 rounded-lg th-bg-secondary th-text-primary text-sm"
          />
          <p class="text-xs th-text-secondary mt-1">{t('settings.p2p.maxClientsHint')}</p>
        </div>
      </div>
    </div>
  </SettingsCard>

  <!-- License 卡 -->
  <SettingsCard title={t('settings.p2p.licenseTitle')} defaultOpen>
    <div class="space-y-4">
      <p class="text-sm th-text-secondary">{t('settings.p2p.licenseHint')}</p>
      <div>
        <label class="block text-sm th-text-secondary mb-1" for="license-document"
          >{t('settings.p2p.licenseDocument')}</label
        >
        <textarea
          id="license-document"
          bind:value={licenseDoc}
          rows="3"
          placeholder="base64(payload).base64(sig)"
          class="w-full px-3 py-2 rounded-lg th-bg-secondary th-text-primary text-xs font-mono"
        ></textarea>
      </div>
      <div>
        <label class="block text-sm th-text-secondary mb-1" for="license-pubkey"
          >{t('settings.p2p.licensePubKey')}</label
        >
        <input
          id="license-pubkey"
          type="text"
          bind:value={licensePubKey}
          placeholder={t('settings.p2p.licensePubKeyKeep')}
          class="w-full px-3 py-2 rounded-lg th-bg-secondary th-text-primary text-xs font-mono"
        />
      </div>
      <label class="flex items-center justify-between cursor-pointer">
        <span class="text-sm font-medium th-text-primary">{t('settings.p2p.licenseRequired')}</span>
        <input type="checkbox" bind:checked={licenseRequired} class="w-4 h-4 accent-blue-600" />
      </label>
      <div class="flex justify-end">
        <button
          type="button"
          onclick={saveLicense}
          disabled={installing}
          class="px-4 py-2 rounded-lg bg-blue-600 text-white text-sm font-medium disabled:opacity-50"
        >
          {installing ? t('settings.saving') : t('settings.p2p.licenseInstall')}
        </button>
      </div>
    </div>
  </SettingsCard>
{/if}
