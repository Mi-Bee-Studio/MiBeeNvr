<script lang="ts">
  import { onMount } from 'svelte';
  import { getTheme, setTheme, getEffectiveTheme } from '$lib/preferences';
  import { Sun, Monitor, Moon } from 'lucide-svelte';

  let currentTheme: 'system' | 'dark' | 'light' = 'system';

  function applyTheme(theme: 'system' | 'dark' | 'light') {
    const effectiveTheme = theme === 'system' ? getEffectiveTheme() : theme;
    document.documentElement.setAttribute('data-theme', effectiveTheme);
    currentTheme = theme;
    if (theme === 'system') {
      setTheme(null);
    } else {
      setTheme(theme);
    }
  }

  function toggleTheme() {
    const states: ('system' | 'dark' | 'light')[] = ['system', 'light', 'dark'];
    const currentIndex = states.indexOf(currentTheme);
    const nextIndex = (currentIndex + 1) % states.length;
    applyTheme(states[nextIndex]);
  }

  onMount(() => {
    const savedTheme = getTheme();
    if (savedTheme !== null) {
      currentTheme = savedTheme;
    } else {
      currentTheme = 'system';
    }
    // Only apply on mount if inline script hasn't already set the theme
    if (!document.documentElement.getAttribute('data-theme') || document.documentElement.getAttribute('data-theme') === 'null') {
      applyTheme(currentTheme);
    }

    const mediaQuery = window.matchMedia('(prefers-color-scheme: light)');
    const handleSystemThemeChange = () => {
      if (currentTheme === 'system') {
        applyTheme('system');
      }
    };
    mediaQuery.addEventListener('change', handleSystemThemeChange);

    return () => {
      mediaQuery.removeEventListener('change', handleSystemThemeChange);
    };
  });
</script>

<button
  onclick={toggleTheme}
  class="btn btn-ghost w-10 h-10 p-2 rounded-full transition-all duration-200 hover:bg-tertiary"
  aria-label="Toggle theme"
  title="Toggle theme">
  {#if currentTheme === 'light'}
    <Sun size={20} />
  {:else if currentTheme === 'system'}
    <Monitor size={20} />
    <span class="sr-only">System theme</span>
  {:else}
    <Moon size={20} />
  {/if}
</button>