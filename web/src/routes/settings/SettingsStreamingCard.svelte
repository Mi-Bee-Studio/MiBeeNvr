<script lang="ts">
  import { t } from '$lib/i18n';

  interface Props {
    title: string;
    protocol: string;
    enabled: boolean;
    onEnabledChange: (val: boolean) => void;
    showToggle?: boolean;
    maxViewers?: number;
    onMaxViewersChange?: (val: number) => void;
    idleTimeout?: string;
    onIdleTimeoutChange?: (val: string) => void;
    port?: number;
    onPortChange?: (val: number) => void;
    llHls?: boolean;
    onLlHlsChange?: (val: boolean) => void;
  }

  let {
    title, protocol, enabled, onEnabledChange, showToggle = true,
    maxViewers, onMaxViewersChange,
    idleTimeout, onIdleTimeoutChange,
    port, onPortChange,
    llHls, onLlHlsChange,
  }: Props = $props();
</script>

<div class="mt-6 pt-6 border-t th-border">
  <h4 class="text-sm font-semibold th-text-primary mb-1">{title}</h4>
  {#if protocol === 'webrtc'}
    <p class="text-xs th-text-tertiary mb-4">{t('settings.streaming.webrtcDesc')}</p>
  {:else if protocol === 'flv'}
    <p class="text-xs th-text-tertiary mb-4">{t('settings.streaming.flvDesc')}</p>
  {:else if protocol === 'hls'}
    <p class="text-xs th-text-tertiary mb-4">{t('settings.streaming.hlsDesc')}</p>
  {:else if protocol === 'rtmp'}
    <p class="text-xs th-text-tertiary mb-4">{t('settings.streaming.rtmpDesc')}</p>
  {:else if protocol === 'srt'}
    <p class="text-xs th-text-tertiary mb-4">{t('settings.streaming.srtDesc')}</p>
  {/if}

  <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
    {#if showToggle}
    <!-- Enable toggle -->
    <div>
      <label class="input-label" for="{protocol}-toggle">{title}</label>
      <div class="flex items-center gap-3 mt-2">
        <button
          id="{protocol}-toggle" aria-label={title}
          type="button"
          class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 {enabled ? 'bg-blue-600' : 'th-bg-tertiary'}"
          onclick={() => onEnabledChange(!enabled)}
          onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onEnabledChange(!enabled); } }}
          role="switch"
          aria-checked={enabled}
        >
          <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {enabled ? 'translate-x-6' : 'translate-x-1'}"></span>
        </button>
      </div>
    </div>
    {/if}

    {#if protocol === 'webrtc'}
      <div>
        <label for="webrtcMaxViewers" class="input-label">{t('settings.streaming.webrtc.maxViewers')}</label>
        <input id="webrtcMaxViewers" type="number" class="input" value={maxViewers ?? 4}
          oninput={(e) => onMaxViewersChange?.(parseInt((e.target as HTMLInputElement).value) || 1)}
          min="1" max="20" />
      </div>
      <div>
        <label for="webrtcIdleTimeout" class="input-label">{t('settings.streaming.webrtc.idleTimeout')}</label>
        <select id="webrtcIdleTimeout" class="input" value={idleTimeout ?? '5m'}
          onchange={(e) => onIdleTimeoutChange?.((e.target as HTMLSelectElement).value)}>
          <option value="1m">1 min</option>
          <option value="5m">5 min</option>
          <option value="10m">10 min</option>
          <option value="30m">30 min</option>
        </select>
        <p class="text-xs th-text-tertiary mt-1">{t('settings.streaming.webrtc.idleTimeoutHint')}</p>
      </div>
    {:else if protocol === 'flv'}
      <div>
        <label for="flvMaxViewers" class="input-label">{t('settings.streaming.flv.maxViewers')}</label>
        <input id="flvMaxViewers" type="number" class="input" value={maxViewers ?? 10}
          oninput={(e) => onMaxViewersChange?.(parseInt((e.target as HTMLInputElement).value) || 1)}
          min="1" max="50" />
      </div>
    {:else if protocol === 'hls'}
      <div>
        <label class="input-label" for="llhls-toggle">{t('settings.streaming.hls.llHls')}</label>
        <div class="flex items-center gap-3 mt-2">
          <button
            id="llhls-toggle" aria-label={t('settings.streaming.hls.llHls')}
            type="button"
            class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 {llHls ? 'bg-blue-600' : 'th-bg-tertiary'}"
            onclick={() => onLlHlsChange?.(!llHls)}
            onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onLlHlsChange?.(!llHls); } }}
            role="switch"
            aria-checked={llHls ?? false}
          >
            <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {llHls ? 'translate-x-6' : 'translate-x-1'}"></span>
          </button>
        </div>
        <p class="text-xs th-text-tertiary mt-1">{t('settings.streaming.hls.llHlsHint')}</p>
      </div>
    {:else if protocol === 'rtmp'}
      <div>
        <label for="rtmpPort" class="input-label">{t('settings.streaming.rtmp.port')}</label>
        <input id="rtmpPort" type="number" class="input" value={port ?? 1935}
          oninput={(e) => onPortChange?.(parseInt((e.target as HTMLInputElement).value) || 1935)}
          min="1" max="65535" />
      </div>
      <div>
        <p class="text-xs th-text-tertiary mt-6">{t('settings.streaming.rtmp.pushHint')}</p>
      </div>
    {:else if protocol === 'srt'}
      <div>
        <label for="srtPort" class="input-label">{t('settings.streaming.srt.port')}</label>
        <input id="srtPort" type="number" class="input" value={port ?? 9000}
          oninput={(e) => onPortChange?.(parseInt((e.target as HTMLInputElement).value) || 9000)}
          min="1" max="65535" />
      </div>
      <div>
        <p class="text-xs th-text-tertiary mt-6">{t('settings.streaming.srt.hint')}</p>
      </div>
    {/if}
  </div>
</div>
