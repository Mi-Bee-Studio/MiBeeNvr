/**
 * Camera API — CRUD and per-camera merge config.
 *
 * Types live in ./camera-types, PTZ in ./camera-ptz; both are re-exported here
 * so callers can keep importing everything from './cameras'.
 */
import { apiRequest, getAuthHeader, clearToken, API_BASE, readJson } from './client';
import type {
	Camera,
	CreateCameraRequest,
	UpdateCameraRequest,
	CameraSummary,
	CameraRecordingStats,
	DiscoveredDevice,
	DiscoveryError,
	DeviceInfo,
	DiscoveryResult,
	ProtocolCapabilities,
	ProtocolInfo,
	PushStatusResponse,
	RelayCapabilities,
} from './camera-types';

// --- Camera CRUD ---

// ETag cache for the full camera list. Avoids re-downloading the body when
// camera statuses haven't changed between polls (304 Not Modified from server).
// The Dashboard polls every 30s; without this, each poll re-downloads the full
// list even when nothing changed.
let fullListEtag: string | null = null;
let cachedFullList: Camera[] | null = null;

/**
 * Discard the full-list ETag cache so the next listCameras() issues a full GET
 * instead of returning a stale body on a 304.
 *
 * The server's full-list ETag is hashed from camera count + ID + Status +
 * LastSeen only — it does NOT cover fields like push_targets, name,
 * transcoding, audio_enabled, ... So any write that mutates such a field leaves
 * the ETag unchanged, the next listCameras() gets a 304, and the caller renders
 * the pre-write cached copy. This is exactly the "deleted push-out target
 * reappears" regression in issue #197.
 *
 * Rather than keep the ETag hash in sync with every field (fragile — the next
 * new field reintroduces the bug), we decouple correctness from caching: every
 * mutating camera call invalidates the cache on success, forcing a fresh GET.
 * The ETag still pays off for the read-only Dashboard 30s poll. This is the
 * structural fix: correctness no longer depends on which fields the hash covers.
 */
export function invalidateCameraListCache() {
  fullListEtag = null;
  cachedFullList = null;
}

export async function listCameras(signal?: AbortSignal): Promise<Camera[]> {
  const headers: Record<string, string> = {};
  if (fullListEtag) headers['If-None-Match'] = fullListEtag;
  const authHeader = getAuthHeader();
  if (authHeader) headers['Authorization'] = authHeader;

  const resp = await fetch(`${API_BASE}/cameras`, {
    headers,
    signal: signal ?? AbortSignal.timeout(30000),
  });
  if (resp.status === 304 && cachedFullList) {
    return cachedFullList; // unchanged — return cached copy
  }
  if (!resp.ok) {
    if (resp.status === 401) {
      clearToken();
      window.location.hash = '#/login';
    }
    throw new Error(`HTTP ${resp.status}`);
  }
  const newEtag = resp.headers.get('ETag');
  if (newEtag) fullListEtag = newEtag;
  // On a gateway bounce the retry skips the ETag capture above; the next poll
  // then misses 304 once and refreshes the full list — self-correcting.
  const data = await readJson<Camera[]>(resp, () =>
    fetch(`${API_BASE}/cameras`, {
      headers,
      signal: signal ?? AbortSignal.timeout(30000),
    }),
  );
  cachedFullList = data;
  return data;
}

// ETag cache for summary view — avoids re-downloading the body when camera
// statuses haven't changed between polls (304 Not Modified from server).
let summaryEtag: string | null = null;
let cachedSummary: CameraSummary[] | null = null;

export interface CameraSummary {
  id: string;
  name: string;
  status: string;
  encoding?: string;
  protocol?: string;
  is_recording: boolean;
  last_seen?: string;
  error_code?: string;
}

/** Lightweight camera list for Dashboard/grid views (~60% smaller than full list).
 *  Supports ETag conditional requests: returns null on 304 (unchanged) so the
 *  caller knows to keep existing data. */
export async function listCamerasSummary(signal?: AbortSignal): Promise<CameraSummary[] | null> {
  const headers: Record<string, string> = {};
  if (summaryEtag) headers['If-None-Match'] = summaryEtag;
  const authHeader = getAuthHeader();
  if (authHeader) headers['Authorization'] = authHeader;

  const resp = await fetch(`${API_BASE}/cameras?view=summary`, {
    headers,
    signal: signal ?? AbortSignal.timeout(30000),
  });
  if (resp.status === 304 && cachedSummary) {
    return cachedSummary; // unchanged — return cached copy
  }
  if (!resp.ok) {
    throw new Error(`HTTP ${resp.status}`);
  }
  const newEtag = resp.headers.get('ETag');
  if (newEtag) summaryEtag = newEtag;
  // Retry skips the ETag capture above on a gateway bounce; the next poll
  // misses 304 once and refreshes — self-correcting.
  const data = await readJson<CameraSummary[]>(resp, () =>
    fetch(`${API_BASE}/cameras?view=summary`, {
      headers,
      signal: signal ?? AbortSignal.timeout(30000),
    }),
  );
  cachedSummary = data;
  return data;
}

