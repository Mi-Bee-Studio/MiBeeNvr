<script lang="ts">
  import { onMount } from 'svelte';
  import { getUpdateStatus, refreshUpdateStatus } from '$lib/api';
  import type { UpdateStatus } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { RefreshCw, CheckCircle2, ArrowUpCircle, ExternalLink } from 'lucide-svelte';

  let status = $state<UpdateStatus | null>(null);
  let loading = $state(true);
  let checking = $state(false);
  let error = $state('');

  // True when current is a real version (not "dev"/empty) — used to avoid
  // "up to date" false confidence on local builds.
  let hasVersion = $derived(status !== null && status.current !== '' && status.current !== 'dev');

  onMount(async () => {
    try {
      status = await getUpdateStatus();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
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
  <div>
    <h3 class="text-lg font-semibold th-text-primary">{t('about.title')}</h3>
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
        {#if status.html_url}
          <a
            href={status.html_url}
            target="_blank"
            rel="noopener noreferrer"
            class="inline-flex items-center gap-1.5 mt-3 text-sm font-medium text-blue-600 dark:text-blue-400 hover:underline"
          >
            <ExternalLink size={14} />
            {t('about.viewRelease')}
          </a>
        {/if}
      </div>
    {/if}

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
