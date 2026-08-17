# WHIP 推入式接入（WebRTC）

WHIP（WebRTC-HTTP Ingest Protocol）让浏览器、手机或 OBS 30+ 以 WebRTC
**推流**进 NVR —— H.264 视频 + Opus 音频，零转码。与 SRT/RTMP 接入不同，
发布端无需网络直连：ICE/STUN/TURN 穿透意味着远程贡献者无需端口暴露、
frp 或 VPN 即可跨网络推流。

| | SRT / RTMP | WHIP |
|---|---|---|
| 浏览器当源 | ✗（浏览器无法推 SRT/RTMP） | ✓ `getUserMedia` |
| OBS 内置客户端 | ✓（手工配 URL） | ✓（设置 → 推流 → WHIP） |
| 音频 | — | ✓ Opus 录制 + 直播 |
| NAT 穿透 | 需直连 | ✓ ICE/STUN/TURN |

## 启用

```yaml
whip:
  enabled: true
```

端点复用主 HTTP 监听 —— 不需要额外端口。

## 添加推流相机

摄像头 → 添加 → 协议选 **WHIP (WebRTC push)**，设置**流密钥**（如
`door-cam`）。表单会显示推流地址：

```
http://<nvr-ip>:9090/whip/door-cam
```

流密钥即凭证 —— 与 RTMP 推流密钥 / SRT `streamid` 同一威胁模型。知道
密钥的人才能推流；不知道的收到 404。

## 推流

**OBS 30+**：设置 → 推流 → 服务：*WHIP* → 服务器：
`http://<nvr-ip>:9090/whip/door-cam` → 开始推流。视频需为 H.264；音频为
Opus。

**浏览器 / 手机**：任何 WHIP 客户端库指向同一地址，配合
`getUserMedia`。录制/直播管线与 OBS 完全一致。

发布端在线期间，相机与普通相机无异：分段录制（MP4 内 H.264 + Opus）、
全协议直播预览、推流保留策略照常生效。每相机仅一个发布端 —— 第二个
并发发布会被 409 拒绝。

## 说明

- **远程发布端（跨网络）**：外网访问请将 NVR 置于 TLS 之后（见
  `remote-access.md`）并配置 `streaming.webrtc.ice_servers`
  （STUN/TURN）—— 与 WHEP 观看端共用同一套基础设施。
- **仅 H.264**，与 WHEP 出口一致（浏览器 WebRTC 的 H.265 支持仍碎片化）。
- **空闲发布端**会被回收：30 秒内未发 RTP、或推流中停滞 60 秒的会话
  自动拆除。
- 录制器与 SRT/RTMP 监听共用同一个 `IngestRecorder` —— 分段滚动、
  SPS/PPS 处理、StreamHub 分发全部共享。

## API

- `POST /whip/{streamKey}` —— SDP offer（Content-Type `application/sdp`）
  → `201 Created` + SDP answer + `Location` 头（会话 URL）
- `DELETE /whip/{streamKey}/{session}` —— 拆除会话
- `GET /api/capabilities` → `ingest.whip.enabled` 反映配置
