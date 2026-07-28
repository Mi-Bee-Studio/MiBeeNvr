<script lang="ts">
  // 直播 (Streaming) — RTMP + SRT ingest settings.
  // Part of the unified settings shell (#153): no save button here; the
  // shell drives save/reset via the settingsForm coordinator.
  import { onMount, onDestroy } from 'svelte';
  import { getStreamingSettings, updateStreamingSettings } from '$lib/api';
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

  // Original values for dirty tracking + destructive detection
  let originalSnapshot = $state('');
  let originalRtmpEnabled = $state(false);
  let originalSrtEnabled = $state(false);

  // Derived: is any streaming setting dirty?
  let isDirty = $derived.by(() => {
    if (loading) return false;
    const current = JSON.stringify({
      streamingRtmpEnabled,
      streamingRtmpPort, streamingSrtEnabled, streamingSrtPort,
      rtmpStreamKeys, srtStreams,
    });
    return current !== originalSnapshot;
  });

  function captureSnapshot() {
    originalSnapshot = JSON.stringify({
      streamingRtmpEnabled,
      streamingRtmpPort, streamingSrtEnabled, streamingSrtPort,
      rtmpStreamKeys, srtStreams,
    });
    originalRtmpEnabled = streamingRtmpEnabled;
    originalSrtEnabled = streamingSrtEnabled;
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
      rtmpStreamKeys = rtmpKeys
        ? Object.entries(rtmpKeys).map(([key, cameraId]) => ({ key, cameraId: String(cameraId) }))
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
          stream_keys: Object.fromEntries(rtmpStreamKeys.map((sk) => [sk.key, sk.cameraId])),
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
      captureSnapshot();
      showToast(t('settings.saved'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('common.failedSaveSettings'), 'error');
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
        // Destructive if: turning OFF RTMP, or turning OFF SRT.
        if (originalRtmpEnabled && !streamingRtmpEnabled)
          return t('settings.destructive.rtmpOff');
        if (originalSrtEnabled && !streamingSrtEnabled)
          return t('settings.destructive.srtOff');
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
{/if}
