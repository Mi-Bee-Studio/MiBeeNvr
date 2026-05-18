<script lang="ts">
  import { t } from '$lib/i18n';
  import { normalizeProtocol } from '$lib/api';
  import type { Camera, ProtocolInfo } from '$lib/api';
  import { Pencil, Play, Square, RotateCw, Eye } from 'lucide-svelte';

  interface Props {
    camera: Camera;
    protocolsMap: Map<string, ProtocolInfo>;
    isEditingName: boolean;
    inlineName: string;
    formatTimeAgo: (lastSeen: string | null | undefined) => { text: string; color: string };
    onstart: (camera: Camera) => void;
    onstop: (camera: Camera) => void;
    onrestart: (camera: Camera) => void;
    onedit: (camera: Camera) => void;
    ondelete: (camera: Camera) => void;
    onsavename: (camera: Camera) => void;
    onstarteditname: (camera: Camera) => void;
    oncanceleditname: () => void;
    onnamedinput: (value: string) => void;
  }

  let {
    camera,
    protocolsMap,
    isEditingName,
    inlineName,
    formatTimeAgo,
    onstart,
    onstop,
    onrestart,
    onedit,
    ondelete,
    onsavename,
    onstarteditname,
    oncanceleditname,
    onnamedinput,
  }: Props = $props();
</script>

<tr class="hover:th-bg-hover transition-colors">
  <td class="px-6 py-4 whitespace-nowrap text-sm">
    {#if isEditingName}
      <input
        type="text"
        class="input py-0.5 px-2 text-sm w-40"
        value={inlineName}
        oninput={(e) => { onnamedinput((e.target as HTMLInputElement).value); }}
        onkeydown={(e) => {
          if (e.key === 'Enter') onsavename(camera);
          if (e.key === 'Escape') oncanceleditname();
        }}
        onblur={() => onsavename(camera)}
        focus
      />
    {:else}
      <button
        class="font-medium th-text-primary hover:underline cursor-pointer flex items-center gap-1"
        onclick={() => onstarteditname(camera)}
        title={t('cameras.editName')}
      >
        {camera.name}
        <Pencil size={12} class="th-text-tertiary" />
      </button>
    {/if}
  </td>
  <td class="px-6 py-4 whitespace-nowrap text-sm">
    {#if camera.status === 'recording'}
      <span class="badge badge-success">{t('cameras.statusRecording')}</span>
    {:else if camera.status === 'error'}
      <span class="badge badge-error">{t('cameras.statusError')}</span>
    {:else if camera.status === 'reconnecting'}
      <span class="badge badge-warning">{t('cameras.statusReconnecting')}</span>
    {:else}
      <span class="badge badge-neutral">{t('cameras.statusStopped')}</span>
    {/if}
    <div class="text-xs th-text-muted mt-0.5">{formatTimeAgo(camera.last_seen).text}</div>
  </td>
  <td class="px-6 py-4 whitespace-nowrap text-sm th-text-secondary">{protocolsMap.get(camera.protocol)?.label || t('cameras.protocol.' + camera.protocol) || camera.protocol}</td>
  <td class="px-6 py-4 whitespace-nowrap text-sm th-text-secondary">{camera.encoding ? (t('cameras.encoding.' + camera.encoding) || camera.encoding) : '-'}</td>
  <td class="px-6 py-4 text-sm th-text-secondary max-w-xs truncate">{camera.url}</td>
  <td class="px-6 py-4 whitespace-nowrap text-sm">
    <div class="flex gap-2 items-center">
      {#if camera.status === 'recording' || camera.status === 'reconnecting'}
        <button
          onclick={() => onstop(camera)}
          class="btn btn-ghost px-2 py-1 text-sm flex items-center gap-1"
          title={t('cameras.stop')}
        >
          <Square size={14} />
          {t('cameras.stop')}
        </button>
      {:else}
        <button
          onclick={() => onstart(camera)}
          class="btn btn-ghost px-2 py-1 text-sm flex items-center gap-1"
          title={t('cameras.start')}
        >
          <Play size={14} />
          {t('cameras.start')}
        </button>
      {/if}
      {#if camera.status === 'recording' || camera.status === 'error' || camera.status === 'reconnecting'}
        <button
          onclick={() => onrestart(camera)}
          class="btn btn-ghost px-2 py-1 text-sm"
          title={t('cameras.restart')}
        >
          <RotateCw size={14} />
        </button>
      {/if}
      {#if protocolsMap.get(normalizeProtocol(camera.protocol))?.capabilities?.hls}
        <a
          href="#/live/{camera.id}"
          class="btn btn-primary px-2 py-1 text-sm flex items-center gap-1"
          title={t('cameras.live')}
        >
          <Eye size={14} />
          {t('cameras.live')}
        </a>
      {/if}
      <button
        onclick={() => onedit(camera)}
        class="btn btn-ghost px-2 py-1 text-sm transition-all duration-200"
      >{t('cameras.edit')}</button>
      <button
        onclick={() => ondelete(camera)}
        class="btn btn-ghost px-2 py-1 text-sm th-color-danger transition-all duration-200"
      >{t('cameras.delete')}</button>
    </div>
  </td>
</tr>