export async function createCamera(data: CreateCameraRequest, signal?: AbortSignal): Promise<Camera> {
  const camera = await apiRequest<Camera>('/cameras', {
    method: 'POST',
    body: JSON.stringify(data),
    signal,
  });
  invalidateCameraListCache();
  return camera;
}

export async function getCamera(id: string, signal?: AbortSignal): Promise<Camera> {
  return apiRequest<Camera>(`/cameras/${id}`, { signal });
}

// ── Camera group registry (v37) ─────────────────────────────────────────────
// The registry lets an empty group exist ahead of any member camera; rename/
// delete walk the member cameras server-side. Per-camera moves (drag & drop)
// go through updateCamera({group}) instead.

export async function listCameraGroups(signal?: AbortSignal): Promise<string[]> {
  const groups = await apiRequest<string[]>(`/cameras/groups`, { signal: signal ?? AbortSignal.timeout(15000) });
  return Array.isArray(groups) ? groups : [];
}

export async function createCameraGroup(name: string, signal?: AbortSignal): Promise<void> {
  await apiRequest<{ name: string }>(`/cameras/groups`, {
    method: 'POST',
    body: JSON.stringify({ name }),
    signal: signal ?? AbortSignal.timeout(15000),
  });
}

export async function renameCameraGroup(oldName: string, newName: string, signal?: AbortSignal): Promise<void> {
  await apiRequest<{ name: string }>(`/cameras/groups/${encodeURIComponent(oldName)}`, {
    method: 'PUT',
    body: JSON.stringify({ name: newName }),
    signal: signal ?? AbortSignal.timeout(15000),
  });
}

/** Stores the full named-group order (drag-to-reorder, v38). */
export async function setCameraGroupsOrder(names: string[], signal?: AbortSignal): Promise<void> {
  await apiRequest<{ count: number }>(`/cameras/groups/order`, {
    method: 'PUT',
    body: JSON.stringify({ names }),
    signal: signal ?? AbortSignal.timeout(15000),
  });
}

/** Deletes the group and ungroups its member cameras. Returns the number ungrouped. */
export async function deleteCameraGroup(name: string, signal?: AbortSignal): Promise<number> {
  const resp = await apiRequest<{ ungrouped: number }>(`/cameras/groups/${encodeURIComponent(name)}`, {
    method: 'DELETE',
    signal: signal ?? AbortSignal.timeout(15000),
  });
  return resp?.ungrouped ?? 0;
}

export async function updateCamera(id: string, data: UpdateCameraRequest, signal?: AbortSignal): Promise<Camera> {
  const camera = await apiRequest<Camera>(`/cameras/${id}`, {
    method: 'PUT',
    body: JSON.stringify(data),
    signal,
  });
  invalidateCameraListCache();
  return camera;
}

/** Fetch live push-out relay status for a camera (per-target state + bitrate). */
export async function getPushStatus(id: string, signal?: AbortSignal): Promise<PushStatusResponse> {
  return apiRequest<PushStatusResponse>(`/cameras/${id}/push-status`, { signal });
}

export async function deleteCamera(id: string, signal?: AbortSignal): Promise<void> {
  await apiRequest<void>(`/cameras/${id}`, {
    method: 'DELETE',
    signal,
  });
  invalidateCameraListCache();
}

export interface CameraRecordingStats {
  recording_count: number;
  total_size: number;
}

export async function getCameraRecordingStats(id: string, signal?: AbortSignal): Promise<CameraRecordingStats> {
  return apiRequest<CameraRecordingStats>(`/cameras/${id}/stats`, { signal });
}

export async function startCamera(id: string, signal?: AbortSignal): Promise<{ status: string }> {
  const res = await apiRequest<{ status: string }>(`/cameras/${id}/start`, {
    method: 'POST',
    signal,
  });
  invalidateCameraListCache();
  return res;
}

export async function stopCamera(id: string, signal?: AbortSignal): Promise<{ status: string }> {
  const res = await apiRequest<{ status: string }>(`/cameras/${id}/stop`, {
    method: 'POST',
    signal,
  });
  invalidateCameraListCache();
  return res;
}

// Manually trigger IP self-healing for a camera whose network address may have
// changed (e.g. after an AP reboot across per-subnet DHCP). Scans candidate
// subnets for a device whose ONVIF serial matches the camera's stable_id and, if
// found, reconnects. Returns whether the camera was relocated.
export async function rediscoverCamera(
  id: string,
  signal?: AbortSignal,
): Promise<{ found: boolean; status?: string; reason?: string }> {
  // The unicast scan can run up to the configured MaxDuration (default 30s) plus
  // restart time, so use a generous client-side timeout rather than the default
  // 30s. Caller-supplied signal takes precedence.
  const effectiveSignal = signal ?? AbortSignal.timeout(90000);
  const res = await apiRequest<{ found: boolean; status?: string; reason?: string }>(`/cameras/${id}/rediscover`, {
    method: 'POST',
    signal: effectiveSignal,
  });
  invalidateCameraListCache();
  return res;
}

