<script lang="ts">
  // Audio-only recording player (v43): try the server-side G.711→WAV transcode
  // first; AAC/Opus recordings get 406 from it and fall back to the raw MP4
  // download URL (browsers play AAC-in-MP4 in <audio> natively). Extracted
  // verbatim from PlaybackPanel.svelte's audio mode.
  import { t } from '$lib/i18n';
  import { appendAuthToken, API_BASE } from '$lib/api';
  import AudioWaveform from '$lib/components/AudioWaveform.svelte';

  interface Props {
    recordingId: string;
  }

  let { recordingId }: Props = $props();

  let audioRecEl = $state<HTMLAudioElement | undefined>(undefined);
</script>

<div class="flex flex-col items-center justify-center gap-4 bg-black py-16 px-4">
  <div class="text-3xl th-text-tertiary">🎙️</div>
  <audio
    bind:this={audioRecEl}
    controls
    preload="metadata"
    src={appendAuthToken(`${API_BASE}/recordings/${recordingId}/audio.wav`)}
    onerror={(e) => {
      const el = e.currentTarget;
      if (!el.src.includes('/download')) {
        el.src = appendAuthToken(`${API_BASE}/recordings/${recordingId}/download`);
        el.load();
      }
    }}
    class="w-full max-w-xl"
  ></audio>
  <div class="w-full max-w-xl">
    <AudioWaveform audio={audioRecEl} label={t('common.audioWaveform')} />
  </div>
  <p class="text-xs th-text-muted">{t('detail.audioRecordingHint')}</p>
</div>
