<script lang="ts">
  /**
   * TimelapseMergePlayer — playback core for a completed periodic timelapse
   * merge; the unified recording viewer embeds it for merged-timelapse playback
   * (RecordingDetail's dual-mode toggle renders it next to PlaybackPanel).
   *
   * h264/h265 outputs play through a plain <video>; MJPEG (mjpa) outputs —
   * which browsers cannot decode via <video> — play through the batched
   * canvas sequence player. An undecodable codec falls back to a download
   * affordance. Exposes pause() so the host can freeze playback when the
   * embedded player is toggled out of view.
   */
  import { getTimelapseMergeDownloadUrl, fetchTimelapseMergeFrameBatch } from '$lib/api';
  import type { TimelapseMerge } from '$lib/api';
  import { t } from '$lib/i18n';
  import { AlertTriangle, Download } from 'lucide-svelte';
  import MjpegSequencePlayer from '$lib/components/MjpegSequencePlayer.svelte';

  let {
    merge,
    autoplay = true,
    maxHeightClass = 'max-h-[70vh]',
  }: {
    merge: TimelapseMerge;
    autoplay?: boolean;
    maxHeightClass?: string;
  } = $props();

  let videoEl = $state<HTMLVideoElement | null>(null);
  let videoError = $state<string | null>(null);
  let videoErrorMsg = $state('');
  let seqPlaying = $state(false);

  const videoUrl = $derived(
    merge.status === 'completed' && merge.output_path ? getTimelapseMergeDownloadUrl(merge.id) : '',
  );

  // Reset decode state when the host swaps in a different merge.
  $effect(() => {
    void merge.id;
    videoError = null;
    videoErrorMsg = '';
    seqPlaying = false;
  });

  export function pause() {
    if (videoEl && !videoEl.paused) videoEl.pause();
    seqPlaying = false;
  }

  // Resume after the host toggled this player into view. The embedded
  // RecordingDetail mounts it hidden with autoplay off (no double audio);
  // the toggle click is the user gesture video.play() needs.
  export function resume() {
    if (videoEl && videoEl.paused) void videoEl.play();
    seqPlaying = true;
  }

  function handleLoadedMetadata(e: Event) {
    // Freeze-frame the first frame on paused entry (embedded usage mounts
    // with autoplay off): seeking one frame in forces the browser to decode
    // + paint it instead of showing a black canvas until play.
    const v = e.target as HTMLVideoElement;
    if (v.paused && v.currentTime < 0.05) v.currentTime = 0.04;
  }

  function handleVideoError(e: Event) {
    const video = e.target as HTMLVideoElement;
    const mediaError = video.error;
    if (!mediaError) return;
    if (mediaError.code === MediaError.MEDIA_ERR_ABORTED) return;
    if (mediaError.code === MediaError.MEDIA_ERR_SRC_NOT_SUPPORTED) {
      // Most likely: H.265 on a browser without a platform HEVC decoder.
      videoError = 'src_not_supported';
      const codec = merge.codec?.toUpperCase() ?? 'H.265';
      videoErrorMsg = t('timelapseMerge.playbackUnsupported', { codec });
    } else if (mediaError.code === MediaError.MEDIA_ERR_NETWORK) {
      videoError = 'network';
      videoErrorMsg = t('detail.videoNetworkError');
    } else if (mediaError.code === MediaError.MEDIA_ERR_DECODE) {
      videoError = 'decode';
      videoErrorMsg = t('detail.videoDecodeError');
    } else {
      videoError = 'unknown';
      videoErrorMsg = t('detail.videoUnknownError');
    }
  }
</script>

{#if merge.status === 'completed' && (videoUrl || merge.codec === 'mjpeg')}
  {#if merge.codec === 'mjpeg'}
    <!-- MJPEG (mjpa) output: browsers can't decode it via <video> — play as a
         batched JPEG sequence on canvas instead. -->
    <MjpegSequencePlayer
      frameCount={merge.frame_count}
      fps={merge.fps > 0 ? merge.fps : 30}
      fetchBatch={(offset, limit, signal) => fetchTimelapseMergeFrameBatch(merge.id, offset, limit, signal)}
      bind:playing={seqPlaying}
    />
  {:else if videoError === 'src_not_supported'}
    <div class="p-8 text-center">
      <AlertTriangle size={40} class="mx-auto mb-3 th-color-warning" />
      <p class="th-text-primary mb-2">{videoErrorMsg}</p>
      <a href={videoUrl} download class="btn btn-primary btn-sm inline-flex items-center gap-1">
        <Download size={14} />
        {t('detail.download')}
      </a>
    </div>
  {:else}
    <video
      bind:this={videoEl}
      controls
      {autoplay}
      class="w-full {maxHeightClass} bg-black"
      src={videoUrl}
      onloadedmetadata={handleLoadedMetadata}
      onerror={handleVideoError}
    ></video>
    {#if videoError}
      <div class="p-3 th-bg-warning-soft th-color-warning text-sm">
        {videoErrorMsg}
      </div>
    {/if}
  {/if}
{/if}
