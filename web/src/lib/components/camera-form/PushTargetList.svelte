<script lang="ts">
    import { t } from '$lib/i18n';
    import { apiRequest, updateCamera, getPushStatus, getRelayCapabilities } from '$lib/api';
    import type {
        Camera,
        PushTargetConfig,
        PushTargetStatus as PushTargetStatusType,
        RelayCapabilities,
        VideoPresetOverrides,
    } from '$lib/api';
    import { ArrowUpRight, Plus, Trash2, Copy } from 'lucide-svelte';
    import { onDestroy } from 'svelte';
    import { showToast } from '$lib/toast';
    import { copyText } from '$lib/clipboard';
    import PushTargetStatus from '$lib/components/PushTargetStatus.svelte';
    import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';

  interface Props {
    /** The form's push-target list — owned by the parent form (validate/save read it). */
    formPushTargets: PushTargetConfig[];
    /** Source encoding decides whether the transcode policy selector renders. */
    formEncoding: string;
    /** Form-wide validation errors (push target URL errors keyed `push_<id>`). */
    validationErrors: Record<string, string>;
    editingCamera: Camera | null;
  }

  let {
    formPushTargets = $bindable([]),
    formEncoding = $bindable(''),
    validationErrors = $bindable({}),
    editingCamera,
  }: Props = $props();

  // Push-out live status (fetched while editing)
  let pushStatus = $state<PushTargetStatusType[]>([]);
  let pushStatusTimer: ReturnType<typeof setInterval> | null = null;
  // Relay presets for platform selector (fetched on mount)
  let relayPresets = $state<{ name: string; description?: string }[]>([]);
  let relayPresetsLoading = $state(true);
  // Relay capabilities (FFmpeg availability for push-out)
  let relayCapabilities = $state<RelayCapabilities | null>(null);

  // Fetch relay presets on mount for platform selector
  $effect(() => {
    const ctrl = new AbortController();
    (async () => {
      try {
        const data: any = await apiRequest('/relay-presets', { signal: ctrl.signal });
        relayPresets = Array.isArray(data) ? data : [];
      } catch (e: any) {
        if (ctrl.signal.aborted) return;
        console.warn('Failed to load relay presets:', e);
        relayPresets = [];
      } finally {
        relayPresetsLoading = false;
      }
    })();
    return () => ctrl.abort();
  });

  // Fetch relay capabilities (FFmpeg availability for relay)
  $effect(() => {
    const ctrl = new AbortController();
    (async () => {
      try {
        relayCapabilities = await getRelayCapabilities(ctrl.signal);
      } catch (e: any) {
        if (ctrl.signal.aborted) return;
        console.warn('Failed to load relay capabilities:', e);
        relayCapabilities = null;
      }
    })();
    return () => ctrl.abort();
  });

  // Poll push-out status while a camera is being edited.
  $effect(() => {
    if (editingCamera) {
      startPushStatusPolling(editingCamera.id);
    }
    return () => stopPushStatusPolling();
  });

  onDestroy(() => {
    stopPushStatusPolling();
  });
  function startPushStatusPolling(cameraId: string) {
    stopPushStatusPolling();
    const poll = async () => {
      try {
        const res = await getPushStatus(cameraId);
        pushStatus = res.targets ?? [];
      } catch {
        // ignore — camera may not be saved yet
      }
    };
    poll();
    pushStatusTimer = setInterval(poll, 3000);
  }
  function stopPushStatusPolling() {
    if (pushStatusTimer) {
      clearInterval(pushStatusTimer);
      pushStatusTimer = null;
    }
  }
  function addPushTarget() {
    const id = 'tgt-' + Math.random().toString(36).slice(2, 8);
    formPushTargets = [
      ...formPushTargets,
      { id, name: '', protocol: 'rtmp', url: '', enabled: true, platform: '', transcode_policy: 'auto', use_ffmpeg: false },
    ];
  }
  function removePushTarget(id: string) {
    formPushTargets = formPushTargets.filter((t) => t.id !== id);
  }

  function updatePushTarget(id: string, patch: Partial<PushTargetConfig>) {
    formPushTargets = formPushTargets.map((t) => (t.id === id ? { ...t, ...patch } : t));
  }
  function updatePushTargetOverride(id: string, patch: Partial<VideoPresetOverrides>) {
    formPushTargets = formPushTargets.map((t) => {
      if (t.id !== id) return t;
      const current = t.video_preset_override || {};
      return { ...t, video_preset_override: { ...current, ...patch } };
    });
  }
  function resetPushTargetOverride(id: string) {
    formPushTargets = formPushTargets.map((t) => {
      if (t.id !== id) return t;
      const { video_preset_override: _, ...rest } = t;
      return rest;
    });
  }
  function pushStatusFor(id: string): PushTargetStatusType | undefined {
    return pushStatus.find((s) => s.id === id);
  }

  // Stop push target state
  let showStopConfirm = $state(false);
  let stopTargetId = $state<string | null>(null);
  let stoppingTargets = $state<Set<string>>(new Set());

  function confirmStopTarget(id: string) {
    stopTargetId = id;
    showStopConfirm = true;
  }

  async function handleStopTarget() {
    if (!stopTargetId || !editingCamera) return;
    const id = stopTargetId;
    stoppingTargets = new Set([...stoppingTargets, id]);
    showStopConfirm = false;
    stopTargetId = null;
    // Disable the target in form state
    formPushTargets = formPushTargets.map((t) =>
      t.id === id ? { ...t, enabled: false } : t
    );
    try {
      await updateCamera(editingCamera.id, {
        push_targets: formPushTargets,
      });
      showToast(t('cameras.pushOutTargetStopped'), 'success');
    } catch (e) {
      console.warn('Failed to stop push target:', e);
      showToast(t('cameras.failedUpdate'), 'error');
      // Revert
      formPushTargets = formPushTargets.map((t) =>
        t.id === id ? { ...t, enabled: true } : t
      );
    } finally {
      const next = new Set(stoppingTargets);
      next.delete(id);
      stoppingTargets = next;
    }
  }
