<script lang="ts">
  import { login, isAuthenticated } from '$lib/api';
  import ThemeToggle from '../components/ThemeToggle.svelte';
  import LanguageSwitcher from '../components/LanguageSwitcher.svelte';
  import { t } from '$lib/i18n';
  import { Eye, EyeOff } from 'lucide-svelte';

  let username = '';
  let password = '';
  let showPassword = false;
  let error = '';
  let loginErrors = $state({ username: '', password: '' });
  let loading = false;
  // Force re-render when language changes


  // Redirect if already logged in
  if (isAuthenticated()) {
    window.location.hash = '#/recordings';
  }

  function validateUsername() {
    if (!username.trim()) {
      loginErrors.username = t('login.usernameRequired');
    } else {
      loginErrors.username = '';
    }
  }

  function validatePassword() {
    if (!password) {
      loginErrors.password = t('login.passwordRequired');
    } else {
      loginErrors.password = '';
    }
  }

  function onUsernameInput() { if (loginErrors.username) loginErrors.username = ''; }
  function onPasswordInput() { if (loginErrors.password) loginErrors.password = ''; }

  async function handleSubmit() {
    validateUsername();
    validatePassword();
    if (loginErrors.username || loginErrors.password) return;

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

  <div class="card w-full max-w-md p-10 border th-border shadow-2xl">
    <div class="text-center mb-10">
      <div class="text-sm font-semibold tracking-widest uppercase th-text-tertiary mb-3">MiBee</div>
      <h1 class="text-3xl font-bold bg-gradient-to-r from-violet-400 to-blue-400 bg-clip-text text-transparent mb-3">{t('login.title')}</h1>
      <p class="th-text-tertiary text-sm">{t('login.subtitle')}</p>
    </div>

    {#if error}
      <div class="mb-6 p-3 bg-[rgba(239,68,68,0.3)] border th-border-danger rounded-lg th-color-danger text-sm">
        {error}
      </div>
    {/if}

    <form on:submit|preventDefault={handleSubmit} class="space-y-6">
      <div>
        <label for="username" class="input-label">{t('login.username')}</label>
        <input
          id="username"
          type="text"
          class="input {loginErrors.username ? 'border-red-500' : ''}"
          bind:value={username}
          placeholder={t('login.usernamePlaceholder')}
          disabled={loading}
          on:keydown={handleKeydown}
          on:blur={validateUsername}
          on:input={onUsernameInput}
          autocomplete="username"
        />
        {#if loginErrors.username}
          <p class="th-color-danger text-xs mt-1">{loginErrors.username}</p>
        {/if}
      </div>

      <div>
        <label for="password" class="input-label">{t('login.password')}</label>
        <div class="relative">
          <input
            id="password"
            type={showPassword ? 'text' : 'password'}
            class="input pr-10 {loginErrors.password ? 'border-red-500' : ''}"
            bind:value={password}
            placeholder={t('login.passwordPlaceholder')}
            disabled={loading}
            on:keydown={handleKeydown}
            on:blur={validatePassword}
            on:input={onPasswordInput}
            autocomplete="current-password"
          />
          <button
            type="button"
            class="absolute right-2 top-1/2 -translate-y-1/2 th-text-tertiary hover:th-text-primary transition-colors"
            on:click={() => showPassword = !showPassword}
            aria-label={showPassword ? t('common.hidePassword') : t('common.showPassword')}
          >
            {#if showPassword}
              <EyeOff class="w-4 h-4" />
            {:else}
              <Eye class="w-4 h-4" />
            {/if}
          </button>
        </div>
        {#if loginErrors.password}
          <p class="th-color-danger text-xs mt-1">{loginErrors.password}</p>
        {/if}
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

    <div class="mt-8 text-center text-sm th-text-tertiary">
      <p class="border-t th-border pt-6">{t('login.secureNote')}</p>
    </div>
  </div>
</div>
