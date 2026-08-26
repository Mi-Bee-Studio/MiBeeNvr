/**
 * Camera API — CRUD, ONVIF discovery, PTZ, protocols, per-camera merge config
 */
import { apiRequest, getAuthHeader, clearToken, API_BASE } from './client';

// --- Types ---

export interface CameraTranscodingConfig {
  enabled: boolean;
  target_codec: string;
  preset: string;
  bitrate: string;
}

export interface Camera {
  id: string;
  name: string;
  protocol: string;
  encoding?: string;
  url: string;
  username?: string;
  has_password?: boolean;
  description?: string;
  location?: string;
  brand?: string;
  model?: string;
  serial_number?: string;
  status?: string;
  error_type?: string | null;
  error_detail?: string | null;
  last_seen?: string;
  retention_days?: number;
  onvif_endpoint?: string;
  profile_token?: string;
  stream_encoding?: string;
  transcoding?: CameraTranscodingConfig;
  channel?: string;
  audio_enabled?: boolean;
  /** Keep the real audio track in recorded segments (default off). */
  audio_in_recordings?: boolean;
  // Recording gate: false = live-only (no segments written to disk; the recorder
  // stays connected for live preview + relay + health). undefined = record.
  recording_enabled?: boolean | null;
  // Cascade gate: false = hidden from the GB28181 cascade catalog (upper
  // platform cannot see or invite it). undefined = exposed.
  cascade_enabled?: boolean | null;
  // Recording mode (#435): 'continuous' (default) or 'adaptive' — dynamic
  // timelapse that drops to sparse keyframes while the compressed-domain
  // activity signal stays calm. 'adaptive' holds the tuning knobs (nil = defaults).
  recording_mode?: string;
  adaptive?: AdaptiveRecordingConfig;
  // Loudness trigger for adaptive recording (#478); only effective with
  // recording_mode 'adaptive'.
  audio_trigger?: CameraAudioTriggerConfig;
  // Xiaomi two-way audio enable flag
  two_way_audio_enabled?: boolean;
  // Push/ingest fields (SRT/RTMP cameras)
  stream_key?: string;
  srt_passphrase?: string;
  srt_stream_id?: string;
  // Push-out relay (forward this camera's stream to remote targets)
  push_targets?: PushTargetConfig[];
  push_retention_days?: number | null;
  // IP self-healing: ONVIF serial number (stable hardware identity) + candidate
  // subnets used to relocate the camera after its IP changes.
  stable_id?: string;
  subnet_hints?: string[];
  // Auto-discover activation gate: "active" (default, recorder runs) or
  // "pending_activation" (auto-discovered but credentials unknown — recorder
  // NOT started, user must supply credentials via the activate endpoint).
  activation_state?: string;
  // GB28181 SIP-registered device/channel binding
  gb28181?: {
    device_id: string;
    channel_id: string;
    manufacturer?: string;
  };
}

/** Tuning knobs for recording_mode: 'adaptive' (#435). */
export interface AdaptiveRecordingConfig {
  /** How long activity must stay calm before sparse keyframe mode. Default '60s'. */
  calm_threshold?: string;
  /** Keyframe cadence while sparse. Default '30s'. */
  timelapse_interval?: string;
  /** Activity spike sensitivity (MAD deviations above baseline). Default 5.0. */
  spike_factor?: number;
  /** Seamless-transition GOP pre-buffer cap in bytes. Default 16MB. */
  gop_buffer_bytes?: number;
  /** Keep the audio track recording continuously while sparse (#496): the
   *  merge renders it into a quiet atmosphere bed under the compressed
   *  timelapse video. G.711 cameras only; ~28.8MB/h while sparse. */
  ambient_audio?: boolean;
  /** Compressed-timeline frame cadence preset (ms): 100/300/500, 0 = default 100. */
  timelapse_frame_ms?: number;
  /** Keep the raw ambient G.711 as a sidecar file for post-production. */
  ambient_archive?: boolean;
}

