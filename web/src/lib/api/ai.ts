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
