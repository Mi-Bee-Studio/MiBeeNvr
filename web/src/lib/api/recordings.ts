/**
 * Recording API — list, download, frames, stats, archives
 */
import { apiRequest, apiRequestBlob, getAuthHeader } from './client';

// --- Types ---

export interface Recording {
  id: string;
  camera_id: string;
  file_path: string;
  format: 'h264' | 'mjpeg' | 'h265' | 'timelapse';
  started_at: string;
  ended_at: string;
  duration: number;
  file_size: number;
  frame_count: number;
  merged: boolean;
  merge_status?: 'pending' | 'merged' | 'failed';
  merge_path?: string;
  archived?: boolean;
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

export async function listRecordings(params: {
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
  signal?: AbortSignal;
} = {}): Promise<RecordingListResponse> {
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

  const query = queryParams.toString();
  const endpoint = query ? `/recordings?${query}` : '/recordings';

  const { signal } = params;
  return apiRequest<RecordingListResponse>(endpoint, { signal });
}

export async function getRecording(id: string, signal?: AbortSignal): Promise<Recording> {
  return apiRequest<Recording>(`/recordings/${id}`, { signal });
}

export async function deleteRecording(
  id: string,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/recordings/${id}`, {
    method: 'DELETE',
    signal,
  });
}

export async function batchDeleteRecordings(
  ids: string[],
  signal?: AbortSignal
): Promise<void> {
  await apiRequest<void>('/recordings/batch-delete', {
    method: 'POST',
    body: JSON.stringify({ ids }),
    signal,
  });
}

export function getRecordingDownloadUrl(id: string): string {
  return `/api/recordings/${id}/download`;
}
export function getMergedRecordingUrl(id: string): string {
  return `/api/recordings/${id}/merged`;
}

export async function downloadRecording(
  id: string,
  onProgress?: (loaded: number, total: number) => void
): Promise<void> {
  const url = `/api/recordings/${id}/download`;

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

export async function listFrames(
  recordingId: string,
  signal?: AbortSignal
): Promise<FramesResponse> {
  return apiRequest<FramesResponse>(`/recordings/${recordingId}/frames`, { signal });
}

// --- Timelapse frames ---

export async function getTimelapseFrames(
  recordingId: string,
  signal?: AbortSignal
): Promise<TimelapseFrame[]> {
  return apiRequest<TimelapseFrame[]>(`/recordings/${recordingId}/timelapse-frames`, { signal });
}

export async function loadTimelapseFrameBlob(
  recordingId: string,
  filename: string,
  signal?: AbortSignal
): Promise<string> {
  const blob = await apiRequestBlob(`/recordings/${recordingId}/timelapse-frames/${filename}`, { signal });
  return URL.createObjectURL(blob);
}
export async function loadFrameBlob(
  recordingId: string,
  frameIndex: number,
  signal?: AbortSignal
): Promise<string> {
  const blob = await apiRequestBlob(`/recordings/${recordingId}/download?frame=${frameIndex}`, { signal });
  return URL.createObjectURL(blob);
}

export async function loadRecordingVideoBlob(
  recordingId: string,
  signal?: AbortSignal
): Promise<string> {
  const blob = await apiRequestBlob(`/recordings/${recordingId}/download`, { signal });
  return URL.createObjectURL(blob);
}

// --- Stats ---

export async function getStats(signal?: AbortSignal): Promise<StorageStats> {
  return apiRequest<StorageStats>('/stats', { signal });
}

export async function getStatsTrends(
  days: number = 7,
  signal?: AbortSignal
): Promise<DailyStats[]> {
  return apiRequest<DailyStats[]>(`/stats/trends?days=${days}`, { signal });
}

// --- Archives ---

export async function listArchives(signal?: AbortSignal): Promise<ArchiveListResponse> {
  return apiRequest<ArchiveListResponse>('/archives', { signal });
}

export async function listArchiveRecordings(
  cameraID: string,
  params?: { offset?: number; limit?: number; signal?: AbortSignal }
): Promise<RecordingListResponse> {
  const queryParams = new URLSearchParams();
  if (params?.offset !== undefined) queryParams.set('offset', String(params.offset));
  if (params?.limit !== undefined) queryParams.set('limit', String(params.limit));
  const query = queryParams.toString();
  const endpoint = query ? `/archives/${cameraID}/recordings?${query}` : `/archives/${cameraID}/recordings`;
  return apiRequest<RecordingListResponse>(endpoint, { signal: params?.signal });
}

export async function deleteArchiveGroup(
  cameraID: string,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/archives/${cameraID}`, { method: 'DELETE', signal });
}

export async function deleteArchiveRecording(
  cameraID: string,
  recordingID: string,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/archives/${cameraID}/recordings/${recordingID}`, {
    method: 'DELETE',
    signal,
  });
}

export async function setArchiveRetention(
  cameraID: string,
  retentionDays: number,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/archives/${cameraID}/retention`, {
    method: 'PUT',
    body: JSON.stringify({ retention_days: retentionDays }),
    signal,
  });
}