/** Loudness trigger knobs for recording_mode: 'adaptive' (#478). */
export interface CameraAudioTriggerConfig {
  /** Arm the loudness input. */
  enabled: boolean;
  /** 1s-window loudness threshold in dBFS. Default -45. Range -90..0. */
  min_dbfs?: number;
  /** Seconds of pre-trigger audio back-filled on a timelapse exit. Default 3. */
  pre_capture_s?: number;
}

/** One push-out relay destination (RTMP/RTSP) for a camera. */
export interface PushTargetConfig {
  id: string;
  name?: string;
  protocol: 'rtmp' | 'rtsp';
  url: string;
  enabled: boolean;
  platform?: string;
  transcode_policy?: 'auto' | 'force_sw' | 'off';
  video_preset_override?: VideoPresetOverrides;
  use_ffmpeg?: boolean;
}

/** Per-target encoding overrides for a push relay destination. */
export interface VideoPresetOverrides {
  resolution?: string;
  framerate?: number;
  video_bitrate_kbps?: number;
  gop_seconds?: number;
  profile?: 'baseline' | 'main' | 'high';
  bframes?: number;
}

/** Live runtime status of one push-out target (from GET push-status). */
export interface PushTargetStatus {
  id: string;
  name: string;
  protocol: string;
  url: string;
  status: 'idle' | 'connecting' | 'streaming' | 'reconnecting' | 'error';
  kbps: number;
  enabled: boolean;
  uptime: string;
  error?: string;
  updated_at: string;
  // T17 enhanced fields (may be missing from older backend)
  transcode_status?: string;
  transcode_resolution?: string;
  audio_codec?: string;
  temperature_c?: number;
  restart_count?: number;
  av_drift_ms?: number;
}

export interface PushStatusResponse {
  camera_id: string;
  targets: PushTargetStatus[];
}

/** Relay system capabilities (from GET /api/relay/capabilities). */
export interface RelayCapabilities {
  ffmpeg_relay_supported: boolean;
  ffmpeg_available: boolean;
  max_targets_per_camera: number;
}

/** Fetch relay system capabilities (FFmpeg availability, limits). */
export async function getRelayCapabilities(signal?: AbortSignal): Promise<RelayCapabilities> {
  return apiRequest<RelayCapabilities>('/relay/capabilities', { signal });
}

export interface CreateCameraRequest {
  name: string;
  protocol: string;
  encoding?: string;
  url?: string;
  username?: string;
  password?: string;
  description?: string;
  location?: string;
  brand?: string;
  model?: string;
  serial_number?: string;
  onvif_endpoint?: string;
  profile_token?: string;
  stream_encoding?: string;
  transcoding?: CameraTranscodingConfig;
  channel?: string;
  // Recording gate: false = live-only (no segments written). Omit = record.
  recording_enabled?: boolean | null;
  // Cascade gate: false = hidden from the GB28181 cascade catalog. Omit = exposed.
  cascade_enabled?: boolean | null;
  // Recording mode (#435). Omit = continuous.
  recording_mode?: string;
  adaptive?: AdaptiveRecordingConfig;
  // Loudness trigger for adaptive recording (#478); only effective with
  // recording_mode 'adaptive'.
  audio_trigger?: CameraAudioTriggerConfig;
  // Push/ingest fields (SRT/RTMP)
  stream_key?: string;
  srt_passphrase?: string;
  srt_stream_id?: string;
  // Push-out relay
  push_targets?: PushTargetConfig[];
  push_retention_days?: number | null;
  // IP self-healing: ONVIF serial (sent at add time so the camera is immediately
  // self-healable after IP changes, without waiting for async ensureStableID).
  stable_id?: string;
  subnet_hints?: string[];
  // Xiaomi two-way audio
  two_way_audio_enabled?: boolean;
  // GB28181 SIP-registered device/channel binding
  gb28181?: {
    device_id: string;
    channel_id: string;
    manufacturer?: string;
  };
}

