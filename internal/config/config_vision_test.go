package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShouldSkipCamera(t *testing.T) {
	cfg := VisionConfig{SkipCameras: []string{"cam-a", "cam-b"}}
	if !cfg.ShouldSkipCamera("cam-a") || !cfg.ShouldSkipCamera("cam-b") {
		t.Fatal("listed cameras must be skipped")
	}
	if cfg.ShouldSkipCamera("cam-c") || cfg.ShouldSkipCamera("") {
		t.Fatal("unlisted cameras must not be skipped")
	}
	empty := VisionConfig{}
	if empty.ShouldSkipCamera("cam-a") {
		t.Fatal("nil/empty list skips nothing")
	}
}

// SubLayerCameraSet: skip_cameras wins over sub_layer_cameras — a camera the
// consumer explicitly rejected must not grow an analysis layer (#514).
func TestSubLayerCameraSet(t *testing.T) {
	cfg := VisionConfig{
		SubLayerCameras: []string{"cam-a", "cam-b", "cam-c"},
		SkipCameras:     []string{"cam-b"},
	}
	set := cfg.SubLayerCameraSet()
	require.True(t, set["cam-a"])
	require.True(t, set["cam-c"])
	require.False(t, set["cam-b"], "skip_cameras must override sub_layer_cameras")
	require.Len(t, set, 2)

	require.Empty(t, VisionConfig{}.SubLayerCameraSet())
}

func boolPtr(b bool) *bool { return &b }

// 多实例:legacy 单 URL 配置必须无损合成为名为 "default" 的实例——
// 现有部署零变化,心跳/推送路径全兼容。
func TestVisionEffectiveInstancesLegacySynthesis(t *testing.T) {
	v := VisionConfig{Enabled: true, URL: "http://jetson:9091"}
	ins := v.EffectiveInstances()
	require.Len(t, ins, 1)
	require.Equal(t, "default", ins[0].Name)
	require.Equal(t, "http://jetson:9091", ins[0].URL)
	require.True(t, ins[0].EnabledOrDefault(), "legacy instance must default to enabled")
}

// 既配 legacy URL 又配 instances:instances 优先(URL 保留作显示字段,不再合成)。
func TestVisionEffectiveInstancesExplicitWins(t *testing.T) {
	v := VisionConfig{
		URL: "http://old:9091",
		Instances: []VisionInstance{
			{Name: "a", URL: "http://a:9091"},
			{Name: "b", URL: "http://b:9091", Enabled: boolPtr(false)},
		},
	}
	ins := v.EffectiveInstances()
	require.Len(t, ins, 2)
	require.Equal(t, "a", ins[0].Name)
	require.Equal(t, "b", ins[1].Name)
}

// 全部禁用 → EnabledInstances 为空(推送整体停摆,但心跳仍可记录)。
func TestVisionEnabledInstancesFilters(t *testing.T) {
	v := VisionConfig{Instances: []VisionInstance{
		{Name: "a", URL: "http://a:9091"},
		{Name: "b", URL: "http://b:9091", Enabled: boolPtr(false)},
	}}
	enabled := v.EnabledInstances()
	require.Len(t, enabled, 1)
	require.Equal(t, "a", enabled[0].Name)

	allOff := VisionConfig{Instances: []VisionInstance{
		{Name: "a", URL: "http://a:9091", Enabled: boolPtr(false)},
	}}
	require.Empty(t, allOff.EnabledInstances())
}

// 路由解析:相机未配置(空)→ 全部启用实例(默认广播);
// 显式配置 → 按配置顺序返回已知实例;未知名称在配置校验层拦截,
// 运行时兜底为忽略(防止 typo 让推送整体瘫痪)。
func TestVisionRouteFor(t *testing.T) {
	v := VisionConfig{Instances: []VisionInstance{
		{Name: "a", URL: "http://a:9091"},
		{Name: "b", URL: "http://b:9091"},
		{Name: "c", URL: "http://c:9091", Enabled: boolPtr(false)},
	}}

	// 空 targets → 全部启用实例(a+b,c 被禁用排除)。
	routed := v.RouteFor(nil)
	require.Len(t, routed, 2)
	require.Equal(t, "a", routed[0].Name)
	require.Equal(t, "b", routed[1].Name)

	// 显式子集,保持配置顺序。
	routed = v.RouteFor([]string{"b", "a"})
	require.Len(t, routed, 2)
	require.Equal(t, "b", routed[0].Name)
	require.Equal(t, "a", routed[1].Name)

	// 显式选中已禁用实例 → 该实例不生效(enabled 过滤优先于路由)。
	routed = v.RouteFor([]string{"a", "c"})
	require.Len(t, routed, 1)
	require.Equal(t, "a", routed[0].Name)

	// 未知名称忽略(配置校验负责 400,运行时容错)。
	routed = v.RouteFor([]string{"ghost", "a"})
	require.Len(t, routed, 1)
	require.Equal(t, "a", routed[0].Name)
}

// legacy 合成下的路由:单实例部署,相机无论配不配 targets 都路由到 default。
func TestVisionRouteForLegacy(t *testing.T) {
	v := VisionConfig{URL: "http://jetson:9091"}
	require.Len(t, v.RouteFor(nil), 1)
	require.Equal(t, "default", v.RouteFor(nil)[0].Name)
	require.Len(t, v.RouteFor([]string{"anything-else"}), 0,
		"explicit unknown target on legacy config routes nowhere (validation catches earlier)")
}

// 配置校验:实例名唯一/必填、URL 必须 http(s)、相机 vision_targets 引用
// 已定义实例(default 合成名也算)、legacy 单 URL(空 instances)不校验 URL。
func TestValidateVisionInstances(t *testing.T) {
	base := func(mod func(*Config)) *Config {
		cfg := &Config{}
		cfg.ApplyDefaults() // 零值配置会被 FTP/segment_duration 等校验拦
		mod(cfg)
		return cfg
	}

	require.NoError(t, Validate(base(func(c *Config) {
		c.Vision = VisionConfig{URL: "http://a:9091"} // legacy,无显式实例
	})))

	require.ErrorContains(t, Validate(base(func(c *Config) {
		c.Vision = VisionConfig{Instances: []VisionInstance{{Name: "a"}}} // 缺 URL
	})), "http(s)")

	require.ErrorContains(t, Validate(base(func(c *Config) {
		c.Vision = VisionConfig{Instances: []VisionInstance{
			{Name: "a", URL: "http://a:9091"},
			{Name: "a", URL: "http://b:9091"},
		}}
	})), "duplicate name")

	require.ErrorContains(t, Validate(base(func(c *Config) {
		c.Vision = VisionConfig{Instances: []VisionInstance{
			{Name: "", URL: "http://a:9091"},
		}}
	})), "name is required")

	// 相机路由引用未知实例 → 拒绝;引用 default/显式实例 → 通过。
	withCam := func(targets []string, instances []VisionInstance) *Config {
		return base(func(c *Config) {
			c.Vision = VisionConfig{Instances: instances}
			c.Cameras = []CameraConfig{{
				ID: "cam-1", Protocol: "rtsp", URL: "rtsp://192.168.1.10:554/stream1",
				Encoding: "h264", VisionTargets: targets,
			}}
		})
	}
	require.ErrorContains(t, Validate(withCam([]string{"ghost"},
		[]VisionInstance{{Name: "a", URL: "http://a:9091"}})),
		"unknown vision instance")
	require.NoError(t, Validate(withCam([]string{"a", "default"},
		[]VisionInstance{{Name: "a", URL: "http://a:9091"}})))
	require.NoError(t, Validate(withCam(nil, nil))) // legacy 全兼容
}
