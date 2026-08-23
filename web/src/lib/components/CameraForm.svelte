<script lang="ts">
    import { t } from '$lib/i18n';
    import { friendlyError } from '$lib/errors';
    import {
        createCamera,
        updateCamera,
        getMergeConfig,
        updateMergeConfig,
        buildProtocolsMap,
        normalizeProtocol,
        testConnection,
        getDeviceCapabilities,
        getPushStatus,
        getRelayCapabilities,
        apiRequest,
        getStorageCandidates,
        getCameraStorageRoot,
        setCameraStorageRoot,
        getStorageMigrateStatus,
    } from '$lib/api';
    import type {
        Camera,
        CameraTranscodingConfig,
        CreateCameraRequest,
        UpdateCameraRequest,
        MergeConfig,
        ProtocolInfo,
        XiaomiDevice,
        TestConnectionResult,
        DeviceCapabilitiesInfo,
        PushTargetConfig,
        PushTargetStatus as PushTargetStatusType,
        VideoPresetOverrides,
        RelayCapabilities,
        AdaptiveRecordingConfig,
    } from '$lib/api';
    import { Eye, EyeOff, PlugZap, Plus, Trash2, ArrowUpRight, Copy } from 'lucide-svelte';
    import { onDestroy } from 'svelte';
    import { showToast } from '$lib/toast';
    import { copyText } from '$lib/clipboard';
    import MergeConfigEditor from '$lib/components/MergeConfigEditor.svelte';
    import TimelapseConfigEditor from '$lib/components/TimelapseConfigEditor.svelte';
    import DeviceCapabilities from '$lib/components/DeviceCapabilities.svelte';
    import ImagingPanel from '$lib/components/ImagingPanel.svelte';
    import PresetManager from '$lib/components/PresetManager.svelte';
    import ONVIFEvents from '$lib/components/ONVIFEvents.svelte';
    import DeviceManagement from '$lib/components/DeviceManagement.svelte';
    import { startBackfill, getUntranscodedRecordingCount } from '$lib/api/transcoding';
    import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  interface Props {
    editingCamera: Camera | null;
    protocols: ProtocolInfo[];
    protocolsMap: Map<string, ProtocolInfo>;
    xiaomiDeviceList?: XiaomiDevice[];
    onsave: () => void;
    oncancel: () => void;
    globalTranscodingEnabled?: boolean;
    h265Available?: boolean;
    onbackfillneeded?: (info: { cameraId: string; count: number; targetCodec: string }) => Promise<boolean>;
  }

  let {
    editingCamera,
    protocols,
    protocolsMap,
    xiaomiDeviceList = [],
    globalTranscodingEnabled = false,
    h265Available = true,
    onsave,
    oncancel,
    onbackfillneeded,
  }: Props = $props();

  // Form state
  let formName = $state('');
  let formProtocol = $state('rtsp');
  let formEncoding = $state('h264');
  let formUrl = $state('');
  let formUsername = $state('');
  let formPassword = $state('');
  let showPassword = $state(false);
  let saving = $state(false);
  let formDescription = $state('');
  let formLocation = $state('');
  let formBrand = $state('');
  let formModel = $state('');
  let formSerialNumber = $state('');
  let formRetentionDays = $state(0);
  let formStreamEncoding = $state('');
  let formChannel = $state('');
  let formAudioEnabled = $state(false);
  // Recording gate — when off, the recorder stays connected for live preview
  // and relay but writes NO segments to disk (live-only / stream-forward mode).
  let formRecordingEnabled = $state(true);
  let formCascadeEnabled = $state(true);
  // Recording mode (#435): continuous, or adaptive — dynamic timelapse that
  // drops to sparse keyframes while the compressed-domain activity signal
  // stays calm and returns to full recording on activity.
  let formRecordingMode = $state<'continuous' | 'adaptive'>('continuous');
  let formAdaptiveCalmThreshold = $state('');
  let formAdaptiveTimelapseInterval = $state('');
  let formAdaptiveSpikeFactor = $state('');
  let formAdaptiveGopBufferMB = $state('');

  // Per-camera storage location (hot switch + background migration)
  let camStorageRoot = $state('');
  let camStorageDefault = $state('');
  let camStorageCandidates = $state<Array<{ path: string; label: string }>>([]);
  let camStorageMigrate = $state(true);
  let camStorageDeleteSource = $state(true);
  let camStorageSwitching = $state(false);
  let camStorageMigration = $state<import('$lib/api').MigrationJob | null>(null);
  let camStoragePoll: ReturnType<typeof setInterval> | undefined;
  // Xiaomi two-way audio
  let formTwoWayAudioEnabled = $state(false);
  // IP self-healing: candidate CIDRs to scan when this camera's IP changes.
  // One per line in the textarea; backend validates as CIDRs.
  let formSubnetHints = $state('');
  // Push/ingest fields (SRT/RTMP)
  let formStreamKey = $state('');
  let formSRTPassphrase = $state('');
  let formSRTStreamID = $state('');
  // GB28181 SIP device/channel binding (required when protocol is gb28181)
  let formGB28181DeviceID = $state('');
  let formGB28181ChannelID = $state('');
  // Push-in retention (SRT/RTMP): null=follow global, 0=live-only, N=keep N days
  let formPushRetentionDays = $state<number | null>(null);
  // Push-out relay targets
  let formPushTargets = $state<PushTargetConfig[]>([]);
  // Push-out live status (fetched when editing)
  let pushStatus = $state<PushTargetStatusType[]>([]);
  let pushStatusTimer: ReturnType<typeof setInterval> | null = null;
  // Relay presets for platform selector (fetched on mount)
  let relayPresets = $state<{ name: string; description?: string }[]>([]);
  let relayPresetsLoading = $state(true);
  // Relay capabilities (FFmpeg availability for push-out)
  let relayCapabilities = $state<RelayCapabilities | null>(null);
  // Transcoding config
  let formTranscodingEnabled = $state(false);
  let formTranscodingCodec = $state('h264');
  let formTranscodingPreset = $state('ultrafast');
