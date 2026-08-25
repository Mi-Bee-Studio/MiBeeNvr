import { mount } from 'svelte';
import './app.css';
import { initI18n } from './lib/i18n';
import { checkLocalBypass, tryGatewaySession } from './lib/api/client';

import App from './App.svelte';

initI18n();

// Bootstrap auth state BEFORE mount so the synchronous isAuthenticated()
// route gate in App.svelte sees the final result:
//
// 1. Local-access bypass: if the browser runs on the machine hosting the NVR
//    itself (/api/health reports local_access=true), the backend skips
//    credential checks and the SPA skips the login page.
// 2. Unified-gateway SSO (#394): inside the fnOS desktop this fetches a signed
//    session token from the gateway. Elsewhere it fails fast (401/timeout) and
//    the login page shows.
//
// mount() still runs exactly once, just after the (bounded) awaits.
(async () => {
  await checkLocalBypass();
  await tryGatewaySession();
  mount(App, {
    target: document.getElementById('app'),
  });
})();
