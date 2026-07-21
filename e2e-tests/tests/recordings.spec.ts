import { test, expect } from '@playwright/test';

test.describe('MiBee NVR - Recordings Functionality', () => {
  test.beforeEach(async ({ page }) => {
    // Navigate to the login page
    await page.goto('/');

    // Login with admin credentials
    await page.fill('input[type="text"], input[name="username"]', 'admin');
    await page.fill('input[type="password"], input[name="password"]', 'admin');
    await page.click('button[type="submit"]');

    // Wait for navigation to complete
    await page.waitForLoadState('networkidle');
  });

  test('should display recordings page', async ({ page }) => {
    // Navigate to recordings page
    await page.goto('/#/recordings');

    // Wait for recordings page to load
    await page.waitForSelector('h2', { timeout: 5000 });

    // Verify we're on the recordings page
    const header = await page.textContent('h2');
    expect(header).toContain('Recordings');
  });

  test('should load in timeline view by default', async ({ page }) => {
    // Navigate to recordings page — timeline is the default view
    await page.goto('/#/recordings');
    await page.waitForLoadState('networkidle');

    // The "Timeline" view tab should be the active (primary) button
    const timelineButton = page.locator('button').filter({ hasText: 'Timeline' }).first();
    await expect(timelineButton).toBeVisible({ timeout: 10000 });
    await expect(timelineButton).toHaveClass(/btn-primary/);

    // Calendar with Today button should also be visible
    const todayButton = page.locator('button').filter({ hasText: 'Today' }).first();
    await expect(todayButton).toBeVisible();
  });

  test('should display recordings in timeline mode', async ({ page }) => {
    // Navigate to recordings page
    await page.goto('/#/recordings');
    await page.waitForLoadState('networkidle');

    // Wait for the timeline to render (DayTimeline) or its empty state
    const timelineLoaded = await page
      .locator('button')
      .filter({ hasText: 'Timeline' })
      .first()
      .isVisible({ timeout: 10000 })
      .catch(() => false);
    expect(timelineLoaded).toBeTruthy();

    const rows = await page.locator('.group:has(.track-bg)').count();
    if (rows > 0) {
      console.log(`Found ${rows} timeline camera rows`);
    } else {
      // Empty state — check for "No recordings" message
      const noRecordings = await page.locator('text=No recordings on this day').count();
      expect(noRecordings).toBeGreaterThan(0);
    }
  });

  test('should switch to compact list view', async ({ page }) => {
    // Navigate to recordings page
    await page.goto('/#/recordings');
    await page.waitForLoadState('networkidle');

    // Click "List" button to switch to list view
    const listButton = page.locator('button').filter({ hasText: 'List' });
    await expect(listButton).toBeVisible({ timeout: 5000 });
    await listButton.click();

    // Wait for compact list to render
    await page.waitForSelector('.compact-list', { timeout: 5000 });

    // List header with column labels should be visible
    await expect(page.locator('.list-header')).toBeVisible();

    // Verify URL contains view=list
    expect(page.url()).toContain('view=list');
  });

  test('should navigate to recording detail page', async ({ page }) => {
    // Navigate to recordings page
    await page.goto('/#/recordings');
    await page.waitForLoadState('networkidle');

    // Switch to list view (gallery was removed) and wait for rows
    await page.locator('button').filter({ hasText: 'List' }).click();
    await page.waitForSelector('.compact-list', { timeout: 10000 });

    const listRows = page.locator('.list-row');
    const rowCount = await listRows.count();

    if (rowCount > 0) {
      // Click the view action on the first row to open its detail page
      const viewButton = listRows.first().locator('button[title="View"], a[title="View"]');
      if (await viewButton.count() > 0) {
        await viewButton.first().click();
      } else {
        // Fallback: click the row's camera name link / title area
        await listRows.first().locator('a[href*="#/recordings/"]').first().click();
      }

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

  test('should test format filter pills', async ({ page }) => {
    // Navigate to recordings page
    await page.goto('/#/recordings');
    await page.waitForLoadState('networkidle');

    // All four format filter pills should be visible
    const allPill = page.locator('button').filter({ hasText: 'All' }).first();
    const videoPill = page.locator('button').filter({ hasText: 'Video' }).first();
    const timelapsePill = page.locator('button').filter({ hasText: 'Timelapse' }).first();
    const mjpegPill = page.locator('button').filter({ hasText: 'MJPEG' }).first();

    await expect(allPill).toBeVisible();
    await expect(videoPill).toBeVisible();
    await expect(timelapsePill).toBeVisible();
    await expect(mjpegPill).toBeVisible();

    // Click Video pill → verify URL updates
    await videoPill.click();
    await page.waitForTimeout(500);
    expect(page.url()).toContain('format=Video');
    console.log('✓ Video format filter applied');

    // Click Timelapse pill → verify URL updates
    await timelapsePill.click();
    await page.waitForTimeout(500);
    expect(page.url()).toContain('format=Timelapse');
    console.log('✓ Timelapse format filter applied');

    // Click MJPEG pill → verify URL updates
    await mjpegPill.click();
    await page.waitForTimeout(500);
    expect(page.url()).toContain('format=MJPEG');
    console.log('✓ MJPEG format filter applied');

    // Click All to reset
    await allPill.click();
    await page.waitForTimeout(500);
    console.log('✓ Filter reset to All');
  });

  test('should test video playback for H264 recordings', async ({ page }) => {
    // Navigate to recordings page
    await page.goto('/#/recordings');
    await page.waitForLoadState('networkidle');

    // Click Video format pill to filter H264/H265 recordings
    const videoPill = page.locator('button').filter({ hasText: 'Video' }).first();
    await videoPill.click();
    await page.waitForTimeout(2000);

    // Switch to list view (gallery was removed) and look for an MP4 row
    await page.locator('button').filter({ hasText: 'List' }).click();
    await page.waitForSelector('.compact-list', { timeout: 5000 });
    let mp4Card = page.locator('.list-row').filter({ hasText: 'MP4' }).first();
    let mp4Count = await mp4Card.count();

    if (mp4Count > 0) {
      // Click the row to view detail
      await mp4Card.click();

      // Wait for navigation to detail page
      await page.waitForURL(/.*\/recordings\/.*/);

      // Check for video element
      const video = page.locator('video');
      await expect(video).toBeVisible({ timeout: 10000 });

      // Video should start paused
      const isPaused = await page.evaluate(() => {
        const v = document.querySelector('video');
        return v ? v.paused : true;
      });
      expect(isPaused).toBe(true);
    } else {
      test.skip('No H264/MP4 recordings available to test video playback');
    }
  });

  test('should test frame playback for MJPEG recordings', async ({ page }) => {
    // Navigate to recordings page
    await page.goto('/#/recordings');
    await page.waitForLoadState('networkidle');

    // Click MJPEG format pill
    const mjpegPill = page.locator('button').filter({ hasText: 'MJPEG' }).first();
    await mjpegPill.click();
    await page.waitForTimeout(2000);

    // Switch to list view (gallery was removed) and look for recording rows
    await page.locator('button').filter({ hasText: 'List' }).click();
    await page.waitForSelector('.compact-list', { timeout: 5000 });
    const listRows = page.locator('.list-row');
    const rowCount = await listRows.count();

    if (rowCount > 0) {
      // Click first row to view detail
      await listRows.first().click();
      await page.waitForURL(/.*\/recordings\/.*/);

      // Check for frame player controls
      const playButton = page.locator('button').filter({ hasText: 'Play' });
      const prevButton = page.locator('button').filter({ hasText: 'Prev' });
      const nextButton = page.locator('button').filter({ hasText: 'Next' });

      await expect(playButton).toBeVisible({ timeout: 10000 });

      // Test frame navigation
      if (await nextButton.count() > 0) {
        await nextButton.click();
        await page.waitForSelector('img[src*="frame"], img[alt]', { timeout: 3000 });
      }
    } else {
      test.skip('No MJPEG recordings available to test frame playback');
    }
  });

  test('should test recording download functionality', async ({ page }) => {
    // Navigate to recordings page
    await page.goto('/#/recordings');
    await page.waitForLoadState('networkidle');

    // Switch to list view for easier navigation
    await page.locator('button').filter({ hasText: 'List' }).click();
    await page.waitForSelector('.compact-list', { timeout: 5000 });

    // Find a recording row
    const rows = page.locator('.list-row');
    const rowCount = await rows.count();

    if (rowCount > 0) {
      // Click first row to navigate to detail
      await rows.first().click();
      await page.waitForURL(/.*\/recordings\/.*/);

      // Set up download handler
      const downloadPromise = page.waitForEvent('download');

      // Click download button
      const downloadButton = page.locator('button').filter({ hasText: 'Download' });
      if (await downloadButton.count() > 0) {
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
        test.skip('No download button available for this recording');
      }
    } else {
      test.skip('No recordings available to test download functionality');
    }
  });

  test('should test calendar Today button', async ({ page }) => {
    // Navigate to recordings page
    await page.goto('/#/recordings');
    await page.waitForLoadState('networkidle');

    // Find the calendar card with a Today button
    const calendarCard = page.locator('.card').filter({ has: page.locator('button').filter({ hasText: 'Today' }) }).first();
    await expect(calendarCard).toBeVisible({ timeout: 5000 });

    // Click the Today button
    const todayButton = page.locator('button').filter({ hasText: 'Today' }).first();
    await todayButton.click();
    await page.waitForTimeout(500);

    // Timeline tab should still be the active view after clicking Today
    const timelineButton = page.locator('button').filter({ hasText: 'Timeline' }).first();
    await expect(timelineButton).toHaveClass(/btn-primary/);
    console.log('✓ Calendar Today button works');
  });

  test('should test batch selection in list mode', async ({ page }) => {
    // Navigate to recordings page
    await page.goto('/#/recordings');
    await page.waitForLoadState('networkidle');

    // Switch to list view where batch selection lives (gallery was removed)
    const listButton = page.locator('button').filter({ hasText: 'List' });
    await listButton.click();
    await page.waitForSelector('.compact-list', { timeout: 10000 });

    const listRows = page.locator('.list-row');
    const rowCount = await listRows.count();

    if (rowCount >= 2) {
      // Check checkboxes on the first two rows
      const firstCheckbox = listRows.nth(0).locator('input[type="checkbox"]').first();
      const secondCheckbox = listRows.nth(1).locator('input[type="checkbox"]').first();

      await firstCheckbox.click({ force: true });
      await secondCheckbox.click({ force: true });

      // Batch action bar should appear with selected count
      const batchBar = page.locator('text=2 selected');
      await expect(batchBar).toBeVisible({ timeout: 3000 });

      // Delete Selected button should be visible
      const deleteSelected = page.locator('button').filter({ hasText: 'Delete Selected' });
      await expect(deleteSelected).toBeVisible();

      // Cancel selection
      const cancelButton = page.locator('button').filter({ hasText: 'Cancel' });
      if (await cancelButton.count() > 0) {
        await cancelButton.click();
        await page.waitForTimeout(500);
      }

      console.log('✓ Batch selection in list mode works');
    } else {
      test.skip('Need at least 2 recordings for batch selection test');
    }
  });

  test('should test recording deletion', async ({ page }) => {
    // Navigate to recordings page
    await page.goto('/#/recordings');
    await page.waitForLoadState('networkidle');

    // Switch to list view (easier to isolate delete action)
    await page.locator('button').filter({ hasText: 'List' }).click();
    await page.waitForSelector('.compact-list', { timeout: 5000 });

    // Check if there are recordings
    const listRows = page.locator('.list-row');
    const initialCount = await listRows.count();

    if (initialCount > 0) {
      // Find the first delete button (Trash2 icon with title "Delete")
      const firstRow = listRows.first();
      const deleteButton = firstRow.locator('button[title="Delete"]');

      if (await deleteButton.count() > 0) {
        await deleteButton.click();

        // Wait for confirmation modal
        await expect(page.locator('text=Delete Recording')).toBeVisible({ timeout: 5000 });

        // Confirm deletion
        const confirmButton = page.locator('button').filter({ hasText: 'Delete' }).last();
        if (await confirmButton.count() > 0) {
          await confirmButton.click();

          // Wait for deletion to complete
          await page.waitForTimeout(2000);
          console.log('✓ Recording deleted successfully');
        }
      } else {
        test.skip('No delete button available');
      }
    } else {
      test.skip('No recordings available to test deletion');
    }
  });

  test('should test camera filter', async ({ page }) => {
    // Navigate to recordings page
    await page.goto('/#/recordings');
    await page.waitForLoadState('networkidle');

    // Camera select should exist
    const cameraSelect = page.locator('select#camera');
    await expect(cameraSelect).toBeVisible({ timeout: 5000 });

    // Check if there are camera options to filter by
    const options = await cameraSelect.locator('option').all();
    if (options.length > 1) {
      // Select a camera option
      await cameraSelect.selectOption({ index: 1 });
      await page.waitForLoadState('networkidle');
      expect(page.url()).toContain('camera=');
      console.log('✓ Camera filter applied');
    }
  });
});
