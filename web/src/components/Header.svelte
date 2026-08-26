<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { t } from '$lib/i18n';
  import { withBase } from '$lib/base-path';
  import { logout, isLocalBypass, getSettings, getUpdateStatus } from '$lib/api';
  import { getMiBeeVisionConnected, refreshMiBeeVisionStatus } from '$lib/mibeevision-status';
  import { getEffectiveTheme } from '$lib/preferences';
  import LanguageSwitcher from './LanguageSwitcher.svelte';
  import ThemeToggle from './ThemeToggle.svelte';
  import Toast from './Toast.svelte';
  import { ArrowLeft, Menu, LogOut } from 'lucide-svelte';

  // Props
  let {
    activeRoute = '',
    showBack = false,
    backLabel = ''
  }: {
    activeRoute?: string;
    showBack?: boolean;
    backLabel?: string;
  } = $props();

  // MiBeeVision connection status (shared reactive store)
  let miBeeVisionConnected = $derived(getMiBeeVisionConnected());

  // Version-check badge: a subtle dot on the Settings link when an update is
  // available. Polled once on mount + on focus (cheap; backend caches + uses
  // ETag so 304s do not count against GitHub's rate limit).
  let updateAvailable = $state(false);
  async function refreshUpdateBadge() {
    try {
      const st = await getUpdateStatus();
      updateAvailable = st.update_available;
    } catch {
      // Backend may be briefly unavailable / update disabled — never bother the user.
      updateAvailable = false;
    }
  }

  // Mobile menu state
  let mobileMenuOpen = $state(false);

  function toggleMobileMenu() {
    mobileMenuOpen = !mobileMenuOpen;
  }

  function closeMobileMenu() {
    mobileMenuOpen = false;
  }

  function handleNavClick(event: Event) {
    closeMobileMenu();
  }

  // Hash change listener to keep activeRoute in sync
  function handleHashChange() {
    const hash = window.location.hash.replace('#', '') || '/surveillance';
    activeRoute = hash;
  }

  onMount(() => {
    // Sync theme — use getEffectiveTheme to handle null (system preference)
    const effectiveTheme = getEffectiveTheme();
    document.documentElement.setAttribute('data-theme', effectiveTheme);

    // Sync active route from current hash
    handleHashChange();
    window.addEventListener('hashchange', handleHashChange);

    // Check MiBeeVision API key status
    void refreshMiBeeVisionStatus();

    // Version-check badge (re-check on tab focus + every 90 min).
    void refreshUpdateBadge();
    const focusHandler = () => void refreshUpdateBadge();
    window.addEventListener('focus', focusHandler);
    const badgeTimer = window.setInterval(refreshUpdateBadge, 90 * 60 * 1000);
    onDestroy(() => {
      window.removeEventListener('focus', focusHandler);
      window.clearInterval(badgeTimer);
    });
  });

  onDestroy(() => {
    window.removeEventListener('hashchange', handleHashChange);
  });

  // Navigation items — AI Events only shown when MiBeeVision is configured
  let navItems = $derived([
    { href: '#/surveillance', labelKey: 'nav.surveillance', route: '/surveillance' },
    { href: '#/cameras', labelKey: 'nav.cameras', route: '/cameras' },
    { href: '#/recordings', labelKey: 'nav.recordings', route: '/recordings' },
    ...(miBeeVisionConnected ? [{ href: '#/ai-events', labelKey: 'nav.aiEvents', route: '/ai-events' }] : []),
    { href: '#/dashboard', labelKey: 'nav.dashboard', route: '/dashboard' },
    { href: '#/settings', labelKey: 'nav.settings', route: '/settings' },
  ]);

  function isActive(route: string): boolean {
    return activeRoute === route || activeRoute.startsWith(route + '/');
  }

  function goBack() {
    // On a recording's detail page the back button returns to the recordings
    // list ON THE WATCHED DAY (?date= carried by the detail URL) instead of
    // raw browser history — watching yesterday's recording and going back must
    // land on yesterday, not on today (#321 follow-up). Other back contexts
    // (live view) keep native history semantics.
    if (window.location.hash.startsWith('#/recordings/')) {
      const m = window.location.hash.match(/[?&]date=(\d{4}-\d{2}-\d{2})/);
      window.location.hash = m ? `#/recordings?date=${m[1]}` : '#/recordings';
      return;
    }
    window.history.back();
  }
