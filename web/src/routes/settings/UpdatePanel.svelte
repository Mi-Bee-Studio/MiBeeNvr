<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import {
    getUpdateStatus,
    refreshUpdateStatus,
    applyUpdate,
    getApplyStatus,
    getUpdateHistory,
    getSettings,
    updateSettings,
  } from '$lib/api';
  import type { UpdateStatus, UpdateApplyStatus, UpdateHistoryEntry } from '$lib/api';
  import { t } from '$lib/i18n';
  import { withBase } from '$lib/base-path';
  import { showToast } from '$lib/toast';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import Toggle from '$lib/components/Toggle.svelte';
  import {
    RefreshCw,
    CheckCircle2,
    ArrowUpCircle,
    ExternalLink,
    Download,
    History,
    Loader2,
  } from 'lucide-svelte';

  let status = $state<UpdateStatus | null>(null);
  let loading = $state(true);
  let checking = $state(false);
  let error = $state('');

  // Upgrade execution (#648). The apply state survives the mid-upgrade process
  // restart — it is polled from the backend's lifecycle files, never held here.
  let applyStatus = $state<UpdateApplyStatus | null>(null);
  let showApplyConfirm = $state(false);
  let showAutoConfirm = $state(false);
  let autoApply = $state(false);
  let history = $state<UpdateHistoryEntry[]>([]);
  let historyOpen = $state(false);
  let pollTimer: ReturnType<typeof setTimeout> | undefined;

  let applyInProgress = $derived(
    applyStatus?.state === 'requested' || applyStatus?.state === 'applying'
  );
  let canApply = $derived(
    !!status &&
      status.update_available &&
      status.deployment !== 'docker' &&
      status.current !== '' &&
      status.current !== 'dev'
  );

  // True when current is a real version (not "dev"/empty) — used to avoid
  // "up to date" false confidence on local builds.
  let hasVersion = $derived(status !== null && status.current !== '' && status.current !== 'dev');

  onMount(async () => {
    try {
      const [st, settings, applySt] = await Promise.all([
        getUpdateStatus(),
        getSettings().catch(() => null),
        getApplyStatus().catch(() => null),
      ]);
      status = st;
      if (settings?.update) autoApply = settings.update.auto_apply ?? false;
      if (applySt) applyStatus = applySt;
      if (applyInProgress) startPolling();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  });

  onDestroy(() => {
    if (pollTimer) clearTimeout(pollTimer);
  });

  async function checkNow() {
    checking = true;
    error = '';
    try {
      status = await refreshUpdateStatus();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
      showToast(t('common.failed'), 'error');
    } finally {
      checking = false;
    }
  }

  // --- One-click upgrade (#648) ---

  async function confirmApply() {
    showApplyConfirm = false;
    try {
      applyStatus = await applyUpdate();
      startPolling();
    } catch (e) {
      showToast(e instanceof Error ? e.message : String(e), 'error');
    }
  }

  function startPolling() {
    if (pollTimer) clearTimeout(pollTimer);
    pollTimer = setTimeout(async () => {
      try {
        applyStatus = await getApplyStatus();
        if (applyInProgress) {
          startPolling();
          return;
        }
        // Terminal state reached. Success means THIS page is served by the OLD
        // process — prompt a reload instead of trusting stale UI state.
        if (applyStatus?.state === 'success') {
          await refreshUpdateStatus().then((st) => (status = st)).catch(() => {});
          loadHistory();
        }
      } catch {
        // The process restarts mid-upgrade: connection refused is EXPECTED.
        // Keep polling until it answers again (bounded by user navigation).
        startPolling();
      }
    }, 2000);
  }

  async function toggleAutoApply(v: boolean) {
    if (v) {
      // Enabling needs a second confirmation (process auto-restarts).
      showAutoConfirm = true;
      return;
    }
    autoApply = false;
    await saveAutoApply(false);
  }

  async function confirmAutoApply() {
    showAutoConfirm = false;
    autoApply = true;
    await saveAutoApply(true);
  }

  async function saveAutoApply(v: boolean) {
    try {
      await updateSettings({ cleanup: undefined, webdav: undefined, update: { auto_apply: v } } as never);
      showToast(t('common.saved'), 'success');
    } catch (e) {
      autoApply = !v;
      showToast(e instanceof Error ? e.message : String(e), 'error');
    }
  }

  async function loadHistory() {
    try {
      history = await getUpdateHistory();
    } catch {
      history = [];
    }
  }

  // Minimal, SAFE markdown → HTML. HTML is escaped FIRST, then a tiny subset of
  // markdown is applied to the escaped text. No raw remote HTML ever reaches the
  // DOM. Supports: ATX headings (## ), bullets (- / *), bold (**..**), links
  // ([text](url)) with href sanitization (http/https only). Everything else is
  // shown as escaped text inside <p> blocks.
  function renderMarkdown(md: string): string {
    if (!md) return '';
    const esc = (s: string) =>
      s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
    const lines = md.replace(/\r\n/g, '\n').split('\n');
    const out: string[] = [];
    let inList = false;
    const closeList = () => {
      if (inList) {
        out.push('</ul>');
        inList = false;
      }
    };
    for (const raw of lines) {
      const line = raw.trimEnd();
      if (line.trim() === '') {
        closeList();
        continue;
      }
      const h = /^(#{1,6})\s+(.*)$/.exec(line);
      if (h) {
        closeList();
        const level = h[1].length;
        out.push(`<h${level}>${inlineFmt(h[2])}</h${level}>`);
        continue;
      }
      const b = /^[-*]\s+(.*)$/.exec(line);
      if (b) {
        if (!inList) {
          out.push('<ul>');
          inList = true;
        }
        out.push(`<li>${inlineFmt(b[1])}</li>`);
        continue;
      }
      closeList();
      out.push(`<p>${inlineFmt(line)}</p>`);
    }
    closeList();
    return out.join('\n');

    function inlineFmt(s: string): string {
      let r = esc(s);
      // **bold**
      r = r.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
      // [text](url) — http/https only
      r = r.replace(
        /\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g,
        '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>'
      );
      return r;
    }
  }

  function fmtTime(iso?: string): string {
    if (!iso) return t('about.never');
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    return d.toLocaleString();
  }
</script>

<div class="space-y-6">
  <div class="flex items-center gap-3">
    <img src={withBase('/logo-icon.svg')} alt="MiBee NVR" class="h-12 w-12" />
    <div>
      <h3 class="text-lg font-semibold th-text-primary">{t('about.title')}</h3>
      <p class="text-xs th-text-tertiary">{t('about.brandTagline')}</p>
    </div>
  </div>

  {#if loading}
    <div class="th-text-secondary">{t('common.loading')}</div>
  {:else if error}
    <div class="p-3 rounded-lg bg-red-50 dark:bg-red-950/40 text-red-600 dark:text-red-400 text-sm">
      {error}
    </div>
  {:else if status}
    <!-- Version table -->
    <div class="p-4 rounded-lg th-bg-secondary border th-border-primary space-y-3">
      <div class="flex items-center justify-between">
        <span class="th-text-secondary text-sm">{t('about.currentVersion')}</span>
        <span class="font-mono font-medium th-text-primary">
          {status.current || t('about.unknown')}
          {#if !hasVersion}
            <span class="ml-2 text-xs th-text-secondary">({t('about.localBuild')})</span>
          {/if}
        </span>
      </div>
      <div class="flex items-center justify-between">
        <span class="th-text-secondary text-sm">{t('about.latestVersion')}</span>
        <span class="font-mono font-medium th-text-primary">
          {status.latest || '—'}
        </span>
      </div>
      <div class="flex items-center justify-between">
        <span class="th-text-secondary text-sm">{t('about.lastChecked')}</span>
        <span class="text-sm th-text-primary">{fmtTime(status.checked_at)}</span>
      </div>

      <!-- Status pill -->
      <div class="pt-1">
        {#if status.update_available}
          <div class="flex items-center gap-2 text-amber-600 dark:text-amber-400 font-medium">
            <ArrowUpCircle size={18} />
            {t('about.updateAvailable')}
          </div>
        {:else if hasVersion}
          <div class="flex items-center gap-2 text-green-600 dark:text-green-400 font-medium">
            <CheckCircle2 size={18} />
            {t('about.upToDate')}
          </div>
        {/if}
      </div>
    </div>

    <!-- Upgrade instructions -->
    {#if status.update_available}
      <div class="p-4 rounded-lg bg-blue-50 dark:bg-blue-950/40 border border-blue-200 dark:border-blue-900">
        <p class="text-sm text-blue-700 dark:text-blue-300">
          {#if status.deployment === 'docker'}
            {t('about.upgradeDocker')}
          {:else}
            {t('about.upgradeBinary')}
          {/if}
        </p>
        <div class="flex items-center gap-4 mt-3">
          {#if canApply}
            <button
              class="inline-flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium bg-blue-600 text-white hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
              onclick={() => (showApplyConfirm = true)}
              disabled={applyInProgress}
            >
              <Download size={16} />
              {t('about.applyNow')} → {status.latest}
            </button>
          {/if}
          {#if status.html_url}
            <a
              href={status.html_url}
              target="_blank"
              rel="noopener noreferrer"
              class="inline-flex items-center gap-1.5 text-sm font-medium text-blue-600 dark:text-blue-400 hover:underline"
            >
              <ExternalLink size={14} />
              {t('about.viewRelease')}
            </a>
          {/if}
        </div>
      </div>
    {/if}

    <!-- Apply progress / result (#648) — state survives the process restart -->
    {#if applyStatus && applyStatus.state !== 'idle'}
      <div
        class="p-4 rounded-lg border space-y-2 text-sm
        {applyStatus.state === 'success'
          ? 'bg-green-50 dark:bg-green-950/40 border-green-200 dark:border-green-900 text-green-700 dark:text-green-300'
          : applyStatus.state === 'failed' || applyStatus.state === 'failed_rolled_back'
            ? 'bg-red-50 dark:bg-red-950/40 border-red-200 dark:border-red-900 text-red-700 dark:text-red-300'
            : 'bg-blue-50 dark:bg-blue-950/40 border-blue-200 dark:border-blue-900 text-blue-700 dark:text-blue-300'}"
      >
        <div class="flex items-center gap-2 font-medium">
          {#if applyInProgress}
            <Loader2 size={16} class="animate-spin" />
            {applyStatus.state === 'applying' ? t('about.applyState.applying') : t('about.applyState.requested')}
          {:else if applyStatus.state === 'success'}
            <CheckCircle2 size={16} />
            {t('about.applyState.success')} ({applyStatus.from} → {applyStatus.to})
          {:else}
            <ArrowUpCircle size={16} />
            {applyStatus.state === 'failed_rolled_back' ? t('about.applyState.failed_rolled_back') : t('about.applyState.failed')}
          {/if}
        </div>
        {#if applyStatus.error}
          <p class="font-mono text-xs opacity-80">{applyStatus.error}</p>
        {/if}
        {#if applyStatus.state === 'success'}
          <p>{t('about.applyRestarted')}</p>
          <button
            class="px-3 py-1.5 rounded-lg text-xs font-medium bg-blue-600 text-white hover:bg-blue-700"
            onclick={() => window.location.reload()}
          >
            {t('about.refreshPage')}
          </button>
        {/if}
      </div>
    {/if}

    <!-- Auto-apply toggle (#648) — bare-metal only, opt-in, double-confirm -->
    {#if status.deployment !== 'docker' && hasVersion}
      <div class="p-4 rounded-lg th-bg-secondary border th-border-primary">
        <div class="flex items-start justify-between gap-4">
          <div>
            <div class="text-sm font-medium th-text-primary">{t('about.autoApply')}</div>
            <p class="text-xs th-text-tertiary mt-1">{t('about.autoApplyHint')}</p>
          </div>
          <Toggle checked={autoApply} onChange={toggleAutoApply} label={t('about.autoApply')} />
        </div>
      </div>
    {/if}

    <!-- Upgrade history (#648) -->
    <details
      class="p-4 rounded-lg th-bg-secondary border th-border-primary"
      ontoggle={(e) => {
        historyOpen = (e.currentTarget as HTMLDetailsElement).open;
        if (historyOpen && history.length === 0) loadHistory();
      }}
    >
      <summary class="flex items-center gap-2 text-sm font-medium th-text-primary cursor-pointer select-none">
        <History size={15} />
        {t('about.updateHistory')}
      </summary>
      <div class="mt-3 space-y-1 text-xs" class:hidden={!historyOpen}>
        {#if history.length === 0}
          <p class="th-text-tertiary">{t('about.historyEmpty')}</p>
        {:else}
          {#each history as row (row.time + row.to)}
            <div class="flex items-center gap-2 th-text-secondary">
              <span class="font-mono">{row.from} → {row.to}</span>
              <span class={row.result === 'ok' ? 'text-green-600 dark:text-green-400' : 'text-red-600 dark:text-red-400'}>
                {t(`about.result.${row.result}`)}
              </span>
              <span class="th-text-tertiary">{fmtTime(row.time)}</span>
              {#if row.error}<span class="th-text-tertiary truncate max-w-xs" title={row.error}>{row.error}</span>{/if}
            </div>
          {/each}
        {/if}
      </div>
    </details>

    <!-- Changelog -->
    {#if status.changelog}
      <div>
        <h4 class="text-sm font-medium th-text-secondary mb-2">{t('about.changelog')}</h4>
        <div class="p-4 rounded-lg th-bg-secondary border th-border-primary prose-sm max-w-none overflow-auto update-changelog">
          <!-- eslint-disable-next-line svelte/no-at-html-tags -- content is HTML-escaped inside renderMarkdown before any markup is applied -->
          {@html renderMarkdown(status.changelog)}
        </div>
      </div>
    {/if}

    <!-- Check button -->
    <div class="flex justify-end">
      <button
        class="inline-flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium bg-blue-600 text-white hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
        onclick={checkNow}
        disabled={checking}
      >
        <RefreshCw size={16} class={checking ? 'animate-spin' : ''} />
        {checking ? t('about.checking') : t('about.checkNow')}
      </button>
    </div>
  {/if}

  <!-- Upgrade confirmation (#648): target version + restart warning -->
  {#if showApplyConfirm && status}
    <ConfirmDialog
      title={t('about.applyConfirmTitle')}
      message={t('about.applyConfirmBody', { version: status.latest })}
      variant="primary"
      onconfirm={confirmApply}
      oncancel={() => (showApplyConfirm = false)}
    />
  {/if}

  <!-- Auto-apply double confirmation (#648) -->
  {#if showAutoConfirm}
    <ConfirmDialog
      title={t('about.autoApplyConfirmTitle')}
      message={t('about.autoApplyConfirmBody')}
      variant="primary"
      onconfirm={confirmAutoApply}
      oncancel={() => (showAutoConfirm = false)}
    />
  {/if}
</div>

<style>
  .update-changelog :global(h1),
  .update-changelog :global(h2),
  .update-changelog :global(h3),
  .update-changelog :global(h4) {
    font-weight: 600;
    margin-top: 0.75em;
    margin-bottom: 0.35em;
    color: var(--txt-primary, #111827);
  }
  .update-changelog :global(h1) {
    font-size: 1.15rem;
  }
  .update-changelog :global(h2) {
    font-size: 1.05rem;
  }
  .update-changelog :global(h3) {
    font-size: 1rem;
  }
  .update-changelog :global(p) {
    margin: 0.4em 0;
    line-height: 1.6;
  }
  .update-changelog :global(ul) {
    list-style: disc;
    padding-left: 1.4em;
    margin: 0.4em 0;
  }
  .update-changelog :global(li) {
    margin: 0.2em 0;
    line-height: 1.6;
  }
  .update-changelog :global(a) {
    color: #2563eb;
    text-decoration: underline;
  }
</style>
