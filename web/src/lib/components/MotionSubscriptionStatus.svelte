<script lang="ts">
  import { onMount } from 'svelte';
  import { API_BASE, getAuthHeader } from '$lib/api';
  import { t } from '$lib/i18n';

  interface Props {
    cameraId: string;
  }

  let { cameraId }: Props = $props();

  interface SubStatus {
    subscribed?: boolean;
    state?: string;
    event_count?: number;
    last_event_at?: string;
    last_error?: string;
    consecutive_poll_errors?: number;
  }

  let status = $state<SubStatus | null>(null);

  onMount(() => {
    let stopped = false;
    const load = async () => {
      try {
        const resp = await fetch(`${API_BASE}/cameras/${encodeURIComponent(cameraId)}/onvif-events`, {
          headers: { ...getAuthHeader() }
        });
        if (!resp.ok) return;
        const data = await resp.json();
        if (!stopped) status = data;
      } catch {
        // transient — next poll retries
      }
    };
    load();
    const timer = setInterval(load, 15_000);
    return () => {
      stopped = true;
      clearInterval(timer);
    };
  });

  let stateLabel = $derived.by(() => {
    if (!status) return t('cameras.motionSubLoading');
    if (status.state === 'active') return t('cameras.motionSubActive');
    if (status.state === 'resubscribing') return t('cameras.motionSubResubscribing');
    if (status.state === 'unsupported') return t('cameras.motionSubUnsupported');
    return t('cameras.motionSubIdle');
  });

  let lastEvent = $derived.by(() => {
    if (!status?.last_event_at) return '';
    const d = new Date(status.last_event_at);
    if (isNaN(d.getTime())) return '';
    return d.toLocaleTimeString();
  });
</script>

<p class="text-xs th-text-muted mt-1 flex flex-wrap items-center gap-x-2">
  <span>{t('cameras.motionSubState')}: {stateLabel}</span>
  {#if status?.state === 'active'}
    <span>· {t('cameras.motionSubEvents')}: {status.event_count ?? 0}</span>
    {#if lastEvent}
      <span>· {t('cameras.motionSubLastEvent')}: {lastEvent}</span>
    {/if}
  {/if}
  {#if status?.last_error}
    <span class="text-amber-500" title={status.last_error}>
      · {t('cameras.motionSubError')}: {status.last_error.slice(0, 80)}
    </span>
  {/if}
</p>
