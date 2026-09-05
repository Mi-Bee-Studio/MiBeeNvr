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
	//
	// 多实例部署时此字段退化为兼容显示字段,实际推送目标以 instances 为准;
	// instances 为空且 URL 非空时,运行时合成为名为 "default" 的单实例。
	URL string `yaml:"url" json:"url"`

	// Instances 多个 Vision 消费端实例。每个实例独立的地址/身份/启停;
	// 相机可通过 camera.vision_targets 选择接哪些实例(空=全部启用实例)。
	// 不同实例可挂不同模型配置,实现按场景分流分析。
	Instances []VisionInstance `yaml:"instances,omitempty" json:"instances,omitempty"`

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

	// TieredCameras 分层录制(#637)相机的分析输入名单:列表内的相机其
	// tierrec 子层段(layer=1,60s 低清连续录制)被推送给外部消费者做语义
	// 门控,主流段不再推送(让位语义与 sub_layer_cameras 相同,但段来源是
	// 正式录像库的 layer=1 行,非 #537 的临时 sublayer 目录)。skip_cameras
	// 优先于此列表。
	TieredCameras []string `yaml:"tiered_cameras" json:"tieredCameras"`
}

// VisionInstance 一个 Vision 消费端实例的接入描述。
type VisionInstance struct {
	// Name 实例名(稳定标识,相机 vision_targets 引用它;唯一)。
	Name string `yaml:"name" json:"name"`

	// URL 实例基地址,NVR POST 到 {URL}/vision/segment/upload。
	URL string `yaml:"url" json:"url"`

	// APIKeyName 关联的 API Key 名(可选)。心跳/事件/ai_status 回传携带
	// 该 key 时,NVR 据此把回传归因到本实例。缺省时回传归因到 default 实例。
	APIKeyName string `yaml:"api_key_name,omitempty" json:"apiKeyName,omitempty"`

	// Enabled 是否参与推送(nil/true=启用)。禁用的实例保留配置但不出现在
	// 路由目标中。
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

// EnabledOrDefault 报告实例是否启用(nil 视为启用,兼容省略写法)。
func (i VisionInstance) EnabledOrDefault() bool {
	return i.Enabled == nil || *i.Enabled
}

// EffectiveInstances 返回生效的实例列表(保持配置顺序)。
// instances 为空时合成单实例 "default"(URL 取 legacy 字段,可为空——
// "Vision 先部署、NVR 还没配地址"的阶段心跳仍可记录,推送自然空转):
// 现有单实例部署零变化。instances 与 URL 同时配置时 instances 优先。
func (v VisionConfig) EffectiveInstances() []VisionInstance {
	if len(v.Instances) > 0 {
		return v.Instances
	}
	return []VisionInstance{{Name: "default", URL: v.URL}}
}

// EnabledInstances 返回启用中的实例(EffectiveInstances 过滤 enabled)。
func (v VisionConfig) EnabledInstances() []VisionInstance {
	all := v.EffectiveInstances()
	out := make([]VisionInstance, 0, len(all))
	for _, ins := range all {
		if ins.EnabledOrDefault() {
			out = append(out, ins)
		}
	}
	return out
}

// RouteFor 解析一台相机的推送目标实例。cameraTargets 为空 → 全部启用实例
// (默认广播,单实例部署即"推给唯一实例");非空 → 按其顺序返回已知且启用的
// 实例(未知名称忽略——配置校验层负责提前 400,运行时容错防 typo 瘫痪)。
func (v VisionConfig) RouteFor(cameraTargets []string) []VisionInstance {
	if len(cameraTargets) == 0 {
		return v.EnabledInstances()
	}
	byName := make(map[string]VisionInstance, len(v.Instances)+1)
	for _, ins := range v.EffectiveInstances() {
		byName[ins.Name] = ins
	}
	out := make([]VisionInstance, 0, len(cameraTargets))
	for _, name := range cameraTargets {
		if ins, ok := byName[name]; ok && ins.EnabledOrDefault() {
			out = append(out, ins)
		}
	}
	return out
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

// TieredCameraSet 返回分层录制分析输入的相机集合(nil/空 → 空 集)。skip_cameras
// 优先,语义同 SubLayerCameraSet。
func (v VisionConfig) TieredCameraSet() map[string]bool {
	set := make(map[string]bool, len(v.TieredCameras))
	for _, c := range v.TieredCameras {
		if !v.ShouldSkipCamera(c) {
			set[c] = true
		}
	}
	return set
}
