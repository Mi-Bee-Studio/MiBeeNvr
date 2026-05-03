/**
 * API Client for MiBee NVR
 * Handles authentication and API requests
 */

// Types for API responses
export interface Recording {
  id: string;
  camera_id: string;
  file_path: string;
  format: 'h264' | 'mjpeg';
  started_at: string;
  ended_at: string;
  duration: number;
  file_size: number;
  frame_count: number;
  pinned: boolean;
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
  url: string;
  enabled: boolean;
}

export interface CreateCameraRequest {
  name: string;
  protocol: string;
  url: string;
  username?: string;
  password?: string;
  enabled?: boolean;
}

export interface UpdateCameraRequest {
  name?: string;
  url?: string;
  protocol?: string;
  username?: string;
  password?: string;
  enabled?: boolean;
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

export interface SettingsConfig {
  cleanup: CleanupConfig;
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
  pinned?: boolean;
  offset?: number;
  limit?: number;
  start?: string;
  end?: string;
} = {}): Promise<RecordingListResponse> {
  const queryParams = new URLSearchParams();

  if (params.camera_id) queryParams.set('camera_id', params.camera_id);
  if (params.format) queryParams.set('format', params.format);
  if (params.pinned !== undefined) queryParams.set('pinned', String(params.pinned));
  if (params.offset !== undefined) queryParams.set('offset', String(params.offset));
  if (params.limit !== undefined) queryParams.set('limit', String(params.limit));
  if (params.start) queryParams.set('start', params.start);
  if (params.end) queryParams.set('end', params.end);

  const query = queryParams.toString();
  const endpoint = query ? `/recordings?${query}` : '/recordings';

  return apiRequest<RecordingListResponse>(endpoint);
}

export async function getRecording(id: string): Promise<Recording> {
  return apiRequest<Recording>(`/recordings/${id}`);
}

export async function deleteRecording(id: string): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/recordings/${id}`, {
    method: 'DELETE',
  });
}

export async function pinRecording(id: string): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/recordings/${id}/pin`, {
    method: 'POST',
  });
}

export async function unpinRecording(id: string): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/recordings/${id}/unpin`, {
    method: 'POST',
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
export async function healthCheck(): Promise<{ status: string }> {
  const response = await fetch('/api/health');
  return response.json();
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
