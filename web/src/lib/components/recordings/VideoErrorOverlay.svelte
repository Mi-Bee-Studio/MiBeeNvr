<script lang="ts">
  // Full-surface <video> error overlay (decode/network/unsupported verdicts,
  // retry ladder, H.265→H.264 transcode offer, inline repair flow) — extracted
  // verbatim from PlaybackPanel.svelte's video mode. All state comes in as
  // props; actions bubble as callbacks.
  import { AlertTriangle, Loader2, RefreshCw, Wrench } from 'lucide-svelte';
  import { t } from '$lib/i18n';

  interface Props {
    errorMsg: string;
    showTranscode: boolean;
    transcoding: boolean;
    transcodeError: string;
    retryCount: number;
    maxRetries: number;
    repairRunning: boolean;
    repairNote: string;
    ontranscode: () => void;
    onretry: () => void;
    onrepair: () => void;
  }

  let {
    errorMsg,
    showTranscode,
    transcoding,
    transcodeError,
    retryCount,
    maxRetries,
    repairRunning,
    repairNote,
    ontranscode,
    onretry,
    onrepair,
  } = $props();
</script>

<div class="absolute inset-0 bg-black/80 flex flex-col items-center justify-center z-20 p-6">
  <AlertTriangle size={48} class="th-color-danger mb-3" />
  <p class="text-white text-center text-sm mb-4">{errorMsg}</p>
  {#if showTranscode}
    {#if transcoding}
      <div class="flex items-center gap-2 text-white/80 mb-3">
        <Loader2 size={16} class="animate-spin" />
        <span class="text-sm">{t('detail.transcodingForPlayback')}</span>
      </div>
    {:else}
      <button onclick={ontranscode} class="btn btn-primary btn-sm flex items-center gap-1 mb-3">
        <RefreshCw size={14} />
        {t('detail.transcodeToH264')}
      </button>
    {/if}
    {#if transcodeError}
      <p class="text-red-400 text-xs mb-3">{transcodeError}</p>
    {/if}
  {/if}
  {#if retryCount < maxRetries}
    <button onclick={onretry} class="btn btn-primary btn-sm flex items-center gap-1">
      <RefreshCw size={14} />
      {retryCount > 0 ? t('detail.videoRetrying', { count: String(retryCount), max: String(maxRetries) }) : t('common.retry')}
    </button>
  {:else}
    <p class="text-white/70 text-xs mb-3">{t('detail.videoMaxRetries')}</p>
  {/if}
  {#if repairRunning}
    <div class="flex items-center gap-2 text-white/80 mt-3">
      <Loader2 size={16} class="animate-spin" />
      <span class="text-sm">{t('detail.repairing')}</span>
    </div>
  {:else}
    <button onclick={onrepair} class="btn btn-outline btn-sm flex items-center gap-1 text-white border-white/30 hover:bg-white/10 mt-3">
      <Wrench size={14} />
      {t('detail.tryRepair')}
    </button>
  {/if}
  {#if repairNote}
    <p class="text-white/80 text-xs text-center max-w-md mt-2">{repairNote}</p>
  {/if}
</div>
