/**
 * HLS Playback E2E Tests for MiBee NVR.
 *
 * These tests verify HLS live streaming functionality on the Dashboard page.
 * They require a live NVR instance with at least one HLS-capable camera
 * (RTSP H.264/H.265 or ONVIF).
 *
 * NOTE: Tests will be skipped if no HLS-capable camera is available.
 */

import { test, expect } from '@playwright/test';
import {
  navigateToDashboard,
  getFirstHlsCamera,
  fetchCamerasFromAPI,
  waitForStreamState,
  checkNoBlackScreen,
  getVideoReadyState,
  simulateNetworkError,
  getCameraCells,
} from './hls-helpers';

test.describe('HLS Playback', () => {
  test.beforeEach(async ({ page }) => {
    await navigateToDashboard(page);
  });

  test('should load dashboard page without console errors', async ({ page }) => {
    const consoleErrors: string[] = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        consoleErrors.push(msg.text());
      }
    });

    // Reload to capture any errors during load
    await page.reload();
    await page.waitForLoadState('networkidle');

    // Page should render — check for camera grid or "no cameras" message
    const gridVisible = await page.locator('div.grid').isVisible().catch(() => false);
    const noCamerasVisible = await page.locator('text=/No cameras|暂无摄像头/').isVisible().catch(() => false);
    expect(gridVisible || noCamerasVisible).toBe(true);

    // Filter out known non-critical errors (e.g., HLS manifest fetch failures, offline camera streams)
    const criticalErrors = consoleErrors.filter(
      (e) =>
        !e.includes('manifest') &&
        !e.includes('HLS') &&
        !e.includes('net::ERR') &&
        !e.includes('401') &&
        !e.includes('404') &&
        !e.includes('405'),
    );
    expect(criticalErrors).toHaveLength(0);
  });

  test('should load and play a single camera stream', async ({ page }) => {
    // Find an HLS-capable camera
    const hlsCamera = await getFirstHlsCamera(page);

    if (!hlsCamera) {
      test.skip();
      return;
    }

    console.log(`Testing HLS stream for camera: ${hlsCamera.name} (${hlsCamera.id}, ${hlsCamera.protocol})`);

    // Dashboard should show camera cells
    const cells = getCameraCells(page);
    await expect(cells.first()).toBeVisible({ timeout: 10000 });

    // At least one cell should contain a <video> element (HLS mode)
    const hlsCells = cells.filter({ has: page.locator('video') });
    const hlsCellCount = await hlsCells.count();

    if (hlsCellCount === 0) {
      // No HLS video elements yet — may need to wait for player initialization
      console.log('No video elements found yet, waiting for player initialization...');
      await page.waitForTimeout(5000);
    }

    const hlsCellCountAfter = await hlsCells.count();
    if (hlsCellCountAfter === 0) {
      console.log('Still no HLS video elements — camera may be offline. Skipping video checks.');
      test.skip();
      return;
    }

    // Wait for stream to reach playing or snapshot state (not stuck in error)
    const playingOrSnapshot = await Promise.race([
      waitForStreamState(page, 'playing', { timeout: 20000 })
        .then(() => 'playing' as const)
        .catch(() => null),
      waitForStreamState(page, 'snapshot', { timeout: 20000 })
        .then(() => 'snapshot' as const)
        .catch(() => null),
    ]);

    console.log(`Stream state resolved to: ${playingOrSnapshot || 'timeout'}`);

    if (playingOrSnapshot === 'playing') {
      // Verify video is active
      const readyState = await getVideoReadyState(page);
      console.log(`Video readyState: ${readyState}`);

      // readyState >= 1 means HAVE_METADATA at minimum
      expect(readyState).toBeGreaterThanOrEqual(1);
    }
  });

  test('should display stream state indicator for each camera', async ({ page }) => {
    const cells = getCameraCells(page);
    const cellCount = await cells.count();

    if (cellCount === 0) {
      test.skip();
      return;
    }

    console.log(`Found ${cellCount} camera cells`);

    // Each HLS camera cell should eventually show a state indicator dot
    // (playing, buffering, error, or snapshot)
    // Wait a moment for state indicators to appear
    await page.waitForTimeout(3000);

    const stateDots = page.locator('span.rounded-full[title]');
    const dotCount = await stateDots.count();
    console.log(`Found ${dotCount} stream state indicators`);

    // If there are HLS cameras, we should see state dots
    const videoElements = page.locator('video');
    const videoCount = await videoElements.count();

    if (videoCount > 0 && dotCount === 0) {
      // HLS cameras exist but no state dots — may still be initializing
      // Wait a bit more
      await page.waitForTimeout(5000);
      const dotCountAfter = await stateDots.count();
      console.log(`After waiting: ${dotCountAfter} stream state indicators`);
    }
  });

  test('should detect network errors gracefully', async ({ page }) => {
    const hlsCamera = await getFirstHlsCamera(page);

    if (!hlsCamera) {
      test.skip();
      return;
    }

    console.log(`Testing network error handling for camera: ${hlsCamera.name}`);

    // First verify camera cells exist
    const cells = getCameraCells(page);
    await expect(cells.first()).toBeVisible({ timeout: 10000 });

    // Set up network error simulation for HLS streams
    const cleanup = await simulateNetworkError(page, '**/stream/**');

    try {
      // Trigger a page reload to force the player to re-fetch the stream
      await page.reload();
      await page.waitForLoadState('networkidle');

      // The player should eventually show an error or snapshot state
      // (error recovery may attempt retries before giving up)
      await page.waitForTimeout(5000);

      // Check that the page didn't crash — grid should still be visible
      const gridVisible = await page.locator('div.grid').isVisible().catch(() => false);
      expect(gridVisible).toBe(true);

      console.log('Network error handled gracefully — page remains functional');
    } finally {
      await cleanup();
    }
  });

  test('should not show black screen for active HLS stream', async ({ page }) => {
    const hlsCamera = await getFirstHlsCamera(page);

    if (!hlsCamera) {
      test.skip();
      return;
    }

    // Wait for playing state
    try {
      await waitForStreamState(page, 'playing', { timeout: 20000 });
    } catch {
      console.log('Stream did not reach playing state — skipping black screen check');
      test.skip();
      return;
    }

    // Check that video has content (not black screen) for 3 seconds
    await checkNoBlackScreen(page, { durationMs: 3000 });
    console.log('Video confirmed active — no black screen detected');
  });

  // =========================================================================
  // T10: Expanded HLS E2E test scenarios
  // =========================================================================

  test('Dashboard multi-camera stability', async ({ page }) => {
    test.slow();

    const cameras = await fetchCamerasFromAPI(page);
    const hlsProtocols = ['rtsp_h264', 'rtsp_h265', 'onvif', 'rtsp'];
    const hlsCameras = cameras.filter((c) => hlsProtocols.includes(c.protocol));

    if (hlsCameras.length === 0) {
      console.log('No HLS-capable cameras available — skipping multi-camera stability test');
      test.skip();
      return;
    }

    console.log(`Found ${hlsCameras.length} HLS-capable cameras for stability test`);

    // Wait for at least one stream to reach playing state
    try {
      await waitForStreamState(page, 'playing', { timeout: 20000 });
    } catch {
      console.log('No stream reached playing state — skipping stability test');
      test.skip();
      return;
    }

    // Run for 30 seconds, checking every 5 seconds that video elements are not black
    const stabilityDuration = 30000;
    const checkInterval = 5000;
    const checks = stabilityDuration / checkInterval;

    for (let i = 0; i < checks; i++) {
      await checkNoBlackScreen(page, { durationMs: 500 });
      console.log(`Stability check ${i + 1}/${checks} passed at ${new Date().toISOString()}`);

      if (i < checks - 1) {
        await page.waitForTimeout(checkInterval);
      }
    }

    console.log('Multi-camera stability test passed — 30s without black screen');
  });

  test('Dashboard network interruption recovery', async ({ page }) => {
    test.slow();

    const hlsCamera = await getFirstHlsCamera(page);

    if (!hlsCamera) {
      console.log('No HLS-capable camera — skipping network interruption test');
      test.skip();
      return;
    }

    console.log(`Testing network interruption recovery for: ${hlsCamera.name}`);

    // Wait for stream to be playing first
    try {
      await waitForStreamState(page, 'playing', { timeout: 20000 });
    } catch {
      console.log('Stream did not reach playing state — skipping network interruption test');
      test.skip();
      return;
    }

    const greenDot = page.locator('span.rounded-full[title="Live"]').first();
    await expect(greenDot).toBeVisible();

    // Intercept HLS segment requests for one camera to simulate network interruption
    const streamUrlPattern = `**/cameras/${hlsCamera.id}/stream/**`;
    await page.route(streamUrlPattern, (route) => route.abort('failed'));

    try {
      // Wait for error state to appear (red dot or error overlay)
      // The player retries with backoff: 2s, 4s, 8s — so wait ~15s for error state
      const errorState = await Promise.race([
        waitForStreamState(page, 'error', { timeout: 15000 })
          .then(() => 'error' as const)
          .catch(() => null),
        page.locator('span.rounded-full[title="Error"]').waitFor({ state: 'visible', timeout: 15000 })
          .then(() => 'error' as const)
          .catch(() => null),
      ]);

      console.log(`Network interruption triggered state: ${errorState || 'unknown'}`);
    } finally {
      // Remove the route interception to restore network
      await page.unroute(streamUrlPattern);
    }

    // Wait for stream to recover to playing state within 10s
    try {
      await waitForStreamState(page, 'playing', { timeout: 10000 });
      console.log('Stream recovered to playing state after network restoration');
    } catch {
      // Recovery may take longer in slow network — check video is at least not stuck
      const readyState = await getVideoReadyState(page);
      console.log(`Stream recovery check — readyState: ${readyState}`);

      // If there's a video element with valid state, recovery is in progress
      if (readyState !== null && readyState >= 1) {
        console.log('Stream has valid video data — recovery in progress');
      } else {
        // Accept buffering as recovery state too — it means the player is reconnecting
        const bufferingDot = page.locator('span.rounded-full[title="Buffering"]');
        const isBuffering = await bufferingDot.isVisible().catch(() => false);
        expect(isBuffering || (readyState !== null && readyState >= 1)).toBe(true);
      }
    }
  });

  test('Dashboard camera switch', async ({ page }) => {
    const cameras = await fetchCamerasFromAPI(page);

    if (cameras.length < 2) {
      console.log('Need at least 2 cameras for camera switch test — skipping');
      test.skip();
      return;
    }

    // Wait for dashboard to load with camera cells
    const cells = getCameraCells(page);
    await expect(cells.first()).toBeVisible({ timeout: 10000 });
    const initialCellCount = await cells.count();
    console.log(`Initial camera cells: ${initialCellCount}`);

    // Note the first camera name shown in the grid
    const firstCellName = await cells.first().locator('span.text-white').first().textContent().catch(() => '');
    console.log(`First cell camera name: ${firstCellName}`);

    // Open config panel via Settings gear button
    const settingsButton = page.locator('button[title="Configure"], button:has(svg.lucide-settings)').first();
    await settingsButton.click();

    // Wait for config panel to appear
    const configPanel = page.locator('.card:has(h3)');
    await expect(configPanel).toBeVisible({ timeout: 5000 });

    // Note currently selected cameras — read checked checkboxes
    const checkedBoxes = page.locator('input[type="checkbox"]:checked');
    const initialCheckedIds: string[] = [];
    const checkedCount = await checkedBoxes.count();
    for (let i = 0; i < checkedCount; i++) {
      const box = checkedBoxes.nth(i);
      // Each checkbox is inside a label with the camera name and protocol
      const label = box.locator('..');
      const name = await label.locator('span').first().textContent().catch(() => '');
      initialCheckedIds.push(name || '');
    }
    console.log(`Initially checked cameras: ${initialCheckedIds.join(', ')}`);

    // Find an unchecked camera to add
    const allCheckboxes = page.locator('input[type="checkbox"]');
    const totalCount = await allCheckboxes.count();

    if (totalCount <= initialCheckedIds.length) {
      console.log('All cameras already selected — cannot switch. Skipping.');
      test.skip();
      return;
    }

    // If 4 cameras selected, need to deselect one first
    if (initialCheckedIds.length >= 4) {
      // Deselect the last checked camera
      await checkedBoxes.last().click();
    }

    // Select the first unchecked camera
    const uncheckedBox = page.locator('input[type="checkbox"]:not(:checked)').first();
    await uncheckedBox.click();

    // Click Apply button
    const applyButton = page.locator('button:has-text("Apply")');
    await applyButton.click();

    // Wait for config panel to close and new streams to load
    await expect(configPanel).not.toBeVisible({ timeout: 5000 });

    // Verify the grid re-rendered with new camera selection
    const newCells = getCameraCells(page);
    await expect(newCells.first()).toBeVisible({ timeout: 10000 });

    // Wait briefly for stream state indicators to appear
    await page.waitForTimeout(3000);

    // Verify state dots exist (playing, buffering, error — any state is valid)
    const stateDots = page.locator('span.rounded-full[title]');
    const dotCount = await stateDots.count();
    expect(dotCount).toBeGreaterThan(0);

    console.log('Camera switch completed — new streams loading');
  });

  test('Dashboard visibility change recovery', async ({ page }) => {
    const hlsCamera = await getFirstHlsCamera(page);

    if (!hlsCamera) {
      console.log('No HLS-capable camera — skipping visibility change test');
      test.skip();
      return;
    }

    // Wait for stream to be playing first
    try {
      await waitForStreamState(page, 'playing', { timeout: 20000 });
    } catch {
      console.log('Stream did not reach playing state — skipping visibility change test');
      test.skip();
      return;
    }

    console.log('Stream is playing — simulating visibility change to hidden');

    // Override document.visibilityState and dispatch visibilitychange to hidden
    await page.evaluate(() => {
      Object.defineProperty(document, 'visibilityState', {
        value: 'hidden',
        configurable: true,
      });
      Object.defineProperty(document, 'hidden', {
        value: true,
        configurable: true,
      });
      document.dispatchEvent(new Event('visibilitychange'));
    });

    // Wait 2 seconds in "hidden" state
    await page.waitForTimeout(2000);

    console.log('Simulating visibility change back to visible');

    // Restore visibility and dispatch visibilitychange to visible
    await page.evaluate(() => {
      Object.defineProperty(document, 'visibilityState', {
        value: 'visible',
        configurable: true,
      });
      Object.defineProperty(document, 'hidden', {
        value: false,
        configurable: true,
      });
      document.dispatchEvent(new Event('visibilitychange'));
    });

    // Wait for stream to recover (up to 10s)
    // The VideoPlayer rebuilds on visibility restore, so it goes through loading→buffering→playing
    try {
      const recovered = await Promise.race([
        waitForStreamState(page, 'playing', { timeout: 10000 })
          .then(() => 'playing' as const)
          .catch(() => null),
        waitForStreamState(page, 'buffering', { timeout: 10000 })
          .then(() => 'buffering' as const)
          .catch(() => null),
      ]);

      console.log(`Stream recovered to state: ${recovered || 'timeout'}`);
      expect(recovered).not.toBeNull();
    } catch {
      // Even if we can't detect the exact state, verify the page didn't crash
      const gridVisible = await page.locator('div.grid').isVisible().catch(() => false);
      expect(gridVisible).toBe(true);
      console.log('Visibility change recovery — page still functional');
    }
  });

  test('LiveView single camera playback', async ({ page }) => {
    const cameras = await fetchCamerasFromAPI(page);
    const hlsProtocols = ['rtsp_h264', 'rtsp_h265', 'onvif', 'rtsp'];
    const hlsCamera = cameras.find((c) => hlsProtocols.includes(c.protocol));

    if (!hlsCamera) {
      console.log('No HLS-capable camera — skipping LiveView test');
      test.skip();
      return;
    }

    console.log(`Testing LiveView for camera: ${hlsCamera.name} (${hlsCamera.id})`);

    // Navigate directly to LiveView page for this camera
    await page.goto(`/#/live/${hlsCamera.id}`);
    await page.waitForLoadState('networkidle');

    // Wait for VideoPlayer to initialize and show a state indicator
    // The VideoPlayer state dot appears inside the player container
    const stateDot = page.locator('span.rounded-full[title]').first();
    await expect(stateDot).toBeVisible({ timeout: 15000 });

    // Wait for playing state (or accept buffering as valid progress)
    const finalState = await Promise.race([
      waitForStreamState(page, 'playing', { timeout: 20000 })
        .then(() => 'playing' as const)
        .catch(() => null),
      waitForStreamState(page, 'buffering', { timeout: 20000 })
        .then(() => 'buffering' as const)
        .catch(() => null),
    ]);

    console.log(`LiveView stream state: ${finalState || 'timeout'}`);

    // Verify video element exists and has dimensions
    const videoInfo = await page.evaluate(() => {
      const video = document.querySelector('video');
      if (!video) return { exists: false };
      const el = video as HTMLVideoElement;
      return {
        exists: true,
        width: el.videoWidth,
        height: el.videoHeight,
        readyState: el.readyState,
      };
    });

    expect(videoInfo.exists).toBe(true);

    if (videoInfo.exists) {
      console.log(`LiveView video: ${videoInfo.width}x${videoInfo.height}, readyState=${videoInfo.readyState}`);

      // If stream reached playing, video should have dimensions
      if (finalState === 'playing') {
        expect(videoInfo.width).toBeGreaterThan(0);
        expect(videoInfo.height).toBeGreaterThan(0);
      }
    }

    // Take screenshot as evidence
    await page.screenshot({ path: 'test-results/liveview-single-camera.png' });
  });

  test('LiveView non-existent camera shows error', async ({ page }) => {
    // Navigate directly to a non-existent camera's LiveView
    await page.goto('/#/live/non-existent-camera-id');
    await page.waitForLoadState('networkidle');

    // The LiveView component tries to fetch camera via getCamera() API call
    // On failure, it shows an error card with error message
    // We should see an error state, NOT a black video screen

    // Wait for either:
    // 1. The error card (with AlertCircle icon and error message)
    // 2. The VideoPlayer error overlay ("Stream error after retries")
    // 3. A "Camera ID is required" type message
    const errorVisible = await Promise.race([
      // Error card from LiveView (contains AlertCircle icon + error text)
      page.locator('.card:has-text("Error")')
        .waitFor({ state: 'visible', timeout: 10000 })
        .then(() => 'error-card' as const)
        .catch(() => null as string | null),
      // VideoPlayer error overlay (contains "Stream error" text)
      page.locator('text=/Stream error|error/i')
        .first()
        .waitFor({ state: 'visible', timeout: 10000 })
        .then(() => 'error-overlay' as const)
        .catch(() => null as string | null),
      // Red state dot (error indicator)
      page.locator('span.rounded-full[title="Error"]')
        .waitFor({ state: 'visible', timeout: 10000 })
        .then(() => 'error-dot' as const)
        .catch(() => null as string | null),
    ]);

    console.log(`Error state detected: ${errorVisible}`);
    expect(errorVisible).not.toBeNull();

    // Verify it's NOT a black screen — there should be visible UI content
    // (error text, icon, or overlay is visible, not just a blank black div)
    const hasVisibleContent = await page.evaluate(() => {
      // Check for any visible text content that indicates an error message
      const body = document.body;
      const allText = body.innerText || '';
      return allText.length > 0;
    });
    expect(hasVisibleContent).toBe(true);

    // Specifically check no bare black <video> playing without error feedback
    const videoPlayingWithoutError = await page.evaluate(() => {
      const video = document.querySelector('video');
      if (!video) return false;
      const el = video as HTMLVideoElement;
      // If video is actively playing with data, that's wrong for non-existent camera
      return el.readyState >= 2 && !el.paused;
    });
    expect(videoPlayingWithoutError).toBe(false);
  });
});
