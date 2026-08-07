<script lang="ts">
  // PlaybackPanel owns all playback state + UI (H.264/H.265 <video>, timelapse
  // JPEG cycler, MJPEG, AVI, unsupported-format fallback, format badge overlay,
  // inline merge UI inside the timelapse panel). Extracted from
  // RecordingDetail.svelte (#136).
  //
  // The host (RecordingDetail.svelte) owns loadRecording() — it decides which
  // player mode to enter based on the codec probe, then sets `recording` +
  // `playbackMode` props. This component re-initializes its player state in a
  // $effect whenever those props change.
  import { tick, onDestroy } from 'svelte';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import type { Recording, TimelapseFrame } from '$lib/api';
  import {
    getRecording,
    getRecordingVideoUrl,
    getMergedRecordingUrl,
    probeMergedRecordingCodec,
    clearMergedCodecCache,
    listRecordings,
    getTimelapseFrames,
    loadTimelapseFrameBlob,
    recordTimelineSeek,
  } from '$lib/api';
  import { AlertTriangle, HelpCircle, SkipForward, Loader2, RefreshCw, Play, Pause, ChevronLeft, ChevronRight } from 'lucide-svelte';
  import MjpegPlayer from '$lib/components/MjpegPlayer.svelte';
  import VideoPlaybackControls from '$lib/components/VideoPlaybackControls.svelte';
  import AviPlayback from '../../components/AviPlayback.svelte';
  import TimelineBar from '$lib/components/TimelineBar.svelte';

  export type PlaybackMode = 'video' | 'timelapse' | 'avi' | 'mjpeg' | 'unsupported';

  interface Props {
    recording: Recording | null;
    currentId: string;
    isTransitioning: boolean;
    /** Deep-link / cross-segment seek offset to apply once the <video> is ready. */
    pendingTimelineSeekOffset: number | null;
    /** Merge progress state (mirrored from MergePanel) for the inline UI. */
    mergeState: { inProgress: boolean; pct: number; eta: string; error: string };
    /** Whether a merged MP4 is available (controls inline merge controls visibility). */
    canMerge: boolean;
    /** Fired when the user requests a merge (host forwards to MergePanel). */
    onstartmerge: () => void;
    /** Fired when the user requests merge cancel (host forwards to MergePanel). */
    oncancelmerge: () => void;
    /** Fired when the <video> naturally ends (host handles next-segment navigation). */
    onended: () => void;
    /** Fired when a cross-segment timeline seek occurs. */
    ontimelineseek: (recordingId: string, offsetSeconds: number) => void;
    /** Fired when playback should move to the next segment (Next button). */
    ongotonext: () => void;
    /** Fired when the timelapse cycler chains seamlessly into a prefetched next
     *  segment; host updates its recording state without a full reload. */
    oncrosssegment?: (nextRecording: Recording) => void;
  }

  let {
    recording,
    currentId,
    isTransitioning,
    pendingTimelineSeekOffset = $bindable(),
    mergeState,
    canMerge,
    onstartmerge,
    oncancelmerge,
    onended,
    ontimelineseek,
    ongotonext,
    oncrosssegment,
  } = $props();

  let mjpegPlayer: MjpegPlayer | undefined = $state();

  // --- Video player state (H.264 / H.265) ---
  let videoUrl = $state('');
  let videoLoading = $state(false);
  let videoSpeed = $state(1);
  let videoFullscreen = $state(false);
  let videoEl = $state<HTMLVideoElement | null>(null);
  let videoCurrentTime = $state(0);
  let videoDuration = $state(0);
  let videoBuffered = $state(0);
  let videoIsPlaying = $state(false);
  let formatBadgeVisible = $state(true);
  let formatBadgeTimeout = $state<ReturnType<typeof setTimeout> | null>(null);
  let videoLoop = $state(false);
  let videoError = $state<string | null>(null);
  let videoErrorMsg = $state('');
  let videoRetryCount = $state(0);
  let videoStalled = $state(false);
  let videoStallTimeout: ReturnType<typeof setTimeout> | null = null;
  const MAX_VIDEO_RETRIES = 3;
  // When true, the merged MP4 failed to load (network / missing / unsupported
  // codec) and we fall back to the JPEG frame viewer for timelapse/MJPEG.
  let useFrameFallback = $state(false);

  // --- H.265 → H.264 transcode-for-playback ---
  let transcodeForPlayback = $state(false);
  let transcodeForPlaybackError = $state('');
  let transcodePollTimer = $state<ReturnType<typeof setInterval> | null>(null);

  // --- Timelapse player state ---
  let timelapseFrames = $state<TimelapseFrame[]>([]);
  let tlCurrentFrame = $state(0);
  let tlIsPlaying = $state(false);
  let tlSpeed = $state(1);
  let tlLoading = $state(false);
  let tlError = $state('');
  const tlSpeeds = [1, 2, 4];
  let tlPlayTimeout: ReturnType<typeof setTimeout> | null = null;
  let tlBlobCache = $state<Map<number, string>>(new Map());
  let tlAbortController: AbortController | null = null;
  let tlLoop = $state(false);
  let tlSeekLoading = $state(false);
  let tlSeekTimeout: ReturnType<typeof setTimeout> | null = null;

  interface PrefetchedSegment {
    recordingId: string;
    frames: TimelapseFrame[];
    blobCache: Map<number, string>;
  }
  let prefetchedNextSegment: PrefetchedSegment | null = null;
  let prefetchingNextSegment = false;

  let formatLabel = $derived.by(() => {
    if (!recording) return '';
    switch (recording.format) {
      case 'h264': return t('recording.format.h264');
      case 'h265': return t('recording.format.h265');
      case 'timelapse': return t('recording.format.timelapse');
      default: return recording.format;
    }
  });

  let formatBadgeClass = $derived.by(() => {
    if (!recording) return 'badge-neutral';
    if (recording.format === 'timelapse') return 'bg-cyan-500/20 text-cyan-300 dark:text-cyan-300';
    if (recording.format === 'h264' || recording.format === 'h265') return 'bg-[var(--color-info)]/20 text-[var(--color-info)]';
    return 'bg-white/10 th-text-secondary';
  });

  // Probed codec of the merged output for timelapse/mjpeg recordings.
  // 'h264'/'h265' → browser-playable in <video>; anything else (mjpa, missing
  // header, HEAD failed) → JPEG frame cycler. Mirrors the original loadRecording
  // probe logic that lived in the host.
  let probedMergedCodec = $state<'h264' | 'h265' | 'other' | ''>('');
  let lastProbedId = '';

  // Effective playback mode: derived from recording.format + probed codec +
  // useFrameFallback (the latter flips a failed <video> to the cycler at runtime).
  let playbackMode = $derived.by<PlaybackMode>(() => {
    if (!recording) return 'unsupported';
    const f = recording.format;
    if (f === 'avi') return 'avi';
    if (f === 'mjpeg' || f === 'timelapse') {
      if (useFrameFallback) return 'timelapse';
      const hasMerged = recording.merge_status === 'merged' && !!recording.merge_path;
      if (hasMerged && (probedMergedCodec === 'h264' || probedMergedCodec === 'h265')) return 'video';
      return 'timelapse';
    }
    if (f === 'h264' || f === 'h265') return 'video';
    return 'unsupported';
  });

  // Re-init when the recording changes. The host's loadRecording sets
  // `recording`; we react here. For timelapse/mjpeg with a merged output we
  // first probe the merged codec to decide <video> vs cycler (mirrors the
  // original probe inside the host's loadRecording). For plain h264/h265 we
  // init the video player directly.
  let lastLoadedId = '';
  $effect(() => {
    if (!recording || recording.id === lastLoadedId) return;
    lastLoadedId = recording.id;
    useFrameFallback = false;
    const f = recording.format;
    if (f === 'h264' || f === 'h265') {
      probedMergedCodec = '';
      initVideoPlayer();
      return;
    }
    if (f === 'timelapse' || f === 'mjpeg') {
      const hasMerged = recording.merge_status === 'merged' && !!recording.merge_path;
      if (hasMerged) {
        // Probe, then init based on the result.
        let cancelled = false;
        probeMergedRecordingCodec(currentId)
          .then((codec) => {
            if (cancelled) return;
            probedMergedCodec = codec === 'h264' || codec === 'h265' ? codec : 'other';
            if (probedMergedCodec === 'h264' || probedMergedCodec === 'h265') {
              initVideoPlayer();
            } else {
              initTimelapsePlayer();
            }
          })
          .catch(() => {
            if (cancelled) return;
            probedMergedCodec = 'other';
            initTimelapsePlayer();
          });
        return () => { cancelled = true; };
      }
      probedMergedCodec = '';
      initTimelapsePlayer();
    }
  });

  // MJPEG auto-init.
  $effect(() => {
    if (recording?.format === 'mjpeg' && mjpegPlayer) {
      mjpegPlayer.initPlayer();
    }
  });

  // Fullscreen tracking for the format-badge auto-hide.
  $effect(() => {
    function onFSChange() { videoFullscreen = !!document.fullscreenElement; }
    document.addEventListener('fullscreenchange', onFSChange);
    return () => document.removeEventListener('fullscreenchange', onFSChange);
  });

  // --- Next-segment loading (shared by <video> ended + timelapse chain) ---
  async function loadNextRecording(): Promise<Recording | null> {
    if (!recording) return null;
    try {
      const resp = await listRecordings({
        camera_id: recording.camera_id,
        format: recording.format,
        start: recording.ended_at ? new Date(recording.ended_at).toISOString() : undefined,
        sort_by: 'started_at',
        order: 'asc',
        limit: 5,
        offset: 0,
      });
      return resp.recordings.find(r => r.merge_status !== 'daily_merged') ?? null;
    } catch { return null; }
  }

  let nextRecordingId = $state<string | null>(null);
  function handleTimeUpdate(e: Event) {
    const video = e.target as HTMLVideoElement;
    videoCurrentTime = video.currentTime;
    videoDuration = video.duration || 0;
    if (video.duration && video.currentTime / video.duration > 0.8 && !nextRecordingId) prefetchNextRecording();
  }
  async function prefetchNextRecording() {
    if (nextRecordingId || !recording) return;
    const next = await loadNextRecording();
    if (next) nextRecordingId = next.id;
  }

  async function handleVideoEnded() {
    if (videoLoop && videoEl) {
      videoEl.currentTime = 0;
      await videoEl.play();
      return;
    }
    onended();
  }

  function timelineDate(): string {
    if (!recording || !recording.started_at) return new Date().toLocaleDateString('en-CA');
    return new Date(recording.started_at).toLocaleDateString('en-CA');
  }

  async function handleTimelineSeek(recordingId: string, offsetSeconds: number) {
    if (!recording) return;
    const isSegmentSwitch = recordingId !== currentId;
    void recordTimelineSeek(recording.camera_id, isSegmentSwitch ? 'segment' : 'intra');
    if (isSegmentSwitch) {
      pendingTimelineSeekOffset = offsetSeconds;
      ontimelineseek(recordingId, offsetSeconds);
    } else if (videoEl) {
      videoEl.currentTime = offsetSeconds;
    }
  }

  // --- Video player init + handlers ---
  function initVideoPlayer() {
    videoSpeed = 1;
    videoLoading = true;
    if (recording && (recording.format === 'timelapse' || recording.format === 'mjpeg')) {
      videoUrl = getMergedRecordingUrl(currentId);
    } else {
      videoUrl = getRecordingVideoUrl(currentId);
    }
    if (formatBadgeTimeout) { clearTimeout(formatBadgeTimeout); formatBadgeTimeout = null; }
    videoError = null;
    videoErrorMsg = '';
    videoRetryCount = 0;
    videoStalled = false;
    if (videoStallTimeout) { clearTimeout(videoStallTimeout); videoStallTimeout = null; }
    void tick().then(() => { videoEl?.load(); });
  }

  function setVideoSpeed(speed: number) {
    videoSpeed = speed;
    const video = document.querySelector('video');
    if (video) video.playbackRate = speed;
  }

  function handleVideoLoadedMetadata(e: Event) {
    const video = e.target as HTMLVideoElement;
    videoDuration = video.duration || 0;
    if (pendingTimelineSeekOffset != null && video) {
      video.currentTime = Math.min(pendingTimelineSeekOffset, video.duration || pendingTimelineSeekOffset);
      pendingTimelineSeekOffset = null;
    }
  }
  function handleVideoLoadedData() {
    videoLoading = false;
    videoError = null;
    videoErrorMsg = '';
    videoRetryCount = 0;
    videoStalled = false;
    if (videoStallTimeout) { clearTimeout(videoStallTimeout); videoStallTimeout = null; }
  }
  function handleVideoProgress() {
    if (!videoEl || !videoEl.buffered.length || !videoEl.duration) { videoBuffered = 0; return; }
    const bf = videoEl.buffered;
    for (let i = 0; i < bf.length; i++) {
      if (bf.start(i) <= videoEl.currentTime && bf.end(i) >= videoEl.currentTime) {
        videoBuffered = (bf.end(i) / videoEl.duration) * 100;
        return;
      }
    }
    videoBuffered = (bf.end(bf.length - 1) / videoEl.duration) * 100;
  }
  function handleVideoPlay() {
    videoIsPlaying = true;
    if (formatBadgeTimeout) clearTimeout(formatBadgeTimeout);
    formatBadgeTimeout = setTimeout(() => { formatBadgeVisible = false; }, 3000);
  }
  function handleVideoPause() {
    videoIsPlaying = false;
    formatBadgeVisible = true;
    if (formatBadgeTimeout) { clearTimeout(formatBadgeTimeout); formatBadgeTimeout = null; }
  }
  function handleVideoContainerMouseEnter() {
    formatBadgeVisible = true;
    if (formatBadgeTimeout) { clearTimeout(formatBadgeTimeout); formatBadgeTimeout = null; }
  }
  function handleVideoContainerMouseLeave() {
    if (videoIsPlaying) formatBadgeTimeout = setTimeout(() => { formatBadgeVisible = false; }, 3000);
  }
  function toggleVideoLoop() { videoLoop = !videoLoop; }

  function handleVideoError(e: Event) {
    const video = e.target as HTMLVideoElement;
    const mediaError = video.error;
    if (!mediaError) return;
    videoStalled = false;
    if (videoStallTimeout) { clearTimeout(videoStallTimeout); videoStallTimeout = null; }
    const code = mediaError.code;
    if (code === MediaError.MEDIA_ERR_ABORTED) return;
    videoLoading = false;

    // Merged timelapse/mjpeg: fall back to the JPEG cycler on decode/network/
    // src-not-supported instead of a dead-end error.
    if (recording && (recording.format === 'timelapse' || recording.format === 'mjpeg')
        && recording.merge_status === 'merged' && !useFrameFallback
        && (code === MediaError.MEDIA_ERR_NETWORK
          || code === MediaError.MEDIA_ERR_DECODE
          || code === MediaError.MEDIA_ERR_SRC_NOT_SUPPORTED)) {
      console.warn('Merged MP4 playback failed, falling back to frame viewer', { format: recording.format, errorCode: code });
      useFrameFallback = true;
      clearMergedCodecCache(recording.id);
      videoError = null;
      videoErrorMsg = '';
      initTimelapsePlayer();
      return;
    }

    if (code === MediaError.MEDIA_ERR_SRC_NOT_SUPPORTED) {
      videoError = 'src_not_supported';
      videoErrorMsg = t('detail.videoFormatNotSupported');
    } else if (code === MediaError.MEDIA_ERR_NETWORK) {
      videoError = 'network';
      videoErrorMsg = t('detail.videoNetworkError');
    } else if (code === MediaError.MEDIA_ERR_DECODE) {
      videoError = 'decode';
      videoErrorMsg = t('detail.videoDecodeError');
    } else {
      videoError = 'unknown';
      videoErrorMsg = t('detail.videoUnknownError');
    }
  }
  function handleVideoRetry() {
    if (videoRetryCount >= MAX_VIDEO_RETRIES) return;
    videoRetryCount++;
    videoError = null;
    videoErrorMsg = '';
    videoLoading = true;
    videoStalled = false;
    if (videoStallTimeout) { clearTimeout(videoStallTimeout); videoStallTimeout = null; }
    const video = document.querySelector('video');
    if (video) {
      video.removeAttribute('src');
      video.load();
      video.src = videoUrl;
      video.load();
    }
  }
  function handleVideoCanPlay() {
    videoStalled = false;
    if (videoStallTimeout) { clearTimeout(videoStallTimeout); videoStallTimeout = null; }
    videoError = null;
    videoErrorMsg = '';
    videoRetryCount = 0;
  }
  function handleVideoWaiting() {
    if (videoStallTimeout) clearTimeout(videoStallTimeout);
    videoStallTimeout = setTimeout(() => { videoStalled = true; }, 3000);
  }
  function handleVideoPlaying() {
    videoStalled = false;
    if (videoStallTimeout) { clearTimeout(videoStallTimeout); videoStallTimeout = null; }
  }

  async function startTranscodeForPlayback() {
    if (!recording) return;
    transcodeForPlayback = true;
    transcodeForPlaybackError = '';
    try {
      const { enqueueTranscodeTask, getTranscodingTasks } = await import('$lib/api/transcoding');
      const task = await enqueueTranscodeTask({
        camera_id: recording.camera_id,
        recording_id: currentId,
        target_codec: 'h264',
      });
      transcodePollTimer = setInterval(async () => {
        try {
          const resp = await getTranscodingTasks({ camera_id: recording.camera_id, limit: 10 });
          const tk = resp.tasks.find((x) => x.id === task.id);
          if (!tk) return;
          if (tk.status === 'completed') {
            stopTranscodePoll();
            transcodeForPlayback = false;
            showToast(t('detail.transcodeReady'), 'success');
            initVideoPlayer();
          } else if (tk.status === 'failed') {
            stopTranscodePoll();
            transcodeForPlayback = false;
            transcodeForPlaybackError = tk.error || t('detail.transcodeFailed');
            showToast(transcodeForPlaybackError, 'error');
          }
        } catch { /* retry next tick */ }
      }, 3000);
    } catch (e) {
      transcodeForPlayback = false;
      transcodeForPlaybackError = e instanceof Error ? e.message : t('detail.transcodeFailed');
      showToast(transcodeForPlaybackError, 'error');
    }
  }
  function stopTranscodePoll() {
    if (transcodePollTimer) { clearInterval(transcodePollTimer); transcodePollTimer = null; }
  }

  // --- Timelapse JPEG cycler ---
  async function initTimelapsePlayer() {
    tlLoading = true;
    tlError = '';
    tlIsPlaying = false;
    tlCurrentFrame = 0;
    stopTimelapsePlayback();
    tlAbortController?.abort();
    tlAbortController = new AbortController();
    const signal = tlAbortController.signal;
    tlBlobCache.forEach(url => URL.revokeObjectURL(url));
    tlBlobCache = new Map();
    prefetchedNextSegment = null;
    prefetchingNextSegment = false;
    try {
      timelapseFrames = await getTimelapseFrames(currentId, signal);
      if (timelapseFrames.length > 0) {
        await ensureFrameCached(0, signal);
        prefetchAhead(0, signal);
      }
    } catch (e) {
      if (signal.aborted) return;
      console.error('Failed to load timelapse frames:', e);
      tlError = t('detail.failedLoadVideo');
      timelapseFrames = [];
    } finally {
      tlLoading = false;
    }
  }

  async function ensureFrameCached(index: number, signal?: AbortSignal) {
    if (tlBlobCache.has(index) || !timelapseFrames[index]) return;
    if (signal?.aborted) return;
    try {
      const blobUrl = await loadTimelapseFrameBlob(currentId, timelapseFrames[index].filename, signal);
      if (signal?.aborted) return;
      tlBlobCache.set(index, blobUrl);
      if (tlBlobCache.size >= 500) {
        const keys = [...tlBlobCache.keys()].sort((a, b) => a - b);
        const toEvict = keys.slice(0, keys.length - 400);
        for (const k of toEvict) {
          const url = tlBlobCache.get(k);
          if (url) URL.revokeObjectURL(url);
          tlBlobCache.delete(k);
        }
      }
    } catch (e) {
      if (signal?.aborted) return;
      console.warn('Failed to load timelapse frame:', index, e);
    }
  }

  async function prefetchAhead(fromIndex: number, signal?: AbortSignal) {
    const windowSize = 200;
    const batchSize = 20;
    const end = Math.min(fromIndex + windowSize, timelapseFrames.length);
    for (let i = fromIndex; i < end; i += batchSize) {
      if (signal?.aborted) return;
      const batch = [];
      for (let j = i; j < Math.min(i + batchSize, end); j++) {
        if (!tlBlobCache.has(j)) batch.push(ensureFrameCached(j, signal));
      }
      await Promise.all(batch);
    }
  }

  function stopTimelapsePlayback() {
    if (tlPlayTimeout) { clearTimeout(tlPlayTimeout); tlPlayTimeout = null; }
  }

  function playNextFrame() {
    if (!tlIsPlaying) return;
    const signal = tlAbortController?.signal;
    if (signal?.aborted) return;
    const next = tlCurrentFrame + 1;

    if (timelapseFrames.length > 0 && next >= timelapseFrames.length * 0.8 && !prefetchedNextSegment && !prefetchingNextSegment) {
      prefetchNextSegmentFrames();
    }

    if (next >= timelapseFrames.length) {
      if (tlLoop) {
        tlCurrentFrame = 0;
        tlPlayTimeout = setTimeout(playNextFrame, 50);
        return;
      }
      // Seamless chain: if the next segment was prefetched, adopt its frames.
      if (prefetchedNextSegment && prefetchedNextSegment.frames.length > 0) {
        const pre = prefetchedNextSegment;
        prefetchedNextSegment = null;
        timelapseFrames = pre.frames;
        tlBlobCache.forEach(url => URL.revokeObjectURL(url));
        tlBlobCache = pre.blobCache;
        tlCurrentFrame = 0;
        clearMergedCodecCache(pre.recordingId);
        // Reload the recording metadata in the background so the side panel /
        // timeline update, without interrupting the cycler.
        void getRecording(pre.recordingId).then(r => { if (r) oncrosssegment?.(r); });
        const fps = 10 * tlSpeed;
        const delay = Math.max(0, (1000 / fps) - 10);
        tlPlayTimeout = setTimeout(playNextFrame, delay);
        return;
      }
      // No prefetch → fall back to the host's next-segment navigation.
      tlIsPlaying = false;
      ongotonext();
      return;
    }
    tlCurrentFrame = next;
    const loadPromise = tlBlobCache.has(next) ? Promise.resolve() : ensureFrameCached(next, signal);
    prefetchAhead(next + 1, signal);
    loadPromise.then(() => {
      if (signal?.aborted) return;
      const fps = 10 * tlSpeed;
      const delay = Math.max(0, (1000 / fps) - 10);
      tlPlayTimeout = setTimeout(playNextFrame, delay);
    });
  }

  async function prefetchNextSegmentFrames() {
    if (!recording || prefetchedNextSegment || prefetchingNextSegment) return;
    prefetchingNextSegment = true;
    try {
      const next = await loadNextRecording();
      if (!next) return;
      const frames = await getTimelapseFrames(next.id);
      if (frames.length === 0) return;
      const blobCache = new Map<number, string>();
      const batchSize = Math.min(20, frames.length);
      await Promise.all(
        Array.from({ length: batchSize }, (_, i) =>
          loadTimelapseFrameBlob(next.id, frames[i].filename)
            .then(url => blobCache.set(i, url))
            .catch(() => {})
        )
      );
      prefetchedNextSegment = { recordingId: next.id, frames, blobCache };
    } catch { /* silent */ } finally {
      prefetchingNextSegment = false;
    }
  }

  function tlTogglePlay() {
    if (tlIsPlaying) {
      tlIsPlaying = false;
      stopTimelapsePlayback();
    } else {
      if (timelapseFrames.length === 0) return;
      tlIsPlaying = true;
      stopTimelapsePlayback();
      playNextFrame();
    }
  }
  function tlSetSpeed(speed: number) { tlSpeed = speed; }
  function tlSeek(index: number) {
    const target = Math.max(0, Math.min(index, timelapseFrames.length - 1));
    tlCurrentFrame = target;
    const signal = tlAbortController?.signal;
    if (!tlBlobCache.has(target)) {
      tlSeekTimeout = setTimeout(() => { tlSeekLoading = true; }, 500);
      ensureFrameCached(target, signal).finally(() => {
        if (tlSeekTimeout) { clearTimeout(tlSeekTimeout); tlSeekTimeout = null; }
        tlSeekLoading = false;
      });
    }
    prefetchAhead(target + 1, signal);
  }
  function tlToggleLoop() { tlLoop = !tlLoop; }
  function toggleFullscreen() {
    const el = document.querySelector('.timelapse-container');
    if (!el) return;
    if (document.fullscreenElement) document.exitFullscreen();
    else el.requestFullscreen();
  }
  function toggleVideoFullscreen() {
    const el = document.querySelector('.video-container');
    if (!el) return;
    if (document.fullscreenElement) document.exitFullscreen();
    else el.requestFullscreen();
  }

  function getFrameTimestamp(): string {
    if (!recording || !timelapseFrames[tlCurrentFrame]) return '';
    const start = new Date(recording.started_at).getTime();
    const frame = timelapseFrames[tlCurrentFrame];
    if (frame.timestamp) {
      const ts = new Date(frame.timestamp).getTime();
      const diff = Math.max(0, ts - start);
      const totalSec = Math.floor(diff / 1000);
      const h = Math.floor(totalSec / 3600);
      const m = Math.floor((totalSec % 3600) / 60);
      const s = totalSec % 60;
      return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
    }
    const totalFrames = timelapseFrames.length;
    const durationMs = recording.duration * 1000;
    const estimatedSec = Math.floor((tlCurrentFrame / Math.max(1, totalFrames)) * (durationMs / 1000));
    const h = Math.floor(estimatedSec / 3600);
    const m = Math.floor((estimatedSec % 3600) / 60);
    const s = estimatedSec % 60;
    return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
  }

  // Cleanup on destroy: abort in-flight requests, revoke blob URLs, clear timers.
  onDestroy(() => {
    tlAbortController?.abort();
    tlAbortController = null;
    tlBlobCache.forEach(url => URL.revokeObjectURL(url));
    tlBlobCache = new Map();
    stopTimelapsePlayback();
    if (formatBadgeTimeout) { clearTimeout(formatBadgeTimeout); formatBadgeTimeout = null; }
    if (videoStallTimeout) { clearTimeout(videoStallTimeout); videoStallTimeout = null; }
    stopTranscodePoll();
  });

  // --- Public API (host's keyboard dispatcher forwards to these) ---
  export function handleKeyAction(key: string) {
    if (!recording) return;
    const f = recording.format;
    if (key === 'space') {
      if (f === 'mjpeg') mjpegPlayer?.handleKeyAction('togglePlay');
      else if (f === 'timelapse') tlTogglePlay();
      else { const v = document.querySelector('video'); if (v) { if (v.paused) v.play(); else v.pause(); } }
    } else if (key === 'arrowleft') {
      if (f === 'mjpeg') mjpegPlayer?.handleKeyAction('prevFrame');
      else if (f === 'timelapse') tlSeek(tlCurrentFrame - 1);
      else { const v = document.querySelector('video'); if (v) v.currentTime = Math.max(0, v.currentTime - 5); }
    } else if (key === 'arrowright') {
      if (f === 'mjpeg') mjpegPlayer?.handleKeyAction('nextFrame');
      else if (f === 'timelapse') tlSeek(tlCurrentFrame + 1);
      else { const v = document.querySelector('video'); if (v) v.currentTime = Math.min(v.duration, v.currentTime + 5); }
    } else if (key === 'f') {
      if (f === 'mjpeg') mjpegPlayer?.handleKeyAction('toggleFullscreen');
      else if (f === 'timelapse') toggleFullscreen();
      else toggleVideoFullscreen();
    } else if (key === 'l') {
      if (f === 'mjpeg') mjpegPlayer?.handleKeyAction('toggleLoop');
      else if (f === 'timelapse') tlToggleLoop();
      else if (f === 'h264' || f === 'h265') toggleVideoLoop();
    } else if (key === 'home') {
      if (f === 'mjpeg') mjpegPlayer?.handleKeyAction('home');
      else if (f === 'timelapse') tlSeek(0);
      else if (f === 'h264' || f === 'h265') setVideoSpeed(1);
    }
  }
