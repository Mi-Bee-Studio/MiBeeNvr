import type { CameraTranscodingConfig } from '$lib/api/cameras';

/**
 * Per-camera transcoding payload for camera create/update — expresses only
 * the preference the user actually has:
 *
 * - enabled            → full per-camera config (overrides the global switch)
 * - unchecked, was on  → explicit {enabled:false} (turns it off)
 * - never enabled      → undefined (field omitted; backend nil = leave unset,
 *                        camera keeps following the global switch)
 *
 * The edit form used to send {enabled:false} unconditionally, so saving a
 * camera for any reason (renaming it) silently overrode a globally-enabled
 * transcoder with a per-camera "off" — and H.265 playback fallback then hit
 * "transcoding is not enabled for camera" with no way to tell why.
 */
export function buildTranscodingPayload(s: {
  enabled: boolean;
  wasEnabled: boolean;
  targetCodec: string;
  preset: string;
  bitrate: string;
  crf: number;
}): CameraTranscodingConfig | undefined {
  if (!s.enabled) return s.wasEnabled ? { enabled: false } : undefined;
  return {
    enabled: true,
    target_codec: s.targetCodec,
    preset: s.preset,
    bitrate: s.bitrate,
    crf: s.crf,
  };
}
