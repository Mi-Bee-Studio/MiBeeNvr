<script lang="ts">
  import { onMount } from 'svelte';
  import {
    listGB28181Devices,
    listGB28181Channels,
    catalogRefreshGB28181,
    inviteGB28181Channel,
    byeGB28181Channel,
    gb28181PtzMove,
  } from '$lib/api';
  import type { GB28181Device, GB28181Channel } from '$lib/api';
  import { getGB28181Enabled, getGB28181Loaded, refreshGB28181Status } from '$lib/gb28181-status';
  import { t } from '$lib/i18n';
  import { formatDate } from '$lib/format';
  import { showToast } from '$lib/toast';
  import { AlertCircle, ChevronDown, ChevronLeft, ChevronRight, ChevronUp, RefreshCw, Settings, Square, Video, VideoOff, ZoomIn, ZoomOut } from 'lucide-svelte';

  let devices = $state<GB28181Device[]>([]);
  let loading = $state(true);
  let error = $state('');
  let expandedDevice = $state<string | null>(null);
  let channelsByDevice = $state<Record<string, GB28181Channel[]>>({});
  let channelsLoading = $state<Record<string, boolean>>({});
  let busyChannel = $state<Record<string, boolean>>({});
  let busyDevice = $state<Record<string, boolean>>({});
  // Active PTZ direction per channel while a direction button is held.
  let ptzActiveDir = $state<Record<string, string>>({});

  const gb28181Enabled = $derived(getGB28181Enabled());
  const gb28181Loaded = $derived(getGB28181Loaded());

  async function loadDevices() {
    loading = true;
    error = '';
    try {
      devices = await listGB28181Devices(100);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }

  async function toggleDevice(deviceId: string) {
    if (expandedDevice === deviceId) {
      expandedDevice = null;
      return;
    }
    expandedDevice = deviceId;
    if (channelsByDevice[deviceId]) return;
    channelsLoading = { ...channelsLoading, [deviceId]: true };
    try {
      channelsByDevice = { ...channelsByDevice, [deviceId]: await listGB28181Channels(deviceId) };
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(e), 'error');
    } finally {
      channelsLoading = { ...channelsLoading, [deviceId]: false };
    }
  }

  async function refreshCatalog(deviceId: string) {
    busyDevice = { ...busyDevice, [deviceId]: true };
    try {
      await catalogRefreshGB28181(deviceId);
      showToast(t('gb28181Devices.catalogRefreshSuccess'), 'success');
      // Catalog refresh may change the channel list — reload it if expanded.
      if (expandedDevice === deviceId) {
        channelsByDevice = { ...channelsByDevice, [deviceId]: await listGB28181Channels(deviceId) };
      }
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(e), 'error');
    } finally {
      busyDevice = { ...busyDevice, [deviceId]: false };
    }
  }

  async function inviteChannel(channelId: string) {
    busyChannel = { ...busyChannel, [channelId]: true };
    try {
      await inviteGB28181Channel(channelId);
      showToast(t('gb28181Devices.inviteSuccess'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(e), 'error');
    } finally {
      busyChannel = { ...busyChannel, [channelId]: false };
    }
  }

  async function byeChannel(channelId: string) {
    busyChannel = { ...busyChannel, [channelId]: true };
    try {
      await byeGB28181Channel(channelId);
      showToast(t('gb28181Devices.byeSuccess'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(e), 'error');
    } finally {
      busyChannel = { ...busyChannel, [channelId]: false };
    }
  }

  // GB28181 PTZ is command-driven: send a move at speed while a direction
  // button is held, send the stop command on release.
  async function ptzMove(channelId: string, direction: string) {
    ptzActiveDir = { ...ptzActiveDir, [channelId]: direction };
    busyChannel = { ...busyChannel, [channelId]: true };
    try {
		await gb28181PtzMove(channelId, direction, { speed: 128 });
    } catch (e) {
      showToast(`${t('gb28181Devices.ptzError')}: ${e instanceof Error ? e.message : String(e)}`, 'error');
    } finally {
      busyChannel = { ...busyChannel, [channelId]: false };
    }
  }

  async function ptzStop(channelId: string) {
    if (!ptzActiveDir[channelId]) return;
    ptzActiveDir = { ...ptzActiveDir, [channelId]: '' };
    busyChannel = { ...busyChannel, [channelId]: true };
    try {
		await gb28181PtzMove(channelId, 'stop');
    } catch (e) {
      showToast(`${t('gb28181Devices.ptzError')}: ${e instanceof Error ? e.message : String(e)}`, 'error');
    } finally {
      busyChannel = { ...busyChannel, [channelId]: false };
    }
  }

  // Explicit stop (pad center button) — always sends stop even when no
  // direction button is currently held (e.g. a stuck camera).
  async function ptzStopForce(channelId: string) {
    ptzActiveDir = { ...ptzActiveDir, [channelId]: '' };
    busyChannel = { ...busyChannel, [channelId]: true };
    try {
		await gb28181PtzMove(channelId, 'stop');
    } catch (e) {
      showToast(`${t('gb28181Devices.ptzError')}: ${e instanceof Error ? e.message : String(e)}`, 'error');
    } finally {
      busyChannel = { ...busyChannel, [channelId]: false };
    }
  }

  function deviceStatusLabel(status: string): string {
    return status === 'online' ? t('gb28181Devices.deviceStatus.online') : t('gb28181Devices.deviceStatus.offline');
  }

  function channelStatusLabel(status: string): string {
    switch (status) {
      case 'inviting':
        return t('gb28181Devices.channelStatus.inviting');
      case 'playing':
        return t('gb28181Devices.channelStatus.playing');
      default:
        return t('gb28181Devices.channelStatus.idle');
    }
  }

  function channelStatusClass(status: string): string {
    switch (status) {
      case 'inviting':
        return 'badge badge-warning';
      case 'playing':
        return 'badge badge-success';
      default:
        return 'badge badge-muted';
    }
  }

  onMount(() => {
    void refreshGB28181Status().then(() => {
      if (getGB28181Enabled()) void loadDevices();
    });
  });
</script>

<div class="page-container">
  <div class="page-header">
    <div>
      <h1 class="page-title">{t('gb28181Devices.title')}</h1>
      <p class="page-subtitle">{t('gb28181Devices.subtitle')}</p>
    </div>
    {#if gb28181Enabled}
      <button class="btn btn-ghost" onclick={loadDevices} disabled={loading}>
        <RefreshCw size={16} class={loading ? 'spin' : undefined} />
        {t('gb28181Devices.refresh')}
      </button>
    {/if}
  </div>

  {#if gb28181Loaded && !gb28181Enabled}
    <div class="empty-state">
      <Settings size={40} class="empty-icon" />
      <h2>{t('gb28181Devices.notEnabled')}</h2>
      <p>{t('gb28181Devices.notEnabledHint')}</p>
      <a href="#/settings" class="btn btn-primary">{t('gb28181Devices.goToSettings')}</a>
    </div>
  {:else if loading}
    <div class="loading-state">{t('gb28181Devices.loading')}</div>
  {:else if error}
    <div class="error-state">
      <AlertCircle size={20} />
      <span>{error}</span>
    </div>
  {:else if devices.length === 0}
    <div class="empty-state">
      <VideoOff size={40} class="empty-icon" />
      <h2>{t('gb28181Devices.empty')}</h2>
      <p>{t('gb28181Devices.emptyHint')}</p>
    </div>
  {:else}
    <div class="device-list">
      {#each devices as device (device.ID)}
        <div class="device-card">
          <div class="device-header-row">
            <button class="device-header" onclick={() => toggleDevice(device.ID)} aria-expanded={expandedDevice === device.ID}>
              <ChevronDown size={18} class={expandedDevice === device.ID ? 'rotate' : undefined} />
              <div class="device-info">
                <span class="device-name">{device.Name || device.ID}</span>
                <span class="device-meta">
                  {device.Manufacturer}{#if device.Model} · {device.Model}{/if}
                </span>
              </div>
              <span class={device.Status === 'online' ? 'badge badge-success' : 'badge badge-muted'}>
                {deviceStatusLabel(device.Status)}
              </span>
              <span class="device-keepalive">
                {t('gb28181Devices.lastKeepalive')}: {formatDate(device.LastKeepalive)}
              </span>
            </button>
            <button
              class="btn btn-ghost btn-sm"
              onclick={() => void refreshCatalog(device.ID)}
              disabled={busyDevice[device.ID]}
              title={t('gb28181Devices.catalogRefresh')}
            >
              <RefreshCw size={14} class={busyDevice[device.ID] ? 'spin' : undefined} />
              {t('gb28181Devices.catalogRefresh')}
            </button>
          </div>

          {#if expandedDevice === device.ID}
            <div class="channel-list">
              {#if channelsLoading[device.ID]}
                <div class="channel-loading">{t('gb28181Devices.loading')}</div>
              {:else if (channelsByDevice[device.ID] || []).length === 0}
                <div class="channel-empty">{t('gb28181Devices.noChannels')}</div>
              {:else}
                {#each channelsByDevice[device.ID] as channel (channel.ID)}
                  <div class="channel-row">
                    <Video size={16} class="channel-icon" />
                    <div class="channel-info">
                      <span class="channel-name">{channel.Name || channel.ID}</span>
                      <span class="channel-meta">{channel.ID}</span>
                    </div>
                    {#if channel.CameraID}
                      <a href="#/cameras/{channel.CameraID}" class="channel-camera">
                        {t('gb28181Devices.boundCamera')}: {channel.CameraID}
                      </a>
                    {/if}
                    <span class={channelStatusClass(channel.Status)}>{channelStatusLabel(channel.Status)}</span>
                    {#if channel.PTZType > 0}
                      <div class="ptz-pad" title={t('ptz.control')}>
                        <div class="ptz-grid">
                          <span class="ptz-cell"></span>
                          <button
                            class="ptz-btn"
                            class:ptz-btn-active={ptzActiveDir[channel.ID] === 'up'}
                            onpointerdown={() => void ptzMove(channel.ID, 'up')}
                            onpointerup={() => void ptzStop(channel.ID)}
                            onpointerleave={() => void ptzStop(channel.ID)}
                            aria-label={t('ptz.up')}
                          >
                            <ChevronUp size={14} />
                          </button>
                          <span class="ptz-cell"></span>
                          <button
                            class="ptz-btn"
                            class:ptz-btn-active={ptzActiveDir[channel.ID] === 'left'}
                            onpointerdown={() => void ptzMove(channel.ID, 'left')}
                            onpointerup={() => void ptzStop(channel.ID)}
                            onpointerleave={() => void ptzStop(channel.ID)}
                            aria-label={t('ptz.left')}
                          >
                            <ChevronLeft size={14} />
                          </button>
                          <button
                            class="ptz-btn ptz-stop"
                            onclick={() => void ptzStopForce(channel.ID)}
                            aria-label={t('ptz.stop')}
                          >
                            <Square size={12} />
                          </button>
                          <button
                            class="ptz-btn"
                            class:ptz-btn-active={ptzActiveDir[channel.ID] === 'right'}
                            onpointerdown={() => void ptzMove(channel.ID, 'right')}
                            onpointerup={() => void ptzStop(channel.ID)}
                            onpointerleave={() => void ptzStop(channel.ID)}
                            aria-label={t('ptz.right')}
                          >
                            <ChevronRight size={14} />
                          </button>
                          <span class="ptz-cell"></span>
                          <button
                            class="ptz-btn"
                            class:ptz-btn-active={ptzActiveDir[channel.ID] === 'down'}
                            onpointerdown={() => void ptzMove(channel.ID, 'down')}
                            onpointerup={() => void ptzStop(channel.ID)}
                            onpointerleave={() => void ptzStop(channel.ID)}
                            aria-label={t('ptz.down')}
                          >
                            <ChevronDown size={14} />
                          </button>
                          <span class="ptz-cell"></span>
                        </div>
                        {#if channel.PTZType === 2}
                          <div class="ptz-zoom">
                            <button
                              class="ptz-btn"
                              class:ptz-btn-active={ptzActiveDir[channel.ID] === 'zoom-in'}
                              onpointerdown={() => void ptzMove(channel.ID, 'zoom-in')}
                              onpointerup={() => void ptzStop(channel.ID)}
                              onpointerleave={() => void ptzStop(channel.ID)}
                              aria-label={t('ptz.zoomIn')}
                            >
                              <ZoomIn size={14} />
                            </button>
                            <button
                              class="ptz-btn"
                              class:ptz-btn-active={ptzActiveDir[channel.ID] === 'zoom-out'}
                              onpointerdown={() => void ptzMove(channel.ID, 'zoom-out')}
                              onpointerup={() => void ptzStop(channel.ID)}
                              onpointerleave={() => void ptzStop(channel.ID)}
                              aria-label={t('ptz.zoomOut')}
                            >
                              <ZoomOut size={14} />
                            </button>
                          </div>
                        {/if}
                      </div>
                    {/if}
                    <div class="channel-actions">
                      <button
                        class="btn btn-primary btn-sm"
                        onclick={() => void inviteChannel(channel.ID)}
                        disabled={busyChannel[channel.ID] || channel.Status === 'playing'}
                      >
                        {t('gb28181Devices.invite')}
                      </button>
                      <button
                        class="btn btn-ghost btn-sm"
                        onclick={() => void byeChannel(channel.ID)}
                        disabled={busyChannel[channel.ID] || channel.Status === 'idle'}
                      >
                        {t('gb28181Devices.bye')}
                      </button>
                    </div>
                  </div>
                {/each}
              {/if}
            </div>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .page-container {
    max-width: 80rem;
    margin: 0 auto;
    padding: 2rem 1rem;
  }

  .page-header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 1rem;
    margin-bottom: 1.5rem;
  }

  .page-title {
    font-size: 1.5rem;
    font-weight: 700;
    margin: 0 0 0.25rem;
  }

  .page-subtitle {
    color: var(--text-secondary);
    font-size: 0.875rem;
    margin: 0;
  }

  .device-list {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  .device-card {
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    overflow: hidden;
  }

  .device-header-row {
    display: flex;
    align-items: center;
  }

  .device-header {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    flex: 1;
    min-width: 0;
    padding: 0.875rem 1rem;
    background: none;
    border: none;
    cursor: pointer;
    color: var(--text-primary);
    text-align: left;
  }

  .device-header:hover {
    background: var(--bg-tertiary);
  }

  .device-info {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-width: 0;
  }

  .device-name {
    font-weight: 600;
    font-size: 0.9375rem;
  }

  .device-meta {
    color: var(--text-secondary);
    font-size: 0.8125rem;
  }

  .device-keepalive {
    color: var(--text-secondary);
    font-size: 0.8125rem;
    white-space: nowrap;
  }

  .channel-list {
    border-top: 1px solid var(--border);
    padding: 0.5rem;
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
  }

  .channel-row {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.625rem 0.75rem;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
  }

  .channel-icon {
    color: var(--text-secondary);
    flex-shrink: 0;
  }

  .channel-info {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-width: 0;
  }

  .channel-name {
    font-weight: 500;
    font-size: 0.875rem;
  }

  .channel-meta {
    color: var(--text-secondary);
    font-size: 0.75rem;
    font-family: monospace;
  }

  .channel-camera {
    color: var(--color-primary);
    font-size: 0.8125rem;
    text-decoration: none;
    white-space: nowrap;
  }

  .channel-camera:hover {
    text-decoration: underline;
  }

  .channel-actions {
    display: flex;
    gap: 0.375rem;
  }

  .channel-loading,
  .channel-empty {
    padding: 1rem;
    color: var(--text-secondary);
    font-size: 0.875rem;
    text-align: center;
  }

  .loading-state,
  .error-state {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 0.5rem;
    padding: 3rem;
    color: var(--text-secondary);
  }

  .error-state {
    color: var(--color-danger, #ef4444);
  }

  .empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.5rem;
    padding: 4rem 1rem;
    text-align: center;
    color: var(--text-secondary);
  }

  .empty-state h2 {
    margin: 0.5rem 0 0;
    color: var(--text-primary);
    font-size: 1.125rem;
  }

  .empty-state p {
    margin: 0 0 1rem;
    max-width: 32rem;
  }

  .empty-icon {
    color: var(--text-tertiary, var(--text-secondary));
  }

  .badge {
    display: inline-flex;
    align-items: center;
    padding: 0.25rem 0.625rem;
    border-radius: 9999px;
    font-size: 0.75rem;
    font-weight: 500;
    white-space: nowrap;
  }

  .badge-success {
    color: #22c55e;
    background: rgba(34, 197, 94, 0.12);
    border: 1px solid rgba(34, 197, 94, 0.3);
  }

  .badge-warning {
    color: #f59e0b;
    background: rgba(245, 158, 11, 0.12);
    border: 1px solid rgba(245, 158, 11, 0.3);
  }

  .badge-muted {
    color: var(--text-secondary);
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
  }

  .btn-sm {
    padding: 0.25rem 0.625rem;
    font-size: 0.8125rem;
  }

  .spin {
    animation: spin 1s linear infinite;
  }

  .rotate {
    transform: rotate(180deg);
    transition: transform var(--duration-fast) var(--ease-out);
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }

  .ptz-pad {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 3px;
    margin-left: auto;
    flex-shrink: 0;
  }

  .ptz-grid {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 2px;
  }

  .ptz-cell {
    width: 1.75rem;
    height: 1.75rem;
  }

  .ptz-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 1.75rem;
    height: 1.75rem;
    border-radius: var(--radius-sm);
    background-color: var(--bg-tertiary);
    border: 1px solid var(--border);
    color: var(--text-secondary);
    cursor: pointer;
    transition: all var(--duration-fast) var(--ease-out);
    user-select: none;
    touch-action: none;
  }

  .ptz-btn:hover {
    background-color: var(--bg-hover);
    color: var(--text-primary);
    border-color: var(--border-hover);
  }

  .ptz-btn-active {
    background-color: var(--color-primary);
    color: #ffffff;
    border-color: var(--color-primary);
  }

  .ptz-stop {
    background-color: rgba(239, 68, 68, 0.15);
    color: var(--color-danger, #ef4444);
  }

  .ptz-zoom {
    display: flex;
    gap: 2px;
  }
</style>