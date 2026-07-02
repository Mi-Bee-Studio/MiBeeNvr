/**
 * API barrel — re-exports everything so existing `$lib/api` imports work unchanged.
 */

// Client — auth, base fetch wrappers
export {
  storeCredentials,
  getCredentials,
  clearCredentials,
  isAuthenticated,
  login,
  logout,
  healthCheck,
  getSystemStats,
  getAuthHeader,
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
  createCamera,
  getCamera,
  updateCamera,
  deleteCamera,
  getPushStatus,
  getCameraRecordingStats,
  startCamera,
  stopCamera,
  rediscoverCamera,
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
} from './cameras';
// Recordings — list, download, frames, stats, archives
export {
  listRecordings,
  listTimelapseRecordings,
  getRecording,
  deleteRecording,
  batchDeleteRecordings,
  getRecordingDownloadUrl,
  getRecordingVideoUrl,
  getMergedRecordingUrl,
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
  getTimelapseFrames,
  loadTimelapseFrameBlob,
  triggerTimelapseMerge,
  batchMergeTimelapse,
  subscribeTimelapseMergeProgress,
  cancelMerge,
  retryRecordingMerge,
  fetchTimelapsePreview,
  recordTimelineSeek,
} from './recordings';

export type {
  Recording,
  FrameInfo,
  FramesResponse,
  RecordingListResponse,
  StorageStats,
  DailyStats,
  ArchiveGroup,
  ArchiveListResponse,
  TimelapseFrame,
  TimelapsePreviewFrame,
} from './recordings';

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
} from './ai';

export type {
  AiDetectionSettings,
  AiStatus,
  AiConfigUpdate,
  Zone,
  ZoneList,
  CreateZoneRequest,
  PerCameraAiState,
} from './ai';
