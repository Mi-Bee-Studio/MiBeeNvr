<script lang="ts">
  import { t } from '$lib/i18n';
  import type { PushTargetStatus as PushTargetStatusType } from '$lib/api';
  import { Thermometer, AlertTriangle, Radio, Disc3 } from 'lucide-svelte';

  interface Props {
    status: PushTargetStatusType;
  }

  let { status }: Props = $props();

  let isStreaming = $derived(status.status === 'streaming');
  let isOverheating = $derived(status.temperature_c != null && status.temperature_c > 70);
  let hasDrift = $derived(status.av_drift_ms != null && status.av_drift_ms > 500);
  let hasTranscode = $derived(status.transcode_status != null && status.transcode_status !== '');
  let hasAudio = $derived(status.audio_codec != null && status.audio_codec !== '');

  let transcodeLabel = $derived(buildTranscodeLabel());
  let audioLabel = $derived(getAudioCodecLabel(status.audio_codec));
  let driftLabel = $derived(hasDrift ? t('cameras.pushStatus.avDrift', { ms: String(status.av_drift_ms) }) : '');
  let thermalLabel = $derived(t('cameras.pushStatus.overheating'));

  function buildTranscodeLabel(): string {
    if (!status.transcode_status) return '';
    const resolution = status.transcode_resolution || '';
    const mode = status.transcode_status;
    let label = t('cameras.pushStatus.transcoding');
    if (resolution) label += ' ' + resolution;
    if (mode === 'transcoding') {
      label += ' ' + t('cameras.pushTranscodeHW');
    } else if (mode === 'throttled') {
      label += ' ' + t('cameras.pushTranscodeSW') + ' ' + t('cameras.pushTranscodeThrottled');
    } else if (mode) {
      label += ' ' + mode;
    }
    return label;
  }

  function getAudioCodecLabel(codec?: string): string {
    if (!codec) return '';
    const labels: Record<string, string> = {
      'AAC': t('cameras.pushAudioAAC'),
      'G.711 μ-law': t('cameras.pushAudioG711Mu'),
      'G.711 a-law': t('cameras.pushAudioG711A'),
      'G.711': t('cameras.pushAudioG711'),
      'Silent AAC': t('cameras.pushAudioSilent'),
    };
    return labels[codec] || codec;
  }
</script>

<div class="flex flex-wrap items-center gap-1.5">
  <!-- Primary status badge -->
  <span
    class="text-xs px-2 py-0.5 rounded-full {isStreaming
      ? 'th-bg-success-light th-color-success'
      : status.status === 'error'
        ? 'th-bg-danger-light th-color-danger'
        : 'th-bg-muted th-text-secondary'}"
    title={status.error || ''}
  >
    {isStreaming
      ? `● ${Math.round(status.kbps)} kbps`
      : t('cameras.pushStatus.' + status.status)}
  </span>

  <!-- Uptime / Duration (streaming only) -->
  {#if isStreaming && status.uptime}
    <span class="text-xs th-text-muted whitespace-nowrap">
      {status.uptime}
    </span>
  {/if}

  <!-- Transcode status badge -->
  {#if hasTranscode}
    <span class="text-xs px-2 py-0.5 rounded-full flex items-center gap-1"
      style="background-color: rgba(56, 184, 248, 0.15); color: var(--color-accent);">
      <Radio size={10} />
      {transcodeLabel}
    </span>
  {/if}

  <!-- Audio codec indicator -->
  {#if hasAudio}
    <span class="text-xs px-2 py-0.5 rounded-full flex items-center gap-1"
      style="background-color: rgba(59, 130, 246, 0.15); color: var(--color-info);">
      <Disc3 size={10} />
      {audioLabel}
    </span>
  {/if}

  <!-- Thermal warning (temperature > 70°C) -->
  {#if isOverheating}
    <span
      class="text-xs px-2 py-0.5 rounded-full flex items-center gap-1"
      style="background-color: rgba(245, 158, 11, 0.15); color: var(--color-warning);"
      title={status.temperature_c != null ? `${status.temperature_c}°C` : undefined}
    >
      <Thermometer size={10} />
      {thermalLabel}
    </span>
  {/if}

  <!-- A/V drift warning (drift > 500ms) -->
  {#if hasDrift}
    <span
      class="text-xs px-2 py-0.5 rounded-full flex items-center gap-1"
      style="background-color: rgba(245, 158, 11, 0.15); color: var(--color-warning);"
      title={driftLabel}
    >
      <AlertTriangle size={10} />
      {driftLabel}
    </span>
  {/if}
</div>
