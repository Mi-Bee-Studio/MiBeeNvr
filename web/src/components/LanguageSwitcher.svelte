<script lang="ts">
  import { t, state, setLang } from '$lib/i18n';

  function handleChange(e: Event) {
    const target = e.target as HTMLSelectElement;
    setLang(target.value);
  }
</script>

<select
  class="input text-sm py-1 px-2 w-auto bg-[var(--bg-tertiary)] border-[var(--border)] text-[var(--text-body)]"
  value={state.currentLang}
  onchange={handleChange}
>
  <option value="zh">{t('lang.zh')}</option>
  <option value="en">{t('lang.en')}</option>
</select>
