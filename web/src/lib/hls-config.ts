/**
 * Shared hls.js configuration optimized for modern browsers.
 *
 * Larger buffer sizes for smoother playback. enableWorker for off-thread parsing.
 */

import { getCredentials } from '$lib/api';
import type Hls from 'hls.js';

/** Modern browser optimized hls.js configuration. */
export function createHlsConfig(): Partial<Hls.Config> {
  return {
    enableWorker: true,
    maxBufferLength: 15,
    maxMaxBufferLength: 30,
    maxBufferSize: 30 * 1024 * 1024, // 30 MB
    backBufferLength: 5,
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
