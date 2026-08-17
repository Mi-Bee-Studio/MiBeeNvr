/**
 * Recording API — list, download, frames, stats, archives
 */
import { apiRequest, apiRequestBlob, apiHeadHeader, getAuthHeader, API_BASE, ApiRequestError } from './client';

// --- Types ---

export interface Recording {
  id: string;
  camera_id: string;
  file_path: string;
  format: 'h264' | 'mjpeg' | 'h265' | 'timelapse' | 'avi';
  started_at: string;
  ended_at: string;
  duration: number;
  file_size: number;
  frame_count: number;
  merge_status: 'pending' | 'merged' | 'failed' | 'incompatible' | 'dark' | 'daily_merged' | 'merging';
  merge_progress?: number; // 0-100, persisted to DB
  merge_path?: string;
  archived?: boolean;
  merge_tier?: string;
  merge_error?: string;
}

export interface FrameInfo {
  filename: string;
  index: number;
}

export interface TimelapseFrame {
  filename: string;
  url: string;
  size: number;
  timestamp: string;
}

export interface FramesResponse {
  frames: FrameInfo[];
}

export interface RecordingListResponse {
  recordings: Recording[];
  total?: number;
  next_cursor?: string; // RFC3339 started_at of last row; pass back as ?cursor= for O(1) deep paging
}

export interface RecordingDaySummary {
  date: string; // "YYYY-MM-DD" in client local timezone
  count: number;
  formats: string[]; // "video" | "timelapse" | "mjpeg"
}

export interface RecordingDaySummaryResponse {
  days: RecordingDaySummary[];
}

export interface StorageStats {
  total_bytes: number;
  used_bytes: number;
  recording_count: number;
  camera_count: number;
}

export interface DailyStats {
  date: string;
  recordings: number;
  total_size: number;
  cameras?: Record<string, number>;
  camera_sizes?: Record<string, number>;
}

export interface ArchiveGroup {
  id: string;
  name: string;
  recording_count: number;
  total_size: number;
  archived_at: string;
  archive_retention_days: number;
}

export interface ArchiveListResponse {
  archives: ArchiveGroup[];
}

// --- Recordings ---

export async function listRecordings(
  params: {
    camera_id?: string;
    format?: string;
    merged?: boolean;
    offset?: number;
    limit?: number;
    start?: string;
    end?: string;
    sort_by?: string;
    order?: string;
    search?: string;
    archived?: boolean;
    cursor?: string; // keyset cursor (started_at of last row on prev page) for O(1) deep paging
    ai_class?: string; // "person" / "car" / ... — filter to recordings with an AI event of this class
    signal?: AbortSignal;
  } = {},
): Promise<RecordingListResponse> {
  const queryParams = new URLSearchParams();

  if (params.camera_id) queryParams.set('camera_id', params.camera_id);
  if (params.format) queryParams.set('format', params.format);
  if (params.merged !== undefined) queryParams.set('merged', String(params.merged));
  if (params.offset !== undefined) queryParams.set('offset', String(params.offset));
  if (params.limit !== undefined) queryParams.set('limit', String(params.limit));
  if (params.start) queryParams.set('start', params.start);
  if (params.end) queryParams.set('end', params.end);
  if (params.sort_by) queryParams.set('sort_by', params.sort_by);
  if (params.order) queryParams.set('order', params.order);
  if (params.search) queryParams.set('search', params.search);
  if (params.archived !== undefined) queryParams.set('archived', String(params.archived));
  if (params.cursor) queryParams.set('cursor', params.cursor);
  if (params.ai_class) queryParams.set('ai_class', params.ai_class);

  const query = queryParams.toString();
  const endpoint = query ? `/recordings?${query}` : '/recordings';

  const { signal } = params;
  return apiRequest<RecordingListResponse>(endpoint, { signal });
}