export interface UpdateCameraRequest {
  name?: string;
  url?: string;
  protocol?: string;
  encoding?: string;
  username?: string;
  password?: string;
  description?: string;
  location?: string;
  brand?: string;
  model?: string;
  serial_number?: string;
  retention_days?: number;
  onvif_endpoint?: string;
  profile_token?: string;
  stream_encoding?: string;
  transcoding?: CameraTranscodingConfig;
  channel?: string;
  // Recording gate: false = live-only (no segments written). Omit = unchanged.
  recording_enabled?: boolean | null;
  // Cascade gate: false = hidden from the GB28181 cascade catalog. Omit = unchanged.
  cascade_enabled?: boolean | null;
  // Recording mode (#435). Omit = unchanged. Changing it restarts the recorder.
  recording_mode?: string;
  adaptive?: AdaptiveRecordingConfig;
  // Loudness trigger for adaptive recording (#478); only effective with
  // recording_mode 'adaptive'.
  audio_trigger?: CameraAudioTriggerConfig;
  // Push/ingest fields (SRT/RTMP)
  stream_key?: string;
  srt_passphrase?: string;
  srt_stream_id?: string;
  // Push-out relay (replace whole list when set)
  push_targets?: PushTargetConfig[];
  push_retention_days?: number | null;
  // Xiaomi two-way audio
  // Xiaomi two-way audio
  two_way_audio_enabled?: boolean;
  // GB28181 SIP-registered device/channel binding
  gb28181?: {
    device_id: string;
    channel_id: string;
    manufacturer?: string;
  };
}

export interface DiscoveredDevice {
  uuid: string;
  name: string;
  xaddrs: string[];
  scopes: string[];
  hardware: string;
  endpoint: string;
  // Enriched via GetDeviceInformation (backend). Previously these were displayed
  // in the discovery list but discarded on add — now sent in the create payload
  // so the camera is added metadata-complete.
  manufacturer?: string;
  model?: string;
  firmware?: string;
  // ONVIF serial number. Sent as stable_id on add so IP self-healing (re-acquire
  // by serial after IP change) works immediately, without waiting for the async
  // ensureStableID goroutine that runs after the recorder connects.
  serial?: string;
}

export interface DiscoveryError {
  category: 'NETWORK' | 'TIMEOUT' | 'NO_DEVICES' | 'PARSE_ERROR';
  message: string;
}

export interface DeviceInfo {
  manufacturer: string;
  model: string;
  firmware: string;
  serial_number: string;
  hardware_id: string;
}

export interface DiscoveryResult {
  devices: DiscoveredDevice[];
  error: DiscoveryError | null;
}

export interface PTZMoveRequest {
  mode: 'continuous' | 'absolute' | 'relative';
  pan: number;
  tilt: number;
  zoom: number;
}

export interface ProtocolCapabilities {
  hls: boolean;
  ptz: boolean;
  snapshot: boolean;
  discovery: boolean;
  auth: boolean;
}

export interface ProtocolInfo {
  id: string;
  label: string;
  encodings: string[];
  builtIn: boolean;
  capabilities: ProtocolCapabilities;
}

