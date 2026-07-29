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
  /** EMA smoothing factor for box positions (#183, 0.1–0.9, default 0.3). Optional for back-compat. */
  emaAlpha?: number;
  /** Max detection cycles a disappeared box lingers (#183, 3–30, default 15). Optional for back-compat. */
  maxAge?: number;
  /** Restrict detection to these COCO class labels (#184). null/undefined = all 80 classes. */
  enabledClasses?: string[] | null;
}

// ─── Constants ────────────────────────────────────────────────────────────────

const STORAGE_KEY = 'mibee_nvr_ai_settings';

const DEFAULTS: AiDetectionSettings = {
  enabled: false,
  confidenceThreshold: 0.5,
  frameSkip: 3,
  emaAlpha: 0.3,
  maxAge: 15,
  enabledClasses: null,
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
      emaAlpha: clampEmaAlpha(parsed.emaAlpha),
      maxAge: clampMaxAge(parsed.maxAge),
      // null/undefined/[] all mean "all classes"; only a non-empty array filters.
      enabledClasses: Array.isArray(parsed.enabledClasses) ? parsed.enabledClasses : null,
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
        emaAlpha: clampEmaAlpha(settings.emaAlpha),
        maxAge: clampMaxAge(settings.maxAge),
        enabledClasses: Array.isArray(settings.enabledClasses) ? settings.enabledClasses : null,
      }),
    );
  } catch (e) {
    console.error('Failed to save AI settings:', e);
  }
}

// ─── Single source of truth (#182) ───────────────────────────────────────────

/**
 * Resolve the authoritative AI detection settings.
 *
 * The backend YAML config (`GET /api/ai/status`) is the single source of truth.
 * localStorage is only an offline cache used when the API is unreachable (NVR
 * restarting, network down) so the player can still show *something* rather
 * than nothing. This fixes #182: previously the player read localStorage only
 * (`getAiSettings`), so editing mibee-nvr.yaml and restarting had no effect on
 * an already-open browser (its localStorage was stale), and different browsers
 * saw different AI behavior for the same NVR.
 *
 * On success the backend value is written back to localStorage so the next
 * offline fallback is fresh.
 */
export async function resolveAiSettings(): Promise<AiDetectionSettings> {
  try {
    const status = await getAiStatus();
    const settings: AiDetectionSettings = {
      enabled: status.enabled,
      confidenceThreshold: clampConfidence(status.confidence_threshold),
      frameSkip: clampFrameSkip(status.frame_skip_rate),
      emaAlpha: clampEmaAlpha(status.ema_alpha),
      maxAge: clampMaxAge(status.max_age),
      // Backend omits enabled_classes when unset (all classes). Treat null/empty
      // as "all classes" on the client too.
      enabledClasses:
        Array.isArray(status.enabled_classes) && status.enabled_classes.length > 0
          ? status.enabled_classes
          : null,
    };
    // Mirror into localStorage so a later offline path falls back to fresh data.
    saveAiSettings(settings);
    return settings;
  } catch {
    // API unreachable (offline / NVR mid-restart): fall back to the cached copy.
    return getAiSettings();
  }
}

// ─── Validation helpers ───────────────────────────────────────────────────────

function clampConfidence(value: number): number {
  if (typeof value !== 'number' || isNaN(value)) return DEFAULTS.confidenceThreshold;
  // Upper bound 0.99 (was 0.9): YOLOv11-nano on complex scenes produces
  // background false positives in the 0.85–0.92 range; pushing the threshold
  // to 0.95 filters most of them while keeping real persons (>0.93). Two
  // decimal places so 0.95 is representable (the old 1-decimal rounding turned
  // 0.95 into 0.9 or 1.0).
  return Math.round(Math.min(0.99, Math.max(0.1, value)) * 100) / 100;
}

function clampFrameSkip(value: number): number {
  if (typeof value !== 'number' || isNaN(value)) return DEFAULTS.frameSkip;
  return Math.min(10, Math.max(1, Math.round(value)));
}

function clampEmaAlpha(value: number | undefined): number {
  if (typeof value !== 'number' || isNaN(value) || value <= 0) return DEFAULTS.emaAlpha!;
  return Math.round(Math.min(0.9, Math.max(0.1, value)) * 10) / 10;
}

function clampMaxAge(value: number | undefined): number {
  if (typeof value !== 'number' || isNaN(value) || value <= 0) return DEFAULTS.maxAge!;
  return Math.min(30, Math.max(3, Math.round(value)));
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

// ─── Zone management (server-side) ────────────────────────────────────────────

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

export async function getAIZones(): Promise<ZoneList> {
  return apiRequest<ZoneList>('/ai/zones');
}

export async function createAIZone(zone: CreateZoneRequest): Promise<Zone> {
  return apiRequest<Zone>('/ai/zones', {
    method: 'POST',
    body: JSON.stringify(zone),
  });
}

export async function deleteAIZone(id: string): Promise<void> {
  return apiRequest<void>(`/ai/zones/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  });
}
// ─── Backend API types ────────────────────────────────────────────────────

export interface AiStatus {
  enabled: boolean;
  model_url: string;
  confidence_threshold: number;
  frame_skip_rate: number;
  ema_alpha?: number;
  max_age?: number;
  enabled_classes?: string[] | null;
}

export interface AiConfigUpdate {
  enabled?: boolean;
  confidence_threshold?: number;
  frame_skip_rate?: number;
  model_url?: string;
  ema_alpha?: number;
  max_age?: number;
  enabled_classes?: string[] | null;
}

export interface AiModelInfo {
  name: string;
  url: string;
  size: number;
}

// ─── Backend API functions ────────────────────────────────────────────────

export async function getAiStatus(): Promise<AiStatus> {
  return apiRequest<AiStatus>('/ai/status');
}

export async function updateAiConfig(config: AiConfigUpdate): Promise<void> {
  return apiRequest<void>('/ai/config', {
    method: 'PUT',
    body: JSON.stringify(config),
  });
}

/** List selectable ONNX models under {storage_root}/models/ (#185). */
export async function listAiModels(): Promise<AiModelInfo[]> {
  const resp = await apiRequest<{ models: AiModelInfo[] }>('/ai/models');
  return resp.models ?? [];
}

// ─── Per-camera localStorage ──────────────────────────────────────────────────

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
