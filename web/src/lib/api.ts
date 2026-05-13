/**
 * API Client for MiBee NVR
 * Handles authentication and API requests
 */

// Types for API responses
export interface Recording {
  id: string;
  camera_id: string;
  file_path: string;
  format: 'h264' | 'mjpeg' | 'h265';
  started_at: string;
  ended_at: string;
  duration: number;
  file_size: number;
  frame_count: number;
  merged: boolean;
}

export interface FrameInfo {
  filename: string;
  index: number;
}

export interface FramesResponse {
  frames: FrameInfo[];
}

export interface Camera {
  id: string;
  name: string;
  protocol: string;
  encoding?: string;
  url: string;
  username?: string;
  has_password?: boolean;
  enabled: boolean;
  description?: string;
  location?: string;
  brand?: string;
  model?: string;
  serial_number?: string;
  status?: string;
  last_seen?: string;
  retention_days?: number;
  onvif_endpoint?: string;
  profile_token?: string;
  stream_encoding?: string;
}

export interface CreateCameraRequest {
  name: string;
  protocol: string;
  encoding?: string;
  url?: string;
  username?: string;
  password?: string;
  enabled?: boolean;
  description?: string;
  location?: string;
  brand?: string;
  model?: string;
  serial_number?: string;
  onvif_endpoint?: string;
  profile_token?: string;
  stream_encoding?: string;
}

export interface UpdateCameraRequest {
  name?: string;
  url?: string;
  protocol?: string;
  encoding?: string;
  username?: string;
  password?: string;
  enabled?: boolean;
  description?: string;
  location?: string;
  brand?: string;
  model?: string;
  serial_number?: string;
  retention_days?: number;
  onvif_endpoint?: string;
  profile_token?: string;
  stream_encoding?: string;
}

export interface MergeConfig {
  enabled?: boolean;
  check_interval?: string;
  window_size?: string;
  batch_limit?: number;
  min_segment_age?: string;
  min_segments_to_merge?: number;
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

export interface CleanupConfig {
  retention_days: number;
  disk_threshold_percent: number;
  check_interval: string;
}

export interface WebDAVConfig {
  enabled: boolean;
  path_prefix: string;
  read_write: boolean;
}

export interface SettingsConfig {
  cleanup: CleanupConfig;
  webdav: WebDAVConfig;
}

export interface RecordingListResponse {
  recordings: Recording[];
  total?: number;
}

export interface LoginResponse {
  status: string;
}

export interface ApiError {
  error: string;
}
export interface HealthCheck {
  status: 'ok' | 'warning' | 'error';
  message?: string;
}

export interface HealthResponse {
  status: 'ok' | 'degraded' | 'unhealthy';
  checks: Record<string, HealthCheck>;
  uptime: string;
}

export interface SystemStats {
  cpu: {
    total: number;
    idle: number;
  };
  memory: {
    total: number;
    available: number;
    process_rss: number;
  };
  network: {
    bytes_sent: number;
    bytes_recv: number;
  };
  uptime: string;
  timestamp: number;
}

// Auth credentials storage
const AUTH_KEY = 'mibee_nvr_auth';

export interface AuthCredentials {
  username: string;
  password: string;
}

// Store credentials in localStorage
export function storeCredentials(username: string, password: string): void {
  const encoded = btoa(`${username}:${password}`);
  localStorage.setItem(AUTH_KEY, encoded);
}

// Get credentials from localStorage
export function getCredentials(): AuthCredentials | null {
  const encoded = localStorage.getItem(AUTH_KEY);
  if (!encoded) return null;

  try {
    const decoded = atob(encoded);
    const [username, password] = decoded.split(':');
    return { username, password };
  } catch {
    return null;
  }
}

// Clear credentials
export function clearCredentials(): void {
  localStorage.removeItem(AUTH_KEY);
}

// Check if user is authenticated
export function isAuthenticated(): boolean {
  return getCredentials() !== null;
}

// Get Basic Auth header value
function getAuthHeader(): string | null {
  const creds = getCredentials();
  if (!creds) return null;

  const encoded = btoa(`${creds.username}:${creds.password}`);
  return `Basic ${encoded}`;
}

// API base URL (relative path for embedded static files)
const API_BASE = '/api';

// Generic API request function
async function apiRequest<T>(
  endpoint: string,
  options: RequestInit = {}
): Promise<T> {
  const url = `${API_BASE}${endpoint}`;

  const headers: HeadersInit = {
    'Content-Type': 'application/json',
    ...options.headers,
  };

  const authHeader = getAuthHeader();
  if (authHeader) {
    headers['Authorization'] = authHeader;
  }

  const response = await fetch(url, {
    ...options,
    headers,
  });

  if (!response.ok) {
    const errorData = await response.json().catch(() => ({ error: 'Unknown error' }));
    throw new Error((errorData as ApiError).error || `HTTP ${response.status}`);
  }

  return response.json();
}

// Generic API request for blob responses (e.g. file downloads)
async function apiRequestBlob(endpoint: string): Promise<Blob> {
  const url = `${API_BASE}${endpoint}`;

  const headers: HeadersInit = {};
  const authHeader = getAuthHeader();
  if (authHeader) {
    headers['Authorization'] = authHeader;
  }

  const response = await fetch(url, { headers });
  if (!response.ok) {
    throw new Error(`HTTP ${response.status}`);
  }
  return response.blob();
}

// Login endpoint
export async function login(username: string, password: string): Promise<LoginResponse> {
  // First, test credentials by making an authenticated request to a protected endpoint
  const authHeader = `Basic ${btoa(`${username}:${password}`)}`;

  const response = await fetch('/api/auth/login', {
    method: 'POST',
    headers: {
      'Authorization': authHeader,
    },
  });

  if (!response.ok) {
    const errorData = await response.json().catch(() => ({ error: 'Invalid credentials' }));
    throw new Error((errorData as ApiError).error || 'Invalid credentials');
  }

  const data = await response.json();

  // Store credentials on success
  storeCredentials(username, password);

  return data as LoginResponse;
}

// Logout
export function logout(): void {
  clearCredentials();
  window.location.hash = '#/login';
}

// Recordings endpoints
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

