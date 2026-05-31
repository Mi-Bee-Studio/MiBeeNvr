import { defineConfig } from 'vitest/config'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

// https://vite.dev/config/
export default defineConfig({
  plugins: [svelte(), tailwindcss()],
  resolve: {
    alias: {
      $lib: path.resolve('./src/lib'),
    },
    extensions: ['.js', '.ts', '.svelte', '.svelte.ts'],
    conditions: ['browser'],
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
