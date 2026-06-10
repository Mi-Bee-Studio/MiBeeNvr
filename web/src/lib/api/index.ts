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
  getCameraRecordingStats,
  enableCamera,
  disableCamera,
  startCamera,
  stopCamera,
  getDashboardCameras,
  getSnapshotUrl,
  testConnection,
  getMergeConfig,
  updateMergeConfig,
  deleteCameraMergeConfig,
  ptzMove,
  ptzStop,
  getPTZStatus,
  discoverONVIFDevices,
  getONVIFDeviceDetail,
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
  pauseTimelapse,
  resumeTimelapse,
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
  PTZStatus,
  ProtocolCapabilities,
  ProtocolInfo,
  MergeConfig,
  TestConnectionRequest,
  TestConnectionResult,
  VendorCheckResult,
  PushTarget,
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
  subscribeTimelapseMergeProgress,
  getTimelapseThumbnailUrl,
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
} from './settings';

export type {
  CleanupConfig,
  WebDAVConfig,
  SettingsConfig,
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
export { getHealthStatus, getHealthEvents, getCameraHealth, getHealthCameras, getStabilityData } from './health';
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
  StabilityMetrics,
  StabilityDataResponse,
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
  getTranscodingCameras,
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

// AI Detection — localStorage-backed settings
export { getAiSettings, saveAiSettings, detectAiBackend } from './ai';

export type { AiDetectionSettings } from './ai';