</script>

{#if playbackMode === 'video'}
  <div role="presentation"
    class="video-container relative max-w-full bg-black rounded-t-[var(--radius-md)]"
    onmouseenter={handleVideoContainerMouseEnter}
    onmouseleave={handleVideoContainerMouseLeave}>
    {#if isTransitioning}
      <div class="absolute inset-0 bg-black/60 flex items-center justify-center z-10">
        <Loader2 size={32} class="animate-spin th-text-secondary" />
      </div>
    {/if}
    {#if videoUrl}
      <video bind:this={videoEl} preload="metadata" controlsList="nodownload" class="w-full max-h-[80vh]" src={videoUrl}
        onended={handleVideoEnded} ontimeupdate={handleTimeUpdate} onplay={handleVideoPlay} onpause={handleVideoPause}
        onloadedmetadata={handleVideoLoadedMetadata} onprogress={handleVideoProgress} onloadeddata={handleVideoLoadedData}
        onerror={handleVideoError} onwaiting={handleVideoWaiting} oncanplay={handleVideoCanPlay} onplaying={handleVideoPlaying}>
        <track kind="captions" />
        {t('detail.videoUnsupported')}
      </video>
      {#if videoLoading}
        <div class="absolute inset-0 skeleton-shimmer" style="border-radius: var(--radius-md) var(--radius-md) 0 0;"></div>
      {/if}
      {#if videoError}
        <div class="absolute inset-0 bg-black/80 flex flex-col items-center justify-center z-20 p-6">
          <AlertTriangle size={48} class="th-color-danger mb-3" />
          <p class="text-white text-center text-sm mb-4">{videoErrorMsg}</p>
          {#if videoError === 'src_not_supported' && recording?.format === 'h265'}
            {#if transcodeForPlayback}
              <div class="flex items-center gap-2 text-white/80 mb-3">
                <Loader2 size={16} class="animate-spin" />
                <span class="text-sm">{t('detail.transcodingForPlayback')}</span>
              </div>
            {:else}
              <button onclick={startTranscodeForPlayback} class="btn btn-primary btn-sm flex items-center gap-1 mb-3">
                <RefreshCw size={14} />
                {t('detail.transcodeToH264')}
              </button>
            {/if}
            {#if transcodeForPlaybackError}
              <p class="text-red-400 text-xs mb-3">{transcodeForPlaybackError}</p>
            {/if}
          {/if}
          {#if videoRetryCount < MAX_VIDEO_RETRIES}
            <button onclick={handleVideoRetry} class="btn btn-primary btn-sm flex items-center gap-1">
              <RefreshCw size={14} />
              {videoRetryCount > 0 ? t('detail.videoRetrying', { count: String(videoRetryCount), max: String(MAX_VIDEO_RETRIES) }) : t('common.retry')}
            </button>
          {:else}
            <p class="text-white/70 text-xs mb-3">{t('detail.videoMaxRetries')}</p>
          {/if}
        </div>
      {:else if videoStalled}
        <div class="absolute inset-0 bg-black/40 flex items-center justify-center z-20">
          <div class="flex items-center gap-2 text-white/80">
            <Loader2 size={20} class="animate-spin" />
            <span class="text-sm">{t('detail.videoBuffering')}</span>
          </div>
        </div>
      {/if}
    {:else if !videoLoading}
      <div class="flex items-center justify-center h-64 th-text-muted">{t('detail.failedLoadVideo')}</div>
    {/if}

    <!-- Format badge overlay -->
    <div class="absolute top-2 left-2 z-10 pointer-events-none transition-opacity duration-300 ease-in-out"
      style="opacity: {formatBadgeVisible ? 1 : 0};">
      <span class="badge text-[10px] leading-none py-0.5 px-1.5 {formatBadgeClass}">
        {formatLabel}
      </span>
    </div>
  </div>
  <VideoPlaybackControls
    currentTime={videoCurrentTime}
    duration={videoDuration}
    isPlaying={videoIsPlaying}
    playbackRate={videoSpeed}
    buffered={videoBuffered}
    isLooping={videoLoop}
    ontoggleplay={() => { if (videoEl) { if (videoEl.paused) videoEl.play(); else videoEl.pause(); } }}
    onseek={(ratio) => { if (videoEl) videoEl.currentTime = ratio * videoDuration; }}
    onsetspeed={(speed) => setVideoSpeed(speed)}
    onfullscreen={toggleVideoFullscreen}
    ontoggleloop={toggleVideoLoop}
    onarrowleft={() => { if (videoEl) videoEl.currentTime = Math.max(0, videoEl.currentTime - 5); }}
    onarrowright={() => { if (videoEl) videoEl.currentTime = Math.min(videoEl.duration, videoEl.currentTime + 5); }}
  />
  {#if recording?.camera_id}
    <TimelineBar
      cameraId={recording.camera_id}
      date={timelineDate()}
      currentRecording={recording}
      currentVideoTime={videoCurrentTime}
      onseek={handleTimelineSeek}
      showEvents={true}
    />
  {/if}
  <div class="flex items-center justify-between px-4 py-2 th-bg-secondary border-t th-border">
    <span class="text-sm th-text-muted">{t('detail.playing')} <span class="font-mono th-text-primary">{recording?.camera_id}</span></span>
    <button onclick={ongotonext} class="btn btn-ghost btn-sm flex items-center gap-1">
      {t('detail.nextRecording')} <SkipForward size={16} />
    </button>
  </div>
{:else if playbackMode === 'timelapse'}
  {#if tlLoading}
    <div class="flex items-center justify-center h-64 bg-black">
      <div class="spinner spinner-lg"></div>
      <span class="th-text-muted ml-3">{t('detail.loadingFrames')}</span>
    </div>
  {:else if tlError}
    <div class="flex items-center justify-center h-64 bg-black">
      <div class="text-center th-text-muted">
        <AlertTriangle size={48} class="mx-auto mb-2" />
        <p>{tlError}</p>
      </div>
    </div>
  {:else if timelapseFrames.length === 0}
    <div class="flex items-center justify-center h-64 bg-black">
      <div class="text-center th-text-muted">
        <HelpCircle size={48} class="mx-auto mb-2" />
        <p>{t('detail.noFrames')}</p>
      </div>
    </div>
  {:else}
    <!-- Frame display -->
    <div class="timelapse-container relative max-h-[75vh] overflow-hidden flex items-center justify-center bg-black min-h-[200px]">
      {#if timelapseFrames[tlCurrentFrame]}
        {@const frame = timelapseFrames[tlCurrentFrame]}
        {#if tlBlobCache.has(tlCurrentFrame)}
          <img src={tlBlobCache.get(tlCurrentFrame)} alt={frame.filename} class="max-w-full max-h-[75vh]" style="transition: opacity 0.2s ease-in-out" />
        {:else if tlCurrentFrame > 0 && tlBlobCache.has(tlCurrentFrame - 1)}
          <img src={tlBlobCache.get(tlCurrentFrame - 1)} alt={frame.filename} class="max-w-full max-h-[75vh] opacity-50" style="transition: opacity 0.3s ease-in-out" />
          {#if tlSeekLoading}
            <div class="absolute inset-0 flex items-center justify-center bg-black/30">
              <div class="spinner spinner-lg"></div>
            </div>
          {/if}
        {:else}
          <div class="flex items-center justify-center h-64 bg-black">
            <div class="spinner spinner-lg"></div>
          </div>
        {/if}
      {/if}
    </div>

    <!-- Inline merge controls -->
    {#if !mergeState.inProgress && canMerge}
      <div class="th-bg-secondary px-4 py-3 border-t th-border">
        <div class="flex items-center justify-center gap-2">
          <button onclick={onstartmerge} class="btn btn-primary flex items-center gap-2">
            <Play size={16} /> {t('detail.mergeAndPlay')}
          </button>
        </div>
      </div>
    {/if}
    {#if mergeState.inProgress}
      <div class="th-bg-secondary px-4 py-3 border-t th-border">
        <div class="flex items-center gap-3 justify-center flex-wrap">
          <div class="w-32 h-1.5 rounded-full th-bg-tertiary overflow-hidden">
            <div class="h-full rounded-full bg-[var(--color-info)] transition-all duration-500" style="width: {mergeState.pct}%"></div>
          </div>
          <span class="text-xs th-text-secondary">{t('detail.mergingProgress', { percent: String(mergeState.pct) })}</span>
          {#if mergeState.eta}
            <span class="text-xs th-text-muted">{mergeState.eta}</span>
          {/if}
          <button onclick={oncancelmerge} class="btn btn-ghost btn-xs text-xs th-color-danger">
            {t('detail.cancelMerge')}
          </button>
        </div>
      </div>
    {/if}
    {#if mergeState.error}
      <div class="th-bg-secondary px-4 py-3 border-t th-border text-center">
        <div class="flex items-center gap-3 justify-center">
          <span class="text-xs th-color-danger">{t('detail.mergeFailed', { error: mergeState.error })}</span>
          <button onclick={onstartmerge} class="btn btn-secondary btn-sm">{t('detail.mergeRetry')}</button>
        </div>
      </div>
    {/if}

    <!-- Controls -->
    <div class="th-bg-secondary px-4 py-3 space-y-2">
      <div
        class="relative h-2 th-bg-tertiary rounded cursor-pointer group"
        role="slider"
        tabindex="0"
        aria-label={t('detail.frameCounter', { current: String(tlCurrentFrame + 1), total: String(timelapseFrames.length) })}
        aria-valuenow={tlCurrentFrame}
        aria-valuemin={0}
        aria-valuemax={timelapseFrames.length - 1}
        onclick={(e) => {
          const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
          const ratio = (e.clientX - rect.left) / rect.width;
          tlSeek(Math.round(ratio * (timelapseFrames.length - 1)));
        }}
        onkeydown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); }
          else if (e.key === 'ArrowLeft') { e.preventDefault(); tlSeek(tlCurrentFrame - 1); }
          else if (e.key === 'ArrowRight') { e.preventDefault(); tlSeek(tlCurrentFrame + 1); }
        }}
      >
        <div
          class="absolute top-0 left-0 h-full th-bg-accent rounded group-hover:th-bg-info transition-colors"
          style="width: {timelapseFrames.length > 1 ? (tlCurrentFrame / (timelapseFrames.length - 1)) * 100 : 100}%"
        ></div>
        <div
          class="absolute top-1/2 -translate-y-1/2 w-3 h-3 th-bg-info rounded-full shadow group-hover:th-bg-accent transition-colors"
          style="left: calc({timelapseFrames.length > 1 ? (tlCurrentFrame / (timelapseFrames.length - 1)) * 100 : 100}% - 6px)"
        ></div>
      </div>

      <div class="flex items-center justify-between">
        <div class="flex items-center gap-2">
          <button
            onclick={() => tlSeek(tlCurrentFrame - 1)}
            disabled={tlCurrentFrame === 0 || tlIsPlaying}
            class="px-3 py-1.5 rounded text-sm font-medium transition-colors"
            style="color: {tlCurrentFrame === 0 || tlIsPlaying ? 'var(--text-tertiary)' : 'var(--text-body)'}; background-color: {tlCurrentFrame === 0 || tlIsPlaying ? 'transparent' : 'var(--bg-tertiary)'}"
          >
            <ChevronLeft size={16} />
          </button>
          <button
            onclick={tlTogglePlay}
            class="px-4 py-1.5 rounded text-sm font-medium text-white transition-colors flex items-center gap-1"
            style="background-color: {tlIsPlaying ? 'var(--color-danger)' : 'var(--color-info)'}"
          >
            {#if tlIsPlaying}
              <Pause size={14} /> {t('detail.pause')}
            {:else}
              <Play size={14} /> {t('detail.play')}
            {/if}
          </button>
          <button
            onclick={() => tlSeek(tlCurrentFrame + 1)}
            disabled={tlCurrentFrame >= timelapseFrames.length - 1 || tlIsPlaying}
            class="px-3 py-1.5 rounded text-sm font-medium transition-colors"
            style="color: {tlCurrentFrame >= timelapseFrames.length - 1 || tlIsPlaying ? 'var(--text-tertiary)' : 'var(--text-body)'}; background-color: {tlCurrentFrame >= timelapseFrames.length - 1 || tlIsPlaying ? 'transparent' : 'var(--bg-tertiary)'}"
          >
            <ChevronRight size={16} />
          </button>
        </div>

        <div class="flex items-center gap-3">
          <span class="th-text-secondary text-sm font-mono">
            {tlCurrentFrame + 1} / {timelapseFrames.length}
          </span>
          <span class="th-text-tertiary text-xs font-mono">
            {getFrameTimestamp()}
          </span>
        </div>

        <div class="flex items-center gap-1">
          <span class="th-text-tertiary text-xs mr-1">{t('detail.speed')}</span>
          {#each tlSpeeds as speed}
            <button
              onclick={() => tlSetSpeed(speed)}
              class="px-2 py-1 rounded text-xs font-medium transition-colors"
              style="background-color: {tlSpeed === speed ? 'var(--color-info)' : 'var(--bg-tertiary)'}; color: {tlSpeed === speed ? 'white' : 'var(--text-secondary)'}"
            >
              {speed}x
            </button>
          {/each}
        </div>
        <div class="flex items-center gap-2">
          <button
            onclick={tlToggleLoop}
            class="px-2 py-1 rounded text-xs font-medium transition-colors"
            style="background-color: {tlLoop ? 'var(--color-info)' : 'var(--bg-tertiary)'}; color: {tlLoop ? 'white' : 'var(--text-secondary)'}"
            title="Loop playback"
          >
            🔁 Loop
          </button>
          <button
            onclick={toggleFullscreen}
            class="px-2 py-1 rounded text-xs font-medium transition-colors th-bg-tertiary th-text-secondary"
            title={t('live.fullscreen')}
          >
            ⛶ {t('live.fullscreen')}
          </button>
        </div>
      </div>
    </div>
  {/if}
{:else if playbackMode === 'avi'}
  <AviPlayback recordingId={currentId} />
{:else if playbackMode === 'mjpeg'}
  <MjpegPlayer bind:this={mjpegPlayer} recordingId={currentId} oninitdone={() => {}} />
  <div class="px-4 py-2 th-bg-tertiary">
    <p class="text-xs text-center th-text-muted">
      {t('detail.spacePlayPause')} | {t('detail.arrowSeek')} | Home {t('detail.homeReset')} | F {t('live.fullscreen')} | L {t('detail.loop')} | {t('detail.escapeBack')}
    </p>
  </div>
{:else}
  <div class="flex items-center justify-center h-64 bg-black">
    <div class="text-center th-text-tertiary">
      <div class="text-4xl mb-2 flex justify-center"><HelpCircle size={48} /></div>
      <p class="text-lg">{t('detail.unsupportedFormat')}</p>
      <p class="text-sm mt-2">{t('detail.format')}: {recording?.format}</p>
    </div>
  </div>
{/if}
