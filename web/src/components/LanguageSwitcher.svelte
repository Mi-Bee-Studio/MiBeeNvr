<script lang="ts">
  import { onDestroy } from 'svelte';
  import { t, getCurrentLang, onLangChange } from '$lib/i18n';

  let lang = getCurrentLang();

  const unsubscribe = onLangChange(() => {
    lang = getCurrentLang();
  });

  onDestroy(() => { unsubscribe(); });
  function handleChange(e: Event) {
    const target = e.target as HTMLSelectElement;
    setLang(target.value);
  }

  $: { void lang; } // reactivity trigger
</script>

<select
  class="input text-sm py-1 px-2 w-auto bg-slate-700 border-slate-600 text-slate-200"
  value={lang}
  on:change={handleChange}
>
  <option value="zh">{t('lang.zh')}</option>
  <option value="en">{t('lang.en')}</option>
</select>
