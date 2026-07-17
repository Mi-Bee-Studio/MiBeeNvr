<script lang="ts">
  import { setupApi, storeCredentials } from '$lib/api';
  import ThemeToggle from '../components/ThemeToggle.svelte';
  import LanguageSwitcher from '../components/LanguageSwitcher.svelte';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { Eye, EyeOff, Check, X } from 'lucide-svelte';

  let username = $state('admin');
  let password = $state('');
  let confirmPassword = $state('');
  let showPassword = $state(false);
  let showConfirmPassword = $state(false);
  let language = $state('en');
  let storagePath = $state('/var/lib/mibee-nvr');
  let error = $state('');
  let loading = $state(false);

  let errors = $state({ username: '', password: '', confirmPassword: '' });

  // Browser capability detection
  let capabilities = $state({
    llhls: true,
    webrtc: false,
    flv: false,
    hls: false,
  });
  let bestProtocol = $state('llhls');

  $effect(() => {
    // LL-HLS: hls.js bundled — always available
    capabilities.llhls = true;

    // WebRTC: RTCPeerConnection available
    capabilities.webrtc = typeof RTCPeerConnection !== 'undefined';

    // FLV: ReadableStream + MediaSource available
    capabilities.flv = typeof ReadableStream !== 'undefined' && typeof MediaSource !== 'undefined';

    // HLS: native HLS support (Safari)
    try {
      const video = document.createElement('video');
      capabilities.hls = video.canPlayType('application/vnd.apple.mpegurl') !== '';
    } catch {
      capabilities.hls = false;
    }

    // Auto-select best available: LL-HLS > WebRTC > FLV > HLS
    if (capabilities.llhls) {
      bestProtocol = 'llhls';
    } else if (capabilities.webrtc) {
      bestProtocol = 'webrtc';
    } else if (capabilities.flv) {
      bestProtocol = 'flv';
    } else {
      bestProtocol = 'hls';
    }
  });

  function validateUsername() {
    if (!username.trim()) {
      errors.username = t('setup.errors.usernameRequired');
    } else {
      errors.username = '';
    }
  }

  function validatePassword() {
    if (!password) {
      errors.password = t('setup.errors.passwordRequired');
    } else if (password.length < 8) {
      errors.password = t('setup.errors.passwordMinLength');
    } else {
      errors.password = '';
    }
    // Re-validate confirm if it has content
    if (confirmPassword) validateConfirmPassword();
  }

  function validateConfirmPassword() {
    if (!confirmPassword) {
      errors.confirmPassword = t('setup.errors.confirmRequired');
    } else if (password !== confirmPassword) {
      errors.confirmPassword = t('setup.errors.passwordMismatch');
    } else {
      errors.confirmPassword = '';
    }
  }

  function onUsernameInput() { if (errors.username) errors.username = ''; }
  function onPasswordInput() { if (errors.password) errors.password = ''; }
  function onConfirmInput() { if (errors.confirmPassword) errors.confirmPassword = ''; }

  async function handleSubmit() {
    validateUsername();
    validatePassword();
    validateConfirmPassword();
    if (errors.username || errors.password || errors.confirmPassword) return;

    error = '';
    loading = true;

    try {
      const res = await setupApi(username, password, language, storagePath);

      // Decode token to get credentials and store them
      const decoded = atob(res.token);
      const [user, pass] = decoded.split(':');
      storeCredentials(user, pass);

      // Show completion toast
      const protocolLabel = bestProtocol === 'llhls' ? 'LL-HLS'
        : bestProtocol === 'webrtc' ? 'WebRTC'
        : bestProtocol === 'flv' ? 'HTTP-FLV' : 'HLS';
      showToast(t('setup.complete', { protocol: protocolLabel }), 'success');

      // Redirect to recordings
      window.location.hash = '#/recordings';
    } catch (e) {
      error = e instanceof Error ? e.message : t('setup.errors.failed');
    } finally {
      loading = false;
    }
  }

  function handleKeydown(e: KeyboardEvent) {
    if (e.key === 'Enter') handleSubmit();
  }