export async function getRecordingDailySummary(
  params: {
    camera_id?: string;
    format?: string;
    formats?: string;
    merged?: boolean;
    start?: string;
    end?: string;
    search?: string;
    archived?: boolean;
    ai_class?: string;
    tz_offset?: number;
    signal?: AbortSignal;
  } = {},
): Promise<RecordingDaySummaryResponse> {
  const queryParams = new URLSearchParams();
  if (params.camera_id) queryParams.set('camera_id', params.camera_id);
  if (params.format) queryParams.set('format', params.format);
  if (params.formats) queryParams.set('formats', params.formats);
  if (params.merged !== undefined) queryParams.set('merged', String(params.merged));
  if (params.start) queryParams.set('start', params.start);
  if (params.end) queryParams.set('end', params.end);
  if (params.search) queryParams.set('search', params.search);
  if (params.archived !== undefined) queryParams.set('archived', String(params.archived));
  if (params.ai_class) queryParams.set('ai_class', params.ai_class);
  if (params.tz_offset !== undefined) queryParams.set('tz_offset', String(params.tz_offset));

  const query = queryParams.toString();
  const endpoint = query ? `/recordings/daily-summary?${query}` : '/recordings/daily-summary';
  const { signal } = params;
  return apiRequest<RecordingDaySummaryResponse>(endpoint, { signal });
}

export async function getRecording(id: string, signal?: AbortSignal): Promise<Recording> {
  return apiRequest<Recording>(`/recordings/${id}`, { signal });
}

// RecordingTimelineSegment is the lightweight projection of a Recording used
// for timeline rendering only. It carries the 7 fields the timeline components
// (DayTimeline / TimelineBar) actually read — omitting file_path/merge_*/
// file_size/frame_count etc. — so a full fragmented day (~5000 segments for a
// Xiaomi reconnect storm) ships in ~10x less bandwidth than a full Recording
// list and never hits the 500-row cap that truncated the afternoon (issue #115).
//
// Named "Recording..." (not "TimelineSegment") to avoid colliding with the
// local TimelineSegment type inside TimelineBar.svelte.
export interface RecordingTimelineSegment {
  id: string;
  camera_id: string;
  started_at: string;
  ended_at: string;
  duration: number;
  format: Recording['format'];
  merge_status: Recording['merge_status'];
}

export interface RecordingTimelineResponse {
  segments: RecordingTimelineSegment[];
  total: number;
  truncated: boolean; // true when the day exceeded the backend cap (maxTimelineSegments)
}

// getRecordingsTimeline fetches the lightweight day-window timeline for the
// recordings-page day strip and the player DVR bar. Sorting is fixed to
// started_at ASC server-side; the cap (10k) is also server-side, so this client
// takes no limit/sort_by/order params. Issue #115.
export async function getRecordingsTimeline(
  params: {
    camera_id?: string;
    format?: string;
    merged?: boolean;
    ai_class?: string;
    start?: string; // RFC3339 day-start
    end?: string; // RFC3339 day-end
    signal?: AbortSignal;
  } = {},
): Promise<RecordingTimelineResponse> {
  const queryParams = new URLSearchParams();
  if (params.camera_id) queryParams.set('camera_id', params.camera_id);
  if (params.format) queryParams.set('format', params.format);
  if (params.merged !== undefined) queryParams.set('merged', String(params.merged));
  if (params.ai_class) queryParams.set('ai_class', params.ai_class);
  if (params.start) queryParams.set('start', params.start);
  if (params.end) queryParams.set('end', params.end);

  const query = queryParams.toString();
  const endpoint = query ? `/recordings/timeline?${query}` : '/recordings/timeline';
  const { signal } = params;
  return apiRequest<RecordingTimelineResponse>(endpoint, { signal });
}

