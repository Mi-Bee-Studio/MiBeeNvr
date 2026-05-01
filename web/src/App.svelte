<script lang="ts">
  import { onMount } from 'svelte';
  import { isAuthenticated } from '$lib/api';
  import Login from './routes/Login.svelte';
  import Recordings from './routes/Recordings.svelte';
  import RecordingDetail from './routes/RecordingDetail.svelte';
  import Stats from './routes/Stats.svelte';
  import Settings from './routes/Settings.svelte';
  import Cameras from './routes/Cameras.svelte';

  // Current route
  // Current route
  let currentRoute = 'login';
  let params: Record<string, string> = {};

  // Parse hash-based routes
  function parseRoute(hash: string) {
    const path = hash.slice(1); // Remove #

    if (!path || path === '/') {
      // Default to login or recordings based on auth status
      return isAuthenticated() ? { route: 'recordings', params: {} } : { route: 'login', params: {} };
    }

    const segments = path.split('/').filter(Boolean);

    if (segments[0] === 'login') {
      return { route: 'login', params: {} };
    }

    if (segments[0] === 'recordings') {
      if (segments[1]) {
        // /recordings/:id
        return { route: 'recording-detail', params: { id: segments[1] } };
      }
      return { route: 'recordings', params: {} };
    }

    if (segments[0] === 'cameras') {
      return { route: 'cameras', params: {} };
    }

    if (segments[0] === 'stats') {
      return { route: 'stats', params: {} };
    }

    if (segments[0] === 'settings') {
      return { route: 'settings', params: {} };
    }

    // Default to login for unknown routes
    return { route: 'login', params: {} };
  }

  function updateRoute() {
    const hash = window.location.hash;
    const { route, params: routeParams } = parseRoute(hash);
    currentRoute = route;
    params = routeParams;
  }

  // Listen for hash changes
  onMount(() => {
    updateRoute();
    window.addEventListener('hashchange', updateRoute);

    return () => {
      window.removeEventListener('hashchange', updateRoute);
    };
  });
</script>

{#if currentRoute === 'login'}
  <Login />
{:else if currentRoute === 'recordings'}
  <Recordings />
{:else if currentRoute === 'recording-detail'}
  <RecordingDetail recordingId={params.id} />
{:else if currentRoute === 'cameras'}
  <Cameras />
{:else if currentRoute === 'stats'}
  <Stats />
{:else if currentRoute === 'settings'}
  <Settings />
{/if}
