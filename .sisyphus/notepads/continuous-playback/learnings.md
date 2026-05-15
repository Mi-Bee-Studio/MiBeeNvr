# Continuous Playback Feature - Learnings

## API Sort/Order Params
- Backend `listRecordings` supports `sort_by` and `order` query params (whitelisted: started_at, duration, file_size, camera_id)
- Frontend `listRecordings()` in api.ts did NOT expose these params — had to add them (2-line change)
- Default sort is `started_at DESC` — to find "next recording after current", need `sort_by=started_at&order=asc&start=<ended_at>`

## Svelte 5 Event Handling
- H.264 `<video>` events: use `onended={handler}` and `ontimeupdate={handler}` (Svelte 5 syntax, not `on:ended`)
- The `on:` directive syntax is deprecated in Svelte 5 — use `on*` attribute syntax

## MJPEG Lazy Loading Design
- Batch size of 50 frames, window of ±20 frames around current position
- `loadFrameBatch()` uses sub-batches of 5 concurrent requests
- `ensureFramesLoaded()` is fire-and-forget for loading more frames (no await needed)
- Memory optimization: null out frames outside the ±20 window
- Progress bar still works during lazy loading (preloadProgress tracks current batch progress)

## Prefetch Strategy
- At 80% video playback, trigger `prefetchNextRecording()` — only prefetches 1 recording ahead
- Blob URL is stored in `nextBlobUrl` state and revoked in `onDestroy` and when switching recordings
- `nextRecordingId` prevents duplicate prefetch calls
