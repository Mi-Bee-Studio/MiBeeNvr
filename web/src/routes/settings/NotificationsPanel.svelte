<script lang="ts">
  // Outbound alert notifications — forward NVR alerts to external webhook
  // receivers. Part of the unified settings shell (#153): no save button
  // here; the shell drives save/reset via the settingsForm coordinator.
  // Section changes apply on the next start — performSave surfaces the
  // restart_required the API answers with.
  import { onMount, onDestroy } from 'svelte';
  import {
    getSettings,
    updateSettings,
    getNotificationsStatus,
    sendTestNotification,
    type NotificationSinkSettings,
    type NotificationsStatus,
  } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast.svelte';
  import { settingsForm } from '$lib/settings/settings-form.svelte';
  import SettingsCard from '$lib/components/SettingsCard.svelte';
  import Toggle from '$lib/components/Toggle.svelte';
  import { Plus, X, Send } from 'lucide-svelte';

  const SEVERITIES = ['info', 'warning', 'error', 'critical'];

  let loading = $state(true);
  let error = $state('');

  // Config state — mirrors internal/config/config_notify.go.
  let enabled = $state(false);
  let baseUrl = $state('');
  let apiKeyInput = $state('');
  let apiKeyConfigured = $state(false);
  let sinks = $state<NotificationSinkSettings[]>([]);

  // Event-type vocabulary (from /api/notifications/status) + live counters.
  let types = $state<string[]>([]);
  let status = $state<NotificationsStatus | null>(null);
  let testing = $state(false);

  // Original values for dirty tracking + destructive detection.
  let originalSnapshot = $state('');
  let originalEnabled = $state(false);

  let isDirty = $derived.by(() => {
    if (loading) return false;
    const current = JSON.stringify(persistShape());
    return current !== originalSnapshot;
  });

  function persistShape() {
    return {
      enabled,
      baseUrl,
      apiKey: apiKeyInput, // blank = keep existing server-side
      sinks: sinks.map((s) => ({ ...s })),
    };
  }

  function captureSnapshot() {
    originalSnapshot = JSON.stringify(persistShape());
    originalEnabled = enabled;
  }

  async function loadConfig() {
    loading = true;
    error = '';
    try {
      const [settings, st] = await Promise.all([getSettings(), getNotificationsStatus()]);
      const cfg = settings.notifications;
      enabled = cfg?.enabled ?? false;
      baseUrl = cfg?.base_url ?? '';
      apiKeyConfigured = cfg?.api_key_configured ?? false;
      apiKeyInput = '';
      sinks = (cfg?.sinks ?? []).map((s) => ({ ...s }));
      types = st.types ?? [];
      status = st;
      captureSnapshot();
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedLoadSettings');
    } finally {
      loading = false;
    }
  }

  async function refreshStatus() {
    try {
      status = await getNotificationsStatus();
    } catch {
      // Counters are best-effort decoration; ignore transient failures.
    }
  }

  async function performSave() {
    // Client-side validation — mirrors backend Validate().
    if (enabled) {
      if (sinks.length === 0) {
        const msg = t('settings.notifications.validationSinks');
        showToast(msg, 'error');
        throw new Error(msg);
      }
      for (const s of sinks) {
        if (!s.name.trim() || !/^https?:\/\/.+/i.test(s.url.trim())) {
          const msg = t('settings.notifications.validationSink');
          showToast(msg, 'error');
          throw new Error(msg);
        }
        if (!s.token?.trim() && !s.token_configured) {
          const msg = t('settings.notifications.validationToken');
          showToast(msg, 'error');
          throw new Error(msg);
        }
      }
    }
    try {
      const res = await updateSettings({
        notifications: {
          enabled,
          base_url: baseUrl.trim(),
          api_key: apiKeyInput.trim(),
          sinks: sinks.map((s) => ({
            name: s.name.trim(),
            url: s.url.trim(),
            token: s.token?.trim() ?? '',
            events: s.events ?? [],
            severity_min: s.severity_min ?? 'info',
            timeout_s: s.timeout_s ?? 5,
            burst_per_min: s.burst_per_min ?? 30,
          })),
        },
      });
      captureSnapshot();
      apiKeyInput = '';
      showToast(t('settings.saved'), 'success');
      if (res.restart_required) {
        showToast(t('settings.notifications.restartRequired'), 'info');
      }
      await loadConfig();
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.failedSaveSettings'), 'error');
      throw e;
    }
  }

  function resetForm() {
    try {
      const snap = JSON.parse(originalSnapshot);
      enabled = snap.enabled;
      baseUrl = snap.baseUrl;
      apiKeyInput = '';
      sinks = (snap.sinks ?? []).map((s: NotificationSinkSettings) => ({ ...s }));
    } catch { /* ignore */ }
  }

  // --- Sink list editor ---

  function addSink() {
    sinks = [
      ...sinks,
      {
        name: '',
        url: '',
        token: '',
        token_configured: false,
        events: [],
        severity_min: 'info',
        timeout_s: 5,
        burst_per_min: 30,
      },
    ];
  }

  function removeSink(index: number) {
    sinks = sinks.filter((_, i) => i !== index);
  }

  function toggleSinkEvent(sink: NotificationSinkSettings, type: string) {
    const events = sink.events ? [...sink.events] : [];
    const i = events.indexOf(type);
    if (i >= 0) {
      events.splice(i, 1);
    } else {
      events.push(type);
    }
    sink.events = events;
    sinks = [...sinks];
  }

  async function sendTest() {
    if (testing) return;
    testing = true;
    try {
      await sendTestNotification({ severity: 'warning' });
      showToast(t('settings.notifications.testQueued'), 'info');
      // Counters update asynchronously — poll a couple of times.
      setTimeout(refreshStatus, 800);
      setTimeout(refreshStatus, 2500);
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.error'), 'error');
    } finally {
      testing = false;
    }
  }

  let unregister: (() => void) | undefined;
  onMount(() => {
    loadConfig();
    unregister = settingsForm.register('notifications', {
      isDirty: () => isDirty,
      save: performSave,
      reset: resetForm,
      getDestructiveWarning: () => {
        if (originalEnabled && !enabled) {
          return t('settings.notifications.destructiveOff');
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
    title={t('settings.notifications.title')}
    subtitle={t('settings.notifications.description')}
    badge={enabled
      ? { text: t('settings.featureToggles.enabled'), color: 'success' as const }
      : { text: t('settings.featureToggles.disabled'), color: 'warning' as const }}
  >
    <div class="flex items-center justify-between mb-6">
      <span class="text-sm th-text-secondary">{t('settings.notifications.enabled')}</span>
      <Toggle checked={enabled} onChange={(v) => { enabled = v; }} label={t('settings.notifications.enabled')} />
    </div>

    {#if enabled}
      <div class="space-y-5">
        <!-- Snapshot links: base URL + dedicated API key -->
        <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div>
            <label class="block text-sm th-text-secondary mb-1" for="notify-base-url">
              {t('settings.notifications.baseUrl')}
            </label>
            <input
              id="notify-base-url"
              type="text"
              class="input"
              placeholder="http://192.168.1.10:9090"
              bind:value={baseUrl}
            />
            <p class="text-xs th-text-tertiary mt-1">{t('settings.notifications.baseUrlHint')}</p>
          </div>
          <div>
            <label class="block text-sm th-text-secondary mb-1" for="notify-api-key">
              {t('settings.notifications.apiKey')}
            </label>
            <input
              id="notify-api-key"
              type="password"
              class="input"
              autocomplete="new-password"
              placeholder={apiKeyConfigured ? t('settings.notifications.secretConfigured') : 'mbv_...'}
              bind:value={apiKeyInput}
            />
            <p class="text-xs th-text-tertiary mt-1">{t('settings.notifications.apiKeyHint')}</p>
          </div>
        </div>

        <!-- Sinks -->
        <div class="space-y-4">
          <div class="flex items-center justify-between">
            <h4 class="text-sm font-medium th-text-primary">{t('settings.notifications.sinks')}</h4>
            <button class="btn btn-sm btn-secondary" onclick={addSink}>
              <Plus size={14} class="mr-1" />
              {t('settings.notifications.addSink')}
            </button>
          </div>

          {#each sinks as sink, i (i)}
            <div class="border th-border rounded-lg p-4 space-y-4">
              <div class="flex items-start justify-between gap-2">
                <div class="grid grid-cols-1 md:grid-cols-3 gap-3 flex-1">
                  <div>
                    <label class="block text-xs th-text-secondary mb-1" for="sink-name-{i}">
                      {t('settings.notifications.sinkName')}
                    </label>
                    <input id="sink-name-{i}" type="text" class="input" bind:value={sink.name} />
                  </div>
                  <div>
                    <label class="block text-xs th-text-secondary mb-1" for="sink-url-{i}">
                      {t('settings.notifications.sinkUrl')}
                    </label>
                    <input
                      id="sink-url-{i}"
                      type="text"
                      class="input"
                      placeholder="http://192.168.1.20:9080/api/ingest/webhook"
                      bind:value={sink.url}
                    />
                  </div>
                  <div>
                    <label class="block text-xs th-text-secondary mb-1" for="sink-token-{i}">
                      {t('settings.notifications.sinkToken')}
                    </label>
                    <input
                      id="sink-token-{i}"
                      type="password"
                      class="input"
                      autocomplete="new-password"
                      placeholder={sink.token_configured ? t('settings.notifications.secretConfigured') : ''}
                      bind:value={sink.token}
                    />
                  </div>
                </div>
                <button
                  class="btn btn-ghost btn-sm mt-5"
                  onclick={() => removeSink(i)}
                  title={t('settings.notifications.removeSink')}
                >
                  <X size={14} />
                </button>
              </div>

              <div class="grid grid-cols-1 md:grid-cols-3 gap-3">
                <div>
                  <label class="block text-xs th-text-secondary mb-1" for="sink-sev-{i}">
                    {t('settings.notifications.severityMin')}
                  </label>
                  <select id="sink-sev-{i}" class="input" bind:value={sink.severity_min}>
                    {#each SEVERITIES as sev}
                      <option value={sev}>{t(`settings.notifications.severity.${sev}`)}</option>
                    {/each}
                  </select>
                </div>
                <div>
                  <label class="block text-xs th-text-secondary mb-1" for="sink-timeout-{i}">
                    {t('settings.notifications.timeoutS')}
                  </label>
                  <input id="sink-timeout-{i}" type="number" min="1" max="120" class="input" bind:value={sink.timeout_s} />
                </div>
                <div>
                  <label class="block text-xs th-text-secondary mb-1" for="sink-burst-{i}">
                    {t('settings.notifications.burstPerMin')}
                  </label>
                  <input id="sink-burst-{i}" type="number" min="1" class="input" bind:value={sink.burst_per_min} />
                </div>
              </div>

              <div>
                <p class="text-xs th-text-secondary mb-2">
                  {t('settings.notifications.events')}
                  <span class="th-text-tertiary"> — {t('settings.notifications.eventsHint')}</span>
                </p>
                <div class="flex flex-wrap gap-2">
                  {#each types as type (type)}
                    <button
                      type="button"
                      class="inline-flex items-center px-2 py-1 text-xs rounded-md border transition-colors {(sink.events ?? []).includes(type) ? 'border-accent bg-accent-subtle text-accent' : 'th-border th-bg-hover th-text-secondary'}"
                      onclick={() => toggleSinkEvent(sink, type)}
                    >
                      {t(`settings.notifications.eventType.${type}`)}
                    </button>
                  {/each}
                </div>
              </div>

              {#if status?.sinks?.[i]}
                {@const stats = status.sinks[i]}
                <div class="text-xs th-text-tertiary flex flex-wrap gap-x-4 gap-y-1">
                  <span>{t('settings.notifications.stats.sent')}: <span class="th-text-primary">{stats.sent_total}</span></span>
                  <span>{t('settings.notifications.stats.failed')}: <span class="th-text-primary">{stats.failed_total}</span></span>
                  <span>{t('settings.notifications.stats.dropped')}: <span class="th-text-primary">{stats.dropped_queue + stats.dropped_rate + stats.dropped_filter}</span></span>
                </div>
              {/if}
            </div>
          {/each}

          {#if sinks.length === 0}
            <p class="text-sm th-text-tertiary">{t('settings.notifications.noSinks')}</p>
          {/if}
        </div>

        <!-- Test button -->
        <div class="flex items-center gap-3 pt-2">
          <button class="btn btn-sm btn-secondary" disabled={testing || sinks.length === 0} onclick={sendTest}>
            <Send size={14} class="mr-1" />
            {testing ? t('settings.notifications.testing') : t('settings.notifications.testButton')}
          </button>
          <span class="text-xs th-text-tertiary">{t('settings.notifications.testHint')}</span>
        </div>
      </div>
    {/if}
  </SettingsCard>
{/if}
