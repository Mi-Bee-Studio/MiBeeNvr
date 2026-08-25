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
