<script lang="ts">
  import { onMount } from 'svelte';
  import { isAuthenticated } from '$lib/api';
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
  import Archives from './routes/Archives.svelte';

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

    if (segments[0] === 'stats') {
      return { route: 'stats', params: {} };
    }


    if (segments[0] === 'archives') {
      return { route: 'archives', params: {} };
    }


    if (segments[0] === 'settings') {
      return { route: 'settings', params: {} };
    }

    if (segments[0] === 'dashboard') {
      return { route: 'dashboard', params: {} };
    }

    // Default to login for unknown routes
    return { route: 'login', params: {} };
  }

  // Current route — initialize from hash synchronously to prevent
  // Login component from redirecting to recordings before onMount runs
  const initialRoute = typeof window !== 'undefined' ? parseRoute(window.location.hash) : { route: 'login', params: {} };
  let currentRoute = initialRoute.route;
  let params: Record<string, string> = initialRoute.params;


  function updateRoute() {
    const hash = window.location.hash;
    const { route, params: routeParams } = parseRoute(hash);
    currentRoute = route;
    params = routeParams;
  }

  // Listen for hash changes + network status
  onMount(() => {
    updateRoute();
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
    {:else if currentRoute === 'stats'}
      <Stats />
    {:else if currentRoute === 'settings'}
      <Settings />
    {:else if currentRoute === 'dashboard'}
      <Dashboard />
    {:else if currentRoute === 'archives'}
      <Archives />
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
