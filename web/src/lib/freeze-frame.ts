/**
 * Capture the current frame from a video element as a JPEG data URL.
 * Returns null if capture fails or video has no data.
 */
export function captureFrame(videoEl: HTMLVideoElement | null): string | null {
  if (!videoEl || videoEl.readyState < 2) return null;

  try {
    const canvas = document.createElement('canvas');
    canvas.width = videoEl.videoWidth || 640;
    canvas.height = videoEl.videoHeight || 480;
    const ctx = canvas.getContext('2d');
    if (!ctx) return null;
    ctx.drawImage(videoEl, 0, 0, canvas.width, canvas.height);
    return canvas.toDataURL('image/jpeg', 0.8);
  } catch {
    return null;
  }
}
