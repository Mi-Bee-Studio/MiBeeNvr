<script lang="ts">
  import { onMount } from 'svelte';
  import { apiRequest } from '$lib/api';
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
    last_event?: string;
    last_error?: string;
    consecutive_poll_errors?: number;
  }

  let status = $state<SubStatus | null>(null);

  onMount(() => {
    let stopped = false;
    const load = async () => {
      try {
        // apiRequest carries the Authorization header (getAuthHeader returns
        // string | null — spreading it into a Headers-init object produced
        // {0:'B',1:'e',…} and sent NO auth, so every poll 401'd silently).
        const data = await apiRequest<SubStatus>(
          `/cameras/${encodeURIComponent(cameraId)}/onvif-events`
        );
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
    {#if status?.last_event}
      <span class="truncate max-w-[240px]" title={status.last_event}>
        · {t('cameras.motionSubLastRaw')}: {status.last_event}
      </span>
    {/if}
  {/if}
  {#if status?.last_error}
    <span class="text-amber-500" title={status.last_error}>
      · {t('cameras.motionSubError')}: {status.last_error.slice(0, 80)}
    </span>
  {/if}
</p>
