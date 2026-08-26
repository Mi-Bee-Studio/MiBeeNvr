package config

// VisionConfig 配置 NVR → MiBeeVision 的主动推送集成。
//
// 启用后,NVR 会:
//  1. 监听 segment.completed 事件
//  2. 检查 Vision 心跳是否健康(POST /api/vision/heartbeat)
//  3. 健康时,主动推送段信息到 Vision(而非 Vision 通过 SSE 拉取 + HTTP 下载)
//
// 这解决了 Vision 下载录像时 78% 返回 404 的问题(段文件在 Vision 下载前已被合并/删除)。
// 推送发生在段落盘后、合并前的窗口期,确保 Vision 能拿到文件。
type VisionConfig struct {
	// Enabled 是否启用 Vision 推送集成。默认 false(SSE 模式向后兼容)。
	Enabled bool `yaml:"enabled" json:"enabled"`

	// URL Vision 服务的基地址,如 "http://192.168.63.110:9091"。
	// NVR 将 POST 到 {URL}/vision/segment/notify。
	URL string `yaml:"url" json:"url"`

	// HeartbeatTimeoutSecs 心跳超时(秒)。超过此时间未收到 Vision 心跳,
	// NVR 认为 Vision 不健康,暂停推送。默认 60。
	HeartbeatTimeoutSecs int `yaml:"heartbeat_timeout_secs" json:"heartbeatTimeoutSecs"`

	// PushMode 推送模式:
	//   "notify" — 仅发送 file_path,Vision 从共享文件系统直读(Sidecar 同主机部署,零拷贝)
	//   "upload" — 发送压缩视频字节流(Remote 跨主机部署)
	// 默认 "notify"。
	PushMode string `yaml:"push_mode" json:"pushMode"`

	// SkipCameras 永不推送的相机 ID 列表。用于外部消费者明确无法消费的相机
	// (如 MJPEG/JPEG 编码——视频字节流对其无意义,推送只是白白消耗带宽与 CPU)。
	// 跳过的段不会推送,也不计入离线补偿重推窗口。
	SkipCameras []string `yaml:"skip_cameras" json:"skipCameras"`

	// SubLayerCameras 推送子流分析层的相机 ID 列表 (#514)。列表内的相机:
	//   - NVR 按需拉取其子码流并录制成独立的分析段(不进录像库、不参与合并,
	//     存放于 <storage.root>/sublayer/<camera_id>/,消费成功即删,保留时长兜底);
	//   - 推送改走子流段(X-Layer: sub),主流段不再推送——低分辨率段解码成本
	//     为主流的 1/4~1/16,单个消费者即可覆盖全部高分辨率相机;
	//   - X-Recording-Id 关联同时段的主流录像 ID,ai_status/ai_processed_at
	//     语义不变。子流不可用(无配置/拉流失败)期间不推送(不回退主流,避免
	//     消费者端编码/分辨率抖动)。
	// 需要 #512 的 sub_profile_token 或手填 sub_stream_url。
	SubLayerCameras []string `yaml:"sub_layer_cameras" json:"subLayerCameras"`

	// SubLayerSegmentSecs 子流分析段时长(秒),默认 60。
	SubLayerSegmentSecs int `yaml:"sub_layer_segment_secs" json:"subLayerSegmentSecs"`

	// SubLayerRetentionSecs 子流分析段磁盘保留上限(秒),默认 7200。推送成功
	// 即删;此值仅兜底(消费者离线/推送失败时防止无限堆积)。
	SubLayerRetentionSecs int `yaml:"sub_layer_retention_secs" json:"subLayerRetentionSecs"`

	// SubLayerPushIntervalSecs 子流段推送扫描间隔(秒),默认 20。子流段不受
	// 合并/删除窗口竞争,无需事件驱动——磁盘即队列,扫描推送。
	SubLayerPushIntervalSecs int `yaml:"sub_layer_push_interval_secs" json:"subLayerPushIntervalSecs"`
}

// ShouldSkipCamera 报告 camera_id 是否在 SkipCameras 列表中。
func (v VisionConfig) ShouldSkipCamera(cameraID string) bool {
	for _, c := range v.SkipCameras {
		if c == cameraID {
			return true
		}
	}
	return false
}

// SubLayerCameraSet 返回子流分析层的相机集合(nil/空 → 空 集)。skip_cameras
// 优先:同时出现在两个列表的相机按 skip 处理(消费者明确拒绝的相机没有分析层)。
func (v VisionConfig) SubLayerCameraSet() map[string]bool {
	set := make(map[string]bool, len(v.SubLayerCameras))
	for _, c := range v.SubLayerCameras {
		if !v.ShouldSkipCamera(c) {
			set[c] = true
		}
	}
	return set
}