// Hardcoded fallback if API is unreachable
export const DEFAULT_PROTOCOLS: ProtocolInfo[] = [
  {
    id: 'rtsp',
    label: 'RTSP',
    encodings: ['h264', 'h265', 'mjpeg'],
    builtIn: true,
    capabilities: { hls: true, ptz: false, snapshot: false, discovery: false, auth: true },
  },
  {
    id: 'http',
    label: 'HTTP',
    encodings: ['jpeg'],
    builtIn: true,
    capabilities: { hls: false, ptz: false, snapshot: true, discovery: false, auth: true },
  },
  {
    id: 'onvif',
    label: 'ONVIF',
    encodings: ['h264', 'h265', 'mjpeg'],
    builtIn: true,
    capabilities: { hls: true, ptz: true, snapshot: false, discovery: true, auth: true },
  },
  {
    id: 'xiaomi',
    label: 'Xiaomi',
    encodings: ['h264', 'h265'],
    builtIn: true,
    // Xiaomi cameras authenticate via Xiaomi cloud account token (configured in
    // Settings), NOT per-camera username/password. Hiding the credential fields
    // avoids the misconception that they apply (issue #68-1).
    capabilities: { hls: true, ptz: false, snapshot: false, discovery: true, auth: false },
  },
  {
    id: 'whip',
    label: 'WHIP (WebRTC push)',
    encodings: ['h264'],
    builtIn: true,
    capabilities: { hls: true, ptz: false, snapshot: false, discovery: false, auth: false },
  },
  {
    id: 'rtmp',
    label: 'RTMP',
    encodings: ['h264'],
    builtIn: true,
    capabilities: { hls: true, ptz: false, snapshot: false, discovery: false, auth: false },
  },
  {
    id: 'srt',
    label: 'SRT',
    encodings: ['h264', 'h265'],
    builtIn: true,
    capabilities: { hls: true, ptz: false, snapshot: false, discovery: false, auth: false },
  },
  {
    id: 'gb28181',
    label: 'GB28181',
    encodings: ['h264', 'h265'],
    builtIn: true,
    // GB28181 devices register via SIP and push RTP media; the NVR invites
    // channels by DeviceID/ChannelID. No per-camera credentials (SIP digest
    // auth is configured server-side in Settings) and no discovery. PTZ goes
    // through /api/cameras/{id}/ptz/* which the backend routes to the
    // GB/T 28181 DeviceControl transport for gb28181 cameras.
    capabilities: { hls: true, ptz: true, snapshot: false, discovery: false, auth: false },
  },
]

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
  const data = (await resp.json()) as Camera[];
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
  const data = (await resp.json()) as CameraSummary[];
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

// --- PTZ ---

export async function ptzMove(
  cameraId: string,
  request: PTZMoveRequest,
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/ptz/move`, {
    method: 'POST',
    body: JSON.stringify(request),
    signal,
  });
}

export async function ptzStop(cameraId: string, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/ptz/stop`, {
    method: 'POST',
    signal,
  });
}

// --- ONVIF Discovery ---

export async function discoverONVIFDevices(timeout: number = 5, signal?: AbortSignal): Promise<DiscoveryResult> {
  const result = await apiRequest<DiscoveryResult>('/onvif/discover', {
    method: 'POST',
    body: JSON.stringify({ timeout }),
    signal,
  });
  return {
    devices: result.devices || [],
    error: result.error || null,
  };
}

export async function getONVIFDeviceDetail(ip: string, signal?: AbortSignal): Promise<ONVIFDeviceDetail> {
  return apiRequest<ONVIFDeviceDetail>(`/onvif/discover/${ip}`, { signal });
}

export async function probeONVIFDevice(
  host: string,
  port: number = 80,
  signal?: AbortSignal,
): Promise<DiscoveredDevice | null> {
  const result = await apiRequest<{ device: DiscoveredDevice | null }>('/onvif/probe', {
    method: 'POST',
    body: JSON.stringify({ host, port }),
    signal,
  });
  return result.device;
}

// --- Protocols ---

export async function listProtocols(signal?: AbortSignal): Promise<ProtocolInfo[]> {
  const response = await apiRequest<{ protocols: ProtocolInfo[] }>('/protocols', { signal });
  return response.protocols;
}

// A single streaming protocol entry as returned by GET /api/cameras/{id}/protocols.
// `Protocol` is the backend handler name (webrtc/flv/ll-hls/hls/wasm/mjpeg);
// `Available` is false when the handler recognizes the codec but can't serve it
// (e.g. WebRTC for H.265) — in which case `Reason` explains why.
export interface CameraProtocolDetail {
  Protocol: string;
  Available: boolean;
  Reason: string;
}

