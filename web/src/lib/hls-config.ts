/**
 * Shared hls.js configuration optimized for RPi.
 *
 * Conservative buffer sizes for 512MB RAM. enableWorker disabled for Web Worker compat.
 */

import { getCredentials } from '$lib/api';
import type Hls from 'hls.js';

/** RPi-optimized hls.js configuration. */
export function createHlsConfig(): Partial<Hls.Config> {
  return {
    enableWorker: false,
    maxBufferLength: 10,
    maxMaxBufferLength: 20,
    maxBufferSize: 15 * 1024 * 1024, // 15 MB
    backBufferLength: 3,
    liveSyncDurationCount: 3,
    liveMaxLatencyDurationCount: 7,
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
  };
}
