<script lang="ts">
    import { t } from '$lib/i18n';

  interface Props {
    /** Adaptive-recording fields — owned by the parent form (payload builders read them). */
    formRecordingMode?: 'continuous' | 'adaptive';
    formRecordingTier?: string;
    formAdaptiveCalmThreshold?: string;
    formAdaptiveTimelapseInterval?: string;
    formAdaptiveSpikeFactor?: string;
    formAdaptiveGopBufferMB?: string;
    formNoiseFloorKB?: string;
    formAutoNoiseFloor?: boolean;
    formVideoExit?: boolean;
    formTimelapseFrameMs?: string;
    formAudioTriggerEnabled?: boolean;
    formAudioMinDBFS?: string;
    formAudioPreCaptureS?: string;
    formPixgateEnabled?: boolean;
    formPixgateFPS?: string;
    formPixgateMinArea?: string;
    formPixgateHold?: string;
    formAmbientAudio?: boolean;
    formAmbientArchive?: boolean;
    formProtocol: string;
  }

  let {
    formRecordingMode = $bindable('continuous'),
    formRecordingTier = $bindable(''),
    formAdaptiveCalmThreshold = $bindable(''),
    formAdaptiveTimelapseInterval = $bindable(''),
    formAdaptiveSpikeFactor = $bindable(''),
    formAdaptiveGopBufferMB = $bindable(''),
    formNoiseFloorKB = $bindable(''),
    formAutoNoiseFloor = $bindable(true),
    formVideoExit = $bindable(true),
    formTimelapseFrameMs = $bindable(''),
    formAudioTriggerEnabled = $bindable(false),
    formAudioMinDBFS = $bindable(''),
    formAudioPreCaptureS = $bindable(''),
    formPixgateEnabled = $bindable(false),
    formPixgateFPS = $bindable(''),
    formPixgateMinArea = $bindable(''),
    formPixgateHold = $bindable(''),
    formAmbientAudio = $bindable(false),
    formAmbientArchive = $bindable(false),
    formProtocol,
  }: Props = $props();