// Sub-stream capability block in the protocols response (#512/#513): where a
// lower-resolution secondary feed exists. `codec` is the puller's observed
// codec once a sub pull has come up (may differ from the main stream's).
export interface CameraSubStreamDetail {
  available: boolean;
  source?: string;
  reason?: string;
  codec?: string;
}

// Response of GET /api/cameras/{id}/protocols — codec-aware per-camera protocol
// ranking. The backend probes the RUNNING recorder for the real codec (correcting
// ONVIF cameras that lie), then asks each registered stream handler CanHandle(codec).
// `default` is the latency-optimal available protocol (webrtc→flv→ll-hls→hls→mjpeg).
export interface CameraProtocolsResponse {
  protocols: CameraProtocolDetail[];
  encoding: string;
  default: string;
  sub_stream?: CameraSubStreamDetail;
}

// Fetch the available streaming protocols for a specific camera. The grid uses
// this (instead of the global /protocols list) to pick the best playback mode
// per camera, accounting for codec and handler availability.
export async function getCameraProtocols(cameraId: string, signal?: AbortSignal): Promise<CameraProtocolsResponse> {
  return apiRequest<CameraProtocolsResponse>(`/cameras/${cameraId}/protocols`, { signal });
}

// Normalize legacy combined protocol names (rtsp_h264, etc.) to base protocol ID
export function normalizeProtocol(protocol: string): string {
  if (protocol === 'rtsp_h264' || protocol === 'rtsp_h265' || protocol === 'rtsp_mjpeg') return 'rtsp';
  if (protocol === 'http_jpeg') return 'http';
  return protocol;
}

// Build a lookup map from protocol list
export function buildProtocolsMap(protocols: ProtocolInfo[]): Map<string, ProtocolInfo> {
  const map = new Map<string, ProtocolInfo>();
  for (const p of protocols) {
    map.set(p.id, p);
  }
  return map;
}

// Get capabilities for a protocol, handling legacy protocol names
export function getProtocolCapabilities(
  protocol: string,
  protocolsMap: Map<string, ProtocolInfo>,
): ProtocolCapabilities {
  const baseId = normalizeProtocol(protocol);
  const info = protocolsMap.get(baseId);
  if (info) return info.capabilities;
  // Fall back to the built-in catalog. The map is populated asynchronously
  // from GET /api/protocols and is EMPTY until that resolves (and on fetch
  // failure) — returning all-false raced the LiveView's orchestrator
  // registration into isHlsCapable=false → empty chain → "live not
  // supported" even though the camera streams fine (the Surveillance grid
  // registers only after the map loads, so only LiveView hit the race).
  const fallback = DEFAULT_PROTOCOLS.find((p) => p.id === baseId);
  if (fallback) return fallback.capabilities;
  return { hls: false, ptz: false, snapshot: false, discovery: false, auth: false };
}

// --- Xiaomi Vendor Check ---

export interface VendorCheckResult {
  vendor: string;
  compatible: boolean;
  message?: string;
}

export async function checkVendor(did: string): Promise<VendorCheckResult> {
  return apiRequest<VendorCheckResult>(`/xiaomi/check-vendor?did=${encodeURIComponent(did)}`);
}

// --- Imaging ---

export interface ImagingSettings {
  brightness?: number;
  contrast?: number;
  saturation?: number;
  sharpness?: number;
  exposure?: {
    mode: string;
    exposure_time?: number;
    gain?: number;
  };
  white_balance?: {
    mode: string;
    color_temperature?: number;
  };
}

export interface ImagingOptionRange {
  min: number;
  max: number;
}

export interface ImagingOptions {
  brightness?: ImagingOptionRange;
  contrast?: ImagingOptionRange;
  saturation?: ImagingOptionRange;
  sharpness?: ImagingOptionRange;
  exposure_time?: ImagingOptionRange;
  gain?: ImagingOptionRange;
  color_temperature?: ImagingOptionRange;
}

