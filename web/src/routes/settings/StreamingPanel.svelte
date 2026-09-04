<script lang="ts">
  // 直播 (Streaming) — RTMP + SRT ingest settings + RTSP output server.
  // Part of the unified settings shell (#153): no save button here; the
  // shell drives save/reset via the settingsForm coordinator.
  import { onMount, onDestroy } from 'svelte';
  import { getStreamingSettings, updateStreamingSettings, getRtspOutputSettings, updateRtspOutputSettings } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { settingsForm } from '$lib/settings/settings-form.svelte';
  import SettingsCard from '$lib/components/SettingsCard.svelte';
  import SettingsStreamingCard from './SettingsStreamingCard.svelte';

  let loading = $state(true);
  let error = $state('');

  // Streaming settings state — only RTMP/SRT remain (external ingest).
  // WebRTC/FLV viewer counts + timeouts removed (#153).
  let streamingRtmpEnabled = $state(false);
  let streamingRtmpPort = $state(1935);
  let streamingSrtEnabled = $state(false);
  let streamingSrtPort = $state(9000);

  // RTMP stream key mappings
  let rtmpStreamKeys = $state<{ key: string; cameraId: string }[]>([]);

  // SRT stream configurations
  let srtStreams = $state<{ streamId: string; cameraId: string; mode: string; address: string; passphrase: string }[]>([]);

  // RTSP output server (#686): pull URLs for third-party platforms. The
  // password is never returned by the API — passwordTyped is what the user
  // entered this session (blank = keep current), clearAuth queues
  // clear_credentials for the next save.
  let rtspEnabled = $state(true);
  let rtspPort = $state(8554);
  let rtspUsername = $state('');
  let rtspPasswordTyped = $state('');
  let rtspPasswordConfigured = $state(false);
  let rtspClearAuth = $state(false);

  // Original values for dirty tracking + destructive detection
  let originalSnapshot = $state('');
  let originalRtmpEnabled = $state(false);
  let originalSrtEnabled = $state(false);
  let originalRtspEnabled = $state(true);

  // Derived: is any streaming setting dirty?
  let isDirty = $derived.by(() => {
    if (loading) return false;
    const current = JSON.stringify({
      streamingRtmpEnabled,
      streamingRtmpPort, streamingSrtEnabled, streamingSrtPort,
      rtmpStreamKeys, srtStreams,
      rtspEnabled, rtspPort, rtspUsername, rtspPasswordTyped, rtspClearAuth,
    });
    return current !== originalSnapshot;
  });

  function captureSnapshot() {
    originalSnapshot = JSON.stringify({
      streamingRtmpEnabled,
      streamingRtmpPort, streamingSrtEnabled, streamingSrtPort,
      rtmpStreamKeys, srtStreams,
      rtspEnabled, rtspPort, rtspUsername, rtspPasswordTyped, rtspClearAuth,
    });
    originalRtmpEnabled = streamingRtmpEnabled;
    originalSrtEnabled = streamingSrtEnabled;
    originalRtspEnabled = rtspEnabled;
  }

  async function loadStreamingConfig() {
    loading = true;
    error = '';
    try {
      const config = await getStreamingSettings();
      // WebRTC/FLV viewer counts + timeouts removed (#153) — backend defaults.
      streamingRtmpEnabled = config.rtmp?.enabled ?? false;
      streamingRtmpPort = config.rtmp?.port ?? 1935;
      const rtmpKeys = config.rtmp?.stream_keys;
      // Backend map is camera_id → stream_key (legacy global map; per-camera
      // stream_key fields take precedence at publish time).
      rtmpStreamKeys = rtmpKeys
        ? Object.entries(rtmpKeys).map(([cameraId, key]) => ({ key: String(key), cameraId }))
        : [];
      streamingSrtEnabled = config.srt?.enabled ?? false;
      streamingSrtPort = config.srt?.port ?? 9000;
      const srtStreamList = config.srt?.streams;
      srtStreams = srtStreamList
        ? srtStreamList.map((s) => ({
            streamId: s.stream_id || '',
            cameraId: s.camera_id || '',
            mode: s.mode || 'listener',
            address: s.address || '',
            passphrase: s.passphrase || '',
          }))
        : [];
      try {
        const rtsp = await getRtspOutputSettings();
        rtspEnabled = rtsp.enabled;
        rtspPort = rtsp.port || 8554;
        rtspUsername = rtsp.username ?? '';
        rtspPasswordConfigured = rtsp.password_configured ?? false;
      } catch {
        // Older binary without /settings/rtsp-output — keep card defaults,
        // the save attempt will surface an actionable error.
      }
      captureSnapshot();
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.failedLoadSettings');
    } finally {
      loading = false;
    }
  }

  async function performSave() {
    try {
      // Save streaming settings. WebRTC/FLV viewer counts and timeouts use
      // backend defaults (#153). Only RTMP/SRT ports + stream keys are user
      // controlled — the rest are hardcoded defaults that must be sent so the
      // protocols stay available (the Player Orchestrator enables them on demand).
      await updateStreamingSettings({
        webrtc: {
          enabled: true,
          max_viewers: 4,
          idle_timeout: '5m',
        },
        flv: {
          enabled: true,
          max_viewers: 10,
          idle_timeout: '5m',
        },
        hls: { low_latency: true },
        rtmp: {
          enabled: streamingRtmpEnabled,
          port: streamingRtmpPort,
          stream_keys: Object.fromEntries(rtmpStreamKeys.map((sk) => [sk.cameraId, sk.key])),
        },
        srt: {
          enabled: streamingSrtEnabled,
          port: streamingSrtPort,
          streams: srtStreams.map((s) => ({
            stream_id: s.streamId,
            camera_id: s.cameraId,
            mode: s.mode,
            address: s.address,
            passphrase: s.passphrase,
          })),
        },
      });
      // RTSP output (#686) — the password only rides along when the user
      // typed one this session; clear_credentials when they queued a wipe.
      await updateRtspOutputSettings({
        enabled: rtspEnabled,
        port: rtspPort,
        username: rtspUsername,
        ...(rtspPasswordTyped ? { password: rtspPasswordTyped } : {}),
        ...(rtspClearAuth ? { clear_credentials: true } : {}),
      });
      rtspPasswordTyped = '';
      rtspClearAuth = false;
      try {
        const rtsp = await getRtspOutputSettings();
        rtspPasswordConfigured = rtsp.password_configured ?? false;
      } catch { /* keep local view */ }
      captureSnapshot();
      showToast(t('settings.saved'), 'success');
    } catch (e) {
      // Re-throw so the unified shell keeps the dirty bar visible and reports
      // the failure (#160). Without this, saveAll treats the panel as saved
      // and a backend failure is hidden behind a one-shot toast.
      showToast(e instanceof Error ? e.message : t('common.failedSaveSettings'), 'error');
      throw e;
    }
  }

  function resetForm() {
    // Restore from the last captured snapshot.
    try {
      const snap = JSON.parse(originalSnapshot);
      streamingRtmpEnabled = snap.streamingRtmpEnabled;
      streamingRtmpPort = snap.streamingRtmpPort;
      streamingSrtEnabled = snap.streamingSrtEnabled;
      streamingSrtPort = snap.streamingSrtPort;
      rtmpStreamKeys = snap.rtmpStreamKeys;
      srtStreams = snap.srtStreams;
      rtspEnabled = snap.rtspEnabled;
      rtspPort = snap.rtspPort;
      rtspUsername = snap.rtspUsername;
      rtspPasswordTyped = snap.rtspPasswordTyped;
      rtspClearAuth = snap.rtspClearAuth;
    } catch { /* ignore */ }
  }

  let unregister: (() => void) | undefined;
  onMount(() => {
    loadStreamingConfig();
    unregister = settingsForm.register('streaming', {
      isDirty: () => isDirty,
      save: performSave,
      reset: resetForm,
      getDestructiveWarning: () => {
        // Destructive if: turning OFF RTMP, SRT, or the RTSP output server.
        if (originalRtmpEnabled && !streamingRtmpEnabled)
          return t('settings.destructive.rtmpOff');
        if (originalSrtEnabled && !streamingSrtEnabled)
          return t('settings.destructive.srtOff');
        if (originalRtspEnabled && !rtspEnabled)
          return t('settings.destructive.rtspOff');
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
      <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
        <div class="space-y-2"><div class="h-4 w-24 th-bg-tertiary rounded animate-pulse"></div><div class="h-10 th-bg-tertiary rounded animate-pulse"></div></div>
        <div class="space-y-2"><div class="h-4 w-28 th-bg-tertiary rounded animate-pulse"></div><div class="h-10 th-bg-tertiary rounded animate-pulse"></div></div>
      </div>
    </div>
  </div>
{:else if error}
  <div class="card border th-border-danger p-8 text-center">
    <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
    <p class="th-text-secondary mb-4">{error}</p>
    <button onclick={loadStreamingConfig} class="btn btn-primary btn-sm">{t('common.retry')}</button>
  </div>
{:else}
  <!-- RTMP Ingest -->
  <SettingsCard
    title={t('settings.streaming.rtmp')}
    subtitle={t('settings.advanced.streaming.description')}
    defaultOpen={false}
  >
    <SettingsStreamingCard
      title={t('settings.streaming.rtmp')}
      protocol="rtmp"
      enabled={streamingRtmpEnabled}
      onEnabledChange={(val) => (streamingRtmpEnabled = val)}
      port={streamingRtmpPort}
      onPortChange={(val) => (streamingRtmpPort = val)}
    />
  </SettingsCard>

  <!-- SRT -->
  <SettingsCard
    title={t('settings.streaming.srt')}
    subtitle={t('settings.advanced.streaming.description')}
    defaultOpen={false}
  >
    <SettingsStreamingCard
      title={t('settings.streaming.srt')}
      protocol="srt"
      enabled={streamingSrtEnabled}
      onEnabledChange={(val) => (streamingSrtEnabled = val)}
      port={streamingSrtPort}
      onPortChange={(val) => (streamingSrtPort = val)}
    />
  </SettingsCard>

  <!-- RTSP output (#686): pull URLs for third-party platforms (VLC/Synology) -->
  <SettingsCard
    title={t('settings.streaming.rtspOutput')}
    subtitle={t('settings.streaming.rtspOutputDesc')}
    defaultOpen={false}
  >
    <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
      <div>
        <label class="input-label" for="rtsp-output-toggle">{t('settings.streaming.rtspOutput')}</label>
        <div class="flex items-center gap-3 mt-2">
          <button
            id="rtsp-output-toggle"
            aria-label={t('settings.streaming.rtspOutput')}
            type="button"
            class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 {rtspEnabled ? 'bg-blue-600' : 'th-bg-tertiary'}"
            onclick={() => (rtspEnabled = !rtspEnabled)}
            onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); rtspEnabled = !rtspEnabled; } }}
            role="switch"
            aria-checked={rtspEnabled}
          >
            <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {rtspEnabled ? 'translate-x-6' : 'translate-x-1'}"></span>
          </button>
        </div>
      </div>
      <div>
        <label for="rtspOutputPort" class="input-label">{t('settings.streaming.rtspOutput.port')}</label>
        <input id="rtspOutputPort" type="number" class="input" value={rtspPort}
          oninput={(e) => (rtspPort = parseInt((e.target as HTMLInputElement).value) || 8554)}
          min="1" max="65535" />
      </div>
      <div>
        <label for="rtspOutputUsername" class="input-label">{t('settings.streaming.rtspOutput.username')}</label>
        <input id="rtspOutputUsername" type="text" class="input" value={rtspUsername}
          oninput={(e) => (rtspUsername = (e.target as HTMLInputElement).value)}
          autocomplete="off" />
      </div>
      <div>
        <label for="rtspOutputPassword" class="input-label">{t('settings.streaming.rtspOutput.password')}</label>
        <input
          id="rtspOutputPassword" type="password" class="input" bind:value={rtspPasswordTyped}
          placeholder={rtspPasswordConfigured ? t('settings.streaming.rtspOutput.passwordSet') : t('settings.streaming.rtspOutput.passwordUnset')}
          autocomplete="new-password" disabled={rtspClearAuth}
        />
        <div class="flex items-center justify-between gap-2 mt-1">
          <p class="text-xs th-text-tertiary">
            {#if rtspClearAuth}
              {t('settings.streaming.rtspOutput.clearAuthQueued')}
            {:else}
              {t('settings.streaming.rtspOutput.passwordHint')}
            {/if}
          </p>
          {#if !rtspClearAuth}
            <button
              type="button" class="text-xs th-text-muted hover:th-text-danger shrink-0"
              onclick={() => { rtspClearAuth = true; rtspPasswordTyped = ''; }}
            >
              {t('settings.streaming.rtspOutput.clearAuth')}
            </button>
          {:else if rtspPasswordConfigured}
            <button
              type="button" class="text-xs th-text-muted hover:th-text-primary shrink-0"
              onclick={() => (rtspClearAuth = false)}
            >
              {t('common.cancel')}
            </button>
          {/if}
        </div>
      </div>
    </div>
    <p class="text-xs th-text-tertiary mt-4">{t('settings.streaming.rtspOutput.restartHint')}</p>
  </SettingsCard>
{/if}