</script>

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
{#if formProtocol !== 'srt' && formProtocol !== 'rtmp' && formProtocol !== 'whip'}
  <!-- Recording tier (#637): tiered adds a continuous sub-stream channel -->
  <div>
    <label for="cam-recording-tier" class="input-label">{t('cameras.recordingTier')}</label>
    <select id="cam-recording-tier" class="input" bind:value={formRecordingTier}>
      <option value="">{t('cameras.recordingTierSingle')}</option>
      <option value="tiered">{t('cameras.recordingTierTiered')}</option>
    </select>
    <p class="text-xs th-text-muted mt-1">{t('cameras.recordingTierHint')}</p>
  </div>
{/if}
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
      <input id="cam-adaptive-spike" class="input" type="number" step="0.1" min="1.5" max="20" placeholder="5.0" bind:value={formAdaptiveSpikeFactor} />
    </div>
    <div>
      <label for="cam-adaptive-gop" class="input-label">{t('cameras.adaptiveGopBufferMB')}</label>
      <input id="cam-adaptive-gop" class="input" type="number" step="1" min="1" max="64" placeholder="16" bind:value={formAdaptiveGopBufferMB} />
    </div>
    <div>
      <label for="cam-adaptive-noisefloor" class="input-label">{t('cameras.adaptiveNoiseFloorKB')}</label>
      <input id="cam-adaptive-noisefloor" class="input" type="number" step="0.5" min="0" placeholder="0" bind:value={formNoiseFloorKB} />
    </div>
    <div class="flex items-end gap-2 pb-1">
      <input id="cam-adaptive-autonoise" type="checkbox" class="checkbox" bind:checked={formAutoNoiseFloor} />
      <label for="cam-adaptive-autonoise" class="input-label cursor-pointer">{t('cameras.adaptiveAutoNoiseFloor')}</label>
    </div>
  </div>
  <div class="flex items-start gap-2">
    <input
      id="cam-adaptive-videoexit"
      type="checkbox"
      class="checkbox mt-0.5"
      bind:checked={formVideoExit}
    />
    <label for="cam-adaptive-videoexit" class="input-label cursor-pointer">
      {t('cameras.adaptiveVideoExit')}
      <span class="block text-xs th-text-muted font-normal">{t('cameras.adaptiveVideoExitHint')}</span>
    </label>
  </div>
  <div>
    <label for="cam-adaptive-framems" class="input-label">{t('cameras.timelapseFrameMs')}</label>
    <select id="cam-adaptive-framems" class="input" bind:value={formTimelapseFrameMs}>
      <option value="">{t('cameras.timelapseFrameMsDefault')}</option>
      <option value="100">0.1s</option>
      <option value="300">0.3s</option>
      <option value="500">0.5s</option>
    </select>
  </div>
  <p class="text-xs th-text-muted -mt-1">{t('cameras.adaptiveParamsHint')}</p>
  <div class="flex items-center gap-2">
    <input
      id="cam-audio-trigger"
      type="checkbox"
      class="checkbox"
      bind:checked={formAudioTriggerEnabled}
    />
    <label for="cam-audio-trigger" class="input-label cursor-pointer">
      {t('cameras.audioTrigger')}
    </label>
  </div>
  {#if formAudioTriggerEnabled}
    <div class="grid grid-cols-2 gap-3">
      <div>
        <label for="cam-audio-dbfs" class="input-label">{t('cameras.audioTriggerMinDBFS')}</label>
        <input id="cam-audio-dbfs" class="input" type="number" step="1" min="-90" max="0" placeholder="-45" bind:value={formAudioMinDBFS} />
      </div>
      <div>
        <label for="cam-audio-precap" class="input-label">{t('cameras.audioTriggerPreCapture')}</label>
        <input id="cam-audio-precap" class="input" type="number" step="1" min="0" max="30" placeholder="3" bind:value={formAudioPreCaptureS} />
      </div>
    </div>
  {/if}
  <p class="text-xs th-text-muted -mt-1">{t('cameras.audioTriggerHint')}</p>
  <div class="flex items-start gap-2">
    <input
      id="cam-pixgate"
      type="checkbox"
      class="checkbox mt-0.5"
      bind:checked={formPixgateEnabled}
    />
    <label for="cam-pixgate" class="input-label cursor-pointer">
      {t('cameras.pixgate')}
      <span class="block text-xs th-text-muted font-normal">{t('cameras.pixgateHint')}</span>
    </label>
  </div>
  {#if formPixgateEnabled}
    <div class="grid grid-cols-3 gap-3">
      <div>
        <label for="cam-pixgate-fps" class="input-label">{t('cameras.pixgateFPS')}</label>
        <input id="cam-pixgate-fps" class="input" type="number" step="0.1" min="0.2" max="2" placeholder="1" bind:value={formPixgateFPS} />
      </div>
      <div>
        <label for="cam-pixgate-area" class="input-label">{t('cameras.pixgateMinArea')}</label>
        <input id="cam-pixgate-area" class="input" type="number" step="0.1" min="0.1" max="50" placeholder="1.5" bind:value={formPixgateMinArea} />
      </div>
      <div>
        <label for="cam-pixgate-hold" class="input-label">{t('cameras.pixgateHold')}</label>
        <input id="cam-pixgate-hold" class="input" type="text" placeholder="30s" bind:value={formPixgateHold} />
      </div>
    </div>
    <p class="text-xs th-text-muted -mt-1">{t('cameras.pixgateApplyHint')}</p>
  {/if}
  <div class="flex items-center gap-2">
    <input
      id="cam-ambient-audio"
      type="checkbox"
      class="checkbox"
      bind:checked={formAmbientAudio}
    />
    <label for="cam-ambient-audio" class="input-label cursor-pointer">
      {t('cameras.ambientAudio')}
    </label>
  </div>
  {#if formAmbientAudio}
    <p class="text-xs th-text-muted -mt-1">{t('cameras.ambientAudioHint')}</p>
    <div class="flex items-center gap-2">
      <input
        id="cam-ambient-archive"
        type="checkbox"
        class="checkbox"
        bind:checked={formAmbientArchive}
      />
      <label for="cam-ambient-archive" class="input-label cursor-pointer">
        {t('cameras.ambientArchive')}
      </label>
    </div>
    <p class="text-xs th-text-muted -mt-1">{t('cameras.ambientArchiveHint')}</p>
  {/if}
{/if}
