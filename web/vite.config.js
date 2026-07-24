import { defineConfig } from 'vitest/config'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'
import fs from 'node:fs'

/**
 * Service-Worker cache-versioning plugin.
 *
 * Rewrites the `__SW_CACHE_VERSION__` placeholder in the built `dist/sw.js`
 * to a unique per-build stamp (timestamp). `public/sw.js` is copied verbatim
 * by Vite, so without this plugin the SW's CACHE_VERSION is a fixed literal —
 * which means a newly deployed frontend never invalidates the old SW cache and
 * users keep running stale JS chunks (orchestrator/zombie fixes would be
 * invisible to browsers that loaded an older build). With a fresh version per
 * build, the SW's `activate` handler purges every prior cache on next load.
 *
 * The version string also changes sw.js's bytes each build, so the browser
 * detects an updated SW → skipWaiting → clients.claim applies it immediately.
 */
function swVersionPlugin() {
  const version = 'mibee-nvr-' + Date.now();
  return {
    name: 'sw-cache-version',
    apply: 'build',
    // closeBundle runs after Vite has copied public/ into dist/, so dist/sw.js
    // exists and can be rewritten in place.
    closeBundle() {
      const outDir = path.resolve('dist');
      const swPath = path.join(outDir, 'sw.js');
      if (fs.existsSync(swPath)) {
        const src = fs.readFileSync(swPath, 'utf8');
        fs.writeFileSync(swPath, src.replace(/__SW_CACHE_VERSION__/g, version));
      }
    },
  };
}

// https://vite.dev/config/
export default defineConfig({
  plugins: [svelte(), tailwindcss(), swVersionPlugin()],
  resolve: {
    alias: {
      $lib: path.resolve('./src/lib'),
    },
    extensions: ['.js', '.ts', '.svelte', '.svelte.ts'],
    conditions: ['browser'],
  },
  optimizeDeps: {
    exclude: ['@yume-chan/libde265'],
  },
  build: {
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes('node_modules/chart.js')) {
            return 'vendor-chart';
          }
          if (id.includes('node_modules/svelte')) {
            return 'vendor-svelte';
          }
          if (id.includes('node_modules/hls.js')) {
            return 'vendor-hls';
          }
          if (id.includes('node_modules/lucide-svelte')) {
            return 'vendor-lucide';
          }
          if (id.includes('node_modules/onnxruntime-web')) {
            return 'vendor-onnx';
          }
          if (id.includes('node_modules/@yume-chan/libde265')) {
            return 'vendor-libde265';
          }
          if (id.includes('node_modules')) {
            return 'vendor';
          }
        },
      },
    },
  },
  test: {
    environment: 'jsdom',
  },
})
