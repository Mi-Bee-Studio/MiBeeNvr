import { mount } from 'svelte';
import './app.css';
import { initI18n } from './lib/i18n';
import { tryGatewaySession } from './lib/api/client';

import App from './App.svelte';

initI18n();

// Unified-gateway SSO bootstrap (#394): inside the fnOS desktop this fetches a
// signed session token from the gateway before the app mounts, so the
// synchronous isAuthenticated() route gate sends the user straight to the
// dashboard. Elsewhere it fails fast (401/timeout) and the login page shows.
// mount() still runs exactly once, just after the (bounded) await.
(async () => {
  await tryGatewaySession();
  mount(App, {
    target: document.getElementById('app'),
  });
})();
