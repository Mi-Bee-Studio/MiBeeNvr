# 独立音频源

NVR 把音频当作一等公民录制,不依附于任何摄像头:音源像摄像头一样入册
(协议 RTSP 或 WHIP,编码选**音频**),用自己的分段和留存策略录制,并可选地
关联到一路视频摄像头做同步回放。摄像头自身流里的音频请看
[音频](audio.md)。

可录制编码:**G.711**(A-law/μ-law——支持最完整的路径:WAV 转码、实时监听、
时间轴同步)、**AAC** 与 **Opus**(录制后原生播放)。

## 添加音源并探测

摄像头 → 添加 → 协议 **RTSP** → 编码**音频**,粘贴流地址后按
**测试连接**。探测会真实连接并对 URL 做 DESCRIBE,报告流里实际有什么:

- `audio source ready: PCMA 8000Hz`——找到了可录制的音频轨;
- `codec G726 is not supported`——有音频,但不是 G.711/AAC/Opus;换一条流
  (子码流常常不同)或在上游转码;
- `no audio track`——该地址只有视频。

视频摄像头同样受益:探测现在会把流的音频布局(`audio: PCMA 8000Hz`)
连同视频编码一起报出来。

## 家族一:网络拾音器(IP 麦克风)

专用 IP 麦克风和摄像头音频子码流都说 RTSP。常见 URL 模式(子码流序号因
设备而异;`<user>`/`<pass>`/`<ip>` 为占位符):

| 厂商 | URL 模式 |
|------|----------|
| 海康 | `rtsp://<user>:<pass>@<ip>:554/Streaming/Channels/102` |
| 大华 | `rtsp://<user>:<pass>@<ip>:554/cam/realmonitor?channel=1&subtype=1` |
| Axis | `rtsp://<user>:<pass>@<ip>/axis-media/media.3gp?audio=1` |
| 通用 | `rtsp://<user>:<pass>@<ip>/live` · `rtsp://<user>:<pass>@<ip>/stream` |

把候选地址粘进表单探测一下——报出来的编码才是权威(设备手册经常写错)。
多数拾音器是 G.711;只出其它编码的设备会被明确点名。

## 家族二:旧手机当房间麦克风

退役手机通过浏览器采集麦克风、经 [WHIP](whip-ingest.md) 推流,就是个够用
的 Wi-Fi 麦克风,无需装任何 App。

1. 建音源:摄像头 → 添加 → 协议 **WHIP(WebRTC 推入)** → 编码**音频**,
   流密钥如 `room-mic`。表单会显示端点 URL,如
   `http://<nvr-ip>:9090/whip/room-mic`。
2. 把下面这个单文件页面存到手机上用浏览器打开(本地文件在
   Chrome/Edge/Firefox 里算安全上下文,可以采集麦克风;若浏览器拒绝,
   改用 HTTPS 提供此页面):

```html
<!doctype html>
<meta name="viewport" content="width=device-width">
<title>mic</title>
<button id="b">&#127908; 开始</button> <span id="s"></span>
<script>
const ENDPOINT = 'http://<nvr-ip>:9090/whip/room-mic';
const pc = new RTCPeerConnection();
document.getElementById('b').onclick = async () => {
  const s = document.getElementById('s');
  try {
    const mic = await navigator.mediaDevices.getUserMedia({ audio: true });
    pc.addTransceiver(mic.getAudioTracks()[0], { direction: 'sendonly' });
    const offer = await pc.createOffer();
    await pc.setLocalDescription(offer);
    await new Promise(r => pc.onicegatheringstatechange = () =>
      pc.iceGatheringState === 'complete' && r());
    const res = await fetch(ENDPOINT, {
      method: 'POST',
      headers: { 'Content-Type': 'application/sdp' },
      body: pc.localDescription.sdp,
    });
    if (!res.ok) throw new Error('WHIP ' + res.status);
    await pc.setRemoteDescription({ type: 'answer', sdp: await res.text() });
    s.textContent = '推流中';
  } catch (e) { s.textContent = String(e); }
};
</script>
```

3. 点开始——手机的 Opus 音频像其它音源一样落库(浏览器需保持前台;
   省电模式会暂停采集)。

是否把这个页面内置进 Web UI 属于待定的产品决策;目前以复制粘贴的
形式放在这里。

## 家族三:ESP32-S3 + I2S 麦克风节点

自建节点是固定点位最省钱的路径。常见接线:

| 部件 | 说明 |
|------|------|
| ESP32-S3 开发板 | 任意型号;2.4 GHz Wi-Fi |
| INMP441 I2S MEMS 麦克风 | WS→GPIO 4,SCK→GPIO 5,SD→GPIO 6,L/R→GND(左声道单声道) |
| 供电 | USB 5 V;持续约 100 mA |

固件:围绕 esp-adf / ESP-IDF I2S 驱动采集 PCM 并推流的社区方案——选
**G.711 8 kHz 单声道 over RTSP** 的项目(对本 NVR 兼容性最好:WAV 转码、
实时监听、时间轴同步全可用),或经 WHIP 推 Opus。采集缓冲保持小
(20–40 ms),重连时丢的音频就少。

## 把音源关联到摄像头

编辑视频摄像头,选择关联音频源(表单会给出候选)。收益:

- 录像详情页出现**伴随面板**——视频窗口附近的音源分段、同步播放,以及
  ±5 秒手动偏移滑杆(按摄像头对持久化),适配没有共同时钟的设备;
- 录像日时间轴在关联摄像头的行上,把音源分段画成绿色音频轨;点击条带
  打开对应的音频录像。

不关联也完全可以——音源独立存在于录像库。

## 下一步

- [音频](audio.md)——摄像头自带音频的录制、实时监听、双向对讲
- [WHIP 推入](whip-ingest.md)——手机和 OBS 发布用的推入端点
- [录像与回放](recording-playback.md)——日时间轴与录像库
