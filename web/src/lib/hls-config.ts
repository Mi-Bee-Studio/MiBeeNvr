/**
 * Shared hls.js configuration optimized for RPi.
 *
 * Conservative buffer sizes for 512MB RAM. enableWorker disabled for Web Worker compat.
 */

import { getCredentials } from '$lib/api';
import type Hls from 'hls.js';

/** RPi-optimized hls.js configuration. When protocol is 'll-hls', returns LL-HLS tuned config. */
export function createHlsConfig(protocol: string = 'hls'): Partial<Hls.Config> {
  const baseConfig = {
    enableWorker: false,
    liveDurationInfinity: true,
    progressive: true,
    xhrSetup: (xhr: XMLHttpRequest, url: string) => {
      const creds = getCredentials();
      if (creds) {
        if (!xhr.readyState) {
          xhr.open('GET', url, true);
        }
        xhr.setRequestHeader('Authorization', 'Basic ' + btoa(`${creds.username}:${creds.password}`));
      }
    },
    // HLS.js 1.6+ uses fetch by default; xhrSetup alone doesn't add auth to fetch requests.
    fetchSetup: (context, initParams) => {
      const creds = getCredentials();
      if (creds) {
        initParams.headers = {
          ...initParams.headers,
          'Authorization': 'Basic ' + btoa(`${creds.username}:${creds.password}`),
        };
      }
      return new Request(context.url, initParams);
    },
  };

  if (protocol === 'll-hls') {
    return {
      ...baseConfig,
      lowLatencyMode: true,
      maxBufferLength: 10,
      maxMaxBufferLength: 12,
      maxBufferSize: 10 * 1024 * 1024,
      backBufferLength: 2.0,
      liveSyncDurationCount: 3,
      liveMaxLatencyDurationCount: 5,
    };
  }

  return {
    ...baseConfig,
    maxBufferLength: 10,
    maxMaxBufferLength: 20,
    maxBufferSize: 15 * 1024 * 1024, // 15 MB
    backBufferLength: 3,
    liveSyncDurationCount: 3,
    liveMaxLatencyDurationCount: 7,
  };
}