export async function getImagingSettings(cameraId: string, signal?: AbortSignal): Promise<ImagingSettings> {
  return apiRequest<ImagingSettings>(`/cameras/${cameraId}/imaging/settings`, { signal });
}

export async function setImagingSettings(
  cameraId: string,
  settings: Partial<ImagingSettings>,
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/imaging/settings`, {
    method: 'PUT',
    body: JSON.stringify(settings),
    signal,
  });
}

export async function getImagingOptions(cameraId: string, signal?: AbortSignal): Promise<ImagingOptions> {
  return apiRequest<ImagingOptions>(`/cameras/${cameraId}/imaging/options`, { signal });
}

// --- PTZ Presets ---

export interface PTZPreset {
  token: string;
  name: string;
}

export async function getPTZPresets(cameraId: string, signal?: AbortSignal): Promise<PTZPreset[]> {
  // The handler wraps the list: {"presets": [...]} — unwrap to the array the
  // consumers iterate (a raw object made the preset dropdown render empty).
  const resp = await apiRequest<{ presets?: PTZPreset[] } | PTZPreset[]>(
    `/cameras/${cameraId}/ptz/presets`,
    { signal },
  );
  return Array.isArray(resp) ? resp : (resp.presets ?? []);
}

export async function createPTZPreset(cameraId: string, name: string, signal?: AbortSignal): Promise<PTZPreset> {
  return apiRequest<PTZPreset>(`/cameras/${cameraId}/ptz/presets`, {
    method: 'POST',
    body: JSON.stringify({ name }),
    signal,
  });
}

export async function goToPTZPreset(
  cameraId: string,
  token: string,
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/ptz/presets/${encodeURIComponent(token)}/goto`, {
    method: 'POST',
    signal,
  });
}

export async function deletePTZPreset(
  cameraId: string,
  token: string,
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/ptz/presets/${encodeURIComponent(token)}`, {
    method: 'DELETE',
    signal,
  });
}

// --- Snapshot URI ---

export interface SnapshotUriResponse {
  uri: string;
}

export async function getSnapshotUri(cameraId: string, signal?: AbortSignal): Promise<SnapshotUriResponse> {
  return apiRequest<SnapshotUriResponse>(`/cameras/${cameraId}/snapshot/uri`, { signal });
}

// --- Device Capabilities ---

export interface DeviceCapabilitiesInfo {
  ptz: boolean;
  imaging: boolean;
  events: boolean;
  snapshot: boolean;
  streaming: boolean;
  device_info?: {
    manufacturer?: string;
    model?: string;
    firmware?: string;
    serial_number?: string;
    hardware_id?: string;
  };
}

export async function getDeviceCapabilities(cameraId: string, signal?: AbortSignal): Promise<DeviceCapabilitiesInfo> {
  return apiRequest<DeviceCapabilitiesInfo>(`/cameras/${cameraId}/onvif/capabilities`, { signal });
}

// --- Device Management ---

export interface NetworkIPv4 {
  enabled: boolean;
  dhcp: boolean;
  address?: string;
  netmask?: string;
  gateway?: string;
}

export interface NetworkIPv6 {
  enabled: boolean;
  dhcp: boolean;
  address?: string;
  prefix?: number;
  gateway?: string;
}

export interface NetworkNTP {
  manual?: string[];
  dhcp: boolean;
}

export interface NetworkInterface {
  name: string;
  enabled: boolean;
  ipv4: NetworkIPv4;
  ipv6?: NetworkIPv6;
  dns?: string[];
  ntp?: NetworkNTP;
}

export interface ONVIFDeviceUser {
  username: string;
  password?: string;
  level: string; // "Administrator", "Operator", "User", "Anonymous"
}

export async function rebootDevice(cameraId: string, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/onvif/reboot`, {
    method: 'POST',
    signal,
  });
}

export async function getNetworkInterfaces(
  cameraId: string,
  signal?: AbortSignal,
): Promise<{ interfaces: NetworkInterface[] }> {
  return apiRequest<{ interfaces: NetworkInterface[] }>(`/cameras/${cameraId}/onvif/network`, { signal });
}

