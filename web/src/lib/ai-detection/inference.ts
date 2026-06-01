/**
 * YOLOv11-nano Inference Pipeline — ObjectDetector
 *
 * Preprocesses VideoFrame → 640×640 Float32 tensor → runs ONNX inference →
 * parses YOLO output → applies NMS → returns Detection[] with EMA smoothing.
 *
 * Frame skipping: configurable (default every 3 frames → ~10 FPS at 30 FPS video).
 */

import type { AiRuntime } from './runtime';

// ─── Types ────────────────────────────────────────────────────────────────────

/** A single object detection result. */
export interface Detection {
  /** Bounding box: [x1, y1, x2, y2] in pixel coordinates (original frame). */
  bbox: [number, number, number, number];
  /** Confidence score [0, 1]. */
  confidence: number;
  /** COCO class ID (0–79). */
  classId: number;
  /** COCO class label. */
  label: string;
}

/** Internal raw detection before NMS. */
interface RawDetection {
  x1: number;
  y1: number;
  x2: number;
  y2: number;
  score: number;
  classId: number;
}

/** EMA-tracked detection state. */
interface SmoothedDetection {
  bbox: [number, number, number, number];
  score: number;
  classId: number;
  age: number;
}

/** ObjectDetector configuration. */
export interface ObjectDetectorOptions {
  /** Confidence threshold for filtering detections (default: 0.5). */
  confidenceThreshold?: number;
  /** NMS IoU threshold (default: 0.45). */
  nmsThreshold?: number;
  /** Detect every N frames (default: 3). */
  frameSkip?: number;
  /** EMA smoothing factor: higher = more responsive, lower = smoother (default: 0.3). */
  emaAlpha?: number;
  /** Max age (in skipped frames) before a smoothed detection is removed (default: 15). */
  maxAge?: number;
  /** Input size for YOLO model (default: 640). */
  inputSize?: number;
  /** Number of COCO classes (default: 80). */
  numClasses?: number;
  /** Number of anchor boxes in YOLO output (default: 8400). */
  numBoxes?: number;
}

// ─── Constants ────────────────────────────────────────────────────────────────

const INPUT_SIZE = 640;
const NUM_CLASSES = 80;
const NUM_BOXES = 8400;
const CONFIDENCE_THRESHOLD = 0.5;
const NMS_THRESHOLD = 0.45;
const FRAME_SKIP = 3;
const EMA_ALPHA = 0.3;
const MAX_AGE = 15;
const LETTERBOX_COLOR = 114; // Gray padding (0–255)

/** COCO 80-class labels. */
const COCO_CLASSES: string[] = [
  'person', 'bicycle', 'car', 'motorcycle', 'airplane', 'bus', 'train', 'truck', 'boat',
  'traffic light', 'fire hydrant', 'stop sign', 'parking meter', 'bench', 'bird', 'cat',
  'dog', 'horse', 'sheep', 'cow', 'elephant', 'bear', 'zebra', 'giraffe', 'backpack',
  'umbrella', 'handbag', 'tie', 'suitcase', 'frisbee', 'skis', 'snowboard', 'sports ball',
  'kite', 'baseball bat', 'baseball glove', 'skateboard', 'surfboard', 'tennis racket',
  'bottle', 'wine glass', 'cup', 'fork', 'knife', 'spoon', 'bowl', 'banana', 'apple',
  'sandwich', 'orange', 'broccoli', 'carrot', 'hot dog', 'pizza', 'donut', 'cake', 'chair',
  'couch', 'potted plant', 'bed', 'dining table', 'toilet', 'tv', 'laptop', 'mouse',
  'remote', 'keyboard', 'cell phone', 'microwave', 'oven', 'toaster', 'sink', 'refrigerator',
  'book', 'clock', 'vase', 'scissors', 'teddy bear', 'hair drier', 'toothbrush',
];

// ─── OffscreenCanvas helpers ──────────────────────────────────────────────────

/**
 * Get or create an OffscreenCanvas for preprocessing.
 * Reuses a single canvas to avoid leaks.
 */
function getCanvas(size: number): { canvas: OffscreenCanvas; ctx: OffscreenCanvasRenderingContext2D } {
  const canvas = new OffscreenCanvas(size, size);
  const ctx = canvas.getContext('2d');
  if (!ctx) {
    throw new Error('Failed to get 2D context from OffscreenCanvas');
  }
  return { canvas, ctx };
}

// ─── Preprocessing ───────────────────────────────────────────────────────────

/**
 * Letterbox dimensions: calculates scale and offsets to fit image into inputSize×inputSize.
 */
