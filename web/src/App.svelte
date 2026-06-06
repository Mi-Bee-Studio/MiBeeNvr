<script lang="ts">
  import { onMount } from 'svelte';
  import { isAuthenticated, healthCheck } from '$lib/api';
  import { t } from '$lib/i18n';
  import { WifiOff } from 'lucide-svelte';
  import Login from './routes/Login.svelte';
  import Recordings from './routes/Recordings.svelte';
  import RecordingDetail from './routes/RecordingDetail.svelte';
  import Stats from './routes/Stats.svelte';
  import Settings from './routes/Settings.svelte';
  import Cameras from './routes/Cameras.svelte';
  import LiveView from './routes/LiveView.svelte';
  import Dashboard from './routes/Dashboard.svelte';
  import Setup from './routes/Setup.svelte';

  import TranscodingHistory from './routes/TranscodingHistory.svelte';
  import Surveillance from './routes/Surveillance.svelte';
  import Status from './routes/Status.svelte';
  import Timelapse from './routes/Timelapse.svelte';
  import Header from './components/Header';

  // Network status
  let isOffline = $state(false);
  let showOfflineBanner = $state(false);
  let showOnlineBanner = $state(false);
  let onlineBannerTimer: ReturnType<typeof setTimeout> | null = null;

  function handleOffline() {
    isOffline = true;
    showOfflineBanner = true;
    showOnlineBanner = false;
    if (onlineBannerTimer) clearTimeout(onlineBannerTimer);
  }

  function handleOnline() {
    isOffline = false;
    showOfflineBanner = false;
    showOnlineBanner = true;
    if (onlineBannerTimer) clearTimeout(onlineBannerTimer);
    onlineBannerTimer = setTimeout(() => { showOnlineBanner = false; }, 3000);
  }

  async function checkSetupRequired() {
    if (isAuthenticated()) return;
    try {
      const health = await healthCheck();
      if (health.setup_required && currentRoute === 'login') {
        window.location.hash = '#/setup';
      }
    } catch {
      // Health check failed — ignore, user stays on login page
    }
  }


  // Parse hash-based routes (hoisted — function declarations are available before this line)
  function parseRoute(hash: string) {
    const path = hash.slice(1); // Remove #

    if (!path || path === '/') {
      return isAuthenticated() ? { route: 'recordings', params: {} } : { route: 'login', params: {} };
    }

    const segments = path.split('/').filter(Boolean);

    if (segments[0] === 'login') {
      return { route: 'login', params: {} };
    }

    if (segments[0] === 'setup') {
      return { route: 'setup', params: {} };
    }

    // All routes below require authentication
    if (!isAuthenticated()) {
      return { route: 'login', params: {} };
    }

    if (segments[0] === 'recordings') {
      if (segments[1]) {
        return { route: 'recording-detail', params: { id: segments[1] } };
      }
      return { route: 'recordings', params: {} };
    }

    if (segments[0] === 'cameras') {
      if (segments[1]) {
        return { route: 'cameras-detail', params: { id: segments[1] } };
      }
      return { route: 'cameras', params: {} };
    }

    if (segments[0] === 'live') {
      if (segments[1]) {
        return { route: 'live', params: { id: segments[1] } };
      }
      return { route: 'cameras', params: {} };
    }

    if (segments[0] === 'surveillance') {
      const tab = segments[1] === 'recordings' ? 'recordings' : 'cameras';
      return { route: 'surveillance', params: { tab } };
    }

    if (segments[0] === 'status') {
      const tab = segments[1] === 'transcoding' ? 'transcoding' : 'health';
      return { route: 'status', params: { tab } };
    }
    if (segments[0] === 'stats') {
      return { route: 'stats', params: {} };
    }

    if (segments[0] === 'settings') {
      return { route: 'settings', params: {} };
    }

    if (segments[0] === 'transcoding-history') {
      return { route: 'transcoding-history', params: {} };
    }

    if (segments[0] === 'timelapse') {
      return { route: 'timelapse', params: {} };
    }
    if (segments[0] === 'dashboard') {
      const tab = segments[1] === 'health' ? 'health' : 'dashboard';
      return { route: 'dashboard', params: { tab } };
    }

    // Default to login for unknown routes
    return { route: 'login', params: {} };
  }

  // Current route — initialize from hash synchronously to prevent
  // Login component from redirecting to recordings before onMount runs
  // Redirect legacy #/health route
  if (typeof window !== 'undefined' && window.location.hash === '#/health') {
    window.location.replace('#/status');
  }

  const initialRoute = typeof window !== 'undefined' ? parseRoute(window.location.hash) : { route: 'login', params: {} };
  let currentRoute = $state(initialRoute.route);
  let params: Record<string, string> = $state(initialRoute.params);


  function updateRoute() {
    const hash = window.location.hash;
    // Redirect legacy #/health route
    if (hash === '#/health') {
      window.location.replace('#/status');
      return;
    }
    const { route, params: routeParams } = parseRoute(hash);
    currentRoute = route;
    params = routeParams;

    // When auth guard redirects to login, sync the hash so that
    // post-login hash change actually triggers hashchange.
    // Without this, if hash was already #/recordings when auth expired,
    // setting hash to #/recordings after login won't fire hashchange.
    if (route === 'login' && hash !== '#/login' && hash !== '' && hash !== '#') {
      window.location.hash = '#/login';
      return;
    }
  }

  // Listen for hash changes + network status
  onMount(() => {
    updateRoute();
    checkSetupRequired();
    window.addEventListener('hashchange', updateRoute);

    // Network detection
    isOffline = !navigator.onLine;
    if (isOffline) showOfflineBanner = true;
    window.addEventListener('offline', handleOffline);
    window.addEventListener('online', handleOnline);

    return () => {
      window.removeEventListener('hashchange', updateRoute);
      window.removeEventListener('offline', handleOffline);
      window.removeEventListener('online', handleOnline);
      if (onlineBannerTimer) clearTimeout(onlineBannerTimer);
    };
  });
