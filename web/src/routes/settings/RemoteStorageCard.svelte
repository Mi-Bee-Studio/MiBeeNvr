<script lang="ts">
  // 远程对象存储归档设置 (S3-compatible offload, issue #874) — mounted
  // inside StoragePanel. Registers itself with the settingsForm coordinator
  // under its own id so the unified save bar drives it like any panel.
  // Next-start semantics: the uploader service + playback proxy are built
  // at boot; the API answers restart_required=true when this section changes.
  import { onMount, onDestroy } from 'svelte';
  import { getSettings, updateSettings, getOffloadStatus } from '$lib/api';
  import type { SettingsConfig, OffloadStatus } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { settingsForm } from '$lib/settings/settings-form.svelte';
  import SettingsCard from '$lib/components/SettingsCard.svelte';
  import Toggle from '$lib/components/Toggle.svelte';
  import { Cloud, RefreshCw } from 'lucide-svelte';

  let loading = $state(true);
  let error = $state('');
  let saving = $state(false);

  // Form state
  let enabled = $state(false);
  let endpointURL = $state('');
  let region = $state('auto');
  let bucket = $state('');
  let prefix = $state('recordings');
  let pathStyle = $state(true);
  let accessKeyID = $state('');
  let secretInput = $state('');
  let minAgeS = $state(900);
  let backlogLimit = $state(5000);
  let maxConcurrency = $state(1);
  let afterDays = $state(0);
  let secretConfigured = $state(false);

  // Observability
  let status = $state<OffloadStatus | null>(null);

  let originalSnapshot = $state('');
  let originalAfterDays = $state(0);

  let isDirty = $derived.by(() => {
    if (loading) return false;
    const current = JSON.stringify({
      enabled, endpointURL, region, bucket, prefix, pathStyle, accessKeyID,
      secretInput, minAgeS, backlogLimit, maxConcurrency, afterDays,
    });
    return current !== originalSnapshot;
  });

  function captureSnapshot() {
    originalSnapshot = JSON.stringify({
      enabled, endpointURL, region, bucket, prefix, pathStyle, accessKeyID,
      secretInput, minAgeS, backlogLimit, maxConcurrency, afterDays,
    });
    originalAfterDays = afterDays;
  }

  async function loadAll() {
    loading = true;
    error = '';
    try {
      const settings = await getSettings();
      const remote = settings.storage?.remote;
      if (remote) {
        enabled = remote.enabled;
        endpointURL = remote.endpoint_url ?? '';
        region = remote.region ?? 'auto';
        bucket = remote.bucket ?? '';
        prefix = remote.prefix ?? 'recordings';
        pathStyle = remote.path_style ?? true;
        accessKeyID = remote.access_key_id ?? '';
        secretConfigured = !!remote.secret_configured;
        minAgeS = remote.upload?.min_age_s ?? 900;
        backlogLimit = remote.upload?.backlog_limit ?? 5000;
        maxConcurrency = remote.upload?.max_concurrency ?? 1;
        afterDays = remote.evict?.after_days ?? 0;
      }
      secretInput = '';
      captureSnapshot();
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedLoadSettings');
    } finally {
      loading = false;
    }
    await refreshStatus();
  }

  async function refreshStatus() {
    status = await getOffloadStatus().catch(() => status);
  }

  async function performSave() {
    saving = true;
    try {
      // Partial PUT: only this section. Blank secret keeps the stored value
      // (the GET never returns it), so it is only sent when the user typed
      // a new one.
      const payload: SettingsConfig = {
        storage: {
          remote: {
            enabled,
            endpoint_url: endpointURL,
            region,
            bucket,
            prefix,
            path_style: pathStyle,
            access_key_id: accessKeyID,
            ...(secretInput.trim() ? { secret_access_key: secretInput.trim() } : {}),
            upload: {
              max_concurrency: maxConcurrency,
              min_age_s: minAgeS,
              backlog_limit: backlogLimit,
            },
            evict: { after_days: afterDays },
          },
        },
      };
      const res = await updateSettings(payload);
      await loadAll();
      if (res.restart_required) {
        showToast(t('settings.remote.restartHint'), 'success');
      } else {
        showToast(t('settings.saved'), 'success');
      }
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.failedSaveSettings'), 'error');
      throw e;
    } finally {
      saving = false;
    }
  }

  function resetForm() {
    try {
      const snap = JSON.parse(originalSnapshot);
      enabled = snap.enabled; endpointURL = snap.endpointURL; region = snap.region;
      bucket = snap.bucket; prefix = snap.prefix; pathStyle = snap.pathStyle;
      accessKeyID = snap.accessKeyID; minAgeS = snap.minAgeS;
      backlogLimit = snap.backlogLimit; maxConcurrency = snap.maxConcurrency;
      afterDays = snap.afterDays;
    } catch { /* ignore */ }
    secretInput = '';
  }

  let unregister: (() => void) | undefined;
  onMount(() => {
    loadAll();
    unregister = settingsForm.register('remote-storage', {
      isDirty: () => isDirty,
      save: performSave,
      reset: resetForm,
      getDestructiveWarning: () => {
        // Turning auto-eviction on deletes local copies after the retention
        // window — the user must see that coming.
        if (originalAfterDays === 0 && afterDays > 0)
          return t('settings.remote.destructiveEvictOn');
        return null;
      },
    });
  });
  onDestroy(() => unregister?.());