export async function deleteRecording(id: string, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/recordings/${id}`, {
    method: 'DELETE',
    signal,
  });
}

export async function batchDeleteRecordings(ids: string[], signal?: AbortSignal): Promise<void> {
  await apiRequest<void>('/recordings/batch-delete', {
    method: 'POST',
    body: JSON.stringify({ ids }),
    signal,
  });
}

export function getRecordingDownloadUrl(id: string): string {
  return `${API_BASE}/recordings/${id}/download`;
}

export function getRecordingVideoUrl(id: string): string {
  return `${API_BASE}/recordings/${id}/download`;
}

export function getMergedRecordingUrl(id: string): string {
  return `${API_BASE}/recordings/${id}/merged`;
}

// getCameraPlaybackPlaylistURL builds the day-range VOD HLS playlist URL
// (#321 Phase 2). The playlist stitches every H.264/H.265 recording of the
// camera within [start, end] (RFC3339) into one seekable timeline.
export function getCameraPlaybackPlaylistURL(cameraId: string, startISO: string, endISO: string): string {
  return `${API_BASE}/cameras/${cameraId}/playback/playlist.m3u8?start=${encodeURIComponent(startISO)}&end=${encodeURIComponent(endISO)}`;
}

// probeMergedRecordingCodec issues a HEAD request to the /merged endpoint and
// returns the X-Timelapse-Codec header value ('h264' / 'h265' / 'mjpeg') so the
// frontend can pick the right playback path: <video> for H.264/H.265 (browser-
// playable), JPEG frame cycler for mjpeg/mjpa (browsers can't decode in <video>).
//
// Returns null when the merged file is absent (404) or the codec header is not
// set. Cached per recordingId for the page lifetime to avoid repeated HEADs.
const mergedCodecCache = new Map<string, string | null>();

export async function probeMergedRecordingCodec(recordingId: string): Promise<string | null> {
  const cached = mergedCodecCache.get(recordingId);
  if (cached !== undefined) return cached;
  const codec = await apiHeadHeader(`/recordings/${recordingId}/merged`, 'X-Timelapse-Codec');
  mergedCodecCache.set(recordingId, codec);
  return codec;
}

// clearMergedCodecCache invalidates the cached codec for a recording — call
// after a merge completes or when the recording is re-loaded.
export function clearMergedCodecCache(recordingId?: string): void {
  if (recordingId) {
    mergedCodecCache.delete(recordingId);
  } else {
    mergedCodecCache.clear();
  }
}

export async function downloadRecording(
  id: string,
  onProgress?: (loaded: number, total: number) => void,
): Promise<void> {
  const url = `${API_BASE}/recordings/${id}/download`;

  const blob = await new Promise<Blob>((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('GET', url);

    const authHeader = getAuthHeader();
    if (authHeader) {
      xhr.setRequestHeader('Authorization', authHeader);
    }

    xhr.responseType = 'blob';

    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(xhr.response);
      } else {
        reject(new Error(`HTTP ${xhr.status}`));
      }
    };

    xhr.onerror = () => reject(new Error('Network error'));

    if (onProgress) {
      xhr.onprogress = (e) => {
        if (e.lengthComputable) {
          onProgress(e.loaded, e.total);
        }
      };
    }

    xhr.send();
  });

  const objectUrl = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = objectUrl;
  a.download = `recording_${id}.mp4`;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(objectUrl);
}

// --- Frames (MJPEG recordings) ---

export async function listFrames(recordingId: string, signal?: AbortSignal): Promise<FramesResponse> {
  return apiRequest<FramesResponse>(`/recordings/${recordingId}/frames`, { signal });
}

// --- Timelapse frames ---

export async function getTimelapseFrames(recordingId: string, signal?: AbortSignal): Promise<TimelapseFrame[]> {
  return apiRequest<TimelapseFrame[]>(`/recordings/${recordingId}/timelapse-frames`, { signal });
}

export async function loadTimelapseFrameBlob(
  recordingId: string,
  filename: string,
  signal?: AbortSignal,
): Promise<string> {
  const blob = await apiRequestBlob(`/recordings/${recordingId}/timelapse-frames/${filename}`, { signal });
  return URL.createObjectURL(blob);
}
export async function loadFrameBlob(recordingId: string, frameIndex: number, signal?: AbortSignal): Promise<string> {
  const blob = await apiRequestBlob(`/recordings/${recordingId}/download?frame=${frameIndex}`, { signal });
  return URL.createObjectURL(blob);
}

/**
 * @deprecated Use direct URL playback via getRecordingVideoUrl() or <video src={getRecordingVideoUrl(id)}> instead.
 * Loading entire recordings as blobs causes memory crashes with large files.
 */
export async function loadRecordingVideoBlob(recordingId: string, signal?: AbortSignal): Promise<string> {
  const blob = await apiRequestBlob(`/recordings/${recordingId}/download`, { signal });
  return URL.createObjectURL(blob);
}

// --- Timelapse Merge ---

export async function triggerTimelapseMerge(cameraId: string, date?: string, duration?: string): Promise<void> {
  const params = new URLSearchParams();
  if (date) params.set('date', date);
  if (duration) params.set('duration', duration);
  const query = params.toString() ? `?${params.toString()}` : '';
  const url = `${API_BASE}/timelapse/${cameraId}/merge${query}`;
  const headers: Record<string, string> = {};
  const authHeader = getAuthHeader();
  if (authHeader) headers['Authorization'] = authHeader;
  const res = await fetch(url, { method: 'POST', headers });
  if (!res.ok) {
    const errorData = await res.json().catch(() => ({ error: 'Failed to start merge' }));
    throw new ApiRequestError(errorData.error || `HTTP ${res.status}`, errorData.code);
  }
}

export async function batchMergeTimelapse(params: {
  camera_ids: string[];
  duration?: string;
  date?: string;
}): Promise<{ results: Array<{ camera_id: string; status: string; error?: string }>; triggered: number }> {
  const url = `${API_BASE}/timelapse/batch-merge`;
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  const authHeader = getAuthHeader();
  if (authHeader) headers['Authorization'] = authHeader;

  const body = JSON.stringify({
    camera_ids: params.camera_ids,
    duration: params.duration || '1h',
    date: params.date || '',
  });

  const res = await fetch(url, { method: 'POST', headers, body });
  if (!res.ok) {
    const errorData = await res.json().catch(() => ({ error: 'Failed to batch merge' }));
    throw new ApiRequestError(errorData.error || `HTTP ${res.status}`, errorData.code);
  }
  return res.json();
}
export function subscribeTimelapseMergeProgress(
  cameraId: string,
  onProgress: (data: any) => void,
  onError?: (e: Event) => void,
): AbortController {
  const abortController = new AbortController();

  (async () => {
    try {
      const authHeader = getAuthHeader();
      const headers: Record<string, string> = {};
      if (authHeader) headers['Authorization'] = authHeader;

      const response = await fetch(`${API_BASE}/timelapse/merge/progress/${cameraId}`, {
        headers,
        signal: abortController.signal,
      });

      if (!response.ok) {
        console.warn('SSE connection failed:', response.status);
        return;
      }

      const reader = response.body?.getReader();
      if (!reader) {
        console.warn('SSE response body not readable');
        return;
      }

      const decoder = new TextDecoder();
      let buffer = '';

      while (true) {
        const { done, value } = await reader.read();
        if (done) break;

        buffer += decoder.decode(value, { stream: true });
        const lines = buffer.split('\n');
        buffer = lines.pop() || '';

        for (const line of lines) {
          if (line.startsWith('data: ')) {
            const jsonStr = line.slice(6);
            try {
              const data = JSON.parse(jsonStr);
              onProgress(data);
            } catch (e) {
              console.warn('Failed to parse merge progress event:', e);
            }
          }
        }
      }
    } catch (err: any) {
      if (err?.name === 'AbortError') return;
      console.warn('SSE stream error:', err);
      if (onError) onError(new Event('error'));
    }
  })();

  return abortController;
}
// --- Stats ---

export async function getStats(signal?: AbortSignal): Promise<StorageStats> {
  return apiRequest<StorageStats>('/stats', { signal });
}

export async function getStatsTrends(days: number = 7, signal?: AbortSignal): Promise<DailyStats[]> {
  return apiRequest<DailyStats[]>(`/stats/trends?days=${days}`, { signal });
}

// --- Archives ---

export async function listArchives(signal?: AbortSignal): Promise<ArchiveListResponse> {
  return apiRequest<ArchiveListResponse>('/archives', { signal });
}

export async function listArchiveRecordings(
  cameraID: string,
  params?: { offset?: number; limit?: number; signal?: AbortSignal },
): Promise<RecordingListResponse> {
  const queryParams = new URLSearchParams();
  if (params?.offset !== undefined) queryParams.set('offset', String(params.offset));
  if (params?.limit !== undefined) queryParams.set('limit', String(params.limit));
  const query = queryParams.toString();
  const endpoint = query ? `/archives/${cameraID}/recordings?${query}` : `/archives/${cameraID}/recordings`;
  return apiRequest<RecordingListResponse>(endpoint, { signal: params?.signal });
}

export async function deleteArchiveGroup(cameraID: string, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/archives/${cameraID}`, { method: 'DELETE', signal });
}