</script>

<!-- Push-out (relay) targets: forward this camera's stream to remote destinations -->
<div class="md:col-span-2">
  <details class="rounded-md border th-border">
    <summary class="cursor-pointer p-3 flex items-center gap-2 th-bg-hover">
      <ArrowUpRight size={16} class="th-text-secondary" />
      <span class="font-medium th-text-primary">{t('cameras.pushOutTitle')}</span>
      {#if formPushTargets.length > 0}
        <span class="text-xs px-2 py-0.5 rounded-full th-bg-muted th-text-secondary">{formPushTargets.length}</span>
      {/if}
    </summary>
    <div class="p-3 border-t th-border space-y-2">
      <p class="text-xs th-text-muted mb-2">{t('cameras.pushOutHint')}</p>

      {#if formPushTargets.length === 0}
        <p class="text-sm th-text-muted py-2">{t('cameras.pushOutEmpty')}</p>
      {:else}
        {#each formPushTargets as tgt (tgt.id)}
          {@const st = pushStatusFor(tgt.id)}
          <div class="p-2 rounded-md th-bg-muted space-y-2">
            <div class="flex flex-wrap items-center gap-2">
              <input type="text" class="input flex-1 min-w-[100px]" placeholder={t('cameras.pushOutName')}
                value={tgt.name} oninput={(e) => updatePushTarget(tgt.id, { name: (e.target as HTMLInputElement).value })} />
              <select class="input w-auto" value={tgt.protocol}
                onchange={(e) => updatePushTarget(tgt.id, { protocol: (e.target as HTMLSelectElement).value as 'rtmp' | 'rtsp' })}>
                <option value="rtmp">RTMP</option>
                <option value="rtsp">RTSP</option>
              </select>

              <!-- Platform selector -->
              <select class="input w-auto" value={tgt.platform || ''}
                onchange={(e) => updatePushTarget(tgt.id, { platform: (e.target as HTMLSelectElement).value })}>
                {#if relayPresetsLoading}
                  <option value="">Loading...</option>
                {:else}
                  <option value="">{t('cameras.pushPlatformGeneric')}</option>
                  {#each relayPresets as preset (preset.name)}
                    <option value={preset.name}>{preset.name}{preset.description ? ` — ${preset.description}` : ''}</option>
                  {/each}
                {/if}
              </select>

              <!-- Transcode policy (hidden for H.264 source) -->
              {#if formEncoding === 'h264'}
                <span class="text-xs th-text-muted whitespace-nowrap">{t('cameras.pushTranscodeNA')}</span>
              {:else}
                <select class="input w-auto" value={tgt.transcode_policy || 'auto'}
                  onchange={(e) => updatePushTarget(tgt.id, { transcode_policy: (e.target as HTMLSelectElement).value as 'auto' | 'force_sw' | 'off' | 'passthrough' })}>
                  <option value="auto">{t('cameras.pushTranscodeAuto')}</option>
                  <option value="force_sw">{t('cameras.pushTranscodeForceSW')}</option>
                  <option value="passthrough">{t('cameras.pushTranscodePassthrough')}</option>
                  <option value="off">{t('cameras.pushTranscodeRejectH265')}</option>
                </select>
              {/if}

              <input type="text" class="input flex-[2] min-w-[160px] {validationErrors['push_' + tgt.id] ? 'border-red-500' : ''}" placeholder={tgt.protocol === 'rtsp' ? 'rtsp://host:8554/stream' : 'rtmp://host:1935/live/你的直播密钥'}
                value={tgt.url} oninput={(e) => updatePushTarget(tgt.id, { url: (e.target as HTMLInputElement).value })} />
              <label class="flex items-center gap-1 text-xs th-text-secondary whitespace-nowrap">
                <input type="checkbox" class="checkbox" checked={tgt.enabled}
                  onchange={(e) => updatePushTarget(tgt.id, { enabled: (e.target as HTMLInputElement).checked })} />
                {t('cameras.pushOutEnabled')}
              </label>
              <label class="flex items-center gap-1 text-xs th-text-secondary whitespace-nowrap" title={t('cameras.pushOutUseFFmpegHint')}>
                <input type="checkbox" class="checkbox" checked={tgt.use_ffmpeg ?? false}
                  disabled={!relayCapabilities?.ffmpeg_available}
                  onchange={(e) => updatePushTarget(tgt.id, { use_ffmpeg: (e.target as HTMLInputElement).checked })} />
                {t('cameras.pushOutUseFFmpeg')}
                {#if !relayCapabilities?.ffmpeg_available}
                  <span class="th-text-muted">({t('cameras.pushOutFFmpegNotInstalled')})</span>
                {/if}
              </label>
              {#if st}
                <PushTargetStatus status={st} />
              {/if}
              <button type="button" class="btn-ghost p-1 th-color-danger" title={t('cameras.pushOutRemove')}
                onclick={() => removePushTarget(tgt.id)}>
                <Trash2 size={14} />
              </button>
              {#if st && tgt.enabled && st.status !== 'idle'}
                <button
                  type="button"
                  class="btn-ghost p-1 th-color-danger text-xs flex items-center gap-1"
                  disabled={stoppingTargets.has(tgt.id)}
                  onclick={() => confirmStopTarget(tgt.id)}
                >
                  {#if stoppingTargets.has(tgt.id)}
                    <span class="spinner w-3 h-3"></span>
                    {t('cameras.pushOutStopping')}
                  {:else}
                    {t('cameras.pushOutStop')}
                  {/if}
                </button>
              {/if}
            </div>

            <!-- Validation error for this target's URL -->
            {#if validationErrors['push_' + tgt.id]}
              <p class="th-color-danger text-xs">{validationErrors['push_' + tgt.id]}</p>
            {/if}

            <!-- Live preview of the full push address the relay will
                 actually use. This answers "这个输入框到底是什么意思": the
                 URL field IS the full destination address (including the
                 RTMP stream key, which lives in the path). Show it
                 read-only with a copy button so the user can verify what
                 they typed equals the address the platform gave them. -->
            {#if tgt.url.trim()}
              <div class="flex items-center gap-2 px-2 py-1 rounded th-bg-muted/60">
                <span class="text-[10px] th-text-muted whitespace-nowrap shrink-0">{t('cameras.pushOutPreview')}</span>
                <code class="text-[11px] th-text-secondary truncate flex-1 font-mono">{tgt.url.trim()}</code>
                <button type="button" class="btn-ghost p-1 th-text-muted hover:th-text-primary shrink-0"
                  title={t('cameras.pushOutCopyUrl')} aria-label={t('cameras.pushOutCopyUrl')}
                  onclick={() => copyText(tgt.url.trim()).then((ok) =>
                    showToast(ok ? t('cameras.pushOutUrlCopied') : t('cameras.pushOutUrlCopyFailed'), ok ? 'success' : 'error')
                  )}>
                  <Copy size={12} />
                </button>
              </div>
            {/if}

            <!-- Preset override panel (collapsed) -->
            <details class="text-xs">
              <summary class="cursor-pointer th-text-secondary hover:th-text-primary transition-colors select-none">
                {t('cameras.pushPresetOverrides')}
                {#if tgt.video_preset_override}
                  <span class="ml-1 text-[var(--color-accent)]">{t('cameras.pushPresetCustom')}</span>
                {/if}
              </summary>
              <div class="grid grid-cols-3 gap-x-3 gap-y-2 pt-2 pb-1">
                <div>
                  <label for={tgt.id + '-resolution'} class="input-label">{t('cameras.pushPresetResolution')}</label>
                  <input id={tgt.id + '-resolution'} type="text" class="input w-full" placeholder="1920x1080"
                    value={tgt.video_preset_override?.resolution || ''}
                    oninput={(e) => updatePushTargetOverride(tgt.id, { resolution: (e.target as HTMLInputElement).value || undefined })} />
                </div>
                <div>
                  <label for={tgt.id + '-framerate'} class="input-label">{t('cameras.pushPresetFramerate')}</label>
                  <input id={tgt.id + '-framerate'} type="number" class="input w-full" placeholder="30" min="1" max="120"
                    value={tgt.video_preset_override?.framerate ?? ''}
                    oninput={(e) => {
                      const v = parseInt((e.target as HTMLInputElement).value);
                      updatePushTargetOverride(tgt.id, { framerate: isNaN(v) ? undefined : v });
                    }} />
                </div>
                <div>
                  <label for={tgt.id + '-bitrate'} class="input-label">{t('cameras.pushPresetBitrate')}</label>
                  <input id={tgt.id + '-bitrate'} type="number" class="input w-full" placeholder="3000" min="100" max="50000"
                    value={tgt.video_preset_override?.video_bitrate_kbps ?? ''}
                    oninput={(e) => {
                      const v = parseInt((e.target as HTMLInputElement).value);
                      updatePushTargetOverride(tgt.id, { video_bitrate_kbps: isNaN(v) ? undefined : v });
                    }} />
                </div>
                <div>
                  <label for={tgt.id + '-gop'} class="input-label">{t('cameras.pushPresetGOP')}</label>
                  <input id={tgt.id + '-gop'} type="number" class="input w-full" placeholder="2" min="1" max="10"
                    value={tgt.video_preset_override?.gop_seconds ?? ''}
                    oninput={(e) => {
                      const v = parseInt((e.target as HTMLInputElement).value);
                      updatePushTargetOverride(tgt.id, { gop_seconds: isNaN(v) ? undefined : v });
                    }} />
                </div>
                <div>
                  <label for={tgt.id + '-profile'} class="input-label">{t('cameras.pushPresetProfile')}</label>
                  <select id={tgt.id + '-profile'} class="input w-full" value={tgt.video_preset_override?.profile || ''}
                    onchange={(e) => {
                      const v = (e.target as HTMLSelectElement).value;
                      updatePushTargetOverride(tgt.id, { profile: (v as 'baseline' | 'main' | 'high') || undefined });
                    }}>
                    <option value="">{t('cameras.pushPresetDefault')}</option>
                    <option value="baseline">baseline</option>
                    <option value="main">main</option>
                    <option value="high">high</option>
                  </select>
                </div>
                <div>
                  <label for={tgt.id + '-bframes'} class="input-label">{t('cameras.pushPresetBFrames')}</label>
                  <input id={tgt.id + '-bframes'} type="number" class="input w-full" placeholder="0" min="0" max="2"
                    value={tgt.video_preset_override?.bframes ?? ''}
                    oninput={(e) => {
                      const v = parseInt((e.target as HTMLInputElement).value);
                      updatePushTargetOverride(tgt.id, { bframes: isNaN(v) ? undefined : v });
                    }} />
                </div>
              </div>
              <button type="button" class="btn-ghost text-xs th-text-muted mt-1"
                onclick={() => resetPushTargetOverride(tgt.id)}>
                {t('cameras.pushPresetReset')}
              </button>
            </details>
          </div>
        {/each}
      {/if}

      <button type="button" class="btn btn-ghost btn-sm mt-2 flex items-center gap-1" onclick={addPushTarget}>
        <Plus size={14} /> {t('cameras.pushOutAdd')}
      </button>
    </div>
  </details>
</div>

<!-- Stop push target confirm dialog -->
{#if showStopConfirm}
  <ConfirmDialog
    title={t('cameras.pushOutStopConfirm')}
    message={t('cameras.pushOutStopConfirmDesc')}
    variant="danger"
    onconfirm={handleStopTarget}
    oncancel={() => { showStopConfirm = false; stopTargetId = null; }}
    confirmText={t('cameras.pushOutStop')}
    loading={stoppingTargets.has(stopTargetId || '')}
  />
{/if}
