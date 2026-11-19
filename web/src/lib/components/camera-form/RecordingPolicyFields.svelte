<script lang="ts">
  /**
   * Recording and cascade-policy section of the camera form.
   *
   * Extracted from CameraForm.svelte: the recording on/off toggle, the ONVIF
   * motion-source picker, the adaptive-recording field group, and the GB28181
   * cascade exposure / sub-stream forwarding toggles.
   *
   * All state is bound through so the parent stays the single owner of the form
   * model; this component renders and forwards edits only.
   */
  import { t } from '$lib/i18n';
  import type { Camera } from '$lib/api';
  import AdaptiveRecordingFields from './AdaptiveRecordingFields.svelte';
  import MotionSubscriptionStatus from '$lib/components/MotionSubscriptionStatus.svelte';

  let {
    formProtocol = 'rtsp',
    formEncoding = 'h264',
    formRecordingEnabled = $bindable(false),
    formMotionSource = $bindable('nvr'),
    formRecordingMode = $bindable('continuous'),
    formRecordingTier = $bindable(),
    formAdaptiveCalmThreshold = $bindable(),
    formAdaptiveTimelapseInterval = $bindable(),
    formAdaptiveSpikeFactor = $bindable(),
    formAdaptiveGopBufferMB = $bindable(),
    formNoiseFloorKB = $bindable(),
    formAutoNoiseFloor = $bindable(),
    formVideoExit = $bindable(),
    formTimelapseFrameMs = $bindable(),
    formAudioTriggerEnabled = $bindable(),
    formAudioMinDBFS = $bindable(),
    formAudioPreCaptureS = $bindable(),
    formPixgateEnabled = $bindable(),
    formPixgateFPS = $bindable(),
    formPixgateMinArea = $bindable(),
    formPixgateHold = $bindable(),
    formAmbientAudio = $bindable(),
    formAmbientArchive = $bindable(),
    formCascadeEnabled = $bindable(false),
    formCascadeSubStream = $bindable(false),
    formImagingType = 'visible',
    isAudioSource = false,
    editingCamera = null,
    onCascadeTouched = () => {},
  }: {
    formProtocol?: string;
    formEncoding?: string;
    formRecordingEnabled: boolean;
    formMotionSource: string;
    formRecordingMode: string;
    formRecordingTier?: string;
    formAdaptiveCalmThreshold?: number;
    formAdaptiveTimelapseInterval?: number;
    formAdaptiveSpikeFactor?: number;
    formAdaptiveGopBufferMB?: number;
    formNoiseFloorKB?: number;
    formAutoNoiseFloor?: boolean;
    formVideoExit?: boolean;
    formTimelapseFrameMs?: number;
    formAudioTriggerEnabled?: boolean;
    formAudioMinDBFS?: number;
    formAudioPreCaptureS?: number;
    formPixgateEnabled?: boolean;
    formPixgateFPS?: number;
    formPixgateMinArea?: number;
    formPixgateHold?: number;
    formAmbientAudio?: boolean;
    formAmbientArchive?: boolean;
    formCascadeEnabled: boolean;
    formCascadeSubStream: boolean;
    formImagingType?: string;
    isAudioSource?: boolean;
    editingCamera?: Camera | null;
    onCascadeTouched?: () => void;
  } = $props();
</script>

<!-- Recording toggle: when off, the camera is live-only (no segments on disk) -->
<div class="flex items-center gap-2">
  <input id="cam-recording" type="checkbox" class="checkbox" bind:checked={formRecordingEnabled} />
  <label for="cam-recording" class="input-label cursor-pointer">
    {t('cameras.recordingEnabled')}
  </label>
</div>
{#if formProtocol === 'onvif'}
  <!-- Motion source (#711): NVR-side detectors or the camera's own Pull-Point
       MotionAlarm subscription (mibee_cam WiFi-CSI). -->
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
  {#if formImagingType === 'thermal' && formRecordingMode === 'adaptive'}
    <p class="text-xs th-color-warning -mt-1">{t('cameras.thermalAdaptiveWarn')}</p>
  {/if}
{/if}

<!-- Cascade catalog toggle: when off, the camera is hidden from the GB28181
     cascade upper platform (catalog + INVITE). -->
<div class="flex items-center gap-2">
  <input
    id="cam-cascade"
    type="checkbox"
    class="checkbox"
    bind:checked={formCascadeEnabled}
    onchange={onCascadeTouched}
  />
  <label for="cam-cascade" class="input-label cursor-pointer">
    {t('cameras.cascadeEnabled')}
  </label>
</div>
{#if !formCascadeEnabled}
  <p class="text-xs th-text-muted -mt-1">{t('cameras.cascadeDisabledHint')}</p>
{:else if formProtocol !== 'gb28181' && !isAudioSource}
  <p class="text-xs th-text-muted -mt-1">{t('cameras.cascadeNonGBHint')}</p>
{/if}
{#if formCascadeEnabled && !isAudioSource}
  <div class="flex items-center gap-2 ml-5">
    <input id="cam-cascade-sub" type="checkbox" class="checkbox" bind:checked={formCascadeSubStream} />
    <label for="cam-cascade-sub" class="input-label cursor-pointer text-sm">
      {t('cameras.cascadeSubStream')}
    </label>
  </div>
  {#if formCascadeSubStream}
    <p class="text-xs th-text-muted -mt-1 ml-5">{t('cameras.cascadeSubStreamHint')}</p>
  {/if}
{/if}