</script>

{#if !loading && !error}
  <SettingsCard title={t('settings.remote.title')} subtitle={t('settings.remote.subtitle')}>
    <div class="max-w-2xl space-y-5">
      <!-- Enable -->
      <div>
        <span class="input-label">{t('settings.remote.enable')}</span>
        <div class="flex items-center gap-3 mt-2">
          <Toggle checked={enabled} onChange={(v) => { enabled = v; }} label={t('settings.remote.enable')} />
          <span class="text-sm th-text-secondary">{enabled ? t('settings.remote.enabledOn') : t('settings.remote.enabledOff')}</span>
        </div>
        <p class="text-xs th-text-muted mt-1">{t('settings.remote.enableHint')}</p>
      </div>

      <!-- Connection -->
      <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div class="md:col-span-2">
          <label for="remote-endpoint" class="input-label">{t('settings.remote.endpoint')}</label>
          <input id="remote-endpoint" class="input font-mono text-xs" placeholder={t('settings.remote.endpointPlaceholder')} bind:value={endpointURL} />
        </div>
        <div>
          <label for="remote-bucket" class="input-label">{t('settings.remote.bucket')}</label>
          <input id="remote-bucket" class="input font-mono text-xs" bind:value={bucket} />
        </div>
        <div>
          <label for="remote-region" class="input-label">{t('settings.remote.region')}</label>
          <input id="remote-region" class="input font-mono text-xs" placeholder="auto" bind:value={region} />
        </div>
        <div>
          <label for="remote-ak" class="input-label">{t('settings.remote.accessKey')}</label>
          <!-- Placeholder must come from i18n data: a literal `${...}` inside a
               quoted template attribute makes Svelte treat {S3_ACCESS_KEY} as
               an expression tag → ReferenceError at render → dead card. -->
          <input id="remote-ak" class="input font-mono text-xs" placeholder={t('settings.remote.accessKeyPlaceholder')} bind:value={accessKeyID} />
        </div>
        <div>
          <label for="remote-sk" class="input-label">{t('settings.remote.secretKey')}</label>
          <input
            id="remote-sk" type="password" class="input font-mono text-xs"
            placeholder={secretConfigured ? t('settings.remote.secretConfigured') : t('settings.remote.secretEmpty')}
            bind:value={secretInput}
          />
        </div>
        <div>
          <label for="remote-prefix" class="input-label">{t('settings.remote.prefix')}</label>
          <input id="remote-prefix" class="input font-mono text-xs" bind:value={prefix} />
        </div>
        <div class="flex items-end pb-1">
          <label class="flex items-center gap-2 text-sm th-text-secondary cursor-pointer">
            <input type="checkbox" bind:checked={pathStyle} />
            {t('settings.remote.pathStyle')}
          </label>
        </div>
      </div>
      <p class="text-xs th-text-muted -mt-1">{t('settings.remote.credEnvHint')}</p>

      <!-- Upload tuning -->
      <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
        <div>
          <label for="remote-minage" class="input-label">{t('settings.remote.minAge')}</label>
          <input id="remote-minage" type="number" class="input" min="60" max="86400" bind:value={minAgeS} />
        </div>
        <div>
          <label for="remote-backlog" class="input-label">{t('settings.remote.backlogLimit')}</label>
          <input id="remote-backlog" type="number" class="input" min="0" bind:value={backlogLimit} />
        </div>
        <div>
          <label for="remote-workers" class="input-label">{t('settings.remote.workers')}</label>
          <input id="remote-workers" type="number" class="input" min="1" max="8" bind:value={maxConcurrency} />
        </div>
      </div>

      <!-- Evict -->
      <div>
        <label for="remote-evict" class="input-label">{t('settings.remote.evictAfter')}</label>
        <input id="remote-evict" type="number" class="input" min="0" max="3650" bind:value={afterDays} />
        <p class="text-xs th-text-muted mt-1">{t('settings.remote.evictHint')}</p>
      </div>

      <!-- Outbox observability -->
      {#if status}
        <div class="pt-2 border-t th-border">
          <div class="flex items-center gap-2 mb-2">
            <Cloud size={16} class="th-text-secondary" />
            <span class="text-sm font-medium th-text-primary">{t('settings.remote.statusTitle')}</span>
            <button class="btn btn-ghost btn-xs ml-auto" onclick={refreshStatus} aria-label={t('common.refresh')}>
              <RefreshCw size={14} />
            </button>
          </div>
          <div class="flex flex-wrap gap-2 text-xs">
            {#each ['pending', 'uploading', 'uploaded', 'evicted', 'skipped'] as st (st)}
              <span class="px-2 py-1 rounded-md th-bg-secondary border th-border">
                {t(`settings.remote.status_${st}`)}: <b>{status.counts[st] ?? 0}</b>
              </span>
            {/each}
          </div>
          <p class="text-xs th-text-muted mt-2">
            {t('settings.remote.backlogNow', { n: String(status.backlog) })}
          </p>
        </div>
      {/if}
    </div>
  </SettingsCard>
{/if}
