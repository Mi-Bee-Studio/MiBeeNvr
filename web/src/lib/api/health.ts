/**
 * Health API — camera health status, health events, per-camera health
 */
import { apiRequest } from './client';

// --- Types ---

// Health status values
export type HealthStatus = 'healthy' | 'warning' | 'error' | 'unknown';

// Health event type values
export type HealthEventType =
  | 'connection_lost'
  | 'connection_restored'
  | 'stream_anomaly'
  | 'freeze_detected'
  | 'freeze_recovered';

// A single health event
export interface HealthEvent {
  id: number;
  camera_id: string;
  event_type: HealthEventType;
  status: HealthStatus;
  message?: string;
  metadata?: Record<string, unknown>;
  created_at: string;
}

// Camera health status
export interface CameraHealth {
  camera_id: string;
  status: HealthStatus;
  last_event?: HealthEvent;
  updated_at: string;
}

// API response for health status
export interface HealthStatusResponse {
  [cameraId: string]: CameraHealth;
}

// API response for health events
export interface HealthEventsResponse {
  events: HealthEvent[];
  total: number;
  limit: number;
  offset: number;
}

// Query parameters for health events
export interface HealthEventsParams {
  camera_id?: string;
  event_type?: HealthEventType;
  since?: string;
  limit?: number;
  offset?: number;
}

// --- API functions ---

// Fetch health status for all cameras
export async function getHealthStatus(): Promise<HealthStatusResponse> {
  return apiRequest<HealthStatusResponse>('/health/status');
}

// Fetch health events with optional filters
export async function getHealthEvents(params?: HealthEventsParams): Promise<HealthEventsResponse> {
  const query = new URLSearchParams();
  if (params?.camera_id) query.set('camera_id', params.camera_id);
  if (params?.event_type) query.set('event_type', params.event_type);
  if (params?.since) query.set('since', params.since);
  if (params?.limit !== undefined) query.set('limit', String(params.limit));
  if (params?.offset !== undefined) query.set('offset', String(params.offset));

  const qs = query.toString();
  const endpoint = qs ? `/health/events?${qs}` : '/health/events';
  return apiRequest<HealthEventsResponse>(endpoint);
}

// Fetch health status for a single camera
export async function getCameraHealth(cameraId: string): Promise<CameraHealth> {
  return apiRequest<CameraHealth>(`/cameras/${cameraId}/health`);
}

// Camera health detail with score
export interface CameraHealthDetail {
  camera_id: string;
  latest_status: string;
  score: number;
  score_factors?: Record<string, number>;
}

// Health cameras response (map of camera ID to detail)
export interface HealthCamerasResponse {
  [cameraId: string]: CameraHealthDetail;
}

// Per-camera stability metrics
export interface StabilityMetrics {
  uptime_percent: number;
  total_failures: number;
  mtbf: string;
  avg_session: string;
  trend: string;
  current_status: string;
}

// Stability data response (map of camera ID to metrics)
export interface StabilityDataResponse {
  cameras: { [cameraId: string]: StabilityMetrics };
}

// Fetch health cameras (public, no auth required)
export async function getHealthCameras(): Promise<HealthCamerasResponse> {
  const response = await fetch('/api/health/cameras');
  if (!response.ok) {
    throw new Error(`HTTP ${response.status}`);
  }
  return response.json();
}

// Fetch stability data (auth required)
export async function getStabilityData(): Promise<StabilityDataResponse> {
  return apiRequest<StabilityDataResponse>('/health/stability');
}
