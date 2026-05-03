<script lang="ts">
  import { login, isAuthenticated } from '$lib/api';
  import ThemeToggle from '../components/ThemeToggle.svelte';
  import LanguageSwitcher from '../components/LanguageSwitcher.svelte';
  import { t } from '$lib/i18n';

  let username = '';
  let password = '';
  let error = '';
  let loading = false;
  // Force re-render when language changes


  // Redirect if already logged in
  if (isAuthenticated()) {
    window.location.hash = '#/recordings';
  }

  async function handleSubmit() {
    error = '';
    loading = true;

    try {
      await login(username, password);
      // Redirect to recordings on success
      window.location.hash = '#/recordings';
    } catch (e) {
      error = e instanceof Error ? e.message : t('login.failed');
    } finally {
      loading = false;
    }
  }

  function handleKeydown(e: KeyboardEvent) {
    if (e.key === 'Enter') {
      handleSubmit();
    }
  }
</script>

<div class="min-h-screen flex items-center justify-center th-bg-primary px-4">
  <div class="fixed top-4 right-4 flex items-center gap-2 z-50">
    <ThemeToggle />
    <LanguageSwitcher />
  </div>
    <div class="card w-full max-w-md p-8 border th-border shadow-2xl">
    <div class="text-center mb-8">
      <h1 class="text-3xl font-bold bg-gradient-to-r from-cyan-400 to-blue-400 bg-clip-text text-transparent mb-2">{t('login.title')}</h1>
      <p class="th-text-tertiary">{t('login.subtitle')}</p>
    </div>
    {#if error}
      <div class="mb-4 p-3 bg-[rgba(239,68,68,0.3)] border th-border-danger rounded-md th-color-danger text-sm">
        {error}
      </div>
    {/if}

    <form on:submit|preventDefault={handleSubmit} class="space-y-4">
      <div>
        <label for="username" class="input-label">{t('login.username')}</label>
        <input
          id="username"
          type="text"
          class="input"
          bind:value={username}
          placeholder={t('login.usernamePlaceholder')}
          required
          disabled={loading}
          on:keydown={handleKeydown}
          autocomplete="username"
        />
      </div>

      <div>
        <label for="password" class="input-label">{t('login.password')}</label>
        <input
          id="password"
          type="password"
          class="input"
          bind:value={password}
          placeholder={t('login.passwordPlaceholder')}
          required
          disabled={loading}
          on:keydown={handleKeydown}
          autocomplete="current-password"
        />
      </div>

      <button type="submit" class="btn btn-primary w-full" disabled={loading}>
        {#if loading}
          <span class="spinner mr-2"></span>
          {t('login.signingIn')}
        {:else}
          {t('login.signIn')}
        {/if}
      </button>
    </form>

    <div class="mt-6 text-center text-sm th-text-tertiary">
      <p class="border-t th-border pt-6">{t('login.secureNote')}</p>
    </div>
  </div>
</div>
