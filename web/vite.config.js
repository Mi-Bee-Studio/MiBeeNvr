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

/**
 * onnxruntime-web WASM asset plugin.
 *
 * onnxruntime-web 1.27's threaded WASM backend loads a sibling pair at runtime:
 *   ort-wasm-simd-threaded.jsep.mjs  (ESM worker module — spawns the threads)
 *   ort-wasm-simd-threaded.jsep.wasm (the binary)
 * Vite's code-splitting copies the .wasm as a hashed asset but DOES NOT emit
 * the .mjs (it's only referenced by ORT's internal dynamic import() at runtime,
 * which Vite can't see). Without the .mjs, ORT fails to spawn the worker and
 * reports "no available backend" — which AiRuntime surfaces as a misleading
 * "protobuf parsing failed" (issue #109).
 *
 * This plugin copies both files from node_modules into dist/ort/ at their
 * canonical (un-hashed) names, and runtime.ts sets `ort.env.wasmPaths='/ort/'`
 * so ORT finds them. A fixed path (not hashed) is required because ORT builds
 * the filenames itself and can't be told the hash.
 */
function ortAssetsPlugin() {
  return {
    name: 'ort-wasm-assets',
    apply: 'build',
    closeBundle() {
      const outDir = path.resolve('dist');
      const ortDir = path.join(outDir, 'ort');
      fs.mkdirSync(ortDir, { recursive: true });
      const ortPkgDir = path.resolve('node_modules/onnxruntime-web/dist');
      // The .mjs worker module + the matching un-hashed .wasm binary, served
      // at /ort/ for ort.env.wasm.wasmPaths to find.
      for (const file of ['ort-wasm-simd-threaded.jsep.mjs', 'ort-wasm-simd-threaded.jsep.wasm']) {
        const src = path.join(ortPkgDir, file);
        if (fs.existsSync(src)) {
          fs.copyFileSync(src, path.join(ortDir, file));
        }
      }
      // Also copy the all-bundle ESM build (ort.all.bundle.min.mjs). This build
      // inlines the wasm-JS glue so it doesn't depend on the separate .mjs
      // worker resolution, useful as an alternative load path.
      const bundleMjs = path.join(ortPkgDir, 'ort.all.bundle.min.mjs');
      if (fs.existsSync(bundleMjs)) {
        fs.copyFileSync(bundleMjs, path.join(ortDir, 'ort.all.bundle.min.mjs'));
      }
      // Also copy the UMD bundle (ort.min.js) to dist root. runtime.ts loads ORT
      // via this UMD script (NOT via Vite's bundled import) because Vite's
      // code-splitting breaks onnxruntime-web 1.27's internal wasm/worker path
      // resolution — the bundled chunk reports INVALID_PROTOBUF (ERROR_CODE 7)
      // on model load, while the stock UMD bundle loaded from /ort.min.js with
      // wasmPaths='/ort/' works correctly (verified via isolated test page on
      // Banana Pi M5, issue #109).
      const umd = path.join(ortPkgDir, 'ort.min.js');
      if (fs.existsSync(umd)) {
        fs.copyFileSync(umd, path.join(outDir, 'ort.min.js'));
      }
    },
  };
}

// https://vite.dev/config/
export default defineConfig({
  plugins: [svelte(), tailwindcss(), swVersionPlugin(), ortAssetsPlugin()],
  resolve: {
    alias: {
      $lib: path.resolve('./src/lib'),
    },
    extensions: ['.js', '.ts', '.svelte', '.svelte.ts'],
    conditions: ['browser'],
  },
  optimizeDeps: {
    // @yume-chan/libde265 dynamically loads its WASM blob in a way Vite's
    // dep-optimizer mis-resolves; exclude it so the WASM loader runs at
    // runtime via dynamic import(). (opus-decoder, which would have the same
    // issue, is not used — Opus goes through WebCodecs AudioDecoder instead.
    // @audio/decode-aac was removed entirely in #319.)
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
          // Note: opus-decoder is deliberately NOT given an explicit
          // manualChunk. Forcing it into a separate named chunk triggers a
          // Rolldown panic ("Symbol assignNames should belong to a chunk").
          // Vite's default code-splitting keeps the payload lazy.
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