export async function setNetworkInterfaces(
  cameraId: string,
  interfaces: NetworkInterface[],
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/onvif/network`, {
    method: 'PUT',
    body: JSON.stringify({ interfaces }),
    signal,
  });
}

export async function getDeviceUsers(cameraId: string, signal?: AbortSignal): Promise<{ users: ONVIFDeviceUser[] }> {
  return apiRequest<{ users: ONVIFDeviceUser[] }>(`/cameras/${cameraId}/onvif/users`, { signal });
}

export async function createDeviceUsers(
  cameraId: string,
  users: ONVIFDeviceUser[],
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/onvif/users`, {
    method: 'POST',
    body: JSON.stringify({ users }),
    signal,
  });
}

export async function deleteDeviceUsers(
  cameraId: string,
  usernames: string[],
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/onvif/users`, {
    method: 'DELETE',
    body: JSON.stringify({ usernames }),
    signal,
  });
}

// --- Per-camera timelapse config ---

export interface TimeRange {
  start: string;
  end: string;
}

export interface ScheduleConfig {
  time_ranges: TimeRange[];
  days_of_week: number[];
}

export interface TimelapseConfig {
  enabled: boolean;
  interval: string;
  frame_source: string;
  snapshot_url: string;
  schedule: ScheduleConfig | null;
  paused: boolean;
  delete_original: boolean;
  merge_enabled?: boolean;
  merge_mode?: string;
  daily_merge?: boolean;
  merge_output_fps?: number;
  merge_duration?: string;
}

export async function getTimelapseConfig(cameraId: string, signal?: AbortSignal): Promise<TimelapseConfig> {
  return apiRequest<TimelapseConfig>(`/cameras/${cameraId}/timelapse`, { signal });
}

export async function updateTimelapseConfig(
  cameraId: string,
  config: TimelapseConfig,
  signal?: AbortSignal,
): Promise<any> {
  return apiRequest(`/cameras/${cameraId}/timelapse`, {
    method: 'PUT',
    body: JSON.stringify(config),
    signal,
  });
}

// --- Xiaomi PTZ ---

export interface XiaomiPtzMoveRequest {
  direction: 'left' | 'right' | 'up' | 'down';
  speed: number;
}

export async function xiaomiPtzMove(
  cameraId: string,
  direction: string,
  speed: number,
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/xiaomi/ptz/move`, {
    method: 'POST',
    body: JSON.stringify({ direction, speed }),
    signal,
  });
}

export async function xiaomiPtzStop(cameraId: string, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/xiaomi/ptz/stop`, {
    method: 'POST',
    signal,
  });
}

// --- Xiaomi Device Info ---

export interface XiaomiDeviceInfo {
  firmware_version?: string;
  hardware_version?: string;
  model?: string;
  serial_number?: string;
  mac_address?: string;
  [key: string]: unknown;
}

export async function getXiaomiDeviceInfo(cameraId: string, signal?: AbortSignal): Promise<XiaomiDeviceInfo> {
  return apiRequest<XiaomiDeviceInfo>(`/cameras/${cameraId}/xiaomi/device-info`, { signal });
}

// --- Two-way Audio ---

export async function startTwoWayAudio(cameraId: string, signal?: AbortSignal): Promise<{ speaker_codec: number }> {
  return apiRequest<{ speaker_codec: number }>(`/cameras/${cameraId}/xiaomi/two-way-audio/start`, {
    method: 'POST',
    signal,
  });
}

export async function stopTwoWayAudio(cameraId: string, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/xiaomi/two-way-audio/stop`, {
    method: 'POST',
    signal,
  });
}

/** Return the WebSocket URL for two-way audio upstream PCM. */
export function getAudioUpstreamWS(cameraId: string): string {
  const base = API_BASE.replace(/\/api$/, '');
  return `${base}/api/ws/camera/${cameraId}/audio-upstream`;
}