/**
 * Activate a pending_activation camera by supplying credentials. The backend
 * flips activation_state to "active" and starts the recorder. Idempotent for an
 * already-active camera (re-applies credentials + restarts with new creds).
 * Activation may trigger an ONVIF handshake + RTSP dial, so use a generous timeout.
 */
export async function activateCamera(
  id: string,
  credentials: { username: string; password: string },
  signal?: AbortSignal,
): Promise<{ status: string }> {
  const effectiveSignal = signal ?? AbortSignal.timeout(60000);
  const res = await apiRequest<{ status: string }>(`/cameras/${id}/activate`, {
    method: 'POST',
    body: JSON.stringify(credentials),
    signal: effectiveSignal,
  });
  invalidateCameraListCache();
  return res;
}

export function getDashboardCameras(signal?: AbortSignal): Promise<Camera[]> {
  return apiRequest('/cameras', { signal });
}

// --- Test Connection ---

export interface TestConnectionRequest {
  protocol: string;
  url: string;
  username?: string;
  password?: string;
  encoding?: string;
  onvif_endpoint?: string;
  /** ONVIF: probe this media profile's stream (default: the first one). */
  profile_token?: string;
}

export interface TestConnectionResult {
  success: boolean;
  message: string;
  latency_ms: number;
  // ONVIF structured probe fields (issues #29/#30). Distinguish "device
  // reachable" from "stream actually playable" so users aren't told success
  // when only the device_service URL responded.
  reachable?: boolean;
  stream_ok?: boolean;
  encoding?: string;
  codec_lie?: boolean;
  // RTSP DESCRIBE probe fields (#964): what the URL actually serves. `audio`
  // is a human-readable fragment ("PCMA 8000Hz", "G726 (unsupported)") —
  // absent when the stream has no audio track at all.
  audio?: string;
  has_audio?: boolean;
  audio_supported?: boolean;
  // ONVIF media profiles (dual-spectrum/multi-lens main-profile picker):
  // present when GetProfiles succeeded during the probe. auto_main_token is
  // the token the backend's automatic selection would pick ('' when none).
  profiles?: DeviceProfileOption[];
  auto_main_token?: string;
}

/** One ONVIF media profile as offered by the main-profile picker. */
export interface DeviceProfileOption {
  token: string;
  name?: string;
  encoding?: string;
  width?: number;
  height?: number;
}

/** Fetch a registered ONVIF camera's media profiles (edit-form picker). */
export async function getCameraProfiles(cameraId: string, signal?: AbortSignal): Promise<DeviceProfileOption[]> {
  const result = await apiRequest<{ profiles: DeviceProfileOption[] }>(`/cameras/${cameraId}/onvif/profiles`, {
    signal,
  });
  return result.profiles || [];
}

export async function testConnection(data: TestConnectionRequest, signal?: AbortSignal): Promise<TestConnectionResult> {
  return apiRequest<TestConnectionResult>('/cameras/test-connection', {
    method: 'POST',
    body: JSON.stringify(data),
    signal,
  });
}

// Snapshot URL helper (returns JPEG from camera snapshot endpoint)
export function getSnapshotUrl(cameraId: string): string {
  return `${API_BASE}/cameras/${cameraId}/snapshot`;
}

// --- Per-camera merge config ---

export interface MergeConfig {
  // customized=false means no per-camera override exists (all fields NULL) →
  // the camera uses global defaults. Drives editor collapse state (issue #68-3).
  customized?: boolean;
  enabled?: boolean;
  check_interval?: string;
  window_size?: string;
  batch_limit?: number;
  min_segment_age?: string;
  min_segments_to_merge?: number;
  // Fragment batching window in seconds (#852): segments <30s are held this
  // long and folded into the hour bucket in ONE merge. The global settings
  // GET reports the effective value; 0 disables batching.
  rolling_fragment_hold_s?: number;
  // Sequential-append hour bucket (#853): folds append at the mdat tail
  // instead of rewriting the bucket. Experimental, default OFF (RAM cost on
  // small boards).
  rolling_append_bucket?: boolean;
}

export async function getMergeConfig(cameraId: string, signal?: AbortSignal): Promise<MergeConfig | null> {
  try {
    return await apiRequest<MergeConfig>(`/cameras/${cameraId}/merge-config`, { signal });
  } catch {
    return null;
  }
}

export async function updateMergeConfig(
  cameraId: string,
  config: MergeConfig,
  signal?: AbortSignal,
): Promise<{ status: string }> {
  const res = await apiRequest<{ status: string }>(`/cameras/${cameraId}/merge-config`, {
    method: 'PUT',
    body: JSON.stringify(config),
    signal,
  });
  invalidateCameraListCache();
  return res;
}

export async function deleteCameraMergeConfig(cameraId: string, signal?: AbortSignal): Promise<{ status: string }> {
  const res = await apiRequest<{ status: string }>(`/cameras/${cameraId}/merge-config`, {
    method: 'DELETE',
    signal,
  });
  invalidateCameraListCache();
  return res;
}

// ---- Re-exports (backwards compatibility for existing imports) ----
export * from './camera-types';
export * from './camera-ptz';
