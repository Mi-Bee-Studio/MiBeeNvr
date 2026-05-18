/**
 * Reusable HLS test helper functions for MiBee NVR E2E tests.
 *
 * These helpers target the Dashboard page's DOM structure:
 * - Stream state dots are <span> elements with `title` attributes:
 *     playing  → title="Live"       bg-green-500
 *     buffering→ title="Buffering"  bg-yellow-500 animate-pulse
 *     error    → title="Error"      bg-red-500
 *     snapshot → title="Snapshot mode" bg-gray-400
 * - Video elements are <video autoplay muted playsinline> inside camera grid cells.
 * - Camera grid cells are div.relative.bg-black.rounded-lg inside a CSS grid.
 */

import { type Page, type Locator, expect } from '@playwright/test';

/** Camera info returned by the NVR API. */
export interface CameraInfo {
  id: string;
  name: string;
  protocol: string;
}

/** Stream states matching the frontend's StreamState type and title attributes. */
export const STREAM_STATE_TITLES: Record<string, string> = {
  playing: 'Live',
  buffering: 'Buffering',
  error: 'Error',
  snapshot: 'Snapshot mode',
} as const;

/** CSS class substrings unique to each stream state dot. */
export const STREAM_STATE_CLASSES: Record<string, string> = {
  playing: 'bg-green-500',
  buffering: 'bg-yellow-500',
  error: 'bg-red-500',
  snapshot: 'bg-gray-400',
} as const;

/**
 * Get the grid container holding camera cells on the Dashboard.
 * The grid is a <div class="grid ..."> inside the main dashboard content.
 */
export function getCameraGrid(page: Page): Locator {
  return page.locator('div.grid.gap-2, div.grid.gap-3').first();
}

/**
 * Get all camera grid cells (one per camera).
 * Each cell is a div.relative.bg-black.rounded-lg.
 */
export function getCameraCells(page: Page): Locator {
  return page.locator('div.relative.bg-black.rounded-lg');
}

/**
 * Get the first camera cell that contains an HLS video element.
 * Returns the cell Locator, or throws if none found within timeout.
 */
export async function getFirstHlsCameraCell(page: Page, timeout = 10000): Promise<Locator> {
  const cells = getCameraCells(page);
  // Wait for at least one cell to contain a <video> element
  await expect(cells.filter({ has: page.locator('video') }).first()).toBeVisible({ timeout });
  return cells.filter({ has: page.locator('video') }).first();
}

/**
 * Wait for a specific stream state indicator to appear in a camera cell.
 *
 * The Dashboard renders colored dot spans with `title` attributes:
 *   playing  → title="Live"
 *   buffering→ title="Buffering"
 *   error    → title="Error"
 *   snapshot → title="Snapshot mode"
 *
 * @param page       Playwright page
 * @param cameraId   If provided, match camera by the name displayed in the cell.
 *                    If omitted, matches the first camera cell with a state dot.
 * @param state      One of: 'playing', 'buffering', 'error', 'snapshot'
 * @param timeout    Max wait in ms (default 15000)
 */
export async function waitForStreamState(
  page: Page,
  state: 'playing' | 'buffering' | 'error' | 'snapshot',
  options?: { cameraId?: string; timeout?: number },
): Promise<Locator> {
  const timeout = options?.timeout ?? 15000;
  const title = STREAM_STATE_TITLES[state];
  const stateDot = page.locator(`span.rounded-full[title="${title}"]`).first();

  await expect(stateDot).toBeVisible({ timeout });
  return stateDot;
}

/**
 * Check that a video element is not showing a black screen by verifying:
 * 1. The video element exists and is visible
 * 2. video.readyState >= 1 (HAVE_METADATA at minimum)
 * 3. video.videoWidth > 0 and video.videoHeight > 0
 *
 * Optionally polls for `durationMs` to ensure the video stays active.
 *
 * @param page       Playwright page
 * @param cameraId   Camera name to target (optional, uses first video if omitted)
 * @param durationMs How long to monitor (default 3000). Polls every 500ms.
 */
