/**
 * API barrel — re-exports everything so existing `$lib/api` imports work unchanged.
 */

// Client — auth, base fetch wrappers
export {
  storeToken,
  getToken,
  clearToken,
  getTokenForUrl,
  isAuthenticated,
  login,
  logout,
  healthCheck,
  getSystemStats,
  getAuthHeader,
  forceRelogin,
  API_BASE,
  apiRequest,
  apiRequestBlob,
  ApiRequestError,
  setupApi,
} from './client';

export type {
  AuthCredentials,
  LoginResponse,
  ApiError,
  HealthCheck,
  HealthResponse,
  SystemStats,
  SetupResponse,
} from './client';

// Cameras — CRUD, ONVIF, PTZ, protocols
export {
  listCameras,
  listCamerasSummary,
  createCamera,
  getCamera,
  updateCamera,
  deleteCamera,
  getPushStatus,
  getCameraRecordingStats,
  startCamera,
  stopCamera,
  rediscoverCamera,
  activateCamera,
  getDashboardCameras,
  getSnapshotUrl,
  testConnection,
  getMergeConfig,
  updateMergeConfig,
  deleteCameraMergeConfig,
  ptzMove,
  ptzStop,
  discoverONVIFDevices,
  probeONVIFDevice,
  listProtocols,
  getCameraProtocols,
  normalizeProtocol,
  buildProtocolsMap,
  getProtocolCapabilities,
  checkVendor,
  DEFAULT_PROTOCOLS,
  // Imaging
  getImagingSettings,
  setImagingSettings,
  getImagingOptions,
  // PTZ Presets
  getPTZPresets,
  createPTZPreset,
  goToPTZPreset,
  deletePTZPreset,
  // Snapshot URI
  getSnapshotUri,
  // Device Capabilities
  getDeviceCapabilities,
  // Device Management
  rebootDevice,
  getNetworkInterfaces,
  setNetworkInterfaces,
  getDeviceUsers,
  createDeviceUsers,
  deleteDeviceUsers,
  // Timelapse
  getTimelapseConfig,
  updateTimelapseConfig,
  getRelayCapabilities,
  // Xiaomi PTZ
  xiaomiPtzMove,
  xiaomiPtzStop,
  // Two-way audio
  startTwoWayAudio,
  stopTwoWayAudio,
  getAudioUpstreamWS,
  // Xiaomi device info
  getXiaomiDeviceInfo,
} from './cameras';
export type {
  CameraTranscodingConfig,
  Camera,
  CreateCameraRequest,
  UpdateCameraRequest,
  DiscoveredDevice,
  DiscoveryError,
  DiscoveryResult,
  DeviceInfo,
  DeviceProfile,
  ONVIFDeviceDetail,
  PTZMoveRequest,
  ProtocolCapabilities,
  ProtocolInfo,
  CameraProtocolDetail,
  CameraProtocolsResponse,
  MergeConfig,
  TestConnectionRequest,
  TestConnectionResult,
  VendorCheckResult,
  PushTargetConfig,
  VideoPresetOverrides,
  PushTargetStatus,
  PushStatusResponse,
  // Imaging
  ImagingSettings,
  ImagingOptionRange,
  ImagingOptions,
  // PTZ Presets
  PTZPreset,
  // Snapshot URI
  SnapshotUriResponse,
  // Device Capabilities
  DeviceCapabilitiesInfo,
  // Device Management
  NetworkIPv4,
  NetworkIPv6,
  NetworkNTP,
  NetworkInterface as ONVIFNetworkInterface,
  ONVIFDeviceUser,
  // Timelapse
  TimelapseConfig,
  ScheduleConfig,
  TimeRange,
  CameraRecordingStats,
  RelayCapabilities,
  // Xiaomi PTZ
  XiaomiPtzMoveRequest,
  XiaomiDeviceInfo,
} from './cameras';
// Recordings — list, download, frames, stats, archives
export {
  listRecordings,
  getRecording,
  deleteRecording,
  batchDeleteRecordings,
  getRecordingDownloadUrl,
  getRecordingVideoUrl,
  getMergedRecordingUrl,
  getCameraPlaybackPlaylistURL,
  probeMergedRecordingCodec,
  clearMergedCodecCache,
  downloadRecording,
  listFrames,
  loadFrameBlob,
  getStats,
  getStatsTrends,
  listArchives,
  listArchiveRecordings,
  deleteArchiveGroup,
  deleteArchiveRecording,
  setArchiveRetention,
  getArchiveCleanupStatus,
  getTimelapseFrames,
  loadTimelapseFrameBlob,
  triggerTimelapseMerge,
  batchMergeTimelapse,
  subscribeTimelapseMergeProgress,
  cancelMerge,
  retryRecordingMerge,
  fetchTimelapsePreview,
  recordTimelineSeek,
  getRecordingDailySummary,
  getRecordingsTimeline,
} from './recordings';

