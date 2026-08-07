<script lang="ts">
  // MergePanel owns ALL merge state + SSE/polling logic, extracted from
  // RecordingDetail.svelte (#136). It is a mostly headless controller: it owns
  // the state machine (SSE subscription with exponential backoff reconnect,
  // DB polling fallback, sessionStorage cross-page tracking, cancel confirm
  // dialog) and exposes progress through an onprogress callback + exported
  // query methods. The host owns the merge UI surfaces (inline timelapse
  // controls, actions row, info badge) and reads state from this component;
  // the only markup here is the cancel-merge ConfirmDialog.
  import { onDestroy } from 'svelte';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import type { Recording } from '$lib/api';
  import {
    getRecording,
    triggerTimelapseMerge,
    retryRecordingMerge,
    subscribeTimelapseMergeProgress,
    cancelMerge,
  } from '$lib/api';
  import {
    saveMergeState,
    clearMergeState,
    getMergeStateForCamera,
    computeMergeEta,
  } from '$lib/recording/merge-utils';

  interface Props {
    recording: Recording | null;
    currentId: string;
    /** Fired when a merge completes/fails/cancels so the host can reload (re-probe codec). */
    onmergecompleted?: () => void;
    /** Fired whenever progress state changes so the host UI surfaces can re-render. */
    onprogress?: (info: { inProgress: boolean; pct: number; eta: string; error: string }) => void;
  }

  let { recording, currentId, onmergecompleted, onprogress } = $props();

  // --- Merge state (mirrors the original RecordingDetail declarations) ---
  let mergeInProgress = $state(false);
  let mergeProgressPct = $state(0);
  let mergeErrorMsg = $state('');
  let mergeAbortController = $state<AbortController | null>(null);
  let selectedMergeDuration = $state('1h');
  let mergeStartTime = $state(0);
  let mergeEta = $state('');
  let mergeReconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let cancelMergeConfirm = $state(false);
  let mergePollTimer: ReturnType<typeof setInterval> | null = null;

  // Push progress changes to the host (idempotent — runs on each state change).
  $effect(() => {
    onprogress?.({ inProgress: mergeInProgress, pct: mergeProgressPct, eta: mergeEta, error: mergeErrorMsg });
  });

  // Re-establish merge tracking whenever the host loads a new recording.
  // Mirrors the original restoreMergeState() called inside the host's loadRecording.
  $effect(() => {
    if (recording) restoreMergeState(recording);
  });

  function restoreMergeState(rec: Recording) {
    const stored = getMergeStateForCamera(rec.camera_id);
    if (stored && stored.status === 'pending' && stored.progress < 100) {
      mergeInProgress = true;
      mergeProgressPct = stored.progress;
      mergeErrorMsg = '';
      startMergeSse(rec.camera_id, rec.id);
      startMergePolling(rec.id, rec.camera_id);
      return;
    }
    if (rec.merge_status === 'pending' && rec.merge_progress != null && rec.merge_progress > 0 && rec.merge_progress < 100) {
      mergeInProgress = true;
      mergeProgressPct = rec.merge_progress;
      mergeErrorMsg = '';
      startMergeSse(rec.camera_id, rec.id);
      startMergePolling(rec.id, rec.camera_id);
      saveMergeState({ cameraId: rec.camera_id, recordingId: rec.id, progress: rec.merge_progress, status: 'pending' });
    }
    if (rec.merge_status === 'failed' && rec.merge_error) {
      mergeErrorMsg = rec.merge_error;
    }
  }

  function startMergeSse(cameraId: string, recordingId: string) {
    let reconnectAttempt = 0;
    const maxBackoff = 30000;

    function connect() {
      mergeAbortController?.abort();
      mergeAbortController = null;
      if (!mergeInProgress) return;

      const ac = subscribeTimelapseMergeProgress(
        cameraId,
        (data) => {
          reconnectAttempt = 0;
          if (data.status === 'completed') {
            stopMergePolling();
            mergeInProgress = false;
            mergeProgressPct = 100;
            mergeEta = '';
            mergeAbortController = null;
            clearMergeState(cameraId);
            onmergecompleted?.();
            showToast(t('detail.mergeCompleted'), 'success');
          } else if (data.status === 'failed') {
            stopMergePolling();
            mergeInProgress = false;
            mergeEta = '';
            mergeErrorMsg = data.error || '';
            clearMergeState(cameraId);
            showToast(t('detail.mergeFailed', { error: data.error || '' }), 'error');
          } else if (data.progress !== undefined) {
            mergeProgressPct = data.progress;
            updateMergeEta();
            saveMergeState({ cameraId, recordingId, progress: data.progress, status: 'pending' });
          }
        },
        () => scheduleReconnect(cameraId, recordingId),
      );
      mergeAbortController = ac;
    }

    function scheduleReconnect(cameraId: string, recordingId: string) {
      if (!mergeInProgress) return;
      const delay = Math.min(maxBackoff, 1000 * Math.pow(2, reconnectAttempt));
      reconnectAttempt++;
      if (mergeReconnectTimer) clearTimeout(mergeReconnectTimer);
      mergeReconnectTimer = setTimeout(async () => {
        if (!mergeInProgress) return;
        try {
          const rec = await getRecording(recordingId);
          if (!rec) return;
          if (rec.merge_status === 'merged') {
            stopMergePolling();
            mergeInProgress = false;
            mergeProgressPct = 100;
            mergeEta = '';
            clearMergeState(cameraId);
            onmergecompleted?.();
            showToast(t('detail.mergeCompleted'), 'success');
            return;
          }
          if (rec.merge_status === 'failed') {
            stopMergePolling();
            mergeInProgress = false;
            mergeEta = '';
            mergeErrorMsg = rec.merge_error || '';
            clearMergeState(cameraId);
            showToast(t('detail.mergeFailed', { error: mergeErrorMsg }), 'error');
            return;
          }
          if (rec.merge_progress > 0) {
            mergeProgressPct = rec.merge_progress;
            updateMergeEta();
          }
        } catch { /* swallow — will retry on next connect */ }
        connect();
      }, delay);
    }

    connect();
  }

  async function handleMergeAndPlay() {
    if (!recording) return;
    mergeInProgress = true;
    mergeProgressPct = 0;
    mergeErrorMsg = '';
    mergeStartTime = Date.now();
    mergeEta = '';
    saveMergeState({ cameraId: recording.camera_id, recordingId: recording.id, progress: 0, status: 'pending' });
    try {
      if (recording.format === 'timelapse') {
        await retryRecordingMerge(recording.id);
      } else {
        await triggerTimelapseMerge(recording.camera_id, undefined, selectedMergeDuration);
      }
      startMergeSse(recording.camera_id, recording.id);
      startMergePolling(recording.id, recording.camera_id);
    } catch (e) {
      mergeInProgress = false;
      mergeErrorMsg = e instanceof Error ? e.message : 'Failed to start merge';
      clearMergeState(recording.camera_id);
    }
  }

  function startMergePolling(recId: string, camId: string) {
    stopMergePolling();
    let attempts = 0;
    const maxAttempts = 60; // 2 min at 2s
    mergePollTimer = setInterval(async () => {
      attempts++;
      try {
        const rec = await getRecording(recId);
        if (!rec) { stopMergePolling(); return; }
        if (rec.merge_progress > 0 && rec.merge_progress < 100) {
          mergeProgressPct = rec.merge_progress;
          attempts = 0;
        }
        if (rec.merge_status === 'merged') {
          stopMergePolling();
          mergeInProgress = false;
          mergeProgressPct = 100;
          mergeAbortController?.abort();
          mergeAbortController = null;
          clearMergeState(camId);
          onmergecompleted?.();
          showToast(t('detail.mergeCompleted'), 'success');
        } else if (rec.merge_status === 'failed') {
          stopMergePolling();
          mergeInProgress = false;
          mergeErrorMsg = rec.merge_error || 'Merge failed';
          mergeAbortController?.abort();
          mergeAbortController = null;
          clearMergeState(camId);
          showToast(t('detail.mergeFailed', { error: mergeErrorMsg }), 'error');
        } else if (attempts >= maxAttempts) {
          stopMergePolling();
          mergeInProgress = false;
          mergeErrorMsg = 'Merge timed out — no progress detected';
          clearMergeState(camId);
        }
      } catch { /* retry next interval */ }
    }, 2000);
  }

  function stopMergePolling() {
    if (mergePollTimer) { clearInterval(mergePollTimer); mergePollTimer = null; }
  }

  async function handleCancelMerge() {
    if (!recording) return;
    cancelMergeConfirm = false;
    try {
      await cancelMerge(recording.camera_id);
      mergeInProgress = false;
      mergeProgressPct = 0;
      mergeEta = '';
      mergeErrorMsg = '';
      mergeAbortController?.abort();
      mergeAbortController = null;
      if (mergeReconnectTimer) { clearTimeout(mergeReconnectTimer); mergeReconnectTimer = null; }
      stopMergePolling();
      clearMergeState(recording.camera_id);
      onmergecompleted?.();
      showToast(t('detail.mergeCancelled'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : 'Failed to cancel merge', 'error');
    }
  }

  function updateMergeEta() {
    mergeEta = computeMergeEta(mergeStartTime, mergeProgressPct);
  }

  // --- Public API (host uses these for the merge UI surfaces + keyboard + cleanup) ---
  export function isInProgress() { return mergeInProgress; }
  export function getProgress() {
    return { inProgress: mergeInProgress, pct: mergeProgressPct, eta: mergeEta, error: mergeErrorMsg };
  }
  export function getSelectedDuration() { return selectedMergeDuration; }
  export function setSelectedDuration(d: string) { selectedMergeDuration = d; }
  export function startMerge() { return handleMergeAndPlay(); }
  export function requestCancel() { if (mergeInProgress) cancelMergeConfirm = true; }
  export function handleKeyAction(key: string) {
    if (key === 'c' && mergeInProgress) cancelMergeConfirm = true;
  }
  export function teardown() {
    if (mergeInProgress && recording) {
      saveMergeState({ cameraId: recording.camera_id, recordingId: recording.id, progress: mergeProgressPct, status: 'pending' });
    }
    mergeAbortController?.abort();
    mergeAbortController = null;
    if (mergeReconnectTimer) { clearTimeout(mergeReconnectTimer); mergeReconnectTimer = null; }
    stopMergePolling();
  }

  onDestroy(() => teardown());
</script>

<ConfirmDialog
  open={cancelMergeConfirm}
  title={t('detail.cancelMerge')}
  message={t('detail.cancelMergeConfirm')}
  confirmText={t('detail.cancelMerge')}
  variant="danger"
  onconfirm={handleCancelMerge}
  oncancel={() => (cancelMergeConfirm = false)}
/>