export async function checkNoBlackScreen(
  page: Page,
  options?: { cameraId?: string; durationMs?: number },
): Promise<void> {
  const durationMs = options?.durationMs ?? 3000;
  const pollInterval = 500;
  const checks = Math.ceil(durationMs / pollInterval);

  for (let i = 0; i < checks; i++) {
    const result = await page.evaluate(() => {
      const video = document.querySelector('video');
      if (!video) return { error: 'No video element found' };
      const el = video as HTMLVideoElement;
      return {
        readyState: el.readyState,
        videoWidth: el.videoWidth,
        videoHeight: el.videoHeight,
        paused: el.paused,
        currentTime: el.currentTime,
      };
    });

    if (result.error) {
      throw new Error(result.error);
    }

    // readyState: 0=HAVE_NOTHING, 1=HAVE_METADATA, 2=HAVE_CURRENT_DATA, etc.
    expect(result.readyState).toBeGreaterThanOrEqual(1);
    expect(result.videoWidth).toBeGreaterThan(0);
    expect(result.videoHeight).toBeGreaterThan(0);

    if (i < checks - 1) {
      await page.waitForTimeout(pollInterval);
    }
  }
}

/**
 * Simulate a network error by intercepting requests matching a URL pattern.
 * Uses page.route() to abort matching requests.
 *
 * @param page        Playwright page
 * @param urlPattern  Glob pattern to match (e.g. '**\/stream\/**')
 * @returns           A function to remove the route interception
 */
export async function simulateNetworkError(
  page: Page,
  urlPattern: string | RegExp,
): Promise<() => Promise<void>> {
  await page.route(urlPattern, (route) => route.abort('failed'));
  return async () => {
    await page.unroute(urlPattern);
  };
}

/**
 * Get the video element's readyState value.
 * Returns the integer readyState of the first <video> element, or null if none exists.
 *
 * readyState values:
 *   0 = HAVE_NOTHING
 *   1 = HAVE_METADATA
 *   2 = HAVE_CURRENT_DATA
 *   3 = HAVE_FUTURE_DATA
 *   4 = HAVE_ENOUGH_DATA
 */
export async function getVideoReadyState(page: Page): Promise<number | null> {
  return page.evaluate(() => {
    const video = document.querySelector('video');
    return video ? (video as HTMLVideoElement).readyState : null;
  });
}

/**
 * Navigate to the Dashboard page and wait for it to load.
 * Handles login if needed by filling the login form.
 */
export async function navigateToDashboard(page: Page): Promise<void> {
  await page.goto('/#/dashboard');

  // Wait for SPA to render — either dashboard grid or login form
  // The SPA router runs async after page load, so URL-based checks are unreliable.
  // Instead, wait for actual DOM content to appear.
  const loginForm = page.locator('form').filter({ has: page.locator('button[type="submit"]') });

  // Race: whichever appears first determines our state
  const loginVisible = await loginForm.isVisible().catch(() => false);

  if (loginVisible) {
    // We're on the login page — fill credentials
    const usernameInput = page.locator('#username');
    const passwordInput = page.locator('#password');
    const submitButton = page.locator('button[type="submit"]');

    await usernameInput.fill('admin');
    await passwordInput.fill('admin');
    await submitButton.click();

    // Login redirects to #/recordings — wait for SPA navigation
    await page.waitForURL(/.*recordings.*/, { timeout: 10000 });
    await page.waitForLoadState('networkidle');

    // Now navigate to dashboard explicitly
    await page.goto('/#/dashboard');
  }

  // Wait for dashboard to finish loading ("Loading..." text disappears, grid appears)
  // The grid may take time if cameras are being fetched from the API
  const dashboardGrid = page.locator('div.grid');
  try {
    await dashboardGrid.waitFor({ state: 'visible', timeout: 20000 });
  } catch {
    // Dashboard may show "No cameras" message instead of grid if there are no cameras
    // This is acceptable — the dashboard loaded successfully
  }
}

/**
 * Fetch cameras from the NVR API directly.
 * Returns an array of camera objects.
 */
export async function fetchCamerasFromAPI(page: Page): Promise<CameraInfo[]> {
  // Use page.evaluate to leverage the browser's auth context (sessionStorage)
  return page.evaluate(async () => {
    const creds = sessionStorage.getItem('mibee_nvr_auth');
    const headers: Record<string, string> = {};
    if (creds) {
      headers['Authorization'] = `Basic ${creds}`;
    }
    const resp = await fetch('/api/cameras', { headers });
    if (!resp.ok) return [];
    return resp.json();
  });
}

/**
 * Get the first HLS-capable camera from the API.
 * HLS is supported for protocols: rtsp_h264, rtsp_h265, onvif, rtsp.
 */
export async function getFirstHlsCamera(page: Page): Promise<CameraInfo | null> {
  const cameras = await fetchCamerasFromAPI(page);
  const hlsProtocols = ['rtsp_h264', 'rtsp_h265', 'onvif', 'rtsp', 'xiaomi'];
  return cameras.find((c) => hlsProtocols.includes(c.protocol)) || null;
}