function letterboxParams(
  frameWidth: number,
  frameHeight: number,
  inputSize: number,
): { scale: number; padX: number; padY: number; newW: number; newH: number } {
  const scale = Math.min(inputSize / frameWidth, inputSize / frameHeight);
  const newW = Math.round(frameWidth * scale);
  const newH = Math.round(frameHeight * scale);
  const padX = (inputSize - newW) / 2;
  const padY = (inputSize - newH) / 2;
  return { scale, padX, padY, newW, newH };
}

/**
 * Preprocess a VideoFrame into a Float32 tensor [1, 3, inputSize, inputSize].
 *
 * Pipeline:
 * 1. createImageBitmap(videoFrame) → draw onto 640×640 canvas (letterbox)
 * 2. Get ImageData → extract RGBA pixels
 * 3. Convert to RGB float32 [0,1]
 * 4. Transpose HWC → CHW
 */
async function preprocessFrame(
  videoFrame: VideoFrame,
  inputSize: number,
): Promise<{ tensor: Float32Array; letterboxPadX: number; letterboxPadY: number; letterboxScale: number }> {
  // createImageBitmap for safe drawing
  const bitmap = await createImageBitmap(videoFrame);

  const { scale, padX, padY } = letterboxParams(
    bitmap.width,
    bitmap.height,
    inputSize,
  );

  const { canvas, ctx } = getCanvas(inputSize);

  // Fill with gray padding
  ctx.fillStyle = `rgb(${LETTERBOX_COLOR}, ${LETTERBOX_COLOR}, ${LETTERBOX_COLOR})`;
  ctx.fillRect(0, 0, inputSize, inputSize);

  // Draw scaled image
  const newW = Math.round(bitmap.width * scale);
  const newH = Math.round(bitmap.height * scale);
  ctx.drawImage(bitmap, Math.round(padX), Math.round(padY), newW, newH);
  bitmap.close();

  // Extract pixel data
  const imageData = ctx.getImageData(0, 0, inputSize, inputSize);
  const pixels = imageData.data; // RGBA, Uint8ClampedArray

  // Convert HWC RGBA → CHW RGB float32 [0,1]
  const channelSize = inputSize * inputSize;
  const tensor = new Float32Array(1 * 3 * channelSize);

  for (let y = 0; y < inputSize; y++) {
    for (let x = 0; x < inputSize; x++) {
      const srcIdx = (y * inputSize + x) * 4; // RGBA
      const dstIdx = y * inputSize + x;

      // CHW layout: tensor[c * channelSize + dstIdx]
      tensor[0 * channelSize + dstIdx] = pixels[srcIdx] / 255.0;     // R
      tensor[1 * channelSize + dstIdx] = pixels[srcIdx + 1] / 255.0; // G
      tensor[2 * channelSize + dstIdx] = pixels[srcIdx + 2] / 255.0; // B
    }
  }

  return { tensor, letterboxPadX: padX, letterboxPadY: padY, letterboxScale: scale };
}

// ─── YOLO Output Parsing ──────────────────────────────────────────────────────

/**
 * Parse YOLOv11 output tensor [1, 84, 8400] or [1, 4+numClasses, numBoxes].
 *
 * Extracts cx, cy, w, h from first 4 rows, class scores from remaining rows.
 * Applies sigmoid to scores, filters by confidence threshold, converts to x1,y1,x2,y2.
 *
 * Output layout (transposed from [1, 84, 8400]):
 * - For each box i (0..numBoxes-1):
 *   - offset = i * (4 + numClasses)  (data is row-major: [box][channel])
 *   - cx = data[offset], cy = data[offset+1], w = data[offset+2], h = data[offset+3]
 *   - scores = data[offset+4..offset+4+numClasses-1]
 */
function parseYoloOutput(
  data: Float32Array,
  dims: number[],
  numClasses: number,
  numBoxes: number,
  confidenceThreshold: number,
): RawDetection[] {
  const detections: RawDetection[] = [];

  // Determine layout from dims
  // YOLOv11 ONNX export: [1, 84, 8400] → transposed in data as [8400, 84] row-major
  // OR could be [1, 8400, 84] — check which axis is channels
  const channels = dims[1]; // 84 for COCO
  const boxes = dims[2];     // 8400 for YOLOv11-nano

  const boxStride = channels; // Each box has 'channels' values

  for (let i = 0; i < boxes; i++) {
    const offset = i * boxStride;

    // First 4: cx, cy, w, h
    const cx = data[offset];
    const cy = data[offset + 1];
    const w = data[offset + 2];
    const h = data[offset + 3];

    // Skip boxes with zero or negative dimensions
    if (w <= 0 || h <= 0) continue;

    // Find best class
    let maxScore = -Infinity;
    let maxClass = 0;
    for (let j = 0; j < numClasses; j++) {
      const rawScore = data[offset + 4 + j];
      // YOLOv11 may or may not use sigmoid — handle both
      const score = sigmoid(rawScore);
      if (score > maxScore) {
        maxScore = score;
        maxClass = j;
      }
    }

    if (maxScore < confidenceThreshold) continue;

    // Convert cx,cy,w,h → x1,y1,x2,y2 (in input space)
    const x1 = cx - w / 2;
    const y1 = cy - h / 2;
    const x2 = cx + w / 2;
    const y2 = cy + h / 2;

    detections.push({ x1, y1, x2, y2, score: maxScore, classId: maxClass });
  }

  return detections;
}