export async function deleteArchiveRecording(
  cameraID: string,
  recordingID: string,
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/archives/${cameraID}/recordings/${recordingID}`, {
    method: 'DELETE',
    signal,
  });
}

export async function setArchiveRetention(
  cameraID: string,
  retentionDays: number,
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/archives/${cameraID}/retention`, {
    method: 'PUT',
    body: JSON.stringify({ retention_days: retentionDays }),
    signal,
  });
}

export interface ArchiveCleanupTask {
  camera_id: string;
  camera_name: string;
  recording_count: number;
  total_size: number;
  status: 'pending' | 'running' | 'done' | 'failed';
  error?: string;
  created_at: string;
  completed_at?: string;
}

export interface ArchiveCleanupStatus {
  active: ArchiveCleanupTask[];
  recent: ArchiveCleanupTask[];
}

export async function getArchiveCleanupStatus(signal?: AbortSignal): Promise<ArchiveCleanupStatus> {
  return apiRequest<ArchiveCleanupStatus>('/archives/cleanup-status', { signal });
}

// --- Timelapse Merge Cancel ---

export async function cancelMerge(cameraId: string): Promise<void> {
  const url = `${API_BASE}/timelapse/${cameraId}/merge`;
  const headers: Record<string, string> = {};
  const authHeader = getAuthHeader();
  if (authHeader) headers['Authorization'] = authHeader;
  const res = await fetch(url, { method: 'DELETE', headers });
  if (!res.ok) {
    const errorData = await res.json().catch(() => ({ error: 'Failed to cancel merge' }));
    throw new ApiRequestError(errorData.error || `HTTP ${res.status}`, errorData.code);
  }
}

// --- Recording Retry Merge ---

export async function retryRecordingMerge(recordingId: string): Promise<{ status: string; recording_id: string }> {
  return apiRequest<{ status: string; recording_id: string }>(`/recordings/${recordingId}/retry-merge`, {
    method: 'POST',
  });
}

export interface TimelapsePreviewFrame {
  url: string;
  filename: string;
  timestamp: string;
}

export async function fetchTimelapsePreview(id: string, sample: number = 6): Promise<TimelapsePreviewFrame[]> {
  return apiRequest<TimelapsePreviewFrame[]>(`/timelapse/${id}/preview?sample=${sample}`);
}

// --- Timeline seek event (observability, fire-and-forget) ---

export async function recordTimelineSeek(cameraId: string, type: 'segment' | 'intra'): Promise<void> {
  try {
    await apiRequest<void>('/recordings/timeline/seek-event', {
      method: 'POST',
      body: JSON.stringify({ camera_id: cameraId, type }),
    });
  } catch {
    // Non-fatal — observability only
  }
}