let formTranscodingBitrate = $state('2M');
let formTranscodingCRF = $state(0);
// Dark frame filtering
let formDarkFrameFilterEnabled = $state(false);
let formDarkFrameThreshold = $state(15);
// Recording schedule
let formRecordingScheduleEnabled = $state(false);
let formRecordingScheduleStart = $state('06:00');
let formRecordingScheduleEnd = $state('22:00');
let validationErrors = $state<Record<string, string>>({});

  // Test connection state
  let testing = $state(false);
  let testResult = $state<TestConnectionResult | null>(null);

  // Merge config
  let mergeConfig = $state<MergeConfig | null>(null);
  let mergeConfigLoading = $state(false);

  // ONVIF capabilities
  let deviceCaps = $state<DeviceCapabilitiesInfo | null>(null);
  let capsLoading = $state(false);

  // Auto-select encoding when protocol changes
  $effect(() => {
    const proto = protocolsMap.get(formProtocol);
    if (!proto) return;
    const encodings = proto.encodings;
    if (!encodings.includes(formEncoding)) {
      if (formProtocol === 'onvif' || formProtocol === 'xiaomi' || formProtocol === 'gb28181') {
        // Auto-detect protocols: codec comes from the live stream, not config.
        formEncoding = '';
        // Auto-detect protocols: codec comes from the live stream, not config.
        formEncoding = '';
      } else if (formProtocol === 'http') {
        formEncoding = 'jpeg';
      } else if (encodings.length > 0) {
        formEncoding = encodings[0];
      } else {
        formEncoding = '';
      }
    }
  });

  // Populate form when editingCamera changes
  $effect(() => {
    if (editingCamera) {
      populateForm(editingCamera);
      loadMergeConfig(editingCamera.id);
      loadCapabilities(editingCamera);
      loadCameraStorage(editingCamera.id);
    } else {
      resetFormFields();
      mergeConfig = null;
      mergeConfigLoading = false;
      deviceCaps = null;
    }
  });

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

  // Parse the subnet-hints textarea into a clean CIDR list (one per line,
  // whitespace/comma tolerant, blanks dropped). The backend validates each as a
  // CIDR; /24-or-smaller only (the rediscovery scanner rejects wider ranges).
  function parseSubnetHints(text: string): string[] | undefined {
    const parts = text.split(/[\n,]+/).map(s => s.trim()).filter(s => s.length > 0);
    return parts.length > 0 ? parts : undefined;
  }

  function resetFormFields() {
    formName = '';
    formProtocol = 'rtsp';
    formEncoding = 'h264';
    formUrl = '';
    formUsername = '';
    formPassword = '';
    showPassword = false;
    formDescription = '';
    formLocation = '';
    formBrand = '';
    formModel = '';
    formSerialNumber = '';
    formRetentionDays = 0;
    formStreamEncoding = '';
    formTranscodingEnabled = false;
    formTranscodingCodec = 'h264';
    formTranscodingPreset = 'ultrafast';
    formTranscodingBitrate = '2M';
    formTranscodingCRF = 0;
    validationErrors = {};
    formChannel = '';
    formAudioEnabled = false;
    formTwoWayAudioEnabled = false;
    formSubnetHints = '';
    formDarkFrameFilterEnabled = false;
    formDarkFrameThreshold = 15;
    formRecordingScheduleEnabled = false;
    formRecordingScheduleStart = '06:00';
    formRecordingScheduleEnd = '22:00';
    formStreamKey = '';
    formSRTPassphrase = '';
    formSRTStreamID = '';
    formGB28181DeviceID = '';
    formGB28181ChannelID = '';
    formPushRetentionDays = null;
    formPushTargets = [];
    pushStatus = [];
  }

  async function loadCameraStorage(cameraId: string) {
    camStorageRoot = '';
    camStorageDefault = '';
    camStorageCandidates = [];
    camStorageMigration = null;
    try {
      const [info, cands] = await Promise.all([
        getCameraStorageRoot(cameraId),
        getStorageCandidates(),
      ]);
      camStorageRoot = info.override_root || '';
      camStorageDefault = info.default_root;
      camStorageCandidates = cands.candidates.filter((c) => c.path !== info.default_root);
      if (info.migration) startStoragePoll(cameraId);
    } catch { /* panel degrades silently */ }
  }

  function startStoragePoll(cameraId: string) {
    if (camStoragePoll) clearInterval(camStoragePoll);
    camStoragePoll = setInterval(async () => {
      try {
        const info = await getCameraStorageRoot(cameraId);
        camStorageMigration = info.migration ?? null;
        if (!info.migration && camStoragePoll) {
          clearInterval(camStoragePoll);
          camStoragePoll = undefined;
        }
      } catch { /* transient */ }
    }, 1500);
  }

  async function applyCameraStorage(camera: Camera) {
    if (camStorageSwitching) return;
    camStorageSwitching = true;
    try {
      const res = await setCameraStorageRoot(camera.id, camStorageRoot, camStorageMigrate, camStorageDeleteSource);
      if (res.migration) {
        camStorageMigration = res.migration;
        startStoragePoll(camera.id);
      }
      import('$lib/toast').then(({ showToast }) =>
        showToast(t('cameras.storageSwitched'), 'success'));
    } catch (e) {
      import('$lib/toast').then(({ showToast }) =>
        showToast(e instanceof Error ? e.message : t('cameras.storageSwitchFailed'), 'error'));
    } finally {
      camStorageSwitching = false;
    }
  }

  function populateForm(camera: Camera) {
    formName = camera.name;
    formProtocol = camera.protocol;
    formEncoding = camera.encoding || '';
    // Handle legacy combined protocols
    if (camera.protocol === 'rtsp_h264') { formProtocol = 'rtsp'; formEncoding = 'h264'; }
    else if (camera.protocol === 'rtsp_h265') { formProtocol = 'rtsp'; formEncoding = 'h265'; }
    else if (camera.protocol === 'rtsp_mjpeg') { formProtocol = 'rtsp'; formEncoding = 'mjpeg'; }
    else if (camera.protocol === 'http_jpeg') { formProtocol = 'http'; formEncoding = 'jpeg'; }
    formUrl = camera.url || '';
    formUsername = camera.username || '';
    formPassword = '';
    showPassword = false;
    formDescription = camera.description || '';
    formLocation = camera.location || '';
    formBrand = camera.brand || '';
    formModel = camera.model || '';
    formSerialNumber = camera.serial_number || '';
    formRetentionDays = camera.retention_days || 0;
    formStreamEncoding = camera.stream_encoding || '';
    formTranscodingEnabled = camera.transcoding?.enabled ?? false;
    formTranscodingCodec = !h265Available ? 'h264' : (camera.transcoding?.target_codec || 'h264');
    formTranscodingPreset = camera.transcoding?.preset || 'ultrafast';
    formTranscodingBitrate = camera.transcoding?.bitrate || '2M';
    formTranscodingCRF = camera.transcoding?.crf || 0;
    validationErrors = {};
    // Dark frame filtering
    formDarkFrameFilterEnabled = camera.dark_frame_filter_enabled ?? false;
    formDarkFrameThreshold = camera.dark_frame_threshold || 15;
    // Recording schedule
    const sched = camera.recording_schedule;
    formRecordingScheduleEnabled = sched && sched.time_ranges && sched.time_ranges.length > 0;
    if (sched && sched.time_ranges && sched.time_ranges.length > 0) {
      formRecordingScheduleStart = sched.time_ranges[0].start || '06:00';
      formRecordingScheduleEnd = sched.time_ranges[0].end || '22:00';
    }
    formChannel = camera.channel || '';
    formAudioEnabled = camera.audio_enabled ?? false;
    formRecordingEnabled = camera.recording_enabled ?? true;
    formCascadeEnabled = camera.cascade_enabled ?? true;
    formRecordingMode = camera.recording_mode === 'adaptive' ? 'adaptive' : 'continuous';
    formAdaptiveCalmThreshold = camera.adaptive?.calm_threshold ?? '';
    formAdaptiveTimelapseInterval = camera.adaptive?.timelapse_interval ?? '';
    formAdaptiveSpikeFactor = camera.adaptive?.spike_factor ? String(camera.adaptive.spike_factor) : '';
    formAdaptiveGopBufferMB = camera.adaptive?.gop_buffer_bytes
      ? String(Math.round(camera.adaptive.gop_buffer_bytes / (1024 * 1024)))
      : '';
    formTwoWayAudioEnabled = camera.two_way_audio_enabled ?? false;
    formSubnetHints = (camera.subnet_hints ?? []).join('\n');
    formStreamKey = camera.stream_key || '';
    formSRTPassphrase = camera.srt_passphrase || '';
    formSRTStreamID = camera.srt_stream_id || '';
    formGB28181DeviceID = camera.gb28181?.device_id || '';
    formGB28181ChannelID = camera.gb28181?.channel_id || '';
    formPushRetentionDays = camera.push_retention_days ?? null;
    formPushTargets = (camera.push_targets ?? []).map((p) => ({ ...p }));
    // Start polling push-out status while editing (only if there are targets).
    startPushStatusPolling(camera.id);
  }

  // --- Push-out (relay) helpers ---
  onDestroy(() => {
    stopPushStatusPolling();
    if (camStoragePoll) clearInterval(camStoragePoll);
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

  async function loadMergeConfig(cameraId: string) {
    mergeConfig = null;
    mergeConfigLoading = true;
    try {
      mergeConfig = await getMergeConfig(cameraId);
    } catch (e) { console.warn('Failed to load merge config:', e); mergeConfig = null; } finally {
      mergeConfigLoading = false;
    }
  }

  async function loadCapabilities(cam: Camera) {
    if (normalizeProtocol(cam.protocol) !== 'onvif') {
      deviceCaps = null;
      return;
    }
    capsLoading = true;
    try {
      deviceCaps = await getDeviceCapabilities(cam.id);
    } catch (e) {
      console.warn('Failed to load device capabilities:', e);
      deviceCaps = null;
    } finally {
      capsLoading = false;
    }
  }

  function validateField(field: string, value: string) {
    if (field === 'name' && !value.trim()) {
      validationErrors['name'] = t('cameras.nameRequired');
    } else if (field === 'url' && !value.trim()) {
      validationErrors['url'] = t('cameras.urlRequired');
    } else {
      delete validationErrors[field];
    }
  }

  function validate(): boolean {
    validationErrors = {};
    if (!formName.trim()) validationErrors['name'] = t('cameras.nameRequired');
    if (!formProtocol) validationErrors['protocol'] = t('cameras.protocolRequired');
    // gb28181 cameras are identified by SIP DeviceID/ChannelID — no URL.
    // rtmp/srt/whip push cameras are identified by stream key / stream-id —
    // the form shows no URL field for them, so the requirement must not apply
    // (a hidden-field validation error silently blocked save with no UI hint).
    const urlNotRequired = formProtocol === 'gb28181' || formProtocol === 'whip' || formProtocol === 'rtmp' || formProtocol === 'srt';
    if (!urlNotRequired && !formUrl.trim()) validationErrors['url'] = t('cameras.urlRequired');
    if (formProtocol === 'gb28181') {
      if (!formGB28181DeviceID.trim()) validationErrors['gb28181_device_id'] = t('cameras.gb28181DeviceIdRequired');
      if (!formGB28181ChannelID.trim()) validationErrors['gb28181_channel_id'] = t('cameras.gb28181ChannelIdRequired');
    }
    if (!formProtocol) validationErrors['protocol'] = t('cameras.protocolRequired');
    // Push-out relay targets: an enabled target must have a non-empty URL whose
    // scheme matches its selected protocol. This was unvalidated, so a target
    // saved with a blank/typo URL appeared later as "only the name, no link"
    // in the camera-card popover (issue #297). Disabled targets are skipped —
    // a user may stage a draft target and turn it on later.
    for (const tgt of formPushTargets) {
      if (!tgt.enabled) continue;
      const u = (tgt.url || '').trim();
      if (!u) {
        validationErrors[`push_${tgt.id}`] = t('cameras.pushOutUrlRequired');
      } else if (!/^rtmp:\/\//i.test(u) && !/^rtsp:\/\//i.test(u)) {
        validationErrors[`push_${tgt.id}`] = t('cameras.pushOutUrlBadScheme');
      }
    }
    return Object.keys(validationErrors).length === 0;
  }
  async function handleTestConnection() {
    if (!formUrl.trim()) return;
    testing = true;
    testResult = null;
    try {
      testResult = await testConnection({
        protocol: formProtocol,
        url: formUrl,
        username: formUsername || undefined,
        password: formPassword || undefined,
        encoding: formEncoding || undefined,
        onvif_endpoint: formProtocol === 'onvif' ? formUrl : undefined,
      });
    } catch (e: any) {
      testResult = { success: false, message: friendlyError(e, 'cameras.testFailed'), latency_ms: 0 };
    } finally {
      testing = false;
    }
  }

async function handleSubmit() {
    if (!validate()) return;
    saving = true;

    // Check if transcoding is being newly enabled for an existing camera
    const isEnablingTranscoding = editingCamera && formTranscodingEnabled && !editingCamera.transcoding?.enabled;

    if (isEnablingTranscoding) {
        try {
            const countRes = await getUntranscodedRecordingCount(editingCamera.id);
            if (countRes.count > 0) {
                saving = false;
                const confirmed = await onbackfillneeded?.({
                    cameraId: editingCamera.id,
                    count: countRes.count,
                    targetCodec: formTranscodingCodec,
                }) ?? false;

                if (confirmed) {
                    // Save camera then start backfill
                    saving = true;
                    await performCameraSave();
                    const result = await startBackfill(editingCamera.id);
                    showToast(t('transcoding.backfill.success', { count: String(result.enqueued) }), 'success');
                    saving = false;
                    onsave();
                } else {
                    formTranscodingEnabled = false;
                }
                return;
            }
        } catch (e) {
            console.warn('Failed to check untranscoded recordings:', e);
            // Proceed with save anyway
        }
    }

    try {
        await performCameraSave();
        onsave();
    } catch (e) { console.warn('Failed to save camera:', e); showToast(
        editingCamera ? t('cameras.failedUpdate') : t('cameras.failedAdd'),
        'error'
    );
    } finally {
        saving = false;
    }
}

// Adaptive-recording payload (#435): only sent when the mode is adaptive, and
// only the params the user actually filled (blank = backend default). Eligible
// only for differential encodings — the backend rejects adaptive + jpeg/mjpeg.
function buildAdaptivePayload() {
    if (formRecordingMode !== 'adaptive') return undefined;
    const p: AdaptiveRecordingConfig = {};
    if (formAdaptiveCalmThreshold.trim()) p.calm_threshold = formAdaptiveCalmThreshold.trim();
    if (formAdaptiveTimelapseInterval.trim()) p.timelapse_interval = formAdaptiveTimelapseInterval.trim();
    const spike = parseFloat(formAdaptiveSpikeFactor);
    if (!Number.isNaN(spike) && spike > 0) p.spike_factor = spike;
    const mb = parseInt(formAdaptiveGopBufferMB, 10);
    if (!Number.isNaN(mb) && mb > 0) p.gop_buffer_bytes = mb * 1024 * 1024;
    return p;
}

async function performCameraSave() {
    if (editingCamera) {
        const data: UpdateCameraRequest = {
            name: formName,
            protocol: formProtocol,
            url: formUrl,
            description: formDescription || undefined,
            location: formLocation || undefined,
            brand: formBrand || undefined,
            model: formModel || undefined,
            serial_number: formSerialNumber || undefined,
            retention_days: formRetentionDays,
            stream_encoding: formProtocol === 'onvif' ? (formStreamEncoding || undefined) : undefined,
            // onvif/xiaomi/gb28181 auto-detect codec from the live stream and
            // ignore the stored value, so don't send one (avoids writing a
            // stale label). rtsp/http/srt/rtmp send formEncoding — it drives
            // recorder selection.
            encoding: formProtocol === 'onvif' || formProtocol === 'xiaomi' || formProtocol === 'gb28181' ? undefined : formEncoding,
            transcoding: {
                enabled: formTranscodingEnabled,
                target_codec: formTranscodingCodec,
                preset: formTranscodingPreset,
                bitrate: formTranscodingBitrate,
                crf: formTranscodingCRF || undefined,
            },
            channel: formProtocol === 'xiaomi' ? (formChannel || undefined) : undefined,
            audio_enabled: formAudioEnabled,
            recording_enabled: formRecordingEnabled,
            cascade_enabled: formCascadeEnabled,
            recording_mode: formRecordingMode,
            adaptive: buildAdaptivePayload(),
            two_way_audio_enabled: formProtocol === 'xiaomi' ? formTwoWayAudioEnabled : undefined,
            subnet_hints: formProtocol === 'onvif' ? parseSubnetHints(formSubnetHints) : undefined,
            stream_key: (formProtocol === 'rtmp' || formProtocol === 'whip') ? (formStreamKey || undefined) : undefined,
            srt_passphrase: formProtocol === 'srt' ? (formSRTPassphrase || undefined) : undefined,
            srt_stream_id: formProtocol === 'srt' ? (formSRTStreamID || undefined) : undefined,
            gb28181: formProtocol === 'gb28181' ? { device_id: formGB28181DeviceID, channel_id: formGB28181ChannelID } : undefined,
            push_targets: formPushTargets.length > 0 ? formPushTargets : [],
            push_retention_days: (formProtocol === 'srt' || formProtocol === 'rtmp' || formProtocol === 'whip') ? formPushRetentionDays : undefined,
            dark_frame_filter_enabled: formDarkFrameFilterEnabled,
            dark_frame_threshold: formDarkFrameFilterEnabled ? formDarkFrameThreshold : undefined,
            recording_schedule: formRecordingScheduleEnabled ? {
                time_ranges: [{ start: formRecordingScheduleStart, end: formRecordingScheduleEnd }],
            } : undefined,
        };
        if (formUsername && formUsername !== editingCamera.username) {
            data.username = formUsername;
        }
        if (formPassword) {
            if (!data.username && formUsername === editingCamera.username) {
                data.username = formUsername;
            }
            data.password = formPassword;
        }

        // Save per-camera merge config if editing
        if (mergeConfig) {
            try {
                await updateMergeConfig(editingCamera.id, mergeConfig);
            } catch (e) { console.warn('Failed to save merge config:', e); }
        }
        await updateCamera(editingCamera.id, data);
        showToast(t('cameras.cameraUpdated'), 'success');
    } else {
        const data: CreateCameraRequest = {
            name: formName,
            protocol: formProtocol,
            url: formUrl,
            description: formDescription || undefined,
            location: formLocation || undefined,
            brand: formBrand || undefined,
            model: formModel || undefined,
            serial_number: formSerialNumber || undefined,
            retention_days: formRetentionDays,
            stream_encoding: formProtocol === 'onvif' ? (formStreamEncoding || undefined) : undefined,
            // onvif/xiaomi/gb28181 auto-detect codec from the live stream and
            // ignore the stored value, so don't send one (avoids writing a
            // stale label). rtsp/http/srt/rtmp send formEncoding — it drives
            // recorder selection.
            encoding: formProtocol === 'onvif' || formProtocol === 'xiaomi' || formProtocol === 'gb28181' ? undefined : formEncoding,
            // stored value, so don't send one (avoids writing a stale label).
            // rtsp/http/srt/rtmp send formEncoding — it drives recorder selection.
            encoding: formProtocol === 'onvif' || formProtocol === 'xiaomi' || formProtocol === 'gb28181' ? undefined : formEncoding,
            transcoding: {
                enabled: formTranscodingEnabled,
                target_codec: formTranscodingCodec,
                preset: formTranscodingPreset,
                bitrate: formTranscodingBitrate,
                crf: formTranscodingCRF || undefined,
            },
            channel: formProtocol === 'xiaomi' ? (formChannel || undefined) : undefined,
            audio_enabled: formAudioEnabled,
            recording_enabled: formRecordingEnabled,
            cascade_enabled: formCascadeEnabled,
            recording_mode: formRecordingMode,
            adaptive: buildAdaptivePayload(),
            two_way_audio_enabled: formProtocol === 'xiaomi' ? formTwoWayAudioEnabled : undefined,
            subnet_hints: formProtocol === 'onvif' ? parseSubnetHints(formSubnetHints) : undefined,
            stream_key: (formProtocol === 'rtmp' || formProtocol === 'whip') ? (formStreamKey || undefined) : undefined,
            srt_passphrase: formProtocol === 'srt' ? (formSRTPassphrase || undefined) : undefined,
            srt_stream_id: formProtocol === 'srt' ? (formSRTStreamID || undefined) : undefined,
            gb28181: formProtocol === 'gb28181' ? { device_id: formGB28181DeviceID, channel_id: formGB28181ChannelID } : undefined,
            push_targets: formPushTargets.length > 0 ? formPushTargets : undefined,
            push_retention_days: (formProtocol === 'srt' || formProtocol === 'rtmp' || formProtocol === 'whip') ? formPushRetentionDays : undefined,
            dark_frame_filter_enabled: formDarkFrameFilterEnabled,
            dark_frame_threshold: formDarkFrameFilterEnabled ? formDarkFrameThreshold : undefined,
            recording_schedule: formRecordingScheduleEnabled ? {
                time_ranges: [{ start: formRecordingScheduleStart, end: formRecordingScheduleEnd }],
            } : undefined,
        };
        if (formUsername) data.username = formUsername;
        if (formPassword) data.password = formPassword;
        await createCamera(data);
        showToast(t('cameras.cameraAdded'), 'success');
    }
}


</script>
<div class="card p-6 border th-border">
  <h3 class="text-lg font-semibold th-text-primary mb-4">
    {editingCamera ? t('cameras.editCamera') : t('cameras.addCamera')}
  </h3>

  <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
    <!-- Name -->
    <div>
      <label for="cam-name" class="input-label">{t('cameras.name')}</label>
      <input id="cam-name" type="text" class="input {validationErrors['name'] ? 'border-red-500' : ''}" bind:value={formName} onblur={() => validateField('name', formName)} oninput={() => { if (validationErrors['name']) delete validationErrors['name']; }} />
      {#if validationErrors['name']}
        <p class="th-color-danger text-xs mt-1">{validationErrors['name']}</p>
      {/if}
    </div>

    <!-- Protocol -->
    <div>
      <label for="cam-protocol" class="input-label">{t('cameras.protocol')}</label>
      <select id="cam-protocol" class="input" bind:value={formProtocol}>
        {#each protocols as proto (proto.id)}
          <option value={proto.id}>{proto.label}</option>
        {/each}
      </select>
      {#if validationErrors['protocol']}
        <p class="th-color-danger text-xs mt-1">{validationErrors['protocol']}</p>
      {/if}
    </div>

    <!-- Encoding -->
    <div>
      <label for="cam-encoding" class="input-label">{t('cameras.tableEncoding')}</label>
      <!-- onvif + xiaomi auto-detect their codec from the live stream and ignore
           any stored value, so the field is read-only "auto-detect" for them.
           rtsp/http/srt/rtmp keep it editable — it drives recorder selection
           (H264Recorder vs H265Recorder). See #166. -->
      {#if formProtocol === 'onvif' || formProtocol === 'xiaomi' || formProtocol === 'gb28181'}
        <select id="cam-encoding" class="input" disabled>
          <option value="">{t('cameras.autoDetect')}</option>
        </select>
      {:else}
        <select id="cam-encoding" class="input" bind:value={formEncoding}>
          {#each (protocolsMap.get(formProtocol)?.encodings || [formEncoding]) as enc}
            <option value={enc}>{t('cameras.encoding.' + enc) || enc.toUpperCase()}</option>
          {/each}
        </select>
      {/if}
    </div>

    {#if formProtocol === 'xiaomi'}
      <!-- Lens/Channel -->
      <div>
        <label for="cam-channel" class="input-label">{t('cameras.channel')}</label>
        <select id="cam-channel" class="input" bind:value={formChannel}>
          <option value="">{t('cameras.channelMain')}</option>
          <option value="1">{t('cameras.channelSecondary')}</option>
        </select>
      </div>
    {/if}

    {#if formProtocol === 'gb28181'}
      <!-- GB28181: the camera is identified by its SIP DeviceID + ChannelID.
           The NVR invites the channel over SIP; there is no URL to dial. -->
      <div>
        <label for="cam-gb28181-device-id" class="input-label">{t('cameras.gb28181DeviceId')}</label>
        <input id="cam-gb28181-device-id" type="text" class="input {validationErrors['gb28181_device_id'] ? 'border-red-500' : ''}" bind:value={formGB28181DeviceID}
          placeholder={t('cameras.gb28181DeviceIdPlaceholder')}
          oninput={() => { if (validationErrors['gb28181_device_id']) delete validationErrors['gb28181_device_id']; }} />
        {#if validationErrors['gb28181_device_id']}
          <p class="th-color-danger text-xs mt-1">{validationErrors['gb28181_device_id']}</p>
        {/if}
      </div>
      <div>
        <label for="cam-gb28181-channel-id" class="input-label">{t('cameras.gb28181ChannelId')}</label>
        <input id="cam-gb28181-channel-id" type="text" class="input {validationErrors['gb28181_channel_id'] ? 'border-red-500' : ''}" bind:value={formGB28181ChannelID}
          placeholder={t('cameras.gb28181ChannelIdPlaceholder')}
          oninput={() => { if (validationErrors['gb28181_channel_id']) delete validationErrors['gb28181_channel_id']; }} />
        {#if validationErrors['gb28181_channel_id']}
          <p class="th-color-danger text-xs mt-1">{validationErrors['gb28181_channel_id']}</p>
        {/if}
      </div>
    {/if}

    {#if formProtocol === 'whip'}
      <!-- WHIP push (WebRTC): browser/OBS pushes to the NVR; show the endpoint -->
      <div>
        <label for="cam-stream-key" class="input-label">{t('cameras.streamKey')}</label>
        <input id="cam-stream-key" type="text" class="input" bind:value={formStreamKey}
          placeholder="front-door" />
        <p class="text-xs th-text-muted mt-1">
          {t('cameras.whipPushAddress')}: http{'<'}NVR-IP:PORT{'>'}/whip/{formStreamKey || '<key>'}
        </p>
        <p class="text-xs th-text-muted mt-1">{t('cameras.whipHint')}</p>
      </div>
    {/if}

    {#if formProtocol === 'rtmp'}
      <!-- RTMP push: publisher connects to NVR; show the ingest address -->
      <div>
        <label for="cam-stream-key" class="input-label">{t('cameras.streamKey')}</label>
        <input id="cam-stream-key" type="text" class="input" bind:value={formStreamKey}
          placeholder="front-door" />
        <p class="text-xs th-text-muted mt-1">
          {t('cameras.rtmpPushAddress')}: rtmp://{'<'}NVR-IP{'>'}:1935/live/{formStreamKey || '<key>'}
        </p>
      </div>
    {/if}

    {#if formProtocol === 'srt'}
      <!-- SRT push: publisher connects to NVR -->
      <div>
        <label for="cam-srt-stream-id" class="input-label">{t('cameras.srtStreamID')}</label>
        <input id="cam-srt-stream-id" type="text" class="input" bind:value={formSRTStreamID}
          placeholder="live/front-door" />
        <p class="text-xs th-text-muted mt-1">
          {t('cameras.srtPushAddress')}: srt://{'<'}NVR-IP{'>'}:9000?streamid={formSRTStreamID || editingCamera?.id || '<id>'}
        </p>
      </div>
      <div>
        <label for="cam-srt-passphrase" class="input-label">{t('cameras.srtPassphrase')}</label>
        <input id="cam-srt-passphrase" type="text" class="input" bind:value={formSRTPassphrase}
          placeholder="(optional AES passphrase)" />
        <p class="text-xs th-text-muted mt-1">{t('cameras.srtPassphraseHint')}</p>
      </div>
    {/if}

    {#if formProtocol === 'srt' || formProtocol === 'rtmp' || formProtocol === 'whip'}
      <!-- Push-in save policy: follow global / live-only / custom retention -->
      <div>
        <label for="cam-push-retention" class="input-label">{t('cameras.pushRetention')}</label>
        <select id="cam-push-retention" class="input" onchange={(e) => {
          const v = (e.target as HTMLSelectElement).value;
          formPushRetentionDays = v === '' ? null : v === 'live' ? 0 : parseInt(v, 10);
        }}>
          <option value="">{t('cameras.pushRetentionGlobal')}</option>
          <option value="live" selected={formPushRetentionDays === 0}>{t('cameras.pushRetentionLiveOnly')}</option>
          {#each [1, 3, 7, 14, 30, 90] as d}
            <option value={d} selected={formPushRetentionDays === d}>{d} {t('cameras.days')}</option>
          {/each}
        </select>
        <p class="text-xs th-text-muted mt-1">{t('cameras.pushRetentionHint')}</p>
      </div>
    {/if}

    <!-- Recording toggle: when off, the camera is live-only (no segments on disk) -->
    <div class="flex items-center gap-2">
      <input
        id="cam-recording"
        type="checkbox"
        class="checkbox"
        bind:checked={formRecordingEnabled}
      />
      <label for="cam-recording" class="input-label cursor-pointer">
        {t('cameras.recordingEnabled')}
      </label>
    </div>
    {#if !formRecordingEnabled}
      <p class="text-xs th-text-muted -mt-1">{t('cameras.recordingDisabledHint')}</p>
    {:else if formEncoding === 'h264' || formEncoding === 'h265'}
      <!-- Recording mode (#435): continuous or adaptive (motion-aware sparse) -->
      <div>
        <label for="cam-recording-mode" class="input-label">{t('cameras.recordingMode')}</label>
        <select id="cam-recording-mode" class="input" bind:value={formRecordingMode}>
          <option value="continuous">{t('cameras.recordingModeContinuous')}</option>
          <option value="adaptive">{t('cameras.recordingModeAdaptive')}</option>
        </select>
        <p class="text-xs th-text-muted mt-1">
          {formRecordingMode === 'adaptive' ? t('cameras.recordingModeAdaptiveHint') : t('cameras.recordingModeHint')}
        </p>
      </div>
      {#if formRecordingMode === 'adaptive'}
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label for="cam-adaptive-calm" class="input-label">{t('cameras.adaptiveCalmThreshold')}</label>
            <input id="cam-adaptive-calm" class="input" type="text" placeholder="60s" bind:value={formAdaptiveCalmThreshold} />
          </div>
          <div>
            <label for="cam-adaptive-interval" class="input-label">{t('cameras.adaptiveTimelapseInterval')}</label>
            <input id="cam-adaptive-interval" class="input" type="text" placeholder="30s" bind:value={formAdaptiveTimelapseInterval} />
          </div>
          <div>
            <label for="cam-adaptive-spike" class="input-label">{t('cameras.adaptiveSpikeFactor')}</label>
            <input id="cam-adaptive-spike" class="input" type="number" step="0.1" min="1.5" max="10" placeholder="3.0" bind:value={formAdaptiveSpikeFactor} />
          </div>
          <div>
            <label for="cam-adaptive-gop" class="input-label">{t('cameras.adaptiveGopBufferMB')}</label>
            <input id="cam-adaptive-gop" class="input" type="number" step="1" min="1" max="64" placeholder="16" bind:value={formAdaptiveGopBufferMB} />
          </div>
        </div>
        <p class="text-xs th-text-muted -mt-1">{t('cameras.adaptiveParamsHint')}</p>
      {/if}
    {/if}

    <!-- Cascade catalog toggle: when off, the camera is hidden from the
         GB28181 cascade upper platform (catalog + INVITE). -->
    <div class="flex items-center gap-2">
      <input
        id="cam-cascade"
        type="checkbox"
        class="checkbox"
        bind:checked={formCascadeEnabled}
      />
      <label for="cam-cascade" class="input-label cursor-pointer">
        {t('cameras.cascadeEnabled')}
      </label>
    </div>
    {#if !formCascadeEnabled}
      <p class="text-xs th-text-muted -mt-1">{t('cameras.cascadeDisabledHint')}</p>
    {/if}

    {#if editingCamera}
      <!-- Storage location: hot per-camera switch + background migration -->
      <div class="md:col-span-2 border-t th-border pt-4 mt-2">
        <label class="input-label">{t('cameras.storageLocation')}</label>
        <div class="flex items-center gap-3 mt-2 flex-wrap">
          <select class="input max-w-xs" bind:value={camStorageRoot} disabled={camStorageSwitching}>
            <option value="">{t('cameras.storageDefaultOption')}</option>
            {#each camStorageCandidates as c (c.path)}
              <option value={c.path}>{c.path}</option>
            {/each}
          </select>
          <button
            type="button"
            class="btn btn-primary btn-sm"
            disabled={camStorageSwitching}
            onclick={() => editingCamera && applyCameraStorage(editingCamera)}
          >
            {camStorageSwitching ? t('common.saving') : t('cameras.storageApply')}
          </button>
        </div>
        {#if camStorageCandidates.length === 0}
          <p class="text-xs th-text-muted mt-1">{t('cameras.storageNoCandidatesHint')}</p>
        {:else}
          <label class="flex items-center gap-1.5 text-xs th-text-secondary mt-2 cursor-pointer">
            <input type="checkbox" bind:checked={camStorageMigrate} disabled={camStorageSwitching} />
            {t('cameras.storageMigrateHistory')}
          </label>
          {#if camStorageMigrate && camStorageRoot}
            <label class="flex items-center gap-1.5 text-xs th-text-secondary ml-5 cursor-pointer">
              <input type="checkbox" bind:checked={camStorageDeleteSource} disabled={camStorageSwitching} />
              {t('settings.migrateDeleteSource')}
            </label>
          {/if}
          {#if camStorageMigration && (camStorageMigration.state === 'running' || camStorageMigration.state === 'queued' || camStorageMigration.state === 'paused')}
            <div class="mt-3">
              <progress class="w-full" max={Math.max(camStorageMigration.total_files ?? 1, 1)} value={camStorageMigration.done_files ?? 0}></progress>
              <p class="text-xs th-text-muted mt-1">
                {t('settings.migrateProgress', {
                  done: String(camStorageMigration.done_files ?? 0),
                  total: String(camStorageMigration.total_files ?? 0),
                  mb: ((camStorageMigration.done_bytes ?? 0) / (1024 * 1024)).toFixed(1),
                })}
                {#if camStorageMigration.state === 'paused'}· {t('cameras.storageMigrationPaused')}{/if}
              </p>
            </div>
          {/if}
        {/if}
      </div>
    {/if}

    <!-- Audio recording toggle (not supported for MJPEG/JPEG cameras) -->
    {#if formEncoding !== 'mjpeg' && formEncoding !== 'jpeg'}
      <div class="flex items-center gap-2">
        <input
          id="cam-audio"
          type="checkbox"
          class="checkbox"
          bind:checked={formAudioEnabled}
        />
        <label for="cam-audio" class="input-label cursor-pointer">
          {t('cameras.audioEnabled')}
        </label>
      </div>
    {/if}

    <!-- Xiaomi two-way audio toggle -->
    {#if formProtocol === 'xiaomi'}
      <div class="flex items-center gap-2">
        <input
          id="cam-two-way-audio"
          type="checkbox"
          class="checkbox"
          bind:checked={formTwoWayAudioEnabled}
        />
        <label for="cam-two-way-audio" class="input-label cursor-pointer">
          {t('cameras.twoWayAudioEnabled') || 'Two-way audio'}
        </label>
      </div>
    {/if}

    <!-- Advanced recording options (dark-frame filter + schedule) — collapsed by
         default to reduce clutter; auto-opens when either option is already on. -->
    {#if formProtocol === 'rtsp' || formProtocol === 'onvif' || formProtocol === 'http'}
    <details class="md:col-span-2 border th-border rounded-lg" open={formDarkFrameFilterEnabled || formRecordingScheduleEnabled ? true : undefined}>
      <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
        {t('cameras.advancedRecording')}
      </summary>
      <div class="px-4 pb-4 space-y-4">
        <!-- Dark frame filtering (MJPEG/AVI cameras only) -->
        <div class="space-y-2">
          <div class="flex items-center gap-2">
            <input id="cam-dark-frame" type="checkbox" class="checkbox"
              bind:checked={formDarkFrameFilterEnabled}
            />
            <label for="cam-dark-frame" class="input-label cursor-pointer">
              {t('cameras.darkFrameFilter') || 'Dark frame filter'}
              <span class="text-xs th-text-muted ml-1">({t('cameras.darkFrameFilterHint') || 'skip night/dark segments'})</span>
            </label>
          </div>
          {#if formDarkFrameFilterEnabled}
            <div class="flex items-center gap-2 pl-6">
              <label class="text-sm th-text-muted whitespace-nowrap">{t('cameras.brightnessThreshold') || 'Brightness threshold'}</label>
              <input type="range" min="5" max="50" bind:value={formDarkFrameThreshold} class="range range-sm w-32" />
              <span class="text-sm font-mono w-8">{formDarkFrameThreshold}</span>
            </div>
          {/if}
        </div>

        <!-- Recording schedule -->
        <div class="space-y-2">
          <div class="flex items-center gap-2">
            <input id="cam-rec-schedule" type="checkbox" class="checkbox"
              bind:checked={formRecordingScheduleEnabled}
            />
            <label for="cam-rec-schedule" class="input-label cursor-pointer">
              {t('cameras.recordingSchedule') || 'Recording schedule'}
              <span class="text-xs th-text-muted ml-1">({t('cameras.recordingScheduleHint') || 'time-based recording'})</span>
            </label>
          </div>
          {#if formRecordingScheduleEnabled}
            <div class="flex items-center gap-2 pl-6">
              <label class="text-sm th-text-muted whitespace-nowrap">{t('cameras.recordFrom') || 'Record from'}</label>
              <input type="time" bind:value={formRecordingScheduleStart} class="input w-28 py-1" />
              <label class="text-sm th-text-muted whitespace-nowrap">{t('cameras.recordTo') || 'to'}</label>
              <input type="time" bind:value={formRecordingScheduleEnd} class="input w-28 py-1" />
            </div>
          {/if}
        </div>
      </div>
    </details>
    {/if}

    <!-- URL (hidden for push/ingest protocols — publisher connects to us; and
         for gb28181 — the camera is identified by SIP DeviceID/ChannelID) -->
    {#if formProtocol !== 'srt' && formProtocol !== 'rtmp' && formProtocol !== 'whip' && formProtocol !== 'gb28181'}
    <div class="md:col-span-2">
      <label for="cam-url" class="input-label">
        {t('cameras.url')}
        {#if formProtocol === 'onvif'}
          <span class="text-xs th-text-muted ml-1">({t('cameras.onvifEndpoint')})</span>
        {/if}
      </label>
      <div class="flex gap-2">
        <input id="cam-url" type="text" class="input flex-1 {validationErrors['url'] ? 'border-red-500' : ''}" bind:value={formUrl}
          placeholder={formProtocol === 'xiaomi' ? 'xiaomi://device_id' : formProtocol === 'onvif' ? 'http://192.168.1.100:80/onvif/device_service' : 'rtsp://...'}
          onblur={() => validateField('url', formUrl)} oninput={() => { if (validationErrors['url']) delete validationErrors['url']; testResult = null; }} />
        {#if formProtocol !== 'xiaomi'}
          <button
            type="button"
            onclick={handleTestConnection}
            disabled={testing || !formUrl.trim()}
            class="btn btn-ghost px-3 py-2 flex items-center gap-1.5 whitespace-nowrap"
            title={t('cameras.testConnection')}
          >
            <PlugZap size={14} />
            {#if testing}
              <span class="spinner mr-1"></span>{t('cameras.testing')}
            {:else}
              {t('cameras.testConnection')}
            {/if}
          </button>
        {/if}
      </div>
      {#if testResult}
        <p class="text-xs mt-1 {testResult.success ? 'th-color-success' : 'th-color-danger'}">
          {testResult.success
            ? t('cameras.testSuccess', { latency: String(testResult.latency_ms) })
            : testResult.message}
        </p>
        {#if testResult.success && testResult.codec_lie}
          <p class="text-xs th-text-muted">{t('cameras.testCodecCorrected', { encoding: testResult.encoding || '' })}</p>
        {:else if testResult.reachable && !testResult.stream_ok}
          <p class="text-xs th-text-muted">{t('cameras.testReachableNoStream')}</p>
        {/if}
      {/if}
      {#if validationErrors['url']}
        <p class="th-color-danger text-xs mt-1">{validationErrors['url']}</p>
      {/if}
    </div>
    {/if}

    {#if formProtocol === 'srt' || formProtocol === 'rtmp'}
      <div class="md:col-span-2 p-3 rounded-md th-bg-hover border th-border text-sm">
        <p class="th-text-secondary">{t('cameras.pushHint')}</p>
      </div>
    {/if}

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
                      onchange={(e) => updatePushTarget(tgt.id, { transcode_policy: (e.target as HTMLSelectElement).value as 'auto' | 'force_sw' | 'off' })}>
                      <option value="auto">{t('cameras.pushTranscodeAuto')}</option>
                      <option value="force_sw">{t('cameras.pushTranscodeForceSW')}</option>
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

    {#if formProtocol === 'xiaomi'}
      {#if editingCamera?.protocol === 'xiaomi' && xiaomiDeviceList.length > 0}
        {@const matchDid = formUrl.replace('xiaomi://', '')}
        {@const matchedDevice = xiaomiDeviceList.find(d => d.did === matchDid)}
        {#if matchedDevice}
          <div class="p-3 rounded-md th-bg-hover border th-border text-sm">
            <div class="font-medium th-text-primary">{matchedDevice.name}</div>
            <div class="th-text-secondary">{matchedDevice.model} · {matchedDevice.localip}</div>
            <div class="{matchedDevice.isOnline ? 'th-color-success' : 'th-text-muted'}">
              {matchedDevice.isOnline ? t('xiaomi.online') : t('xiaomi.offline')}
            </div>
          </div>
        {/if}
      {/if}
    {/if}

    {#if protocolsMap.get(formProtocol)?.capabilities?.auth}
      <!-- Username -->
      <div>
        <label for="cam-user" class="input-label">{t('cameras.username')}</label>
        <input id="cam-user" type="text" class="input" bind:value={formUsername} placeholder={editingCamera ? (editingCamera.username || t('cameras.notSet')) : ''} />
      </div>

      <!-- Password -->
      <div>
        <label for="cam-pass" class="input-label">{t('cameras.password')}</label>
        <div class="relative">
          <input
            id="cam-pass"
            type={showPassword ? 'text' : 'password'}
            class="input pr-10"
            bind:value={formPassword}
            placeholder={editingCamera ? (editingCamera.has_password ? t('cameras.passwordSet') : t('cameras.notSet')) : ''}
          />
          <button
            type="button"
            class="absolute right-2 top-1/2 -translate-y-1/2 th-text-tertiary hover:th-text-primary transition-colors"
            onclick={() => showPassword = !showPassword}
            aria-label={showPassword ? t('common.hidePassword') : t('common.showPassword')}
          >
            {#if showPassword}
              <EyeOff class="w-4 h-4" />
            {:else}
              <Eye class="w-4 h-4" />
            {/if}
          </button>
        </div>
      </div>
    {:else if protocolsMap.get(formProtocol)}
      <div class="md:col-span-2 text-sm th-text-secondary">
        {t('cameras.authManagedExternally')}
      </div>
    {/if}


  </div>

  <details class="mt-6 border th-border rounded-lg">
    <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
      {t('cameras.form.advancedSettings')}
    </summary>
    <div class="px-4 pb-4 pt-2">
      <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
        <!-- Description -->
        <div class="md:col-span-2">
          <label for="cam-desc" class="input-label">{t('cameras.description')}</label>
          <textarea id="cam-desc" class="input" rows="2" bind:value={formDescription} placeholder={t('cameras.descriptionPlaceholder')}></textarea>
        </div>

        <!-- Location -->
        <div>
          <label for="cam-location" class="input-label">{t('cameras.location')}</label>
          <input id="cam-location" type="text" class="input" bind:value={formLocation} placeholder={t('cameras.locationPlaceholder')} />
        </div>

        <!-- Brand -->
        <div>
          <label for="cam-brand" class="input-label">{t('cameras.brand')}</label>
          <input id="cam-brand" type="text" class="input" bind:value={formBrand} />
        </div>

        <!-- Model -->
        <div>
          <label for="cam-model" class="input-label">{t('cameras.model')}</label>
          <input id="cam-model" type="text" class="input" bind:value={formModel} />
        </div>

        <!-- Serial Number -->
        <div>
          <label for="cam-serial" class="input-label">{t('cameras.serialNumber')}</label>
          <input id="cam-serial" type="text" class="input" bind:value={formSerialNumber} />
        </div>

        <!-- Retention Days -->
        <div>
          <label for="cam-retention" class="input-label">{t('cameras.retentionDays')}</label>
          <input id="cam-retention" type="number" min="0" class="input" bind:value={formRetentionDays} />
          <p class="th-text-muted text-xs mt-1">{t('cameras.retentionDaysHint')}</p>
        </div>
      </div>
    </div>
  </details>

  <!-- Merge Config (edit mode only) -->
  {#if editingCamera}
    <MergeConfigEditor
      cameraId={editingCamera.id}
      {mergeConfig}
      {mergeConfigLoading}
      onchange={(config) => mergeConfig = { ...config, customized: true }}
      ondelete={() => mergeConfig = null}
    />
  {/if}

  <!-- Transcoding Config (edit mode only, when global enabled) -->
  {#if editingCamera}
    {#if globalTranscodingEnabled}
      <details class="mt-6 border th-border rounded-lg" open={formTranscodingEnabled ? true : undefined}>
        <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
          {t('transcoding.per_camera_config')}
          {#if formTranscodingEnabled}
            <span class="text-xs th-text-muted ml-2">{t('transcoding.enabled')}</span>
          {:else}
            <span class="text-xs th-text-muted ml-2">{t('merge.usingDefault')}</span>
          {/if}
        </summary>

        <div class="px-4 pb-4 pt-2">
          <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
            <!-- Enabled toggle -->
            <div class="md:col-span-2 flex items-center gap-2">
              <input
                id="transcode-enabled"
                type="checkbox"
                class="accent-[var(--color-accent)]"
                bind:checked={formTranscodingEnabled}
              />
              <label for="transcode-enabled" class="th-text-secondary text-sm">{t('transcoding.enabled')}</label>
            </div>

            {#if formTranscodingEnabled}
              <!-- Target Codec -->
              <div>
                <label for="transcode-codec" class="input-label">{t('transcoding.target_codec')}</label>
                <select id="transcode-codec" class="input" bind:value={formTranscodingCodec}>
                  <option value="h264">{t('transcoding.codec_h264')}</option>
                  <option value="h265" disabled={!h265Available}>{t('transcoding.codec_h265')}{!h265Available ? ` (${t('transcoding.unavailable')})` : ''}</option>
                </select>
                {#if !h265Available}
                  <p class="mt-1 text-xs text-[var(--color-danger)]">{t('transcoding.h265_not_available')}</p>
                {:else if formTranscodingCodec === 'h265'}
                  <p class="mt-1 text-xs text-[var(--color-warning)]">{t('transcoding.warning_h265_slow')}</p>
                {/if}
              </div>

              <!-- Preset -->
              <div>
                <label for="transcode-preset" class="input-label">{t('transcoding.preset')}</label>
                <select id="transcode-preset" class="input" bind:value={formTranscodingPreset}>
                  <option value="ultrafast">{t('transcoding.preset_ultrafast')}</option>
                  <option value="faster">{t('transcoding.preset_faster')}</option>
                  <option value="medium">{t('transcoding.preset_medium')}</option>
                </select>
              </div>

              <!-- Bitrate -->
              <div>
                <label for="transcode-bitrate" class="input-label">{t('transcoding.bitrate')}</label>
                <input
                  id="transcode-bitrate"
                  type="text"
                  class="input"
                  bind:value={formTranscodingBitrate}
                  placeholder="2M"
                />
              </div>

              <!-- CRF (Quality) -->
              <div>
                <label for="transcode-crf" class="input-label">{t('transcoding.crf')} <span class="text-xs th-text-muted">({t('transcoding.crfHint')})</span></label>
                <input
                  id="transcode-crf"
                  type="number"
                  min="0"
                  max="51"
                  class="input"
                  bind:value={formTranscodingCRF}
                  placeholder="0"
                />
              </div>
            {/if}
          </div>
        </div>
      </details>
    {:else}
      <div class="mt-6 p-3 rounded-md th-bg-hover border th-border text-sm th-text-muted flex items-center gap-2">
        <svg class="w-4 h-4 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>
        {t('transcoding.warning_global_disabled')}
</div>
  {/if}
{/if}

  <!-- Timelapse Config (edit mode only) -->
  {#if editingCamera}
    <TimelapseConfigEditor cameraId={editingCamera.id} />
  {/if}

  <!-- ONVIF Device Settings (edit mode only, ONVIF cameras) -->
  {#if editingCamera && normalizeProtocol(editingCamera.protocol) === 'onvif' && !capsLoading}
    <div class="mt-6 space-y-4">
      <h4 class="text-sm font-semibold th-text-secondary uppercase tracking-wide">ONVIF</h4>

      <!-- Device Capabilities -->
      <DeviceCapabilities cameraId={editingCamera.id} />

      <!-- IP self-healing: subnet hints (where to look when this camera's IP changes) -->
      <details class="border th-border rounded-lg">
        <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
          {t('cameras.subnetHintsTitle')}
        </summary>
        <div class="px-4 pb-4 space-y-2">
          <p class="text-xs th-text-muted">{t('cameras.subnetHintsHint')}</p>
          <textarea
            bind:value={formSubnetHints}
            rows="3"
            class="input font-mono text-xs"
            placeholder="192.168.1.0/24&#10;10.0.0.0/24"
          ></textarea>
          {#if editingCamera.stable_id}
            <p class="text-xs th-text-muted">{t('cameras.subnetHintsStableId', { id: editingCamera.stable_id })}</p>
          {:else}
            <p class="text-xs th-color-warning">{t('cameras.subnetHintsNoStableId')}</p>
          {/if}
        </div>
      </details>

      <!-- Imaging Panel (if supported) -->
      {#if deviceCaps?.imaging}
        <details class="border th-border rounded-lg" open>
          <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
            {t('onvif.imaging.title')}
          </summary>
          <div class="px-4 pb-4">
            <ImagingPanel cameraId={editingCamera.id} />
          </div>
        </details>
      {/if}

      <!-- Preset Manager (if PTZ supported) -->
      {#if deviceCaps?.ptz}
        <details class="border th-border rounded-lg" open>
          <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
            {t('onvif.presets.title')}
          </summary>
          <div class="px-4 pb-4">
            <PresetManager cameraId={editingCamera.id} />
          </div>
        </details>
      {/if}

      <!-- ONVIF Events (if supported) -->
      {#if deviceCaps?.events}
        <details class="border th-border rounded-lg">
          <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
            {t('onvif.events.title')}
          </summary>
          <div class="px-4 pb-4">
            <ONVIFEvents cameraId={editingCamera.id} maxEvents={50} />
          </div>
        </details>
      {/if}

      <!-- Device Management -->
      <details class="border th-border rounded-lg">
        <summary class="px-4 py-3 cursor-pointer th-text-secondary hover:th-text-primary transition-colors font-medium select-none">
          {t('onvif.device.title')}
        </summary>
        <div class="px-4 pb-4">
          <DeviceManagement cameraId={editingCamera.id} cameraName={editingCamera.name} />
        </div>
      </details>
    </div>
  {/if}

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

  <div class="flex items-center gap-3 mt-6">
    <button onclick={handleSubmit} class="btn btn-primary" disabled={saving}>
      {#if saving}
        <span class="spinner mr-2"></span>
      {/if}
      {t('cameras.save')}
    </button>
    <button onclick={oncancel} class="btn btn-ghost">
      {t('cameras.cancel')}
    </button>
    </div>
</div>