export type {
  Recording,
  FrameInfo,
  FramesResponse,
  RecordingListResponse,
  RecordingDaySummaryResponse,
  RecordingDaySummary,
  StorageStats,
  DailyStats,
  ArchiveGroup,
  ArchiveListResponse,
  ArchiveCleanupTask,
  ArchiveCleanupStatus,
  TimelapseFrame,
  TimelapsePreviewFrame,
  RecordingTimelineSegment,
  RecordingTimelineResponse,
} from './recordings';

// Timelapse merges — periodic-merge outputs (8h / 12h / 24h / natural-day / 7d / 30d)
export {
  listTimelapseMerges,
  getTimelapseMerge,
  getTimelapseMergeDownloadUrl,
  probeTimelapseMergeCodec,
  deleteTimelapseMerge,
} from './timelapse-merges';

export type { TimelapseMerge, TimelapseMergeListResponse, ListTimelapseMergesParams } from './timelapse-merges';

// Settings — cleanup, webdav, merge, features
export {
  getSettings,
  updateSettings,
  getMergeSettings,
  updateMergeSettings,
  getMergeStatus,
  getMergePending,
  getFeatures,
  updateFeatures,
  getStreamingSettings,
  updateStreamingSettings,
  generateAPIKey,
  revokeAPIKey,
} from './settings';

export type {
  CleanupConfig,
  WebDAVConfig,
  SettingsConfig,
  MiBeeVisionConfig,
  GB28181Config,
  MergeStatus,
  MergePending,
  FeatureFlags,
  StreamingConfig,
  WebRTCConfig,
  FLVStreamingConfig,
  RTMPConfig,
  SRTStreamConfig,
  SRTConfig,
} from './settings';

// Xiaomi — cloud auth, devices, sync
export { xiaomiAuth, xiaomiDevices, xiaomiCaptcha, xiaomiVerify, xiaomiSync } from './xiaomi';

export type { XiaomiDevice, XiaomiDevicesResponse, XiaomiAuthResponse } from './xiaomi';

// Health — camera health status and events
export { getHealthStatus, getHealthEvents, getCameraHealth, getHealthCameras } from './health';
export type {
  HealthStatus,
  HealthEventType,
  HealthEvent,
  CameraHealth,
  HealthStatusResponse,
  HealthEventsResponse,
  HealthEventsParams,
  CameraHealthDetail,
  HealthCamerasResponse,
} from './health';

// Transcoding — hardware check, FFmpeg, task management
export {
  getTranscodingCheck,
  getFFmpegStatus,
  downloadFFmpeg,
  retryDownload,
  getTranscodingStatus,
  getTranscodingTasks,
  enqueueTranscodeTask,
  cancelTranscodeTask,
  retryTranscodeTask,
  startBackfill,
  getUntranscodedRecordingCount,
  getTranscodingSettings,
  updateTranscodingSettings,
} from './transcoding';

export type {
  HardwareCapabilities,
  SelfCheckResult,
  DownloadStatus,
  TranscodeTask,
  ManagerStatus,
  TranscodingSettings,
} from './transcoding';

// AI Detection — localStorage-backed settings + zone management
export {
  getAiSettings,
  saveAiSettings,
  detectAiBackend,
  getPerCameraAiSettings,
  savePerCameraAiSettings,
  getAIZones,
  createAIZone,
  deleteAIZone,
  getAiStatus,
  updateAiConfig,
  listAiModels,
} from './ai';

export type {
  AiDetectionSettings,
  AiStatus,
  AiConfigUpdate,
  AiModelInfo,
  Zone,
  ZoneList,
  CreateZoneRequest,
  PerCameraAiState,
} from './ai';

// Update / version check (sensing layer only — never executes an upgrade)
export { getVersion, getUpdateStatus, refreshUpdateStatus } from './update';
export type { UpdateStatus } from './update';

export {
  queryGB28181Records,
  startGB28181Playback,
  gb28181PlaybackStatus,
  stopGB28181Playback,
  controlGB28181Playback,
  getGB28181CascadeStatus,
} from './gb28181';
export type {
  GB28181DeviceRecord,
  GB28181RecordListResponse,
  GB28181PlaybackStatus,
  GB28181CascadeStatus,
} from './gb28181';