/** Fast sigmoid approximation. */
function sigmoid(x: number): number {
  return 1 / (1 + Math.exp(-x));
}

// ─── NMS ──────────────────────────────────────────────────────────────────────

/**
 * Non-Maximum Suppression: suppress overlapping boxes.
 *
 * Sort by confidence descending, iterate and suppress boxes with IoU > threshold.
 */
function nms(detections: RawDetection[], iouThreshold: number): RawDetection[] {
  if (detections.length === 0) return [];

  // Sort by score descending
  const sorted = [...detections].sort((a, b) => b.score - a.score);
  const kept: RawDetection[] = [];

  while (sorted.length > 0) {
    const best = sorted.shift()!;
    kept.push(best);

    // Remove boxes that overlap too much with best
    const remaining: RawDetection[] = [];
    for (const det of sorted) {
      if (iou(best, det) <= iouThreshold) {
        remaining.push(det);
      }
    }
    sorted.length = 0;
    sorted.push(...remaining);
  }

  return kept;
}

/** Intersection over Union between two boxes. */
function iou(a: RawDetection, b: RawDetection): number {
  const interX1 = Math.max(a.x1, b.x1);
  const interY1 = Math.max(a.y1, b.y1);
  const interX2 = Math.min(a.x2, b.x2);
  const interY2 = Math.min(a.y2, b.y2);

  const interW = Math.max(0, interX2 - interX1);
  const interH = Math.max(0, interY2 - interY1);
  const interArea = interW * interH;

  const areaA = (a.x2 - a.x1) * (a.y2 - a.y1);
  const areaB = (b.x2 - b.x1) * (b.y2 - b.y1);

  if (areaA === 0 || areaB === 0) return 0;

  return interArea / (areaA + areaB - interArea);
}

// ─── Coordinate Mapping ───────────────────────────────────────────────────────

/**
 * Map detections from input-space (640×640) back to original frame coordinates.
 * Reverses letterbox padding and scaling.
 */
function mapToOriginal(
  detections: RawDetection[],
  padX: number,
  padY: number,
  scale: number,
): RawDetection[] {
  return detections.map((det) => ({
    x1: (det.x1 - padX) / scale,
    y1: (det.y1 - padY) / scale,
    x2: (det.x2 - padX) / scale,
    y2: (det.y2 - padY) / scale,
    score: det.score,
    classId: det.classId,
  }));
}

// ─── ObjectDetector ──────────────────────────────────────────────────────────

/**
 * ObjectDetector — wraps AiRuntime with preprocessing, postprocessing,
 * NMS, EMA smoothing, and frame skipping.
 */
export class ObjectDetector {
  private _runtime: AiRuntime;
  private _confidenceThreshold: number;
  private _nmsThreshold: number;
  private _frameSkip: number;
  private _emaAlpha: number;
  private _maxAge: number;
  private _inputSize: number;
  private _numClasses: number;
  private _numBoxes: number;

  private _frameCount = 0;
  private _smoothedDetections: Map<string, SmoothedDetection> = new Map();
  private _disposed = false;

  /** Detection key generation for EMA matching. */
  private static detectionKey(classId: number, x1: number, y1: number): string {
    return `${classId}:${Math.round(x1)}:${Math.round(y1)}`;
  }

  constructor(runtime: AiRuntime, options?: ObjectDetectorOptions) {
    this._runtime = runtime;
    this._confidenceThreshold = options?.confidenceThreshold ?? CONFIDENCE_THRESHOLD;
    this._nmsThreshold = options?.nmsThreshold ?? NMS_THRESHOLD;
    this._frameSkip = options?.frameSkip ?? FRAME_SKIP;
    this._emaAlpha = options?.emaAlpha ?? EMA_ALPHA;
    this._maxAge = options?.maxAge ?? MAX_AGE;
    this._inputSize = options?.inputSize ?? INPUT_SIZE;
    this._numClasses = options?.numClasses ?? NUM_CLASSES;
    this._numBoxes = options?.numBoxes ?? NUM_BOXES;
  }

