<script lang="ts">
    import { t } from '$lib/i18n';
    import { friendlyError } from '$lib/errors';
    import {
        createCamera,
        updateCamera,
        getMergeConfig,
        updateMergeConfig,
        testConnection,
        getSettings,
    } from '$lib/api';
    import type {
        Camera,
        CreateCameraRequest,
        UpdateCameraRequest,
        MergeConfig,
        ProtocolInfo,
        XiaomiDevice,
        TestConnectionResult,
        PushTargetConfig,
        AdaptiveRecordingConfig,
        CameraAudioTriggerConfig,
        CameraPixgateConfig,
    } from '$lib/api';
    import { Eye, EyeOff, PlugZap, Layers, Brain } from 'lucide-svelte';
    import { showToast } from '$lib/toast';
    import MergeConfigEditor from '$lib/components/MergeConfigEditor.svelte';
    import TimelapseConfigEditor from '$lib/components/TimelapseConfigEditor.svelte';
    import MotionSubscriptionStatus from '$lib/components/MotionSubscriptionStatus.svelte';
    import PushIngestFields from '$lib/components/camera-form/PushIngestFields.svelte';
    import AdaptiveRecordingFields from '$lib/components/camera-form/AdaptiveRecordingFields.svelte';
    import CameraStorageSection from '$lib/components/camera-form/CameraStorageSection.svelte';
    import PushTargetList from '$lib/components/camera-form/PushTargetList.svelte';
    import TranscodingSection from '$lib/components/camera-form/TranscodingSection.svelte';
    import OnvifDeviceSection from '$lib/components/camera-form/OnvifDeviceSection.svelte';
    import { startBackfill, getUntranscodedRecordingCount } from '$lib/api/transcoding';
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
    /** Existing group labels for the group input's datalist suggestions (v36). */
    knownGroups?: string[];
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
    knownGroups = [],
  }: Props = $props();

  // Unique suffix for this form instance's datalist id — the Cameras page can
  // render more than one CameraForm (add + inline edit) and duplicate element
  // ids would cross-wire the group suggestions.
  const formUid = Math.random().toString(36).slice(2, 8);

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
  // Camera-management group label (v36). Empty = ungrouped.
  let formGroup = $state('');
  let formBrand = $state('');
  let formModel = $state('');
  let formSerialNumber = $state('');
  let formRetentionDays = $state(0);
  let formStreamEncoding = $state('');
  // Sub-stream (#512): manual sub RTSP URL + ONVIF sub profile token.
  let formSubStreamURL = $state('');
  let formSubProfileToken = $state('');
  let formChannel = $state('');
  let formAudioEnabled = $state(false);
  let formAudioInRecordings = $state(false);
  // Recording gate — when off, the recorder stays connected for live preview
  // and relay but writes NO segments to disk (live-only / stream-forward mode).
  let formRecordingEnabled = $state(true);
  let formCascadeEnabled = $state(true);
  let formCascadeSubStream = $state(false);
  // Recording mode (#435): continuous, or adaptive — dynamic timelapse that
  // drops to sparse keyframes while the compressed-domain activity signal
  // stays calm and returns to full recording on activity.
  let formRecordingMode = $state<'continuous' | 'adaptive'>('continuous');
  let formMotionSource = $state<'nvr' | 'camera:onvif'>('nvr');
  let formAdaptiveCalmThreshold = $state('');
  let formAdaptiveTimelapseInterval = $state('');
  let formAdaptiveSpikeFactor = $state('');
  let formAdaptiveGopBufferMB = $state('');
  let formAmbientAudio = $state(false);
  let formAmbientArchive = $state(false);
  let formTimelapseFrameMs = $state('');
  let formAdaptiveWasAmbient = false;
  // Resident-timelapse + noise floor (#635/#638).
  let formVideoExit = $state(true);
  let formAutoNoiseFloor = $state(true);
  let formNoiseFloorKB = $state('');
  // Recording tier (#637): tiered = continuous sub-stream channel.
  let formRecordingTier = $state('');
  // Pixel-domain fine gate (#636).
  let formPixgateEnabled = $state(false);
  let formPixgateFPS = $state('');
  let formPixgateMinArea = $state('');
  let formPixgateHold = $state('');
  let formPixgateWasEnabled = false;
  // Audio trigger (#478): loudness input on top of the adaptive gate. Only
  // meaningful (and only sent) for adaptive + G.711 cameras.
  let formAudioTriggerEnabled = $state(false);
  let formAudioMinDBFS = $state('');
  let formAudioPreCaptureS = $state('');
  let formAudioTriggerWasEnabled = false;

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
  // Vision instance routing: which analysis consumers receive this camera's
  // recordings. Empty selection = all enabled instances.
  let formVisionTargets = $state<string[]>([]);
  let visionInstanceOptions = $state<Array<{ name: string; enabled: boolean }>>([]);

  async function loadVisionInstanceOptions() {
    try {
      const settings = await getSettings();
      const instances = settings.vision?.instances;
      visionInstanceOptions = instances?.length
        ? instances.map((i) => ({ name: i.name, enabled: i.enabled ?? true }))
        // Legacy single-instance deployment: the implicit "default" instance.
        : [{ name: 'default', enabled: true }];
    } catch {
      visionInstanceOptions = [];
    }
  }

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

  // Auto-select encoding when protocol changes
  $effect(() => {
    const proto = protocolsMap.get(formProtocol);
    if (!proto) return;
    const encodings = proto.encodings;
    if (!encodings.includes(formEncoding)) {
      if (formProtocol === 'onvif' || formProtocol === 'xiaomi' || formProtocol === 'gb28181') {
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
    loadVisionInstanceOptions();
    if (editingCamera) {
      populateForm(editingCamera);
      loadMergeConfig(editingCamera.id);
    } else {
      resetFormFields();
      mergeConfig = null;
      mergeConfigLoading = false;
    }
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
    formGroup = '';
    formBrand = '';
    formModel = '';
    formSerialNumber = '';
    formRetentionDays = 0;
    formStreamEncoding = '';
    formSubStreamURL = '';
    formSubProfileToken = '';
    formTranscodingEnabled = false;
    formTranscodingCodec = 'h264';
    formTranscodingPreset = 'ultrafast';
    formTranscodingBitrate = '2M';
    formTranscodingCRF = 0;
    validationErrors = {};
    formChannel = '';
    formAudioEnabled = false;
    formAudioInRecordings = false;
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
    formVisionTargets = [];
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
    formGroup = camera.group || '';
    formBrand = camera.brand || '';
    formModel = camera.model || '';
    formSerialNumber = camera.serial_number || '';
    formRetentionDays = camera.retention_days || 0;
    formStreamEncoding = camera.stream_encoding || '';
    formSubStreamURL = camera.sub_stream_url || '';
    formSubProfileToken = camera.sub_profile_token || '';
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
    formAudioInRecordings = camera.audio_in_recordings ?? false;
    formRecordingEnabled = camera.recording_enabled ?? true;
    formCascadeEnabled = camera.cascade_enabled ?? true;
    formCascadeSubStream = camera.cascade_sub_stream ?? false;
    formRecordingMode = camera.recording_mode === 'adaptive' ? 'adaptive' : 'continuous';
    formMotionSource = camera.motion_source === 'camera:onvif' ? 'camera:onvif' : 'nvr';
    formAdaptiveCalmThreshold = camera.adaptive?.calm_threshold ?? '';
    formAdaptiveTimelapseInterval = camera.adaptive?.timelapse_interval ?? '';
    formAdaptiveSpikeFactor = camera.adaptive?.spike_factor ? String(camera.adaptive.spike_factor) : '';
    formAdaptiveGopBufferMB = camera.adaptive?.gop_buffer_bytes
      ? String(Math.round(camera.adaptive.gop_buffer_bytes / (1024 * 1024)))
      : '';
    formAmbientAudio = camera.adaptive?.ambient_audio ?? false;
    formAmbientArchive = camera.adaptive?.ambient_archive ?? false;
    formTimelapseFrameMs = camera.adaptive?.timelapse_frame_ms ? String(camera.adaptive.timelapse_frame_ms) : '';
    formVideoExit = camera.adaptive?.video_exit ?? true;
    formAutoNoiseFloor = camera.adaptive?.auto_noise_floor ?? true;
    formNoiseFloorKB =
      camera.adaptive?.noise_floor_bytes && camera.adaptive.noise_floor_bytes > 0
        ? String(Math.round(camera.adaptive.noise_floor_bytes / 1024))
        : '';
    formPixgateEnabled = camera.pixgate?.enabled ?? false;
    formPixgateFPS = camera.pixgate?.sample_fps ? String(camera.pixgate.sample_fps) : '';
    formPixgateMinArea = camera.pixgate?.min_area_pct ? String(camera.pixgate.min_area_pct) : '';
    formPixgateHold = camera.pixgate?.hold ?? '';
    formPixgateWasEnabled = formPixgateEnabled;
    formRecordingTier = camera.recording_tier ?? '';
    formAdaptiveWasAmbient = formAmbientAudio;
    formAudioTriggerEnabled = camera.audio_trigger?.enabled ?? false;
    formAudioTriggerWasEnabled = formAudioTriggerEnabled;
    formAudioMinDBFS =
      typeof camera.audio_trigger?.min_dbfs === 'number' ? String(camera.audio_trigger.min_dbfs) : '';
    formAudioPreCaptureS =
      typeof camera.audio_trigger?.pre_capture_s === 'number' ? String(camera.audio_trigger.pre_capture_s) : '';
    formTwoWayAudioEnabled = camera.two_way_audio_enabled ?? false;
    formSubnetHints = (camera.subnet_hints ?? []).join('\n');
    formStreamKey = camera.stream_key || '';
    formSRTPassphrase = camera.srt_passphrase || '';
    formSRTStreamID = camera.srt_stream_id || '';
    formGB28181DeviceID = camera.gb28181?.device_id || '';
    formGB28181ChannelID = camera.gb28181?.channel_id || '';
    formPushRetentionDays = camera.push_retention_days ?? null;
    formPushTargets = (camera.push_targets ?? []).map((p) => ({ ...p }));
    formVisionTargets = camera.vision_targets ? [...camera.vision_targets] : [];
  }

  function toggleVisionTarget(name: string, checked: boolean) {
    formVisionTargets = checked
      ? [...new Set([...formVisionTargets, name])]
      : formVisionTargets.filter((n) => n !== name);
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
    if (formRecordingMode !== 'adaptive') {
        // Leaving adaptive: explicitly clear a previously-armed ambient flag
        // (nil = unchanged server-side, which could leave it stale).
        return formAdaptiveWasAmbient ? { ambient_audio: false } : undefined;
    }
    const p: AdaptiveRecordingConfig = {};
    p.ambient_audio = formAmbientAudio;
    if (formAdaptiveCalmThreshold.trim()) p.calm_threshold = formAdaptiveCalmThreshold.trim();
    if (formAdaptiveTimelapseInterval.trim()) p.timelapse_interval = formAdaptiveTimelapseInterval.trim();
    const spike = parseFloat(formAdaptiveSpikeFactor);
    if (!Number.isNaN(spike) && spike > 0) p.spike_factor = spike;
    const mb = parseInt(formAdaptiveGopBufferMB, 10);
    if (!Number.isNaN(mb) && mb > 0) p.gop_buffer_bytes = mb * 1024 * 1024;
    p.ambient_audio = formAmbientAudio;
    p.ambient_archive = formAmbientAudio && formAmbientArchive;
    const fms = parseInt(formTimelapseFrameMs, 10);
    if (!Number.isNaN(fms) && fms > 0) p.timelapse_frame_ms = fms;
    // #638 resident timelapse / #635 noise floor: sent explicitly so the
    // saved state always matches the form (nil = server default would leave
    // a previously-disabled video_exit sticky after the user re-enables it).
    p.video_exit = formVideoExit;
    p.auto_noise_floor = formAutoNoiseFloor;
    const nkb = parseFloat(formNoiseFloorKB);
    if (!Number.isNaN(nkb) && nkb > 0) p.noise_floor_bytes = Math.round(nkb * 1024);
    return p;
}

// Audio-trigger payload (#478): explicitly {enabled:false} when the mode left
// adaptive or the checkbox is off (nil = unchanged server-side, which could
// leave a stale armed config behind).
function buildAudioTriggerPayload(): CameraAudioTriggerConfig | undefined {
    if (formRecordingMode !== 'adaptive') {
        return formAudioTriggerWasEnabled ? { enabled: false } : undefined;
    }
    if (!formAudioTriggerEnabled) return { enabled: false };
    const p: CameraAudioTriggerConfig = { enabled: true };
    const dbfs = parseFloat(formAudioMinDBFS);
    if (!Number.isNaN(dbfs) && dbfs < 0) p.min_dbfs = dbfs;
    const pcs = parseInt(formAudioPreCaptureS, 10);
    if (!Number.isNaN(pcs) && pcs > 0) p.pre_capture_s = pcs;
    return p;
}

// Pixgate payload (#636): same leaving-adaptive clearing semantics as the
// audio trigger. Applies on NVR restart (documented in the hint).
function buildPixgatePayload(): CameraPixgateConfig | undefined {
    if (formRecordingMode !== 'adaptive') {
        return formPixgateWasEnabled ? { enabled: false } : undefined;
    }
    if (!formPixgateEnabled) return { enabled: false };
    const p: CameraPixgateConfig = { enabled: true };
    const fps = parseFloat(formPixgateFPS);
    if (!Number.isNaN(fps) && fps > 0) p.sample_fps = fps;
    const area = parseFloat(formPixgateMinArea);
    if (!Number.isNaN(area) && area > 0) p.min_area_pct = area;
    if (formPixgateHold.trim()) p.hold = formPixgateHold.trim();
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
            group: formGroup.trim(),
            brand: formBrand || undefined,
            model: formModel || undefined,
            serial_number: formSerialNumber || undefined,
            retention_days: formRetentionDays,
            stream_encoding: formProtocol === 'onvif' ? (formStreamEncoding || undefined) : undefined,
            sub_stream_url: formProtocol === 'onvif' || formProtocol === 'rtsp' ? (formSubStreamURL.trim() || '') : undefined,
            sub_profile_token: formProtocol === 'onvif' ? (formSubProfileToken.trim() || '') : undefined,
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
            audio_in_recordings: formAudioInRecordings,
            recording_enabled: formRecordingEnabled,
            cascade_enabled: formCascadeEnabled,
            cascade_sub_stream: formCascadeSubStream,
            recording_mode: formRecordingMode,
            motion_source: formProtocol === 'onvif' ? formMotionSource : undefined,
            recording_tier: formRecordingTier,
            adaptive: buildAdaptivePayload(),
            audio_trigger: buildAudioTriggerPayload(),
            pixgate: buildPixgatePayload(),
            two_way_audio_enabled: formProtocol === 'xiaomi' ? formTwoWayAudioEnabled : undefined,
            subnet_hints: formProtocol === 'onvif' ? parseSubnetHints(formSubnetHints) : undefined,
            stream_key: (formProtocol === 'rtmp' || formProtocol === 'whip') ? (formStreamKey || undefined) : undefined,
            srt_passphrase: formProtocol === 'srt' ? (formSRTPassphrase || undefined) : undefined,
            srt_stream_id: formProtocol === 'srt' ? (formSRTStreamID || undefined) : undefined,
            gb28181: formProtocol === 'gb28181' ? { device_id: formGB28181DeviceID, channel_id: formGB28181ChannelID } : undefined,
            push_targets: formPushTargets.length > 0 ? formPushTargets : [],
            vision_targets: formVisionTargets,
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
            group: formGroup.trim() || undefined,
            brand: formBrand || undefined,
            model: formModel || undefined,
            serial_number: formSerialNumber || undefined,
            retention_days: formRetentionDays,
            stream_encoding: formProtocol === 'onvif' ? (formStreamEncoding || undefined) : undefined,
            sub_stream_url: formProtocol === 'onvif' || formProtocol === 'rtsp' ? (formSubStreamURL.trim() || '') : undefined,
            sub_profile_token: formProtocol === 'onvif' ? (formSubProfileToken.trim() || '') : undefined,
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
            audio_in_recordings: formAudioInRecordings,
            recording_enabled: formRecordingEnabled,
            cascade_enabled: formCascadeEnabled,
            cascade_sub_stream: formCascadeSubStream,
            recording_mode: formRecordingMode,
            motion_source: formProtocol === 'onvif' ? formMotionSource : undefined,
            recording_tier: formRecordingTier,
            adaptive: buildAdaptivePayload(),
            audio_trigger: buildAudioTriggerPayload(),
            pixgate: buildPixgatePayload(),
            two_way_audio_enabled: formProtocol === 'xiaomi' ? formTwoWayAudioEnabled : undefined,
            subnet_hints: formProtocol === 'onvif' ? parseSubnetHints(formSubnetHints) : undefined,
            stream_key: (formProtocol === 'rtmp' || formProtocol === 'whip') ? (formStreamKey || undefined) : undefined,
            srt_passphrase: formProtocol === 'srt' ? (formSRTPassphrase || undefined) : undefined,
            srt_stream_id: formProtocol === 'srt' ? (formSRTStreamID || undefined) : undefined,
            gb28181: formProtocol === 'gb28181' ? { device_id: formGB28181DeviceID, channel_id: formGB28181ChannelID } : undefined,
            push_targets: formPushTargets.length > 0 ? formPushTargets : undefined,
            vision_targets: formVisionTargets,
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

    <!-- Group (v37 camera-management grouping) — top-level, not buried in
         advanced settings: it drives the management page's sections. The
         datalist suggests existing groups. -->
    <div>
      <label for="cam-group" class="input-label">{t('cameras.group')}</label>
      <input id="cam-group" type="text" class="input" list="{formUid}-group-options"
        bind:value={formGroup} placeholder={t('cameras.groupPlaceholder')} />
      <datalist id="{formUid}-group-options">
        {#each knownGroups as g (g)}
          <option value={g}></option>
        {/each}
      </datalist>
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

    <PushIngestFields {formProtocol} {validationErrors} {editingCamera}
      bind:formGB28181DeviceID bind:formGB28181ChannelID bind:formStreamKey
      bind:formSRTPassphrase bind:formSRTStreamID bind:formPushRetentionDays />

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
      {#if formProtocol === 'onvif'}
        <!-- Motion source (#711): NVR-side detectors or the camera's own
             Pull-Point MotionAlarm subscription (mibee_cam WiFi-CSI). -->
        <div>
          <label for="cam-motion-source" class="input-label">{t('cameras.motionSource')}</label>
          <select id="cam-motion-source" class="input" bind:value={formMotionSource}>
            <option value="nvr">{t('cameras.motionSourceNvr')}</option>
            <option value="camera:onvif">{t('cameras.motionSourceCamera')}</option>
          </select>
          <p class="text-xs th-text-muted mt-1">
            {formMotionSource === 'camera:onvif' ? t('cameras.motionSourceCameraHint') : t('cameras.motionSourceNvrHint')}
          </p>
          {#if formMotionSource === 'camera:onvif' && editingCamera}
            <MotionSubscriptionStatus cameraId={editingCamera.id} />
          {/if}
        </div>
      {/if}
    {#if !formRecordingEnabled}
      <p class="text-xs th-text-muted -mt-1">{t('cameras.recordingDisabledHint')}</p>
    {:else if formEncoding === 'h264' || formEncoding === 'h265'}
      <AdaptiveRecordingFields {formProtocol}
        bind:formRecordingMode bind:formRecordingTier bind:formAdaptiveCalmThreshold
        bind:formAdaptiveTimelapseInterval bind:formAdaptiveSpikeFactor bind:formAdaptiveGopBufferMB
        bind:formNoiseFloorKB bind:formAutoNoiseFloor bind:formVideoExit bind:formTimelapseFrameMs
        bind:formAudioTriggerEnabled bind:formAudioMinDBFS bind:formAudioPreCaptureS
        bind:formPixgateEnabled bind:formPixgateFPS bind:formPixgateMinArea bind:formPixgateHold
        bind:formAmbientAudio bind:formAmbientArchive />
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
    {:else}
      <div class="flex items-center gap-2 ml-5">
        <input
          id="cam-cascade-sub"
          type="checkbox"
          class="checkbox"
          bind:checked={formCascadeSubStream}
        />
        <label for="cam-cascade-sub" class="input-label cursor-pointer text-sm">
          {t('cameras.cascadeSubStream')}
        </label>
      </div>
      {#if formCascadeSubStream}
        <p class="text-xs th-text-muted -mt-1 ml-5">{t('cameras.cascadeSubStreamHint')}</p>
      {/if}
    {/if}

    {#if editingCamera}
      <CameraStorageSection camera={editingCamera} />
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
      {#if formAudioEnabled}
        <div class="flex items-center gap-2 -mt-1">
          <input
            id="cam-audio-in-recordings"
            type="checkbox"
            class="checkbox"
            bind:checked={formAudioInRecordings}
          />
          <label for="cam-audio-in-recordings" class="input-label cursor-pointer">
            {t('cameras.audioInRecordings')}
          </label>
        </div>
        <p class="text-xs th-text-muted -mt-1">{t('cameras.audioInRecordingsHint')}</p>
      {/if}
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

    <!-- Sub-stream (#512): secondary low-res feed for future consumers -->
    {#if formProtocol === 'onvif' || formProtocol === 'rtsp'}
    <div class="md:col-span-2">
      <details class="rounded-md border th-border">
        <summary class="cursor-pointer p-3 flex items-center gap-2 th-bg-hover">
          <Layers size={16} class="th-text-secondary" />
          <span class="font-medium th-text-primary">{t('cameras.subStreamTitle')}</span>
          {#if formSubStreamURL.trim() || formSubProfileToken.trim()}
            <span class="text-xs px-2 py-0.5 rounded-full th-bg-muted th-text-secondary">✓</span>
          {/if}
        </summary>
        <div class="p-3 border-t th-border space-y-3">
          <p class="text-xs th-text-muted">{t('cameras.subStreamHint')}</p>
          <div>
            <label class="label" for="cam-sub-url">
              <span class="text-xs th-text-secondary">{t('cameras.subStreamUrl')}</span>
            </label>
            <input id="cam-sub-url" type="text" class="input" bind:value={formSubStreamURL}
              placeholder={t('cameras.subStreamUrlPlaceholder')} />
          </div>
          {#if formProtocol === 'onvif'}
          <div>
            <label class="label" for="cam-sub-token">
              <span class="text-xs th-text-secondary">{t('cameras.subProfileToken')}</span>
            </label>
            <input id="cam-sub-token" type="text" class="input" bind:value={formSubProfileToken}
              placeholder={t('cameras.subProfileTokenPlaceholder')} />
            <p class="text-xs th-text-muted mt-1">{t('cameras.subProfileTokenHint')}</p>
          </div>
          {/if}
        </div>
      </details>
    </div>
    {/if}

    <!-- AI analysis routing: which vision consumer instances receive this
         camera's recordings. No selection = all enabled instances. -->
    {#if visionInstanceOptions.length > 0}
    <div class="md:col-span-2">
      <details class="rounded-md border th-border">
        <summary class="cursor-pointer p-3 flex items-center gap-2 th-bg-hover">
          <Brain size={16} class="th-text-secondary" />
          <span class="font-medium th-text-primary">{t('cameras.visionRoutingTitle')}</span>
          {#if formVisionTargets.length > 0}
            <span class="text-xs px-2 py-0.5 rounded-full th-bg-muted th-text-secondary">{formVisionTargets.length}</span>
          {:else}
            <span class="text-xs px-2 py-0.5 rounded-full th-bg-muted th-text-secondary">{t('cameras.visionRoutingAll')}</span>
          {/if}
        </summary>
        <div class="p-3 border-t th-border space-y-2">
          <p class="text-xs th-text-muted">{t('cameras.visionRoutingHint')}</p>
          {#each visionInstanceOptions as ins (ins.name)}
            <label class="flex items-center gap-2 text-sm th-text-primary cursor-pointer" data-testid="vision-target-{ins.name}">
              <input
                type="checkbox"
                class="checkbox"
                checked={formVisionTargets.includes(ins.name)}
                onchange={(e) => toggleVisionTarget(ins.name, (e.currentTarget as HTMLInputElement).checked)}
              />
              <span>{ins.name}</span>
              {#if !ins.enabled}
                <span class="badge badge-warning ml-1">{t('cameras.visionRoutingDisabled')}</span>
              {/if}
            </label>
          {/each}
        </div>
      </details>
    </div>
    {/if}

    <PushTargetList bind:formPushTargets {formEncoding} {validationErrors} {editingCamera} />

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
    <TranscodingSection
      bind:formTranscodingEnabled bind:formTranscodingCodec bind:formTranscodingPreset
      bind:formTranscodingBitrate bind:formTranscodingCRF
      {globalTranscodingEnabled} {h265Available} />
  {/if}

  <!-- Timelapse Config (edit mode only) -->
  {#if editingCamera}
    <TimelapseConfigEditor cameraId={editingCamera.id} />
  {/if}

  <!-- ONVIF Device Settings (edit mode only, ONVIF cameras) -->
  {#if editingCamera}
    <OnvifDeviceSection camera={editingCamera} bind:formSubnetHints />
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
