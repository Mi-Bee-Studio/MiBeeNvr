<script lang="ts">
  import { onMount } from 'svelte';
  import { getTheme, setTheme } from '$lib/preferences';

  let currentTheme: 'dark' | 'light' = 'dark';
  let isTransitioning = false;

  // Apply theme to document element
  function applyTheme(theme: 'dark' | 'light') {
    if (isTransitioning) return;
    
    isTransitioning = true;
    document.documentElement.setAttribute('data-theme', theme);
    setTheme(theme);
    currentTheme = theme;
    
    // Ensure CSS transition completes before allowing next toggle
    setTimeout(() => {
      isTransitioning = false;
    }, 300);
  }

  // Toggle between themes
  function toggleTheme() {
    const newTheme = currentTheme === 'dark' ? 'light' : 'dark';
    applyTheme(newTheme);
  }

  // Initialize theme on component mount
  onMount(() => {
    currentTheme = getTheme();
    document.documentElement.setAttribute('data-theme', currentTheme);
  });
</script>

<button
  onclick={toggleTheme}
  class="btn btn-ghost w-10 h-10 p-2 rounded-full transition-all duration-200 hover:bg-tertiary"
  aria-label="Toggle theme"
  title="Toggle theme"
  disabled={isTransitioning}
>
  {#if currentTheme === 'light'}
    <!-- Sun icon for light theme -->
    <svg 
      viewBox="0 0 24 24" 
      fill="none" 
      stroke="currentColor" 
      stroke-width="2"
      class="w-5 h-5"
      aria-hidden="true"
    >
      <circle cx="12" cy="12" r="5" />
      <line x1="12" y1="1" x2="12" y2="3" />
      <line x1="16.65" y1="11.35" x2="21" y2="16.65" />
      <line x1="21" y1="11.35" x2="16.65" y2="16.65" />
      <line x1="16.65" y1="7.65" x2="21" y2="2.35" />
      <line x1="21" y1="2.35" x2="16.65" y2="7.65" />
    </svg>
  {:else}
    <!-- Moon icon for dark theme -->
    <svg 
      viewBox="0 0 24 24" 
      fill="none" 
      stroke="currentColor" 
      stroke-width="2"
      class="w-5 h-5"
      aria-hidden="true"
    >
      <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z" />
      <path d="M17.354 6.344l-1.414-1.415A7 7 0 0 0 14.071 0 7 7 0 0 0 14.071 0l1.414-1.414A9 9 0 0 1 17.354 6.344z" />
    </svg>
  {/if}
</button>