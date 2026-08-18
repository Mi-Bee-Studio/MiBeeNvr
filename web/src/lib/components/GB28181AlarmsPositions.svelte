<script lang="ts">
  // GB28181 alarm notifications + mobile-position reports (#380): the backend
  // subscription ring buffers (#349) surfaced in the device-recordings tab.
  // Polled — alarm SSE remains future work.
  import { onMount, onDestroy } from 'svelte';
  import { getGB28181Alarms, getGB28181Positions } from '$lib/api';
  import type { GB28181Alarm, GB28181Position } from '$lib/api';
  import { t } from '$lib/i18n';
  import { BellRing, MapPin, Gauge } from 'lucide-svelte';

  let { deviceId }: { deviceId: string } = $props();

  let alarms = $state<GB28181Alarm[]>([]);
  let positions = $state<GB28181Position[]>([]);
  let alarmTimer: ReturnType<typeof setInterval> | undefined;
  let posTimer: ReturnType<typeof setInterval> | undefined;

  async function loadAlarms() {
    if (!deviceId) return;
    try {
      alarms = (await getGB28181Alarms(deviceId)) ?? [];
    } catch {
      alarms = [];
    }
  }

  async function loadPositions() {
    if (!deviceId) return;
    try {
      positions = (await getGB28181Positions(deviceId)) ?? [];
    } catch {
      positions = [];
    }
  }

  function priorityClass(p?: string): string {
    switch (p) {
      case '1':
        return 'text-red-500';
      case '2':
        return 'text-yellow-500';
      default:
        return 'th-text-secondary';
    }
  }

  function fmtTime(iso?: string): string {
    if (!iso) return '—';
    const d = new Date(iso);
    return isNaN(d.getTime()) ? iso : d.toLocaleString();
  }

  onMount(() => {
    loadAlarms();
    loadPositions();
    alarmTimer = setInterval(loadAlarms, 10000);
    posTimer = setInterval(loadPositions, 15000);
  });
  onDestroy(() => {
    if (alarmTimer) clearInterval(alarmTimer);
    if (posTimer) clearInterval(posTimer);
  });
</script>

<div class="grid gap-4 md:grid-cols-2">
  <div class="card border th-border p-4">
    <div class="flex items-center gap-2 mb-3">
      <BellRing size={16} class="th-text-primary" />
      <span class="text-sm font-medium th-text-primary">{t('gb28181.alarms.title')}</span>
      {#if alarms.length > 0}
        <span class="text-xs th-text-tertiary">{alarms.length}</span>
      {/if}
    </div>
    {#if alarms.length === 0}
      <p class="text-xs th-text-tertiary">{t('gb28181.alarms.empty')}</p>
    {:else}
      <div class="max-h-64 overflow-y-auto space-y-1.5">
        {#each alarms.slice(0, 30) as a (a.received_at)}
          <div class="flex items-baseline gap-2 text-xs py-1 border-b th-border last:border-0">
            <span class={priorityClass(a.alarm_priority)}>
              {a.alarm_priority === '1' ? t('gb28181.alarms.p1') : a.alarm_priority === '2' ? t('gb28181.alarms.p2') : t('gb28181.alarms.p3')}
            </span>
            <span class="th-text-secondary flex-1 truncate">
              {a.alarm_description || a.alarm_method || a.alarm_type || t('gb28181.alarms.unknown')}
            </span>
            <span class="th-text-tertiary whitespace-nowrap">{fmtTime(a.alarm_time || a.received_at)}</span>
          </div>
        {/each}
      </div>
    {/if}
  </div>

  <div class="card border th-border p-4">
    <div class="flex items-center gap-2 mb-3">
      <MapPin size={16} class="th-text-primary" />
      <span class="text-sm font-medium th-text-primary">{t('gb28181.positions.title')}</span>
      {#if positions.length > 0}
        <span class="text-xs th-text-tertiary">{positions.length}</span>
      {/if}
    </div>
    {#if positions.length === 0}
      <p class="text-xs th-text-tertiary">{t('gb28181.positions.empty')}</p>
    {:else}
      <div class="max-h-64 overflow-y-auto space-y-1.5">
        {#each positions.slice(0, 30) as p (p.time + p.updated_at) }
          <div class="flex items-center gap-2 text-xs py-1 border-b th-border last:border-0">
            <Gauge size={12} class="th-text-tertiary shrink-0" />
            <span class="th-text-secondary flex-1">
              {Number(p.longitude).toFixed(5)}, {Number(p.latitude).toFixed(5)}
            </span>
            {#if p.speed && Number(p.speed) > 0}
              <span class="th-text-tertiary">{(Number(p.speed) * 3.6).toFixed(0)} km/h</span>
            {/if}
            <span class="th-text-tertiary whitespace-nowrap">{fmtTime(p.time)}</span>
          </div>
        {/each}
      </div>
    {/if}
  </div>
</div>
