<script lang="ts">
  import { onMount } from 'svelte';
  import { t } from '$lib/i18n';
  import { getAuthHeader } from '$lib/api';
  import { Activity, Circle, Trash2 } from 'lucide-svelte';

  interface ONVIFEvent {
    topic: string;
    timestamp: string;
    data: Record<string, unknown>;
    camera_id: string;
    camera_name?: string;
  }

  interface Props {
    maxEvents?: number;
    cameraId?: string;
  }

  let { maxEvents = 100, cameraId = '' }: Props = $props();

  let events = $state<ONVIFEvent[]>([]);
  let connected = $state(false);
  let connecting = $state(true);
  let error = $state('');
  let eventListEl: HTMLDivElement | undefined = $state();
  let showMotionOnly = $state(false);

  let filteredEvents = $derived.by(() => {
    if (!showMotionOnly) return events;
    return events.filter(e =>
      e.topic.toLowerCase().includes('motion') ||
      e.topic.toLowerCase().includes('motionalarm') ||
      e.topic.toLowerCase().includes('cellmotion')
    );
  });

  function isMotionEvent(event: ONVIFEvent): boolean {
    const topic = event.topic.toLowerCase();
    return topic.includes('motion') || topic.includes('motionalarm') || topic.includes('cellmotion');
  }

  function formatTime(ts: string): string {
    try {
      const d = new Date(ts);
      return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    } catch {
      return ts;
    }
  }

  function formatDate(ts: string): string {
    try {
      const d = new Date(ts);
      return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
    } catch {
      return '';
    }
  }

  function formatTopic(topic: string): string {
    // Extract last meaningful segment from ONVIF topic path
    const parts = topic.split('/');
    return parts[parts.length - 1] || topic;
  }

  function clearEvents() {
    events = [];
  }

  function scrollToBottom() {
    if (eventListEl) {
      eventListEl.scrollTop = eventListEl.scrollHeight;
    }
  }

  $effect(() => {
    let es: EventSource | null = null;
    let reconnectTimer: ReturnType<typeof setTimeout>;

    function connect() {
      connecting = true;
      error = '';

      const url = cameraId
        ? `/api/events?filter=onvif.&camera_id=${encodeURIComponent(cameraId)}`
        : `/api/events?filter=onvif.`;

      es = new EventSource(url);

      es.onopen = () => {
        connected = true;
        connecting = false;
        error = '';
      };

      es.onmessage = (e) => {
        try {
          const event: ONVIFEvent = JSON.parse(e.data);
          // Filter for ONVIF events by topic prefix
          if (event.topic && event.topic.startsWith('onvif.')) {
            events = [...events.slice(-(maxEvents - 1)), event];
            // Auto-scroll after update
            requestAnimationFrame(scrollToBottom);
          }
        } catch {
          // Ignore malformed events
        }
      };

      // Handle named event types
      es.addEventListener('onvif.event', (e) => {
        try {
          const event: ONVIFEvent = JSON.parse((e as MessageEvent).data);
          events = [...events.slice(-(maxEvents - 1)), event];
          requestAnimationFrame(scrollToBottom);
        } catch {
          // Ignore
        }
      });

      es.onerror = () => {
        connected = false;
        connecting = false;
        es?.close();
        // Retry connection after delay
        reconnectTimer = setTimeout(connect, 5000);
      };
    }

    connect();

    return () => {
      es?.close();
      clearTimeout(reconnectTimer);
    };
  });
</script>