  /**
   * Run detection on a VideoFrame.
   *
   * - Skips frames based on frameSkip setting
   * - Preprocesses → runs inference → parses output → NMS → EMA smoothing
   * - Returns smoothed detections
   */
  async detect(videoFrame: VideoFrame): Promise<Detection[]> {
    if (this._disposed) return [];

    this._frameCount++;

    // Frame skipping: only detect every N frames
    if (this._frameCount % this._frameSkip !== 0) {
      return this.getSmoothedDetections();
    }

    try {
      // 1. Preprocess
      const { tensor, letterboxPadX, letterboxPadY, letterboxScale } =
        await preprocessFrame(videoFrame, this._inputSize);

      // 2. Run inference
      const dims = [1, 4 + this._numClasses, this._numBoxes];
      const results = await this._runtime.run(tensor, dims);

      // 3. Parse output — get first output tensor
      const outputKey = Object.keys(results)[0];
      const output = results[outputKey];

      // 4. Parse YOLO output
      let detections = parseYoloOutput(
        output.data,
        output.dims,
        this._numClasses,
        this._numBoxes,
        this._confidenceThreshold,
      );

      // 5. NMS
      detections = nms(detections, this._nmsThreshold);

      // 6. Map to original coordinates
      detections = mapToOriginal(detections, letterboxPadX, letterboxPadY, letterboxScale);

      // 7. EMA smoothing
      this.updateSmoothedDetections(detections);

      // Dispose output tensors
      if (output.dispose) {
        output.dispose();
      }

      return this.getSmoothedDetections();
    } catch (e) {
      // Non-fatal: log and return smoothed detections from last good frame
      console.warn('[ObjectDetector] Detection failed:', e);
      return this.getSmoothedDetections();
    }
  }

  /**
   * Update EMA-smoothed detections with new raw detections.
   */
  private updateSmoothedDetections(detections: RawDetection[]): void {
    const alpha = this._emaAlpha;
    const matchedKeys = new Set<string>();

    for (const det of detections) {
      // Find closest existing smoothed detection
      const key = ObjectDetector.detectionKey(det.classId, det.x1, det.y1);
      matchedKeys.add(key);

      const existing = this._smoothedDetections.get(key);
      if (existing) {
        // EMA update
        existing.bbox[0] = alpha * det.x1 + (1 - alpha) * existing.bbox[0];
        existing.bbox[1] = alpha * det.y1 + (1 - alpha) * existing.bbox[1];
        existing.bbox[2] = alpha * det.x2 + (1 - alpha) * existing.bbox[2];
        existing.bbox[3] = alpha * det.y2 + (1 - alpha) * existing.bbox[3];
        existing.score = alpha * det.score + (1 - alpha) * existing.score;
        existing.age = 0;
      } else {
        // New detection
        this._smoothedDetections.set(key, {
          bbox: [det.x1, det.y1, det.x2, det.y2],
          score: det.score,
          classId: det.classId,
          age: 0,
        });
      }
    }

    // Age out unmatched detections
    for (const [key, det] of this._smoothedDetections) {
      if (!matchedKeys.has(key)) {
        det.age++;
        if (det.age > this._maxAge) {
          this._smoothedDetections.delete(key);
        }
      }
    }
  }

  /**
   * Get current smoothed detections as Detection[].
   */
  private getSmoothedDetections(): Detection[] {
    const results: Detection[] = [];
    for (const [, det] of this._smoothedDetections) {
      results.push({
        bbox: det.bbox,
        confidence: det.score,
        classId: det.classId,
        label: COCO_CLASSES[det.classId] ?? `class-${det.classId}`,
      });
    }
    return results;
  }

  // ─── Configuration setters ─────────────────────────────────────────────────

  /** Filter low-confidence detections (default: 0.5). */
  setConfidenceThreshold(threshold: number): void {
    this._confidenceThreshold = Math.max(0, Math.min(1, threshold));
  }

  /** NMS IoU threshold (default: 0.45). */
  setNmsThreshold(iouThreshold: number): void {
    this._nmsThreshold = Math.max(0, Math.min(1, iouThreshold));
  }

  /** Detect every N frames (default: 3). Must be >= 1. */
  setFrameSkip(n: number): void {
    this._frameSkip = Math.max(1, Math.round(n));
  }

  /** Reset EMA smoothing and frame counter. */
  reset(): void {
    this._smoothedDetections.clear();
    this._frameCount = 0;
  }

  /** Cleanup resources. */
  dispose(): void {
    this._disposed = true;
    this._smoothedDetections.clear();
    this._frameCount = 0;
  }
}

// ─── Exported helpers (for testing) ───────────────────────────────────────────

/** @internal — exposed for testing only. */
export { parseYoloOutput, nms, iou, sigmoid, COCO_CLASSES };
export type { RawDetection, SmoothedDetection };
