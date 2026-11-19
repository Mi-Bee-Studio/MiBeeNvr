/**
 * Camera API — shared types (config, requests, discovery, protocols, PTZ).
 * Re-exported from ./cameras so existing imports keep working.
 */

// --- Types ---

export interface CameraTranscodingConfig {
  enabled: boolean;
  // Params are optional on the wire (backend omitempty / zero values): a
  // minimal {enabled:false} is a legitimate "explicit off" payload.
  target_codec?: string;
  preset?: string;
  bitrate?: string;
  crf?: number;
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
  /** Camera-management group label (v36). Empty/undefined = ungrouped. */
  group?: string;
  /**
   * Imaging channel (dual-spectrum): 'visible' (default/undefined) or
   * 'thermal'. Thermal applies the default policy — the AI analysis push is
   * skipped and NVR motion gating is discouraged (the calibration shutter
   * re-triggers it every cycle).
   */
  imaging_type?: string;
  /**
   * Explicit dual-spectrum channel link: camera entries sharing a non-empty
   * spectrum_group belong to one physical device whose channels register as
   * SEPARATE GB28181 SIP devices (distinct gb28181.device_id). Drives the
   * sibling switch chip + dual-pane in the live view. Config-injected.
   */
  spectrum_group?: string;
  /**
   * Audio-device → video-camera association (v43). Only meaningful for
   * encoding "audio" (independent mic) devices: the video camera whose
   * recordings the audio should replay alongside. '' = free-standing mic.
   */
  audio_link_camera_id?: string;
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
  /** Cascade tier: forward the sub-stream instead of main (#512). */
  cascade_sub_stream?: boolean | null;
  /** Vision instance names this camera pushes to (empty/undefined = all enabled instances). */
  vision_targets?: string[];
  // Recording mode (#435): 'continuous' (default) or 'adaptive' — dynamic
  // timelapse that drops to sparse keyframes while the compressed-domain
  // activity signal stays calm. 'adaptive' holds the tuning knobs (nil = defaults).
  recording_mode?: string;
  adaptive?: AdaptiveRecordingConfig;
  // Loudness trigger for adaptive recording (#478); only effective with
  // recording_mode 'adaptive'.
  audio_trigger?: CameraAudioTriggerConfig;
  pixgate?: CameraPixgateConfig;
  /** #637: ""/"single" (default) or "tiered" — continuous sub-stream recording.
   *  Applies on NVR restart. */
  recording_tier?: string;
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
  /** #635: absolute per-frame byte floor — P-frames below it never count as
   *  exit spikes (bitrate-starved cameras whose jitter masquerades as motion).
   *  0/unset = off (the auto floor below still applies unless disabled). */
  noise_floor_bytes?: number;
  /** #635: self-calibrate the noise floor from timelapse dwell. Default true. */
  auto_noise_floor?: boolean;
  /** #638: false = resident timelapse — stay sparse through video noise
   *  (rain/glare/foliage); only audio or external semantic triggers resume
   *  full-rate. Default true. */
  video_exit?: boolean;
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
  transcode_policy?: 'auto' | 'force_sw' | 'off' | 'passthrough';
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
  /** Camera-management group label (v36). Empty/undefined = ungrouped. */
  group?: string;
  /** Audio link (v43) — audio-only devices only. '' = free-standing mic. */
  audio_link_camera_id?: string;
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
  /** Cascade tier: forward the sub-stream instead of main (#512). */
  cascade_sub_stream?: boolean | null;
  /** Vision instance names this camera pushes to (empty/undefined = all enabled instances). */
  vision_targets?: string[];
  // Recording mode (#435). Omit = continuous.
  recording_mode?: string;
  /** Motion signal source (#711): "nvr" (default) or "camera:onvif"
   *  (Pull-Point MotionAlarm subscription). */
  motion_source?: string;
  /** Imaging channel (dual-spectrum): 'visible' (default) or 'thermal'. */
  imaging_type?: string;
  /** Dual-spectrum pairing key: cameras sharing it are the channels of one
   *  physical device (switch chip + side-by-side panes). */
  spectrum_group?: string;
  // Time-sync opt in/out (#time-sync): null/undefined = follow time_sync.auto;
  // false = never auto-correct this camera.
  auto_time_sync?: boolean | null;
  /** MJPEG segment shape (#761): "avi" (single-file AVI container, default)
   *  or "dir" (legacy per-frame JPEG directory). Applies on restart. */
  mjpeg_form?: string;
  recording_tier?: string;
  adaptive?: AdaptiveRecordingConfig;
  // Loudness trigger for adaptive recording (#478); only effective with
  // recording_mode 'adaptive'.
  audio_trigger?: CameraAudioTriggerConfig;
  pixgate?: CameraPixgateConfig;
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
  /** Camera-management group label (v36). Empty string = ungroup. */
  group?: string;
  /** Audio link (v43) — '' clears the association. */
  audio_link_camera_id?: string;
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
  /** Cascade tier: forward the sub-stream instead of main (#512). */
  cascade_sub_stream?: boolean | null;
  /** Vision instance names this camera pushes to (empty/undefined = all enabled instances). */
  vision_targets?: string[];
  // Recording mode (#435). Omit = unchanged. Changing it restarts the recorder.
  recording_mode?: string;
  /** Motion signal source (#711): "nvr" (default) or "camera:onvif".
   *  Reconciles immediately (no restart). */
  motion_source?: string;
  /** Imaging channel (dual-spectrum): 'visible' (default) or 'thermal'.
   *  Omit = unchanged. Thermal re-applies the AI-push skip default. */
  imaging_type?: string;
  /** Dual-spectrum pairing key; "" clears it, omit = unchanged. */
  spectrum_group?: string;
  // Time-sync opt in/out (#time-sync): null/undefined = unchanged (follow
  // time_sync.auto); false = never auto-correct this camera.
  auto_time_sync?: boolean | null;
  /** MJPEG segment shape (#761): "avi" (default) or "dir" (legacy
   *  per-frame directory). Applies on restart. */
  mjpeg_form?: string;
  recording_tier?: string;
  adaptive?: AdaptiveRecordingConfig;
  // Loudness trigger for adaptive recording (#478); only effective with
  // recording_mode 'adaptive'.
  audio_trigger?: CameraAudioTriggerConfig;
  pixgate?: CameraPixgateConfig;
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
    // 'audio' = audio-only source device (independent mic): recorder consumes
    // the stream's audio track and writes standalone audio segments.
    encodings: ['h264', 'h265', 'mjpeg', 'audio'],
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
    // ptz: motor control on PT models goes through /api/cameras/{id}/ptz/*
    // which the backend routes to the Xiaomi motor command.
    capabilities: { hls: true, ptz: true, snapshot: false, discovery: true, auth: false },
  },
  {
    id: 'whip',
    label: 'WHIP (WebRTC push)',
    encodings: ['h264', 'audio'],
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
];