</script>

<!-- Offline banner -->
{#if showOfflineBanner}
  <div class="offline-banner" role="alert" aria-live="assertive">
    <WifiOff size={16} />
    <span>{t('network.offline')}</span>
  </div>
{/if}

<!-- Online restored banner -->
{#if showOnlineBanner}
  <div class="online-banner" role="status" aria-live="polite">
    <span>{t('network.online')}</span>
  </div>
{/if}

{#if currentRoute === 'login'}
    <Login />
  {:else if currentRoute === 'setup'}
    <Setup />
  {:else}
    <Header showBack={currentRoute === 'recording-detail' || currentRoute === 'live'} />
    {#if currentRoute === 'recordings'}
      <Recordings />
    {:else if currentRoute === 'recording-detail'}
      <RecordingDetail recordingId={params.id} />
    {:else if currentRoute === 'cameras'}
      <Cameras />
    {:else if currentRoute === 'cameras-detail'}
      <Cameras />
    {:else if currentRoute === 'live'}
      <LiveView cameraId={params.id} />
    {:else if currentRoute === 'surveillance'}
      <Surveillance initialTab={params.tab || 'cameras'} />
    {:else if currentRoute === 'status'}
      <Status initialTab={params.tab || 'health'} />
    {:else if currentRoute === 'stats'}
      <Stats />
    {:else if currentRoute === 'settings'}
      <Settings />
    {:else if currentRoute === 'dashboard'}
      <Dashboard initialTab={params.tab || 'dashboard'} />
    {:else if currentRoute === 'transcoding-history'}
      <TranscodingHistory />
    {:else if currentRoute === 'timelapse'}
      <Timelapse />
    {/if}
  {/if}

<style>
  .offline-banner {
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    z-index: 1800;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 0.5rem;
    padding: 0.625rem 1rem;
    background: var(--color-danger);
    color: #ffffff;
    font-size: 0.875rem;
    font-weight: 500;
    animation: slide-down 0.25s var(--ease-out);
    box-shadow: 0 2px 8px rgba(0, 0, 0, 0.3);
  }

  .online-banner {
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    z-index: 1800;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 0.5rem;
    padding: 0.625rem 1rem;
    background: var(--color-success);
    color: #ffffff;
    font-size: 0.875rem;
    font-weight: 500;
    animation: slide-down 0.25s var(--ease-out);
    box-shadow: 0 2px 8px rgba(0, 0, 0, 0.2);
  }

  @keyframes slide-down {
    from {
      transform: translateY(-100%);
      opacity: 0;
    }
    to {
      transform: translateY(0);
      opacity: 1;
    }
  }
</style>
