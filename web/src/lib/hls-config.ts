/**
 * Shared hls.js configuration optimized for Raspberry Pi 3B.
 *
 * Low buffer sizes minimize memory usage on constrained devices.
 * enableWorker:false ensures compatibility with RPi browsers.
 */

import { getCredentials } from '$lib/api';
import type Hls from 'hls.js';

/** RPi-optimized hls.js configuration. */
export function createHlsConfig(): Partial<Hls.Config> {
  return {
    enableWorker: false,
    maxBufferLength: 5,
    maxMaxBufferLength: 10,
    maxBufferSize: 10 * 1024 * 1024, // 10 MB
    backBufferLength: 2,
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
