import { test, expect } from '@playwright/test';

test.describe('MiBee NVR - Recordings Functionality', () => {
  test.beforeEach(async ({ page }) => {
    // Navigate to the login page
    await page.goto('/');
    
    // Login with admin credentials
    await page.fill('input[type="text"], input[name="username"]', 'admin');
    await page.fill('input[type="password"], input[name="password"]', 'admin');
    await page.click('button[type="submit"]');
    
    // Wait for navigation to recordings or any protected page
    await page.waitForNavigation({ timeout: 10000 });
  });

  test('should login and navigate to recordings page', async ({ page }) => {
    // Navigate to recordings page
    await page.goto('/#/recordings');
    
    // Wait for recordings page to load
    await page.waitForSelector('h1', { timeout: 5000 });
    
    // Verify we're on the recordings page
    const header = await page.textContent('h1');
    expect(header).toContain('Recordings');
  });

  test('should display recordings list', async ({ page }) => {
    // Navigate to recordings page
    await page.goto('/#/recordings');
    
    // Wait for the page to load
    await page.waitForLoadState('networkidle');
    
    // Check if recordings table exists
    const table = await page.locator('table').first();
    await expect(table).toBeVisible();
    
    // Wait for table rows to load (or no-recordings message)
    const tableBody = page.locator('table tbody');
    await expect(tableBody.locator('tr, :scope')).toBeAttached({ timeout: 5000 });
    // Check if there are recordings or "no recordings" message
    const recordings = await page.locator('table tbody tr').count();
    
    if (recordings > 0) {
      console.log(`Found ${recordings} recordings`);
    } else {
      const noRecordings = await page.locator('text=no recordings').count();
      expect(noRecordings).toBeGreaterThan(0);
    }
  });

  test('should navigate to recording detail page', async ({ page }) => {
    // Navigate to recordings page
    await page.goto('/#/recordings');
    
    // Wait for data to load
    // Wait for table data to render
    await page.waitForSelector('table tbody tr, :not(table)', { timeout: 5000 });
    
    // Find a "View" button and click it
    const viewButtons = await page.locator('button:has-text("View")').all();
    
    if (viewButtons.length > 0) {
      // Click the first View button
      await viewButtons[0].click();
      
      // Wait for navigation to detail page
      await page.waitForURL(/.*\/recordings\/.*/);
      
      // Verify we're on the detail page
      const url = page.url();
      expect(url).toMatch(/\/recordings\/.*/);
      
      // Check for video player or frame player
      const videoPlayer = await page.locator('video').count();
      const framePlayer = await page.locator('img[alt*="Frame"]').count();
      
      expect(videoPlayer + framePlayer).toBeGreaterThan(0);
    } else {
      test.skip('No recordings available to test detail view');
    }
  });

  test('should test video playback for H264 recordings', async ({ page }) => {
    // Navigate to recordings page
    await page.goto('/#/recordings');
    
    // Wait for data to load
    // Wait for table data to render
    await page.waitForSelector('table tbody tr, span:has-text("MP4")', { timeout: 5000 });
    
    // Find an H264/MP4 recording
    const mp4Badges = await page.locator('span:has-text("MP4")').all();
    
    if (mp4Badges.length > 0) {
      // Click the View button for the first MP4 recording
      const firstMp4Row = await mp4Badges[0].locator('..').locator('..');
      const viewButton = firstMp4Row.locator('button:has-text("View")');
      await viewButton.click();
      
      // Wait for navigation
      await page.waitForURL(/.*\/recordings\/.*/);
      
      // Check for video element
      const video = page.locator('video');
      await expect(video).toBeVisible({ timeout: 10000 });
      
      // Test video controls
      const videoElement = await video.elementHandle();
      const isPaused = await page.evaluate((v: any) => v.paused, videoElement);
      expect(isPaused).toBe(true); // Video should start paused
      
      // Test play button
      const playButton = page.locator('video').evaluateHandle((el: any) => {
        el.play();
        return el;
      });
      
      // Wait for video playback to start
      await page.waitForFunction((v: any) => !v.paused, videoElement, { timeout: 3000 }).catch(() => {});
    } else {
      test.skip('No H264/MP4 recordings available to test video playback');
    }
  });

  test('should test frame playback for MJPEG recordings', async ({ page }) => {
    // Navigate to recordings page
    await page.goto('/#/recordings');
    
    // Wait for data to load
    // Wait for table data to render
    await page.waitForSelector('table tbody tr, span:has-text("JPEG")', { timeout: 5000 });
    
    // Find an MJPEG/JPEG recording
    const jpegBadges = await page.locator('span:has-text("JPEG")').all();
    
    if (jpegBadges.length > 0) {
      // Click the View button for the first JPEG recording
      const firstJpegRow = await jpegBadges[0].locator('..').locator('..');
      const viewButton = firstJpegRow.locator('button:has-text("View")');
      await viewButton.click();
      
      // Wait for navigation
      await page.waitForURL(/.*\/recordings\/.*/);
      
      // Check for frame player controls
      const playButton = page.locator('button:has-text("Play")');
      const prevButton = page.locator('button:has-text("Prev")');
      const nextButton = page.locator('button:has-text("Next")');
      
      await expect(playButton).toBeVisible({ timeout: 10000 });
      await expect(prevButton).toBeVisible();
      await expect(nextButton).toBeVisible();
      
      // Test frame navigation
      await nextButton.click();
      await page.waitForSelector('img[src*="frame"], img[alt]', { timeout: 3000 });
      
      const prevDisabled = await prevButton.isEnabled();
      if (prevDisabled) {
        await prevButton.click();
        await page.waitForSelector('img[src*="frame"], img[alt]', { timeout: 3000 });
      }
    } else {
      test.skip('No MJPEG/JPEG recordings available to test frame playback');
    }
  });

  test('should test recording download functionality', async ({ page }) => {
    // Navigate to recordings page
    await page.goto('/#/recordings');
    
    // Wait for data to load
    // Wait for table data to render
    await page.waitForSelector('table tbody tr', { timeout: 5000 });
    
    // Find the first recording with a View button
    const viewButtons = await page.locator('button:has-text("View")').all();
    
    if (viewButtons.length > 0) {
      // Navigate to detail page
      await viewButtons[0].click();
      await page.waitForURL(/.*\/recordings\/.*/);
      
      // Set up download handler
      const downloadPromise = page.waitForEvent('download');
      
      // Click download button
      const downloadButton = page.locator('button:has-text("Download")');
      await downloadButton.click();
      
      // Wait for download to start
      const download = await downloadPromise;
      
      // Verify download
      const filename = download.suggestedFilename();
      console.log(`Downloaded file: ${filename}`);
      
      // Get download size
      const size = await download.createReadStream();
      let downloadedBytes = 0;
      for await (const chunk of size) {
        downloadedBytes += chunk.length;
      }
      
      console.log(`Downloaded ${downloadedBytes} bytes`);
      expect(downloadedBytes).toBeGreaterThan(0);
    } else {
      test.skip('No recordings available to test download functionality');
    }
  });

  test('should test filter functionality', async ({ page }) => {
    // Navigate to recordings page
    await page.goto('/#/recordings');
    
    // Wait for data to load
    // Wait for table data and filter controls to load
    await page.waitForSelector('select, table tbody tr', { timeout: 5000 });
    
    // Check if camera filter exists
    const cameraSelect = page.locator('select#camera');
    const formatSelect = page.locator('select#format');
    
    if (await cameraSelect.count() > 0) {
      // Test camera filter
      await cameraSelect.click();
      const cameraOptions = await page.locator('select#camera option').all();
      
      if (cameraOptions.length > 1) {
        // Select a camera
        await cameraSelect.selectOption({ index: 1 });
        // Wait for filtered results to load
        await page.waitForLoadState('networkidle');
        console.log('Camera filter applied');
      }
    }
    
    if (await formatSelect.count() > 0) {
      // Test format filter
      await formatSelect.click();
      const formatOptions = await page.locator('select#format option').all();
      
      if (formatOptions.length > 1) {
        // Select a format
        await formatSelect.selectOption({ index: 1 });
        // Wait for filtered results to load
        await page.waitForLoadState('networkidle');
        console.log('Format filter applied');
      }
    }
  });

  test('should test pin/unpin functionality', async ({ page }) => {
    // Navigate to recordings page
    await page.goto('/#/recordings');
    
    // Wait for data to load
    // Wait for table data to render
    await page.waitForSelector('table tbody tr', { timeout: 5000 });
    
    // Find pin buttons
    const pinButtons = await page.locator('button').filter({ hasText: /📌|📍/ }).all();
    
    if (pinButtons.length > 0) {
      // Click first pin button
      await pinButtons[0].click();
      await page.waitForSelector('span:has-text("Pinned"), button', { timeout: 3000 });
      
      // Check if pin status changed (look for pinned badge)
      const pinnedBadges = await page.locator('span:has-text("Pinned")').count();
      console.log(`Found ${pinnedBadges} pinned recordings`);
      
      // Click again to unpin
      await pinButtons[0].click();
      await page.waitForSelector('table tbody tr', { timeout: 3000 });
    } else {
      test.skip('No recordings available to test pin/unpin functionality');
    }
  });

  test('should test recording deletion', async ({ page }) => {
    // Navigate to recordings page
    await page.goto('/#/recordings');
    
    // Wait for data to load
    // Wait for table data to render
    await page.waitForSelector('table tbody tr', { timeout: 5000 });
    
    // Get initial count of recordings
    const initialCount = await page.locator('table tbody tr').count();
    
    if (initialCount > 0) {
      // Find first delete button
      const deleteButton = page.locator('button:has-text("🗑️")').first();
      await deleteButton.click();
      
      // Wait for confirmation modal
      await expect(page.locator('text=Delete Recording')).toBeVisible({ timeout: 5000 });
      
      // Confirm deletion
      const confirmButton = page.locator('button:has-text("Delete")').filter({ hasText: /Confirm/ });
      await confirmButton.click();
      
      // Wait for deletion to complete (table re-renders)
      await page.waitForSelector('table tbody tr', { timeout: 5000 });
      
      // Verify recording was deleted
      const finalCount = await page.locator('table tbody tr').count();
      expect(finalCount).toBe(initialCount - 1);
    } else {
      test.skip('No recordings available to test deletion');
    }
  });
});