<div class="onvif-events-panel">
  <div class="onvif-events-header">
    <div class="onvif-events-title-row">
      <Activity size={16} class="onvif-events-icon" />
      <h4 class="onvif-events-title">{t('onvif.events.title')}</h4>
      {#if connected}
        <span class="onvif-events-badge onvif-events-badge-live">
          <Circle size={6} class="onvif-events-live-dot" />
          {t('onvif.events.live')}
        </span>
      {:else if connecting}
        <span class="onvif-events-badge onvif-events-badge-connecting">
          <span class="onvif-events-spinner"></span>
          {t('onvif.events.connecting')}
        </span>
      {:else}
        <span class="onvif-events-badge onvif-events-badge-disconnected">
          {t('onvif.events.disconnected')}
        </span>
      {/if}
    </div>

    <div class="onvif-events-actions">
      <button
        class="btn btn-ghost btn-sm"
        onclick={() => showMotionOnly = !showMotionOnly}
        class:btn-primary={showMotionOnly}
        title="Toggle motion filter"
      >
        <Circle size={10} fill={showMotionOnly ? 'currentColor' : 'none'} />
        {t('onvif.events.motion')}
      </button>
      <button
        class="btn btn-ghost btn-sm"
        onclick={clearEvents}
        disabled={events.length === 0}
        title={t('onvif.events.clear')}
      >
        <Trash2 size={14} />
      </button>
    </div>
  </div>

  <div class="onvif-events-list" bind:this={eventListEl}>
    {#if filteredEvents.length === 0}
      <div class="onvif-events-empty">
        <Activity size={32} class="onvif-events-empty-icon" />
        <p class="onvif-events-empty-text">{t('onvif.events.noEvents')}</p>
        <p class="onvif-events-empty-desc">{t('onvif.events.noEventsDesc')}</p>
      </div>
    {:else}
      {#each filteredEvents as event, i (i)}
        <div class="onvif-event-item" class:onvif-event-motion={isMotionEvent(event)}>
          <div class="onvif-event-indicator">
            {#if isMotionEvent(event)}
              <span class="onvif-event-motion-dot"></span>
            {:else}
              <span class="onvif-event-generic-dot"></span>
            {/if}
          </div>
          <div class="onvif-event-content">
            <div class="onvif-event-top-row">
              <span class="onvif-event-type">
                {isMotionEvent(event) ? t('onvif.events.motion') : formatTopic(event.topic)}
              </span>
              <span class="onvif-event-camera">{event.camera_name || event.camera_id}</span>
            </div>
            <div class="onvif-event-bottom-row">
              <span class="onvif-event-topic">{event.topic}</span>
              <span class="onvif-event-time">
                {#if formatDate(event.timestamp)}
                  {formatDate(event.timestamp)}{' '}
                {/if}
                {formatTime(event.timestamp)}
              </span>
            </div>
          </div>
        </div>
      {/each}
    {/if}
  </div>
</div>

<style>
  .onvif-events-panel {
    display: flex;
    flex-direction: column;
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    overflow: hidden;
    background-color: var(--bg-elevated);
  }

  .onvif-events-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.75rem 1rem;
    border-bottom: 1px solid var(--border);
    background-color: var(--bg-secondary);
  }

  .onvif-events-title-row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }



  .onvif-events-title {
    font-size: 0.8125rem;
    font-weight: 600;
    color: var(--text-primary);
    margin: 0;
  }

  .onvif-events-badge {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    font-size: 0.6875rem;
    font-weight: 500;
    padding: 0.125rem 0.5rem;
    border-radius: 9999px;
  }

  .onvif-events-badge-live {
    color: var(--color-success);
    background-color: rgba(16, 185, 129, 0.1);
  }

  .onvif-events-badge-connecting {
    color: var(--color-warning);
    background-color: rgba(245, 158, 11, 0.1);
  }

  .onvif-events-badge-disconnected {
    color: var(--color-danger);
    background-color: rgba(239, 68, 68, 0.1);
  }



  @keyframes pulse-dot {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.3; }
  }

  .onvif-events-spinner {
    width: 0.625rem;
    height: 0.625rem;
    border: 1.5px solid var(--color-warning);
    border-top-color: transparent;
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }

  .onvif-events-actions {
    display: flex;
    gap: 0.25rem;
  }

  .onvif-events-list {
    max-height: 20rem;
    overflow-y: auto;
    scrollbar-width: thin;
  }

  .onvif-events-empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 2rem 1rem;
    text-align: center;
  }



  .onvif-events-empty-text {
    font-size: 0.875rem;
    font-weight: 500;
    color: var(--text-secondary);
    margin: 0;
  }

  .onvif-events-empty-desc {
    font-size: 0.75rem;
    color: var(--text-tertiary);
    margin: 0.25rem 0 0;
  }

  .onvif-event-item {
    display: flex;
    gap: 0.75rem;
    padding: 0.625rem 1rem;
    border-bottom: 1px solid var(--border);
    transition: background-color var(--duration-fast) var(--ease-out);
  }

  .onvif-event-item:last-child {
    border-bottom: none;
  }

  .onvif-event-item:hover {
    background-color: var(--bg-hover);
  }

  .onvif-event-motion {
    background-color: rgba(16, 185, 129, 0.04);
  }

  .onvif-event-motion:hover {
    background-color: rgba(16, 185, 129, 0.08);
  }

  .onvif-event-indicator {
    display: flex;
    align-items: flex-start;
    padding-top: 0.25rem;
  }

  .onvif-event-motion-dot {
    width: 0.5rem;
    height: 0.5rem;
    border-radius: 50%;
    background-color: var(--color-success);
    animation: blink 1.2s ease-in-out infinite;
    box-shadow: 0 0 6px rgba(16, 185, 129, 0.4);
  }

  .onvif-event-generic-dot {
    width: 0.375rem;
    height: 0.375rem;
    border-radius: 50%;
    background-color: var(--text-tertiary);
  }

  @keyframes blink {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.2; }
  }

  .onvif-event-content {
    flex: 1;
    min-width: 0;
  }

  .onvif-event-top-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
  }

  .onvif-event-type {
    font-size: 0.8125rem;
    font-weight: 500;
    color: var(--text-primary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .onvif-event-motion .onvif-event-type {
    color: var(--color-success);
  }

  .onvif-event-camera {
    font-size: 0.6875rem;
    font-weight: 500;
    color: var(--text-tertiary);
    white-space: nowrap;
    flex-shrink: 0;
  }

  .onvif-event-bottom-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
    margin-top: 0.125rem;
  }

  .onvif-event-topic {
    font-size: 0.6875rem;
    color: var(--text-tertiary);
    font-family: monospace;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .onvif-event-time {
    font-size: 0.6875rem;
    color: var(--text-tertiary);
    white-space: nowrap;
    flex-shrink: 0;
    font-variant-numeric: tabular-nums;
  }
</style>
