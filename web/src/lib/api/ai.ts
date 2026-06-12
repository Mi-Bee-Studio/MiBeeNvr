import { apiRequest } from './client';

/**
 * AI Detection Settings — localStorage-backed persistence
 *
 * Stores per-browser AI detection preferences (enable, confidence, frame skip).
 * These are client-side only — no backend API calls.
 */

// ─── Types ────────────────────────────────────────────────────────────────────

export interface AiDetectionSettings {
  /** Whether AI detection is active in live view */
  enabled: boolean;
  /** Confidence threshold for filtering detections (0.1–0.9, default 0.5) */
  confidenceThreshold: number;
  /** Detect every N frames (1–10, default 3) */
  frameSkip: number;
}

// ─── Constants ────────────────────────────────────────────────────────────────

const STORAGE_KEY = 'mibee_nvr_ai_settings';

const DEFAULTS: AiDetectionSettings = {
  enabled: false,
  confidenceThreshold: 0.5,
  frameSkip: 3,
};

// ─── Persistence ──────────────────────────────────────────────────────────────

/**
 * Load AI detection settings from localStorage.
 * Returns defaults if nothing stored or on parse error.
 */
export function getAiSettings(): AiDetectionSettings {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return { ...DEFAULTS };
    const parsed = JSON.parse(raw);
    return {
      enabled: typeof parsed.enabled === 'boolean' ? parsed.enabled : DEFAULTS.enabled,
      confidenceThreshold: clampConfidence(parsed.confidenceThreshold),
      frameSkip: clampFrameSkip(parsed.frameSkip),
    };
  } catch {
    return { ...DEFAULTS };
  }
}

/**
 * Save AI detection settings to localStorage.
 */
export function saveAiSettings(settings: AiDetectionSettings): void {
  try {
    localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({
        enabled: settings.enabled,
        confidenceThreshold: clampConfidence(settings.confidenceThreshold),
        frameSkip: clampFrameSkip(settings.frameSkip),
      }),
    );
  } catch (e) {
    console.error('Failed to save AI settings:', e);
  }
}

// ─── Validation helpers ───────────────────────────────────────────────────────

function clampConfidence(value: number): number {
  if (typeof value !== 'number' || isNaN(value)) return DEFAULTS.confidenceThreshold;
  return Math.round(Math.min(0.9, Math.max(0.1, value)) * 10) / 10;
}

function clampFrameSkip(value: number): number {
  if (typeof value !== 'number' || isNaN(value)) return DEFAULTS.frameSkip;
  return Math.min(10, Math.max(1, Math.round(value)));
}

// ─── Backend detection ────────────────────────────────────────────────────────

/**
 * Detect the best available AI inference backend.
 * Returns 'WebGPU' if available, otherwise 'WASM SIMD'.
 */
export function detectAiBackend(): string {
  try {
    if (typeof navigator !== 'undefined' && (navigator as any).gpu !== undefined) {
      return 'WebGPU';
    }
  } catch {
    // navigator not available
  }
  return 'WASM SIMD';
}

// ─── Backend API types ─────────────────────────────────────────────────

export interface AIConfigUpdate {
  enabled?: boolean;
  model_path?: string;
  confidence_threshold?: number;
  frame_skip_rate?: number;
  inference_timeout_ms?: number;
  enabled_cameras?: string[];
  max_goroutines?: number;
  camera_configs?: Record<string, CameraAIConfig>;
}

export interface CameraAIConfig {
  confidence_threshold?: number;
  frame_skip_rate?: number;
}

export interface Zone {
  camera_id: string;
  name: string;
  points: number[][];
  enabled: boolean;
}

export interface ZoneList {
  zones: Zone[];
}

export interface CreateZoneRequest {
  camera_id: string;
  name: string;
  points: number[][];
  enabled: boolean;
}

// ─── Per-camera localStorage ───────────────────────────────────────────

const PER_CAMERA_STORAGE_KEY = 'mibee_nvr_per_camera_ai';

export interface PerCameraAiState {
  [cameraId: string]: {
    enabled: boolean;
    confidenceThreshold: number;
    frameSkip: number;
  };
}

export function getPerCameraAiSettings(): PerCameraAiState {
  try {
    const raw = localStorage.getItem(PER_CAMERA_STORAGE_KEY);
    if (!raw) return {};
    return JSON.parse(raw);
  } catch {
    return {};
  }
}

export function savePerCameraAiSettings(settings: PerCameraAiState): void {
  try {
    localStorage.setItem(PER_CAMERA_STORAGE_KEY, JSON.stringify(settings));
  } catch (e) {
    console.error('Failed to save per-camera AI settings:', e);
  }
}

// ─── Backend API calls ─────────────────────────────────────────────────

/**
 * Update AI configuration (partial update via PUT /api/ai/config)
 */
export async function updateAIConfig(config: AIConfigUpdate): Promise<void> {
  return apiRequest<void>('/ai/config', {
    method: 'PUT',
    body: JSON.stringify(config),
  });
}

/**
 * Get all ROI zones
 */
export async function getAIZones(): Promise<ZoneList> {
  return apiRequest<ZoneList>('/ai/zones');
}

/**
 * Create a new ROI zone
 */
export async function createAIZone(zone: CreateZoneRequest): Promise<Zone> {
  return apiRequest<Zone>('/ai/zones', {
    method: 'POST',
    body: JSON.stringify(zone),
  });
}

/**
 * Delete an ROI zone by name (ID)
 */
export async function deleteAIZone(id: string): Promise<void> {
  return apiRequest<void>(`/ai/zones/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  });
}

// ─── Server-side AI Status API ────────────────────────────────────────────────

export interface AIStatus {
  enabled: boolean;
  ncnn_available: boolean;
  model_name: string;
  active_cameras: number;
  cameras: Record<string, AICameraStatus>;
}

export interface AICameraStatus {
  running: boolean;
  fps: number;
  detections: number;
}

export interface DetectionList {
  camera_id: string;
  detections: DetectionEvent[];
  total: number;
}

export interface DetectionEvent {
  timestamp: string;
  detections: DetectionObj[];
  source: string;
}

export interface DetectionObj {
  class_id: number;
  class_name: string;
  confidence: number;
  bbox: number[];
}

/** Fetch AI global status with per-camera info */
export async function getAIStatus(): Promise<AIStatus> {
  return apiRequest<AIStatus>('/ai/status');
}

/** Fetch recent detections for a camera */
export async function getAIDetections(cameraID: string, params?: { limit?: number }): Promise<DetectionList> {
  const query = params ? '?' + new URLSearchParams({ limit: String(params.limit ?? 10) }).toString() : '';
  return apiRequest<DetectionList>(`/ai/detections/${encodeURIComponent(cameraID)}${query}`);
}
