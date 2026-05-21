/**
 * Camera API — CRUD, ONVIF discovery, PTZ, protocols, per-camera merge config
 */
import { apiRequest, getAuthHeader, API_BASE } from './client';

// --- Types ---

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

export interface DiscoveredDevice {
  uuid: string;
  name: string;
  xaddrs: string[];
  scopes: string[];
  hardware: string;
  endpoint: string;
}

export interface DiscoveryError {
  category: 'NETWORK' | 'TIMEOUT' | 'NO_DEVICES' | 'PARSE_ERROR';
  message: string;
}

export interface DiscoveryResult {
  devices: DiscoveredDevice[];
  error: DiscoveryError | null;
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
    capabilities: { hls: true, ptz: false, snapshot: false, discovery: true, auth: true },
  },
  {
    id: 'rtmp',
    label: 'RTMP',
    encodings: ['h264'],
    builtIn: true,
    capabilities: { hls: false, ptz: false, snapshot: false, discovery: false, auth: false },
  },
  {
    id: 'srt',
    label: 'SRT',
    encodings: ['h264', 'h265'],
    builtIn: true,
    capabilities: { hls: false, ptz: false, snapshot: false, discovery: false, auth: false },
  },
];

// --- Camera CRUD ---

export async function listCameras(signal?: AbortSignal): Promise<Camera[]> {
  return apiRequest<Camera[]>('/cameras', { signal });
}

export async function createCamera(
  data: CreateCameraRequest,
  signal?: AbortSignal
): Promise<Camera> {
  return apiRequest<Camera>('/cameras', {
    method: 'POST',
    body: JSON.stringify(data),
    signal,
  });
}

export async function getCamera(id: string, signal?: AbortSignal): Promise<Camera> {
  return apiRequest<Camera>(`/cameras/${id}`, { signal });
}

export async function updateCamera(
  id: string,
  data: UpdateCameraRequest,
  signal?: AbortSignal
): Promise<Camera> {
  return apiRequest<Camera>(`/cameras/${id}`, {
    method: 'PUT',
    body: JSON.stringify(data),
    signal,
  });
}

export async function deleteCamera(id: string, signal?: AbortSignal): Promise<void> {
  return apiRequest<void>(`/cameras/${id}`, {
    method: 'DELETE',
    signal,
  });
}

export async function enableCamera(id: string, signal?: AbortSignal): Promise<Camera> {
  return updateCamera(id, { enabled: true }, signal);
}

export async function disableCamera(id: string, signal?: AbortSignal): Promise<Camera> {
  return updateCamera(id, { enabled: false }, signal);
}

export async function startCamera(
  id: string,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${id}/start`, {
    method: 'POST',
    signal,
  });
}

export async function stopCamera(
  id: string,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${id}/stop`, {
    method: 'POST',
    signal,
  });
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
}

export async function testConnection(
  data: TestConnectionRequest,
  signal?: AbortSignal
): Promise<TestConnectionResult> {
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
  enabled?: boolean;
  check_interval?: string;
  window_size?: string;
  batch_limit?: number;
  min_segment_age?: string;
  min_segments_to_merge?: number;
}

export async function getMergeConfig(
  cameraId: string,
  signal?: AbortSignal
): Promise<MergeConfig | null> {
  try {
    return await apiRequest<MergeConfig>(`/cameras/${cameraId}/merge-config`, { signal });
  } catch {
    return null;
  }
}

export async function updateMergeConfig(
  cameraId: string,
  config: MergeConfig,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/merge-config`, {
    method: 'PUT',
    body: JSON.stringify(config),
    signal,
  });
}

export async function deleteCameraMergeConfig(
  cameraId: string,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/merge-config`, {
    method: 'DELETE',
    signal,
  });
}

// --- PTZ ---

export async function ptzMove(
  cameraId: string,
  request: PTZMoveRequest,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/ptz/move`, {
    method: 'POST',
    body: JSON.stringify(request),
    signal,
  });
}

export async function ptzStop(
  cameraId: string,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/ptz/stop`, {
    method: 'POST',
    signal,
  });
}

export async function getPTZStatus(
  cameraId: string,
  signal?: AbortSignal
): Promise<PTZStatus> {
  return apiRequest<PTZStatus>(`/cameras/${cameraId}/ptz/status`, { signal });
}

// --- ONVIF Discovery ---

export async function discoverONVIFDevices(
  timeout: number = 5,
  signal?: AbortSignal
): Promise<DiscoveryResult> {
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

export async function getONVIFDeviceDetail(
  ip: string,
  signal?: AbortSignal
): Promise<ONVIFDeviceDetail> {
  return apiRequest<ONVIFDeviceDetail>(`/onvif/discover/${ip}`, { signal });
}

export async function probeONVIFDevice(
  host: string,
  port: number = 80,
  signal?: AbortSignal
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
  return { hls: false, ptz: false, snapshot: false, discovery: false, auth: false };
}