  const query = queryParams.toString();
  const endpoint = query ? `/recordings?${query}` : '/recordings';

  const { signal } = params;
  return apiRequest<RecordingListResponse>(endpoint, { signal });
}

export async function getRecording(id: string): Promise<Recording> {
  return apiRequest<Recording>(`/recordings/${id}`);
}

export async function deleteRecording(id: string): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/recordings/${id}`, {
    method: 'DELETE',
  });
}

export async function batchDeleteRecordings(ids: string[]): Promise<void> {
  await apiRequest<void>('/recordings/batch-delete', {
    method: 'POST',
    body: JSON.stringify({ ids }),
  });
}


export function getRecordingDownloadUrl(id: string): string {
  return `/api/recordings/${id}/download`;
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

// Frame endpoints (for MJPEG recordings)
export async function listFrames(recordingId: string): Promise<FramesResponse> {
  return apiRequest<FramesResponse>(`/recordings/${recordingId}/frames`);
}

export async function loadFrameBlob(recordingId: string, frameIndex: number): Promise<string> {
  const blob = await apiRequestBlob(`/recordings/${recordingId}/download?frame=${frameIndex}`);
  return URL.createObjectURL(blob);
}

export async function loadRecordingVideoBlob(recordingId: string): Promise<string> {
  const blob = await apiRequestBlob(`/recordings/${recordingId}/download`);
  return URL.createObjectURL(blob);
}

// Cameras endpoint
export async function listCameras(): Promise<Camera[]> {
  return apiRequest<Camera[]>('/cameras');
}

export async function createCamera(data: CreateCameraRequest): Promise<Camera> {
  return apiRequest<Camera>('/cameras', {
    method: 'POST',
    body: JSON.stringify(data),
  });
}

export async function getCamera(id: string): Promise<Camera> {
  return apiRequest<Camera>(`/cameras/${id}`);
}

export async function updateCamera(id: string, data: UpdateCameraRequest): Promise<Camera> {
  return apiRequest<Camera>(`/cameras/${id}`, {
    method: 'PUT',
    body: JSON.stringify(data),
  });
}

export async function deleteCamera(id: string): Promise<void> {
  return apiRequest<void>(`/cameras/${id}`, {
    method: 'DELETE',
  });
}


// Stats endpoint
export async function getStats(): Promise<StorageStats> {
  return apiRequest<StorageStats>('/stats');
}

export async function getStatsTrends(days = 7): Promise<DailyStats[]> {
  return apiRequest<DailyStats[]>(`/stats/trends?days=${days}`);
}

// Health check (no auth required)
export async function healthCheck(): Promise<HealthResponse> {
  const response = await fetch('/api/health');
  return response.json();
}

// System stats endpoint
export async function getSystemStats(): Promise<SystemStats> {
  return apiRequest<SystemStats>('/stats/system');
}
// Settings endpoints
export async function getSettings(): Promise<SettingsConfig> {
  return apiRequest<SettingsConfig>('/settings');
}

export async function updateSettings(settings: SettingsConfig): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/settings', {
    method: 'PUT',
    body: JSON.stringify(settings),
  });
}

// Dashboard-related types and functions
// Interfaces
export interface DiscoveredDevice {
  uuid: string;
  name: string;
  xaddrs: string[];
  scopes: string[];
  hardware: string;
  endpoint: string;
}

export interface DeviceInfo {
  manufacturer: string;
  model: string;
  firmware: string;
  serial_number: string;
  hardware_id: string;
}

export interface DeviceProfile {
  token: string;
  name: string;
  encoding: string;
  width: number;
  height: number;
}

export interface ONVIFDeviceDetail {
  device_info: DeviceInfo;
  profiles: DeviceProfile[];
}

export interface PTZMoveRequest {
  mode: 'continuous' | 'absolute' | 'relative';
  pan: number;
  tilt: number;
  zoom: number;
}

export interface PTZStatus {
  pan: number;
  tilt: number;
  zoom: number;
  moving: boolean;
}

// Functions
export function getDashboardCameras(): Promise<Camera[]> {
  return apiRequest('/cameras');
}

export async function discoverONVIFDevices(timeout: number = 5): Promise<DiscoveredDevice[]> {
  const result = await apiRequest<{ devices: DiscoveredDevice[] }>('/onvif/discover', {
    method: 'POST',
    body: JSON.stringify({ timeout }),
  });
  return result.devices || [];
}

export async function getONVIFDeviceDetail(ip: string): Promise<ONVIFDeviceDetail> {
  return apiRequest<ONVIFDeviceDetail>(`/onvif/discover/${ip}`);
}

export async function probeONVIFDevice(host: string, port: number = 80): Promise<DiscoveredDevice | null> {
  const result = await apiRequest<{ device: DiscoveredDevice | null }>('/onvif/probe', {
    method: 'POST',
    body: JSON.stringify({ host, port }),
  });
  return result.device;
}
export async function ptzMove(cameraId: string, request: PTZMoveRequest): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/ptz/move`, {
    method: 'POST',
    body: JSON.stringify(request),
  });
}
export async function ptzStop(cameraId: string): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/ptz/stop`, {
    method: 'POST',
  });
}
export async function getPTZStatus(cameraId: string): Promise<PTZStatus> {
  return apiRequest<PTZStatus>(`/cameras/${cameraId}/ptz/status`);
}

// Snapshot URL helper (returns JPEG from camera snapshot endpoint)
export function getSnapshotUrl(cameraId: string): string {
  return `/api/cameras/${cameraId}/snapshot`;
}

// Per-camera merge config
export async function getMergeConfig(cameraId: string): Promise<MergeConfig | null> {
  try {
    return await apiRequest<MergeConfig>(`/cameras/${cameraId}/merge-config`);
  } catch {
    return null;
  }
}

export async function updateMergeConfig(cameraId: string, config: MergeConfig): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/merge-config`, {
    method: 'PUT',
    body: JSON.stringify(config),
  });
}

