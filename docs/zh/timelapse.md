#BN|# 延时摄影录制
#KM|
#KM|延时摄影功能从摄像头录制中创建延时摄影视频，将数小时或数天压缩为几分钟。MiBee NVR v2 引入了重大改进，包括灵活的合并间隔、H.264/H.265 双模式支持以及统一的录制界面。
#RW|
#RZ|## 概览
#SY|
#PB|延时摄影系统自动将视频片段合并为压缩的延时摄影录制。v2 版本的关键改进：
#XW|
#XY|- **灵活的合并间隔**：支持 8h、12h、24h、natural-day、7d 和 30d 间隔
#BS|- **H.264/H.265 双模式**：任何 RTSP 摄像机现在无需额外硬件即可生成延时摄影录制
#NS|- **统一界面**：集成的录制页面，支持表格、图库和日历视图模式
#MT|- **关键帧提取**：使用现有 RTSP 流进行零开销的延时摄影生成
#BQ|
#RX|## 配置
#RJ|
#YY|### 基础延时摄影设置
#HX|
#KP|在配置中为摄像头启用延时摄影录制：
#YT|
#VY|```yaml
#BB|cameras:
#HP|  - name: "前门摄像头"
#TP|    protocol: "rtsp"
#WB|    encoding: "h264"
#HS|    url: "rtsp://192.168.1.100:554/stream"
#NH|    enabled: true
#JJ|    
#KW|    # 延时摄影配置 (v2 功能)
#RJ|    timelapse:
#JP|      enabled: true
#MB|      merge_duration: "natural-day"  # v2: 灵活的合并间隔
#RX|      frame_source: "rtsp_keyframe"  # v2: 双模式关键帧提取
#VH|      output_fps: 30
#JB|```
#TX|
#WJ|### 双模式配置（RTSP 摄像头）
#RB|
#XK|对于现有的 RTSP 摄像头，启用双模式延时摄影而无需更改摄像头协议：
#MS|
#VY|```yaml
#BB|cameras:
#ZT|  - name: "客厅摄像头"
#TP|    protocol: "rtsp"
#QN|    encoding: "h265"
#TB|    url: "rtsp://192.168.1.101:554/stream"
#NH|    enabled: true
#VJ|    
#RJ|    timelapse:
#PJ|      enabled: true                          # 启用延时摄影
#BY|      merge_duration: "24h"                  # 每 24 小时合并一次
#TX|      frame_source: "rtsp_keyframe"         # 从 RTSP 流提取
#VH|      output_fps: 30
#KT|```
#YJ|
#WX|### 独立延时摄影配置
#XN|
#XY|创建带有独立 RTSP 源的专用延时摄影摄像头：
#KR|
#VY|```yaml
#BB|cameras:
#JW|  - name: "纯延时摄影摄像头"
#QT|    protocol: "timelapse"
#WB|    encoding: "h264"
#SJ|    url: "rtsp://backup-camera.example.com:554/stream"
#NH|    enabled: true
#JQ|    
#SR|    timelapse:
#JP|      enabled: true
#NB|      merge_duration: "7d"                  # 周度合并
#NT|      frame_source: "rtsp_keyframe"         # 从延时摄影流提取
#QZ|      output_fps: 15                         # 降低帧率以适应长时间
#JH|```
#HV|
#JT|## 合并间隔选项（v2）
#SZ|
#WS|`merge_duration` 字段支持不同用例的灵活间隔：
#VB|
#ZY|| 间隔 | 描述 | 对齐方式 | 用途 |
#JN||----------|-------------|-----------|----------|
#TZ|| `8h` | 8 小时合并 | 00:00、08:00、16:00 UTC | 商务时段、班次变化 |
#ZM|| `12h` | 12 小时合并 | 00:00、12:00 UTC | 日/夜周期、上午/下午片段 |
#YY|| `24h` | 24 小时合并 | 每天 00:00 UTC | 日概览、安全审查 |
#TQ|| `natural-day` | 自然日（0-24h） | 本地时间 | 用户友好的日总结 |
#VW|| `7d` | 周度合并 | 周一 00:00 UTC | 周度审查、模式分析 |
#HN|| `30d` | 月度合并 | 每月 1 日 00:00 UTC | 月度报告、长期分析 |
#KR|
#TK|### 配置示例
#VS|
#VY|```yaml
#VV|# 8小时商务监控
#MK|timelapse:
#BV|  enabled: true
#VR|  merge_duration: "8h"
#JB|  output_fps: 30
#MS|
#WZ|# 自然日日度总结
#MK|timelapse:
#BV|  enabled: true
#XK|  merge_duration: "natural-day"
#BV|  output_fps: 10
#ZS|
#WT|# 周度模式分析
#MK|timelapse:
#BV|  enabled: true
#SQ|  merge_duration: "7d"
#WB|  output_fps: 5
#TS|
#TB|# 月度报告
#MK|timelapse:
#BV|  enabled: true
#VB|  merge_duration: "30d"
#QN|  output_fps: 2
#TM|```
#BJ|
#RS|## 双模式延时摄影（v2）
#BK|
#MH|双模式延时摄影允许任何 RTSP 摄像机生成延时摄影录制，无需额外的硬件要求。
#RM|
#RV|### 工作原理
#XM|
#BR|1. **主要 RTSP 流**：摄像头按常规录制视频片段
#TB|2. **关键帧提取**：KeyframeExtractor 订阅 RTSP StreamHub
#XH|3. **帧处理**：从流中提取 IDR 帧（H.264 类型 5，H.265 类型 19/20）
#QR|4. **延时摄影生成**：提取的帧被处理为压缩的延时摄影视频
#YB|
#XS|### 支持的摄像头类型
#XB|
#MX|- **RTSP H.264**：支持 H.264 编码的 IP 摄像头
#XH|- **RTSP H.265**：支持 H.265 编码的现代摄像头，提供更好的效率
#KK|- **ONVIF**：自动发现摄像头，同时支持 H.264 和 H.265 流
#WP|
#KT|### H.265 支持
#BM|
#VZ|系统自动检测 H.265 流并相应配置 KeyframeExtractor：
#QX|
#VY|```yaml
#VZ|# ONVIF H.265 摄像头
#BB|cameras:
#ZK|  - name: "安全摄像头 1"
#VY|    protocol: "onvif"
#ST|    encoding: "h265"                    # 主要编码
#BH|    stream_encoding: "H265"            # ONVIF 特定字段
#WX|    url: "onvif://192.168.1.102"
#NH|    enabled: true
#VM|    
#RJ|    timelapse:
#JP|      enabled: true
#KM|      merge_duration: "24h"
#BN|      frame_source: "rtsp_keyframe"
#QV|```
#HV|
#NH|## 统一录制界面（v2）
#VX|
#XZ|v2 版本将延时摄影和常规录制合并到统一界面中，支持多种视图模式。
#NT|
#QY|### 视图模式
#HJ|
#PQ|通过 URL 参数访问不同视图：
#XK|
#NW|- **表格视图**：`#/recordings?view=table` - 包含详细信息的列表
#BW|- **图库视图**：`#/recordings?view=gallery` - 缩略图画格布局
#RZ|- **日历视图**：`#/recordings?view=calendar` - 基于日历的导航
#VQ|
#RT|### 图库视图
#NX|
#BV|```bash
#VZ|# URL: /#recordings?view=gallery
#PJ|```
#PN|
#XS|响应式网格布局中显示延时摄影录制，包含：
#NV|
#VK|- 缩略图预览
#WN|- 日期/时间标签
#SH>- 延迟加载以提升性能
#WV>- 点击查看/下载录制文件
#QN|
#VP|### 日历视图
#VY|
#BV|```bash
#VW|# URL: /#recordings?view=calendar  
#MN|```
#NT|
#QN|提供基于日历的导航，包含：
#NB|
#BW>- 月/周/日视图
#SJ>- 录制密度可视化
#ZT>- 点击日期过滤录制
#PK>- 时间线导航控件
#RS|
#ZX|### 时间线栏
#BH|
#SQ>在查看延时摄影录制的视图模式选项卡上方：
#XN>
#RH>- 水平时间线显示录制密度
#YQ>- 时间范围选择器（周/月/3个月）
#RJ>- 可点击的时间期间导航
#KT>- 录制可用性的视觉指示器
#JR>
#WY><!-- TODO: Add screenshot of unified Recordings page -->
#MV|
#ZV## 迁移指南
#JM|
#YM### 从延时摄影 v1 迁移到 v2
#PX|
#RR#### 1. 更新配置
#XQ>
#MN**迁移前（v1）：**
#NZ>
#VY>```yaml
#MK>timelapse:
#BV>  enabled: true
#MB>  daily_merge: true
#JB>  output_fps: 30
#WW>```
#XJ>
#KW**迁移后（v2）：**
#BB>
#VY>```yaml
#MK>timelapse:
#BV>  enabled: true
#YS>  merge_duration: "natural-day"  # v2 字段
#QZ>  frame_source: "rtsp_keyframe"   # v2 字段
#JB>  output_fps: 30
#MT>```
#MJ>
#SW#### 2. 新的合并间隔选项
#VQ>
#TR>如果需要不同的合并间隔：
#TZ>
#VY>```yaml
#RP># 从日度合并改为 8 小时合并
#MK>timelapse:
#BV>  enabled: true
#HP>  merge_duration: "8h"            # v2: 灵活间隔
#HN>  frame_source: "rtsp_keyframe"
#JB>  output_fps: 30
#SR>```
#NQ>
#NV#### 3. 现有 RTSP 摄像头的双模式迁移
#XP>
#NW>为现有 RTSP 摄像头启用延时摄影而无需更改其配置：
#TK>
#VY>```yaml
#MY># 迁移前：仅常规录制
#BB>cameras:
#KV>  - name: "现有摄像头"
#TP>    protocol: "rtsp"
#WB>    encoding: "h264"
#HS>    url: "rtsp://192.168.1.100:554/stream"
#NH>    enabled: true
#TM>
#QH>
#MH># 迁移后：为现有摄像头添加延时摄影
#BB>cameras:
#KV>  - name: "现有摄像头"
#TP>    protocol: "rtsp"
#WB>    encoding: "h264"
#HS>    url: "rtsp://192.168.1.100:554/stream"
#NH>    enabled: true
#WQ>
#JJ>
#JJ>    timelapse:                     # 添加此部分
#JP>      enabled: true
#BN>      merge_duration: "natural-day"
#JH>      frame_source: "rtsp_keyframe"  # v2 双模式
#VH>      output_fps: 30
#KS>```
#XK>
#WK#### 向后兼容性
#RY>
#JB>- **现有摄像头无需更改即可继续工作**
#PR>- **遗留的 `daily_merge` 字段仍然可用但已弃用**
#YM>- **现有的延时摄影录制**在统一界面中仍然可以访问
#ZY>- **API 端点**与现有集成保持兼容
#KQ>
#YR#### 迁移检查清单
#MV>
#SR>1. [ ] 审查现有摄像头配置
#XV>2. [ ] 为需要的 RTSP 摄像头添加 `timelapse.enabled: true`
#SK>3. [ ] 设置适当的 `merge_duration`（默认："natural-day"）
#SV>4. [ ] 使用样本摄像头测试双模式功能
#QW>5. [ ] 验证统一录制界面工作正常
#NV>6. [ ] 检查现有录制仍然可以访问
#QT>
#QS## 故障排除
#XQ>
#NJ### 常见问题
#QB>
#VS#### 1. 关键帧提取不工作
#XS>
#ZJ**症状**：延时摄影录制为空或缺少帧
#YM>
#KB**解决方案**：验证摄像头编码和流配置：
#RT>
#BV>```bash
#VJ># 检查摄像头是否支持关键帧提取
#YX>curl -u admin:password "http://localhost:9090/api/cameras/camera-id/status"
#TH>```
#QN>
#JB>确保在摄像头配置中正确指定 H.264/H.265 编码。
#XN>
#VX#### 2. 合并间隔问题
#RM>
#QZ**症状**：合并未按预期间隔运行
#NK>
#SK**解决方案**：检查合并日志并验证间隔格式：
#NN>
#BV>```bash
#TY># 检查合并管理器状态
#QW>curl -u admin:password "http://localhost:9090/api/timelapse/status"
#ZT>
#KQ># 验证配置中的间隔格式
#QS>grep "merge_duration" /path/to/config.yaml
#NJ>```
#XS>
#RB>有效值：`8h`、`12h`、`24h`、`natural-day`、`7d`、`30d`
#TH>
#RB#### 3. 双模式摄像头设置
#MM>
#QT**症状**：双模式摄像头未生成延时摄影录制
#BJ>
#VP**解决方案**：验证双模式配置：
#JW>
#VY>```yaml
#JY># 正确的双模式设置
#BB>cameras:
#QH>  - name: "双模式摄像头"
#WY>    protocol: "rtsp"                    # 必须是 rtsp/onvif
#TW>    encoding: "h264"                    或 "h265"
#HS>    url: "rtsp://192.168.1.100:554/stream"
#NH>    enabled: true
#MB>
#MB>    timelapse:
#WR>      enabled: true                      # 必须启用
#YW>      merge_duration: "24h"             # 设置间隔
#TZ>      frame_source: "rtsp_keyframe"       # 关键帧源
#VH>      output_fps: 30
#VM>```
#XW>
#WX#### 4. ONVIF 流编码
#SQ>
#ZB**症状**：ONVIF 摄像头 H.265 延时摄影不工作
#PS>
#HT**解决方案**：检查 `encoding` 和 `stream_encoding` 字段：
#QB>
#VY>```yaml
#BB>cameras:
#BT>  - name: "ONVIF H.265"
#VY>    protocol: "onvif"
#QN>    encoding: "h265"
#JM>    stream_encoding: "H265"  # ONVIF 特定字段
#WX>    url: "onvif://192.168.1.102"
#NH>    enabled: true
#NX>
#NX>    timelapse:
#JP>      enabled: true
#KM>      merge_duration: "24h"
#BN>      frame_source: "rtsp_keyframe"
#JM>```
#SQ>
#VP### 调试命令
#ZK>
#BV>```bash
#WH># 检查延时摄影管理器状态
#QW>curl -u admin:password "http://localhost:9090/api/timelapse/status"
#JS>
#VY># 列出所有录制文件（延时摄影 + 常规）
#WM>curl -u admin:password "http://localhost:9090/api/recordings"
#JB>
#QK># 检查摄像头延时摄影配置  
#NZ>curl -u admin:password "http://localhost:9090/api/cameras/camera-id"
#SW>
#ZM># 查看合并日志（如果可用）
#XV>journalctl -u mibee-nvr -f | grep merge
#KS>```
#HM>
#TT## 性能考虑
#RR>
#BB### 内存使用
#ZS>
#KX>- **关键帧提取**使用最少的内存（无视频解码）
#RH>- **合并操作**使用 1MB 缓冲的临时文件
#YY>- **RPi 3B 兼容**：最大 512MB 内存预算
#TB>
#WZ### 存储需求
#ZY>
#TM>- **延时摄影文件**通常比原始素材小 90-95%
#WR>- **合并间隔**影响文件大小：
#MX>  - 8 小时合并：每小时素材约 50-100MB
#VJ>  - 24 小时合并：每日素材约 200-400MB
#QW>  - 7 天合并：每周素材约 1-2GB
#HR>
#YX### 网络影响
#TN>
#ZZ>- **双模式**不使用额外的网络带宽
#VV>- **关键帧提取**与现有 RTSP 流配合工作
#SN>- **Web 界面**使用延迟加载高效加载
#XH>
#ST## API 参考
#ZT>
#WP### 延时摄影端点
#RN>
#PM#### 获取延时摄影状态
#VJ>
#BV>```bash
#MQ>GET /api/timelapse/status
#BW>```
#XT>
#HR>响应包含全局延时摄影设置和合并状态。
#YY>
#TH#### 触发手动合并
#QY>
#BV>```bash
#ST>POST /api/timelapse/merge
#PN>```
#QM>
#JQ>可选的 `duration` 查询参数用于指定时间窗口。
#QY>
#YP#### 列出录制文件
#PB>
#BV>```bash
#YW>GET /api/recordings?format=timelapse
#XK>```
#XH>
#VV>列出延时摄影录制文件。在 Web 界面中使用 `view=gallery|calendar`。
#PX>
#KQ### 配置 API
#XT>
#HP>更新摄像头延时摄影配置：
#PN>
#BV>```bash
#VQ>PUT /api/cameras/camera-id
#NQ>{
#QQ>  "timelapse": {
#QP>    "enabled": true,
#BQ>    "merge_duration": "24h",
#ZB>    "frame_source": "rtsp_keyframe",
#RT>    "output_fps": 30
#SR>  }
#NH>}
#MS>```
#QR>
#QH## 最佳实践
#VN>
#SB### 配置技巧
#MQ>
#BQ>1. **根据用例选择合适的合并间隔**：
#KV>   - 安全监控：8 小时或 24 小时用于日度审查
#SQ>   - 商业分析：7 天用于周度模式
#SR>   - 长期存储：30 天用于月度报告
#TS>
#TM>2. **优化输出 FPS**：
#ZV>   - 30 FPS：实时事件
#KQ>   - 15 FPS：日度总结
#TQ>   - 5 FPS：周度概览
#JS>   - 2 FPS：月度报告
#HP>
#ZM>3. **使用 natural-day** 用于对齐本地时间的用户友好日度总结
#TH>
#QJ### 双模式设置
#SZ>
#PK>1. **先在一个摄像头上测试**，然后再在所有摄像头上启用
#PT>2. **监控存储**使用量，特别是增加了录制体积时
#RZ>3. **验证摄像头编码**是否正确指定（H.264/H.265）
#NS>4. **检查流编码**，特别是 ONVIF 摄像头
#QS>
#HV### 性能监控
#JX>
#ZY>1. **定期维护**：根据保留策略清理旧的延时摄影录制
#KW>2. **存储监控**：监控可用磁盘空间，特别是长时间合并时
#RB>3. **系统资源**：在资源受限设备上监控合并操作期间的内存使用
#XW>
#NH## 相关文档
#RJ>
#KW>- [配置参考](configuration.md)
#MV>- [摄像头指南](camera-guide.md)
#XB>- [API 参考](api-reference.md)
#NV>- [故障排除](troubleshooting.md)