<script lang="ts">
  import { onMount } from 'svelte';
  import { isAuthenticated, healthCheck } from '$lib/api';
  import { t } from '$lib/i18n';
  import { WifiOff } from 'lucide-svelte';
  // Route loader map — lazy loaded on demand
  const routeLoaders = {
    login: () => import('./routes/Login.svelte'),
    setup: () => import('./routes/Setup.svelte'),
    recordings: () => import('./routes/Recordings.svelte'),
    'recording-detail': () => import('./routes/RecordingDetail.svelte'),
    'timelapse-merge': () => import('./routes/TimelapseMergeDetail.svelte'),
    cameras: () => import('./routes/Cameras.svelte'),
    'cameras-detail': () => import('./routes/Cameras.svelte'),
    live: () => import('./routes/LiveView.svelte'),
    surveillance: () => import('./routes/Surveillance.svelte'),
    settings: () => import('./routes/Settings.svelte'),
    dashboard: () => import('./routes/Dashboard.svelte'),
    'transcoding-history': () => import('./routes/TranscodingHistory.svelte'),
    'ai-events': () => import('./routes/AIEvents.svelte'),
  };
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

  // SW 503 detection — when the Service Worker returns offline responses
  function handleApiOffline() {
    if (navigator.onLine) {
      // Browser thinks we're online but API is unreachable — show banner briefly
      showOfflineBanner = true;
      if (onlineBannerTimer) clearTimeout(onlineBannerTimer);
      onlineBannerTimer = setTimeout(() => { showOfflineBanner = false; }, 5000);
    } else {
      handleOffline();
    }
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
    let path = hash.slice(1); // Remove #

    // Strip query parameters from hash for routing
    const qIdx = path.indexOf('?');
    if (qIdx !== -1) {
        path = path.slice(0, qIdx);
    }

    if (!path || path === '/') {
      return isAuthenticated() ? { route: 'surveillance', params: {} } : { route: 'login', params: {} };
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

    if (segments[0] === 'timelapse-merge') {
      if (segments[1]) {
        return { route: 'timelapse-merge', params: { id: segments[1] } };
      }
      // No id → redirect to recordings list.
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


    if (segments[0] === 'status') {
      window.location.replace('#/dashboard/health');
      return parseRoute('#/dashboard/health');
    }
    if (segments[0] === 'stats') {
      window.location.replace('#/dashboard');
      return parseRoute('#/dashboard');
    }

    if (segments[0] === 'settings') {
      return { route: 'settings', params: {} };
    }

    if (segments[0] === 'transcoding-history') {
      return { route: 'transcoding-history', params: {} };
    }

    if (segments[0] === 'ai-events') {
      return { route: 'ai-events', params: {} };
    }

    if (segments[0] === 'dashboard') {
      const tab = segments[1] === 'health' ? 'health' : segments[1] === 'transcoding' ? 'transcoding' : 'storage';
      return { route: 'dashboard', params: { tab } };
    }
    if (segments[0] === 'surveillance') {
      return { route: 'surveillance', params: {} };
    }

    // Default to login for unknown routes
    return { route: 'login', params: {} };
  }

  // Current route — initialize from hash synchronously to prevent
  // Login component from redirecting to recordings before onMount runs
  // Redirect legacy routes
  if (typeof window !== 'undefined') {
    if (window.location.hash === '#/health' || window.location.hash.startsWith('#/health/')) {
      window.location.replace('#/dashboard/health');
    } else if (window.location.hash === '#/stats' || window.location.hash.startsWith('#/stats/')) {
      window.location.replace('#/dashboard');
    } else if (window.location.hash === '#/status' || window.location.hash.startsWith('#/status/')) {
      window.location.replace('#/dashboard/health');
    } else if (window.location.hash === '#/timelapse' || window.location.hash.startsWith('#/timelapse')) {
      window.location.replace('#/recordings');
    }
  }

  const initialRoute = typeof window !== 'undefined' ? parseRoute(window.location.hash) : { route: 'login', params: {} };
  let currentRoute = $state(initialRoute.route);
  let params: Record<string, string> = $state(initialRoute.params);


  function updateRoute() {
    const hash = window.location.hash;
    // Redirect legacy routes
    if (hash === '#/health' || hash.startsWith('#/health/')) {
      window.location.replace('#/dashboard/health');
      return;
    }
    if (hash === '#/stats' || hash.startsWith('#/stats/')) {
      window.location.replace('#/dashboard');
      return;
    }
    if (hash === '#/status' || hash.startsWith('#/status/')) {
      window.location.replace('#/dashboard/health');
      return;
    }
    if (hash === '#/timelapse' || hash.startsWith('#/timelapse')) {
      window.location.replace('#/recordings');
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
    window.addEventListener('nvr-api-offline', handleApiOffline);

    return () => {
      window.removeEventListener('hashchange', updateRoute);
      window.removeEventListener('offline', handleOffline);
      window.removeEventListener('online', handleOnline);
      window.removeEventListener('nvr-api-offline', handleApiOffline);
      window.removeEventListener('online', handleOnline);
      if (onlineBannerTimer) clearTimeout(onlineBannerTimer);
    };
  });

  function getRouteProps(route: string) {
    switch (route) {
      case 'recording-detail': return { recordingId: params.id };
      case 'timelapse-merge': return { mergeId: params.id };
      case 'live': return { cameraId: params.id };
      case 'dashboard': return { initialTab: params.tab || 'storage' };
      default: return {};
    }
  }
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

{#if currentRoute === 'login' || currentRoute === 'setup'}
  {#await routeLoaders[currentRoute]()}
    <div class="skeleton skeleton--page"></div>
  {:then module}
    <module.default />
  {/await}
{:else}
  <Header showBack={currentRoute === 'recording-detail' || currentRoute === 'live'} />
  <!-- Compute route props OUTSIDE the {#await} block so they update reactively
       when params change (even if currentRoute name stays the same, e.g.
       navigating from recording A to recording B). Inside {#await}, expressions
       only re-evaluate when the awaited promise re-resolves. -->
  {@const routeProps = getRouteProps(currentRoute)}
  {#key currentRoute + '|' + (params.id || '')}
  {#await routeLoaders[currentRoute]()}
    <div class="skeleton skeleton--page"></div>
  {:then module}
    <module.default {...routeProps} />
  {/await}
  {/key}
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

  .skeleton {
    background: var(--bg-secondary);
    border-radius: var(--radius-md);
    animation: pulse 1.5s ease-in-out infinite;
  }

  .skeleton--page {
    min-height: 60vh;
    margin: 1rem;
  }

  @keyframes pulse {
    0%, 100% { opacity: 0.4; }
    50% { opacity: 0.8; }
  }

</style>
