/**
 * Shared hls.js configuration optimized for RPi.
 *
 * Conservative buffer sizes for 512MB RAM. enableWorker disabled for Web Worker compat.
 *
 * HLS is ALWAYS low-latency now (the LL-HLS/HLS distinction was collapsed —
 * lowLatencyMode:true is on for every mount, and the smaller LL-HLS buffer
 * sizes are the single config). The `protocol` argument is retained for
 * backward-compat with existing callers but no longer changes the output.
 */

import { getAuthHeader } from '$lib/api';
import type Hls from 'hls.js';

/** RPi-optimized hls.js configuration. Always low-latency. */
export function createHlsConfig(_protocol: string = 'hls'): Partial<Hls.Config> {
  return {
    enableWorker: false,
    liveDurationInfinity: true,
    progressive: true,
    maxLiveSyncPlaybackRate: 1.0,
    lowLatencyMode: true,
    // Fragment retry: more retries with shorter initial delay so transient
    // network blips recover fast without escalating to fatal MEDIA_ERROR
    // (which rebuilds MediaSource and causes black flashing).
    fragLoadPolicy: {
      default: {
        maxTimeToFirstByteMs: 10_000,
        maxLoadTimeMs: 120_000,
        timeoutRetry: {
          maxNumRetry: 10,
          retryDelayMs: 500,
          maxRetryDelayMs: 16_000,
        },
        errorRetry: {
          maxNumRetry: 10,
          retryDelayMs: 500,
          maxRetryDelayMs: 16_000,
        },
      },
    },
    // Manifest retry: faster timeout for playlist reload
    manifestLoadPolicy: {
      default: {
        maxTimeToFirstByteMs: 15_000,
        maxLoadTimeMs: 20_000,
        timeoutRetry: {
          maxNumRetry: 4,
          retryDelayMs: 0,
          maxRetryDelayMs: 8000,
        },
        errorRetry: {
          maxNumRetry: 4,
          retryDelayMs: 1000,
          maxRetryDelayMs: 8000,
        },
      },
    },
    xhrSetup: (xhr: XMLHttpRequest, url: string) => {
      const authHeader = getAuthHeader();
      if (authHeader) {
        if (!xhr.readyState) {
          xhr.open('GET', url, true);
        }
        xhr.setRequestHeader('Authorization', authHeader);
      }
    },
    // HLS.js 1.6+ uses fetch by default; xhrSetup alone doesn't add auth to fetch requests.
    fetchSetup: (context, initParams) => {
      const authHeader = getAuthHeader();
      if (authHeader) {
        initParams.headers = {
          ...initParams.headers,
          Authorization: authHeader,
        };
      }
      return new Request(context.url, initParams);
    },
    // Low-latency buffer tuning (formerly the 'll-hls' branch). Tighter buffers
    // give ~2-5s glass-to-glass latency on a home NVR with no CDN; RPi 3B's
    // 512MB RAM handles the smaller back-buffer fine.
    maxBufferLength: 10,
    maxMaxBufferLength: 12,
    maxBufferSize: 10 * 1024 * 1024, // 10 MB
    backBufferLength: 2.0,
    liveSyncDurationCount: 3,
    liveMaxLatencyDurationCount: 5,
  };
}
