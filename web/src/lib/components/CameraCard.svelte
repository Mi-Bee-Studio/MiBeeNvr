<script lang="ts">
  import { t } from '$lib/i18n';
  import { normalizeProtocol, enableCamera, disableCamera } from '$lib/api';
  import type { Camera, ProtocolInfo } from '$lib/api';
  import { Pencil, Play, Square, RotateCw, Eye, MoreVertical, Archive } from 'lucide-svelte';

  interface Props {
    camera: Camera;
    protocolsMap: Map<string, ProtocolInfo>;
    onedit: (camera: Camera) => void;
    ondelete: (camera: Camera) => void;
    onstart: (camera: Camera) => void;
    onstop: (camera: Camera) => void;
    onrestart: (camera: Camera) => void;
    ontoggle: (camera: Camera) => void;
    onsaveName: (camera: Camera, name: string) => void;
  }

  let {
    camera,
    protocolsMap,
    onedit,
    ondelete,
    onstart,
    onstop,
    onrestart,
    ontoggle,
    onsaveName,
  }: Props = $props();

  let menuOpen = $state(false);
  let editingName = $state(false);
  let nameInput = $state(camera.name);

  let variant = $derived(
    !camera.enabled
      ? 'disabled'
      : camera.status === 'recording'
        ? 'active'
        : 'stopped'
  );

  let isHls = $derived(
    protocolsMap.get(normalizeProtocol(camera.protocol))?.capabilities?.hls ?? false
  );

  let protocolLabel = $derived(
    protocolsMap.get(camera.protocol)?.label ||
    t('cameras.protocol.' + camera.protocol) ||
    camera.protocol
  );

  let encodingLabel = $derived(
    camera.encoding ? (t('cameras.encoding.' + camera.encoding) || camera.encoding) : ''
  );

  function closeMenu() {
    menuOpen = false;
  }

  function toggleMenu(e: MouseEvent) {
    e.stopPropagation();
    menuOpen = !menuOpen;
  }

  async function handleToggle() {
    try {
      if (camera.enabled) {
        await disableCamera(camera.id);
      } else {
        await enableCamera(camera.id);
      }
      ontoggle(camera);
    } catch (e) {
      console.error('Toggle failed:', e);
    }
  }

  function startEditName() {
    nameInput = camera.name;
    editingName = true;
  }

  function saveName() {
    const trimmed = nameInput.trim();
    if (trimmed && trimmed !== camera.name) {
      onsaveName(camera, trimmed);
    }
    editingName = false;
  }

  function cancelEditName() {
    editingName = false;
    nameInput = camera.name;
  }

  $effect(() => {
    if (!menuOpen) return;
    const handler = (e: MouseEvent) => { menuOpen = false; };
    window.addEventListener('click', handler);
    return () => window.removeEventListener('click', handler);
  });
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  class="card camera-card border th-border p-4 transition-all {variant === 'disabled' ? 'is-disabled' : ''}"
>
  <!-- Top: Name + Status -->
  <div class="flex items-start justify-between gap-2 mb-3">
    <div class="min-w-0 flex-1">
      {#if editingName}
        <input
          type="text"
          class="input py-1 px-2 text-sm w-full"
          bind:value={nameInput}
          onkeydown={(e) => {
            if (e.key === 'Enter') saveName();
            if (e.key === 'Escape') cancelEditName();
          }}
          onblur={saveName}
        />
      {:else}
        <button
          class="font-medium th-text-primary hover:underline cursor-pointer flex items-center gap-1.5 text-left"
          onclick={startEditName}
          title={t('cameras.editName')}
        >
          <span class="truncate">{camera.name}</span>
          <Pencil size={12} class="th-text-tertiary shrink-0" />
        </button>
      {/if}
    </div>

    <div class="shrink-0">
      {#if variant === 'disabled'}
        <span class="badge badge-neutral">{t('cameras.status.disabled')}</span>
      {:else if camera.status === 'recording'}
        <span class="badge badge-success">{t('cameras.statusRecording')}</span>
      {:else if camera.status === 'error'}
        <span class="badge badge-error">{t('cameras.statusError')}</span>
      {:else if camera.status === 'reconnecting'}
        <span class="badge badge-warning">{t('cameras.statusReconnecting')}</span>
      {:else}
        <span class="badge badge-neutral">{t('cameras.statusStopped')}</span>
      {/if}
    </div>
  </div>

  <!-- Middle: Protocol + Encoding + URL -->
  <div class="space-y-1.5 mb-3">
    <div class="flex items-center gap-2 flex-wrap">
      <span class="text-xs font-medium th-text-secondary px-2 py-0.5 rounded th-bg-tertiary">{protocolLabel}</span>
      {#if encodingLabel}
        <span class="text-xs th-text-tertiary px-2 py-0.5 rounded th-bg-tertiary">{encodingLabel}</span>
      {/if}
    </div>
    <p class="text-xs th-text-tertiary truncate font-mono" title={camera.url}>{camera.url}</p>
  </div>

  <!-- Bottom: Action bar -->
  <div class="flex items-center justify-between pt-3 border-t th-border">
    <!-- Toggle switch (left) -->
    <button
      type="button"
      class="toggle-switch {camera.enabled ? 'is-on' : ''}"
      onclick={handleToggle}
      onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleToggle(); } }}
      role="switch"
      aria-checked={camera.enabled}
      aria-label={camera.enabled ? t('cameras.action.disable') : t('cameras.action.enable')}
    >
      <span class="toggle-thumb"></span>
    </button>

    <!-- Action buttons (right) -->
    <div class="flex items-center gap-1">
      {#if variant !== 'disabled'}
        {#if camera.status === 'recording' || camera.status === 'reconnecting'}
          <button
            class="btn btn-ghost px-2 py-1 text-sm"
            onclick={() => onstop(camera)}
            title={t('cameras.stop')}
          >
            <Square size={14} />
          </button>
        {:else}
          <button
            class="btn btn-ghost px-2 py-1 text-sm"
            onclick={() => onstart(camera)}
            title={t('cameras.start')}
          >
            <Play size={14} />
          </button>
        {/if}

        {#if camera.status === 'recording' || camera.status === 'error' || camera.status === 'reconnecting'}
          <button
            class="btn btn-ghost px-2 py-1 text-sm"
            onclick={() => onrestart(camera)}
            title={t('cameras.restart')}
          >
            <RotateCw size={14} />
          </button>
        {/if}
      {/if}

      {#if isHls}
        <a
          href="#/live/{camera.id}"
          class="btn btn-ghost px-2 py-1 text-sm"
          title={t('cameras.live')}
        >
          <Eye size={14} />
        </a>
      {/if}

      <!-- More menu -->
      <div class="relative">
        <button
          class="btn btn-ghost px-2 py-1 text-sm"
          onclick={toggleMenu}
          title={t('cameras.moreActions')}
        >
          <MoreVertical size={14} />
        </button>

        {#if menuOpen}
          <div class="dropdown-menu" onclick={(e) => e.stopPropagation()}>
            {#if isHls}
              <a href="#/live/{camera.id}" class="dropdown-item" onclick={closeMenu}>
                <Eye size={14} />
                {t('cameras.live')}
              </a>
            {/if}
            <button class="dropdown-item" onclick={() => { closeMenu(); onedit(camera); }}>
              <Pencil size={14} />
              {t('cameras.edit')}
            </button>
            <button class="dropdown-item dropdown-item--danger" onclick={() => { closeMenu(); ondelete(camera); }}>
              <Archive size={14} />
              {t('cameras.action.archive')}
            </button>
          </div>
        {/if}
      </div>
    </div>
  </div>
</div>

<style>
  .camera-card {
    display: flex;
    flex-direction: column;
  }

  .camera-card.is-disabled {
    opacity: 0.5;
  }

  .camera-card.is-disabled .dropdown-item:not(.dropdown-item--danger),
  .camera-card.is-disabled .btn:not(.toggle-switch) {
    pointer-events: none;
  }

  /* Toggle switch — matches Settings.svelte pattern */
  .toggle-switch {
    position: relative;
    display: inline-flex;
    align-items: center;
    width: 2.75rem;
    height: 1.5rem;
    border-radius: 9999px;
    background-color: var(--bg-tertiary);
    transition: background-color var(--duration-fast) var(--ease-out);
    border: none;
    cursor: pointer;
    padding: 0;
    flex-shrink: 0;
  }

  .toggle-switch.is-on {
    background-color: var(--color-primary);
  }

  .toggle-switch .toggle-thumb {
    display: block;
    width: 1rem;
    height: 1rem;
    border-radius: 9999px;
    background-color: #ffffff;
    transition: transform var(--duration-fast) var(--ease-out);
    transform: translateX(0.25rem);
  }

  .toggle-switch.is-on .toggle-thumb {
    transform: translateX(1.5rem);
  }

  .toggle-switch:focus-visible {
    box-shadow: var(--focus-ring);
    outline: none;
  }

  /* Dropdown menu */
  .dropdown-menu {
    position: absolute;
    right: 0;
    top: 100%;
    margin-top: 0.25rem;
    min-width: 10rem;
    background-color: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    box-shadow: var(--shadow-md);
    z-index: 50;
    padding: 0.25rem 0;
    animation: dropdown-enter 0.12s var(--ease-out);
  }

  @keyframes dropdown-enter {
    from {
      opacity: 0;
      transform: translateY(-4px);
    }
    to {
      opacity: 1;
      transform: translateY(0);
    }
  }

  .dropdown-item {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    padding: 0.5rem 0.75rem;
    font-size: 0.8125rem;
    color: var(--text-primary);
    background: transparent;
    border: none;
    cursor: pointer;
    text-align: left;
    text-decoration: none;
    transition: background-color var(--duration-fast) var(--ease-out);
  }

  .dropdown-item:hover {
    background-color: var(--bg-hover);
  }

  .dropdown-item--danger {
    color: var(--color-danger);
  }

  .dropdown-item--danger:hover {
    background-color: rgba(239, 68, 68, 0.1);
  }
</style>