</script>

<header class="navbar glass">
  <div class="navbar-inner">
    <div class="navbar-left">
      {#if showBack}
        <button class="back-btn" onclick={goBack}>
          <ArrowLeft size={20} />
          <span>{backLabel || t('detail.back')}</span>
        </button>
      {/if}
      <a href="#/surveillance" class="logo">
        <img src={withBase('/logo-icon.svg')} alt="" class="logo-mark" />
        <span>MiBee NVR</span>
      </a>
      
      <!-- Desktop Navigation -->
      <nav class="nav-links">
        {#each navItems as item}
          <a
            href={item.href}
            class="nav-link"
            class:active={isActive(item.route)}
            aria-label={t(item.labelKey)}
          >
            {t(item.labelKey)}
            {#if updateAvailable && item.route === '/settings'}
              <span class="update-dot" title={t('about.updateAvailable')}></span>
            {/if}
          </a>
        {/each}
      </nav>
      
      <!-- Mobile Hamburger Button -->
      <button
        class="hamburger-btn md:hidden"
        onclick={toggleMobileMenu}
        aria-label="Toggle navigation menu"
        aria-expanded={mobileMenuOpen}
      >
        <Menu size={20} />
      </button>
    </div>
    
    <!-- Mobile Menu Overlay -->
    <div class="mobile-menu md:hidden" class:open={mobileMenuOpen}>
      <nav class="mobile-nav-links">
        {#each navItems as item}
          <a
            href={item.href}
            class="mobile-nav-link"
            class:active={isActive(item.route)}
            onclick={handleNavClick}
          >
            {t(item.labelKey)}
          </a>
        {/each}
      </nav>
    </div>
    
    <div class="navbar-right">
      <ThemeToggle />
      <LanguageSwitcher />
      <!-- #516: under auth.local_bypass the loopback session is not a
           credential — there is nothing to log out of, and clearing the
           token just bounces back to the dashboard. Hide the entry. -->
      {#if !isLocalBypass()}
        <button class="btn btn-ghost logout-btn" onclick={logout}>
          <LogOut size={20} />
          <span>{t('nav.logout')}</span>
        </button>
      {/if}
    </div>
  </div>
</header>

<!-- Toast container — rendered at top level so it's always visible -->
<Toast />

<style>
  .navbar {
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    z-index: 1000;
    height: 68px;
    border-bottom: 1px solid var(--border);
    box-shadow: var(--shadow-md);
  }

  .navbar-inner {
    max-width: 80rem;
    margin: 0 auto;
    padding: 0 1rem;
    height: 100%;
    display: flex;
    align-items: center;
    justify-content: space-between;
  }

  @media (min-width: 640px) {
    .navbar-inner { padding: 0 1.5rem; }
  }

  @media (min-width: 1024px) {
    .navbar-inner { padding: 0 2rem; }
  }

  .navbar-left {
    display: flex;
    align-items: center;
    gap: 1.25rem;
  }

  .back-btn {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    color: var(--text-secondary);
    background: none;
    border: none;
    cursor: pointer;
    font-size: 0.875rem;
    font-weight: 500;
    padding: 0.375rem 0.625rem;
    border-radius: var(--radius-sm);
    transition: all var(--duration-fast) var(--ease-out);
  }

  .back-btn:hover {
    color: var(--text-primary);
    background-color: var(--bg-tertiary);
  }


  .logo {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 1.25rem;
    font-weight: 700;
    letter-spacing: -0.025em;
    text-decoration: none;
    white-space: nowrap;
    background: linear-gradient(135deg, #635bff 0%, #3a1c9e 100%);
    -webkit-background-clip: text;
    -webkit-text-fill-color: transparent;
    background-clip: text;
  }

  .logo-mark {
    width: 2rem;
    height: 2rem;
    flex-shrink: 0;
    /* SVG gradient text fill doesn't apply to the <img>; keep it crisp. */
    -webkit-text-fill-color: initial;
  }

  .nav-links {
    display: none;
    gap: 0.25rem;
    align-items: center;
  }

  @media (min-width: 768px) {
    .nav-links {
      display: flex;
    }
  }

  .nav-link {
    padding: 0.375rem 0.75rem;
    border-radius: var(--radius-sm);
    font-size: 0.875rem;
    font-weight: 500;
    color: var(--text-secondary);
    text-decoration: none;
    transition: all var(--duration-fast) var(--ease-out);
  }

  .nav-link:hover {
    color: var(--text-primary);
    background-color: var(--bg-tertiary);
  }

  .nav-link.active {
    color: #ffffff;
    background: var(--color-primary);
    position: relative;
  }

  /* Subtle indicator that a new version is available (sits on the Settings link). */
  .update-dot {
    display: inline-block;
    width: 7px;
    height: 7px;
    margin-left: 0.25rem;
    border-radius: 9999px;
    background: #f59e0b;
    box-shadow: 0 0 0 2px var(--bg-elevated, #fff);
    vertical-align: middle;
  }

  .navbar-right {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }

  .logout-btn {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
  }


  @media (max-width: 639px) {
    .logout-btn span {
      display: none;
    }
  }
  
  /* Hamburger Button */
  .hamburger-btn {
    display: none;
    background: none;
    border: none;
    color: var(--text-primary);
    cursor: pointer;
    padding: 0.5rem;
    transition: all var(--duration-fast) var(--ease-out);
    border-radius: var(--radius-sm);
  }
  
  .hamburger-btn:hover {
    background-color: var(--bg-tertiary);
  }
  
  @media (max-width: 767px) {
    .hamburger-btn {
      display: flex;
    }
  }
  
  
  /* Mobile Menu Overlay */
  .mobile-menu {
    position: absolute;
    top: 100%;
    left: 0;
    right: 0;
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    border-top: none;
    max-height: 0;
    overflow: hidden;
    transition: max-height var(--duration-normal) var(--ease-out),
                opacity var(--duration-normal) var(--ease-out);
    opacity: 0;
  }
  
  .mobile-menu.open {
    max-height: calc(100vh - 68px);
    opacity: 1;
    box-shadow: var(--shadow-lg);
    border-bottom: 1px solid var(--border);
  }
  
  /* Mobile Navigation Links */
  .mobile-nav-links {
    display: flex;
    flex-direction: column;
    padding: 0.5rem;
    gap: 0.125rem;
  }
  
  .mobile-nav-link {
    padding: 0.625rem 1rem;
    border-radius: var(--radius-sm);
    font-size: 0.875rem;
    font-weight: 500;
    color: var(--text-secondary);
    text-decoration: none;
    transition: all var(--duration-fast) var(--ease-out);
    white-space: nowrap;
    border-left: 2px solid transparent;
  }
  
  .mobile-nav-link:hover {
    color: var(--text-primary);
    background-color: var(--bg-tertiary);
  }
  
  .mobile-nav-link.active {
    background: var(--color-primary);
    color: #ffffff;
    border-left-color: transparent;
  }
  
  /* Glass effect for mobile menu */
  .mobile-menu {
    backdrop-filter: blur(var(--glass-blur));
    -webkit-backdrop-filter: blur(var(--glass-blur));
    background: var(--glass-bg);
  }
</style>
