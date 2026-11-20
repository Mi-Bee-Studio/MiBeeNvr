// AI detection-zone filtering: keep only detections whose center falls inside
// an enabled zone polygon of the current camera. Extracted from WasmPlayer.svelte.
import type { Detection } from '$lib/ai-detection/inference';
import type { Zone } from '$lib/api/ai';

export function pointInPolygon(px: number, py: number, polygon: number[][]): boolean {
  let inside = false;
  for (let i = 0, j = polygon.length - 1; i < polygon.length; j = i++) {
    const xi = polygon[i][0],
      yi = polygon[i][1];
    const xj = polygon[j][0],
      yj = polygon[j][1];
    if (yi > py !== yj > py && px < ((xj - xi) * (py - yi)) / (yj - yi) + xi) {
      inside = !inside;
    }
  }
  return inside;
}

export function filterDetectionsByZones(
  detections: Detection[],
  zones: Zone[],
  cameraId: string,
  canvasWidth: number,
  canvasHeight: number,
): Detection[] {
  if (canvasWidth === 0 || canvasHeight === 0) return detections;
  const enabledZones = zones.filter((z) => z.camera_id === cameraId && z.enabled);
  if (enabledZones.length === 0) return detections;
  return detections.filter((d) => {
    const cx = (d.bbox[0] + d.bbox[2]) / (2 * canvasWidth);
    const cy = (d.bbox[1] + d.bbox[3]) / (2 * canvasHeight);
    return enabledZones.some((z) => pointInPolygon(cx, cy, z.points));
  });
}