export async function deleteCameraMergeConfig(cameraId: string): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/merge-config`, {
    method: 'DELETE',
  });
}

// Global merge settings
export async function getMergeSettings(): Promise<MergeConfig> {
  return apiRequest<MergeConfig>('/settings/merge');
}

export async function updateMergeSettings(config: MergeConfig): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/settings/merge', {
    method: 'PUT',
    body: JSON.stringify(config),
  });
}

// Merge status & pending
export interface MergeStatus {
  enabled: boolean;
  last_run_time: string;
  segments_merged: number;
  files_created: number;
  error_count: number;
}

export interface MergePending {
  enabled: boolean;
  pending: Record<string, number>;
}

export async function getMergeStatus(): Promise<MergeStatus> {
  return apiRequest<MergeStatus>('/merge/status');
}

export async function getMergePending(): Promise<MergePending> {
  return apiRequest<MergePending>('/merge/pending');
}

// Xiaomi API
export interface XiaomiDevice {
  did: string;
  name: string;
  model: string;
  ip: string;
  isOnline: boolean;
}

export async function xiaomiAuth(username: string, password: string, region?: string): Promise<{ user_id: string }> {
  return apiRequest('/xiaomi/auth', {
    method: 'POST',
    body: JSON.stringify({ username, password, region: region || 'cn' }),
  });
}

export async function xiaomiDevices(): Promise<XiaomiDevice[]> {
  return apiRequest('/xiaomi/devices');
}