</script>

<div class="min-h-screen flex items-center justify-center th-bg-primary px-4">
  <div class="fixed top-4 right-4 flex items-center gap-2 z-50">
    <ThemeToggle />
    <LanguageSwitcher />
  </div>

  <div class="card w-full max-w-lg p-10 border th-border shadow-2xl">
    <div class="text-center mb-8">
      <div class="text-sm font-semibold tracking-widest uppercase th-text-tertiary mb-3">MiBee</div>
      <h1 class="text-3xl font-bold bg-gradient-to-r from-violet-400 to-blue-400 bg-clip-text text-transparent mb-3">{t('setup.title')}</h1>
      <p class="th-text-tertiary text-sm">{t('setup.subtitle')}</p>
    </div>

    {#if error}
      <div class="mb-6 p-3 bg-[rgba(239,68,68,0.3)] border th-border-danger rounded-lg th-color-danger text-sm">
        {error}
      </div>
    {/if}

    <form onsubmit={(e) => { e.preventDefault(); handleSubmit(); }} class="space-y-5">
      <!-- Username -->
      <div>
        <label for="setup-username" class="input-label">{t('setup.username')}</label>
        <input
          id="setup-username"
          type="text"
          class="input {errors.username ? 'border-red-500' : ''}"
          bind:value={username}
          placeholder="admin"
          disabled={loading}
          onkeydown={handleKeydown}
          onblur={validateUsername}
          oninput={onUsernameInput}
          autocomplete="username"
        />
        {#if errors.username}
          <p class="th-color-danger text-xs mt-1">{errors.username}</p>
        {/if}
      </div>

      <!-- Password -->
      <div>
        <label for="setup-password" class="input-label">{t('setup.password')}</label>
        <div class="relative">
          <input
            id="setup-password"
            type={showPassword ? 'text' : 'password'}
            class="input pr-10 {errors.password ? 'border-red-500' : ''}"
            bind:value={password}
            placeholder={t('setup.passwordPlaceholder')}
            disabled={loading}
            onkeydown={handleKeydown}
            onblur={validatePassword}
            oninput={onPasswordInput}
            autocomplete="new-password"
          />
          <button
            type="button"
            class="absolute right-2 top-1/2 -translate-y-1/2 th-text-tertiary hover:th-text-primary transition-colors"
            onclick={() => showPassword = !showPassword}
            aria-label={showPassword ? t('common.hidePassword') : t('common.showPassword')}
          >
            {#if showPassword}
              <EyeOff class="w-4 h-4" />
            {:else}
              <Eye class="w-4 h-4" />
            {/if}
          </button>
        </div>
        {#if errors.password}
          <p class="th-color-danger text-xs mt-1">{errors.password}</p>
        {/if}
      </div>

      <!-- Confirm Password -->
      <div>
        <label for="setup-confirm" class="input-label">{t('setup.confirmPassword')}</label>
        <div class="relative">
          <input
            id="setup-confirm"
            type={showConfirmPassword ? 'text' : 'password'}
            class="input pr-10 {errors.confirmPassword ? 'border-red-500' : ''}"
            bind:value={confirmPassword}
            placeholder={t('setup.confirmPasswordPlaceholder')}
            disabled={loading}
            onkeydown={handleKeydown}
            onblur={validateConfirmPassword}
            oninput={onConfirmInput}
            autocomplete="new-password"
          />
          <button
            type="button"
            class="absolute right-2 top-1/2 -translate-y-1/2 th-text-tertiary hover:th-text-primary transition-colors"
            onclick={() => showConfirmPassword = !showConfirmPassword}
            aria-label={showConfirmPassword ? t('common.hidePassword') : t('common.showPassword')}
          >
            {#if showConfirmPassword}
              <EyeOff class="w-4 h-4" />
            {:else}
              <Eye class="w-4 h-4" />
            {/if}
          </button>
        </div>
        {#if errors.confirmPassword}
          <p class="th-color-danger text-xs mt-1">{errors.confirmPassword}</p>
        {/if}
      </div>

      <!-- Browser Capabilities -->
      <div class="border th-border rounded-lg p-4 space-y-3">
        <h3 class="text-sm font-semibold th-text-primary">{t('setup.capabilities')}</h3>
        <div class="space-y-2">
          <div class="flex items-center gap-2 text-sm">
            <span class="w-2.5 h-2.5 rounded-full {capabilities.llhls ? 'bg-green-500' : 'bg-gray-500'}"></span>
            <span class="th-text-secondary">LL-HLS</span>
            <span class="th-text-tertiary text-xs ml-auto">
              {#if capabilities.llhls}
                <span class="text-green-500">{t('setup.supported')}</span>
              {:else}
                {t('setup.notSupported')}
              {/if}
            </span>
          </div>
          <div class="flex items-center gap-2 text-sm">
            <span class="w-2.5 h-2.5 rounded-full {capabilities.webrtc ? 'bg-green-500' : 'bg-gray-500'}"></span>
            <span class="th-text-secondary">WebRTC</span>
            <span class="th-text-tertiary text-xs ml-auto">
              {#if capabilities.webrtc}
                <span class="text-green-500">{t('setup.supported')}</span>
              {:else}
                {t('setup.notSupported')}
              {/if}
            </span>
          </div>
          <div class="flex items-center gap-2 text-sm">
            <span class="w-2.5 h-2.5 rounded-full {capabilities.flv ? 'bg-green-500' : 'bg-gray-500'}"></span>
            <span class="th-text-secondary">HTTP-FLV</span>
            <span class="th-text-tertiary text-xs ml-auto">
              {#if capabilities.flv}
                <span class="text-green-500">{t('setup.supported')}</span>
              {:else}
                {t('setup.notSupported')}
              {/if}
            </span>
          </div>
          <div class="flex items-center gap-2 text-sm">
            <span class="w-2.5 h-2.5 rounded-full {capabilities.hls ? 'bg-green-500' : 'bg-gray-500'}"></span>
            <span class="th-text-secondary">HLS</span>
            <span class="th-text-tertiary text-xs ml-auto">
              {#if capabilities.hls}
                <span class="text-green-500">{t('setup.supported')}</span>
              {:else}
                {t('setup.notSupported')}
              {/if}
            </span>
          </div>
        </div>
        <p class="text-xs th-text-tertiary">{t('setup.bestProtocol', { protocol: bestProtocol === 'llhls' ? 'LL-HLS' : bestProtocol === 'webrtc' ? 'WebRTC' : bestProtocol === 'flv' ? 'HTTP-FLV' : 'HLS' })}</p>
      </div>

      <!-- Optional: Language -->
      <div>
        <label for="setup-language" class="input-label">{t('setup.language')}</label>
        <select
          id="setup-language"
          class="input"
          bind:value={language}
          disabled={loading}
        >
          <option value="en">English</option>
          <option value="zh">中文</option>
        </select>
      </div>

      <!-- Optional: Storage Path -->
      <div>
        <label for="setup-storage" class="input-label">{t('setup.storagePath')}</label>
        <input
          id="setup-storage"
          type="text"
          class="input"
          bind:value={storagePath}
          placeholder="/var/lib/mibee-nvr"
          disabled={loading}
        />
        <p class="th-text-tertiary text-xs mt-1">{t('setup.storagePathHint')}</p>
      </div>

      <!-- Submit -->
      <button type="submit" class="btn btn-primary w-full" disabled={loading}>
        {#if loading}
          <span class="spinner mr-2"></span>
          {t('setup.submitting')}
        {:else}
          {t('setup.submit')}
        {/if}
      </button>
    </form>

    <div class="mt-6 text-center text-sm th-text-tertiary">
      <p class="border-t th-border pt-4">{t('setup.secureNote')}</p>
    </div>
  </div>
</div>
