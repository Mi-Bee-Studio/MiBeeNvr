/**
 * Transcoding API — hardware check, FFmpeg download, task management
 */
import { apiRequest } from './client';

// --- Types ---

export interface HardwareCapabilities {
  arch: string;
  total_cores: number;
  total_memory_mb: number;
  h264_encoder: string;
  h265_encoder: string;
  h264_encoder_type: string;
  h265_encoder_type: string;
  devices: string[];
  max_concurrent_streams: number;
  estimated_fps: number;
  ffmpeg_available: boolean;
}

export interface TranscodingSettings {
  enabled?: boolean;
  max_workers?: number;
  replace_original?: boolean;
}

export interface SelfCheckResult {
  supported: boolean;
  ffmpeg_status: string;
  encoders: Record<string, string>;  // {h264: encoder_name, h265: encoder_name}
  warnings: string[];
  max_concurrent: number;
  estimated_fps: number;
  total_cores: number;
  total_memory_mb: number;
  h264_encoder_type: string;
  h265_encoder_type: string;
  devices: string[];
}

export interface DownloadStatus {
	status: string;
	progress: number;
	version: string;
	error: string;
	total_bytes: number;
	downloaded_bytes: number;
}

export interface TranscodeTask {
  id: number;
  camera_id: string;
  recording_id: string;
  input_path: string;
  input_format: string;
  output_path: string;
  output_format: string;
  status: string;
  progress: number;
  error: string;
  created_at: string;
  started_at: string | null;
  completed_at: string | null;
  original_deleted: boolean;
}

export interface ManagerStatus {
  enabled: boolean;
  hardware: HardwareCapabilities | null;
  queue_length: number;
  active_jobs: number;
  recent_results: TranscodeTask[];
}

// --- Self-check ---

export async function getTranscodingCheck(): Promise<SelfCheckResult> {
  return apiRequest<SelfCheckResult>('/transcoding/check');
}

// --- FFmpeg management ---

export async function getFFmpegStatus(): Promise<DownloadStatus> {
  return apiRequest<DownloadStatus>('/transcoding/ffmpeg/status');
}

export async function downloadFFmpeg(): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/transcoding/ffmpeg/download', { method: 'POST' });
}

export async function retryDownload(): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/transcoding/ffmpeg/download/retry', { method: 'POST' });
}

// --- Manager status ---

export async function getTranscodingStatus(): Promise<ManagerStatus> {
  return apiRequest<ManagerStatus>('/transcoding/status');
}

// --- Tasks ---

export async function getTranscodingTasks(
  params?: { status?: string; camera_id?: string; page?: number; limit?: number }
): Promise<{ tasks: TranscodeTask[]; total: number; page: number }> {
  const query = new URLSearchParams();
  if (params?.status) query.set('status', params.status);
  if (params?.camera_id) query.set('camera_id', params.camera_id);
  if (params?.page) query.set('page', String(params.page));
  if (params?.limit) query.set('limit', String(params.limit));
  const qs = query.toString();
  return apiRequest(`/transcoding/tasks${qs ? '?' + qs : ''}`);
}

export async function enqueueTranscodeTask(body: {
  camera_id: string;
  recording_id: string;
  target_codec: string;
  replace_original: boolean;
}): Promise<TranscodeTask> {
  return apiRequest<TranscodeTask>('/transcoding/tasks', {
    method: 'POST',
    body: JSON.stringify(body),
  });
}

export async function cancelTranscodeTask(id: number): Promise<void> {
  return apiRequest<void>(`/transcoding/tasks/${id}`, { method: 'DELETE' });
}

// --- Cameras ---

export async function getTranscodingCameras(): Promise<Record<string, unknown>> {
  return apiRequest<Record<string, unknown>>('/transcoding/cameras');
}

// --- Settings ---

export async function getTranscodingSettings(): Promise<TranscodingSettings> {
  return apiRequest<TranscodingSettings>('/settings/transcoding');
}

export async function updateTranscodingSettings(config: TranscodingSettings): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/settings/transcoding', {
    method: 'PUT',
    body: JSON.stringify(config),
  });
}
