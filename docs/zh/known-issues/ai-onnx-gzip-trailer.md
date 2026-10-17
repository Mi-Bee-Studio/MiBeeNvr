# 已知问题：AI 检测 `INVALID_PROTOBUF` —— gzip 尾部数据损坏二进制响应

> **状态**：✅ **已修复**（issue #109）。本文档保留作复盘 / 回归参考。
> **影响范围**：浏览器端 AI 检测（ONNX Runtime Web）经流式 gzip 中间件加载
> `/models/*.onnx` 下的任何模型。
> **影响（修复前）**：浏览器收到的每个 ONNX 模型都是原始字节 + 多出来约
> 20 字节的 gzip 尾部数据，于是 ONNX Runtime Web 的 C++ protobuf 解析器
> （编译为 WASM）以 `ERROR_CODE: 7, ERROR_MESSAGE: Failed to load model
> because protobuf parsing failed.` 拒绝加载。AI 检测在任何浏览器、任何
> 模型、任何 ORT 版本上都永久不可用。
> **症状看起来完全等同于**：下载被截断、模型 opset 错误、ORT 版本不匹配、
> 浏览器引擎不兼容——这些方向全都排查并排除过，之后才找到真实原因。

## 问题

部署 ONNX Runtime Web（浏览器端 AI 推理）后，每个相机的实时视图都显示：

```text
AI ✗ Can't create a session. ERROR_CODE: 7, ERROR_MESSAGE:
       Failed to load model because protobuf parsing failed.
```text

这被提交为 issue #109，标题 "AI detection fails to load model: protobuf
parsing failed"。错误来自 ORT 的 C++ 核心（`Model::Load` → protobuf
反序列化），编译进 `ort-wasm-simd-threaded.jsep.wasm` 二进制。

## 为什么这个问题极难诊断

错误信息（`INVALID_PROTOBUF`）直指「模型文件损坏」。但每一项显而易见的
检查都通过了：

| 检查项 | 结果 | （误导性的）结论 |
|--------|------|------------------|
| Python 里的 `onnx.checker.check_model()` | ✅ PASS | 「模型有效」 |
| Python `onnxruntime` 1.28 `InferenceSession(model)` | ✅ PASS | 「模型加载正常」 |
| `curl /models/yolo11n.onnx` 前 16 字节对比磁盘 | ✅ MATCH | 「字节正确」 |
| ORT Web JS 层（`new ort.Tensor(...)`） | ✅ WORKS | 「ORT 没问题」 |
| `WebAssembly.compile(26 MB wasm)` | ✅ 139 个导出 | 「WASM 没问题」 |
| ORT 1.26 **和** 1.27（降级测试） | ❌ 都失败 | 「不是版本问题」 |
| UMD bundle 对比 ESM bundle 对比 CDN | ❌ 都失败 | 「不是打包问题」 |
| `ArrayBuffer` 对比 `Uint8Array` 对比 URL 字符串参数 | ❌ 都失败 | 「不是缓冲区问题」 |
| 内置浏览器（Electron）对比真实 Edge/Chrome | ❌ 都失败 | 「不是浏览器引擎问题」 |
| 5 个不同模型（86 B 的 tiny、yolo11n、官方 yolo、squeezenet、Add 算子） | ❌ 都失败 | 「不是模型问题」 |

浏览器取回的缓冲区前 16 字节与磁盘文件**完全一致**——这正是所有人（包括
ORT 自己的维护者在 issue [microsoft/onnxruntime#13117](https://github.com/microsoft/onnxruntime/issues/13117)
中）首先建议的检查。但该检查**必要而不充分**：损坏在文件**末尾**，不在
开头。

## 根因

流式 gzip 中间件（`internal/middleware/compress.go`）为每个响应包上一个
`gzip.Writer`，并在 handler 运行前预设 `Content-Encoding: gzip`。二进制内容
（`application/octet-stream`，如 `/models/*.onnx`）的预期流程是：

1. 中间件设置 `Content-Encoding: gzip`，创建 `gz`，`defer cw.Close()`。
2. `http.ServeFile` 设置 `Content-Type: application/octet-stream`。
3. `WriteHeader` 检测到 octet-stream → `shouldSkipCompression=true` → 删除
   `Content-Encoding`，调用底层的 `WriteHeader`。
4. `Write` 检测到 octet-stream → **绕过** `gz`，把原始字节直接写入底层的
   `ResponseWriter`。✓
5. **`defer cw.Close()` 执行 → 调用 `gz.Close()`**——作用在一个**从未被
   写入过**的 gzip writer 上。

**bug 出在第 5 步。** 对一个从未动过的 writer 调用 `gzip.Writer.Close()`
仍会向底层 writer 输出一个完整、合法的**空** gzip 成员：`1f 8b 08 00 ...`
魔数头 + 空 deflate 流 + 8 字节 trailer（CRC32 + ISIZE），合计约 20 字节。
由于原始响应体已在第 4 步写出，这个空 gzip 帧被**追加**到响应末尾——静默
损坏每一个二进制下载。

浏览器总是发送 `Accept-Encoding: gzip`，所以每一次二进制获取（视频分段
下载、ONNX 模型加载、快照、录像下载）都被静默追加了 20 字节的 gzip 垃圾。
对媒体容器（MP4/MPEG-TS）来说，多出的 20 字节通常能被容忍（播放器读到
moov/mdat 结束即停）；而 ONNX 是带严格长度前缀的单条 protobuf 消息，尾部
数据会让 protobuf 解析器读过消息的真实结尾而失败。

### 铁证

把浏览器取回字节的**完整 md5**（而不只是开头）与磁盘字节对比，暴露了
损坏：

```text
browser fetched md5 : ee7b0660585856f7e37a10b8c5b29edd   ← 10741417 bytes (corrupted)
disk md5 (correct)  : df8e6fccb7797fd340b6bb1358a53394   ← 10741397 bytes
                                                                       ↑ +20 bytes
```text

损坏文件**最后 32 字节**的十六进制转储：

```text
... 46 61 6c 73 65 [1f 8b 08 00 00 09 6e 88 00 ff 03 00 00 00 00 00 00 00 00 00]
                    ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
                    empty gzip member appended by gz.Close() on an unused writer
```text

`curl -H "Accept-Encoding: gzip"` 从服务端复现了损坏（完全不涉及浏览器），
确认这是服务端 bug，不是客户端 bug。

## 修复

`internal/middleware/compress.go`：跟踪 gzip writer 是否真的被用过
（`wroteGzip` 旗标，首次 `gz.Write` 时置位）。只有 `wroteGzip == true` 时才
调用 `gz.Close()` 和 `gz.Flush()`。

```go
type compressWriter struct {
    w             http.ResponseWriter
    gz            *gzip.Writer
    contentType   string
    headerWritten bool
    wroteGzip     bool  // NEW: did we actually write any bytes through gz?
}

func (cw *compressWriter) Write(b []byte) (int, error) {
    if shouldSkipCompression(cw.contentType) {
        return cw.w.Write(b)
    }
    cw.wroteGzip = true
    return cw.gz.Write(b)
}

func (cw *compressWriter) Flush() {
    if !shouldSkipCompression(cw.contentType) && cw.wroteGzip {
        _ = cw.gz.Flush()
    }
    // ...flush underlying writer...
}

func (cw *compressWriter) Close() error {
    if !cw.wroteGzip {
        return nil  // do NOT call gz.Close() — it would emit an empty gzip member
    }
    return cw.gz.Close()
}
```text

这是一个**一次性的收尾守卫**——只要有过任何字节经过 `gz`，该旗标在响应
剩余生命周期内保持为 true，因此正常的压缩响应（JSON、HTML、SSE）仍会正确
flush 和收尾。

### 回归覆盖

`internal/middleware/compress_issue_test.go` ——
`TestStreamingGzip_BinaryNoGzipTrailer`：带着 `Accept-Encoding: gzip` 经
中间件服务一个 `application/octet-stream` 载荷，并断言 (a) 响应体与原始
载荷完全相等（无多余字节）、(b) `Content-Encoding` 为空、(c) 最后 20 字节
不是 gzip 帧（`1f 8b ...`）。这正是该 bug 的精确指纹，能抓住任何重新引入
无条件 `gz.Close()` 的回归。

既有测试（`TestStreamingGzip_JSONResponse`、`TestStreamingGzip_SSEFlush`、
`TestStreamingGzip_SkipsVideo`）全部仍然通过——修复只改变「gzip writer 已
分配但从未使用」路径的行为，而没有任何既有测试端到端覆盖过这条路径（它们
检查了 `Content-Encoding` 头是否存在，但没检查响应体的字节相等性）。

## 经验教训

1. **`gzip.Writer.Close()` 不是免费的。** 对一个从未 `Write()` 过的 writer
   调用 `Close()` 仍会产生输出——一个完整的空 gzip 成员（约 20 字节）。任何
   「乐观地分配 gzip writer、再按条件绕过它」的中间件，必须同样按条件跳过
   `Close()`。这条同样适用于 `deflate`、`zlib`、`brotli`、`zstd` 的 writer
   （它们都会在 `Close()`/`Flush()` 时输出头/尾）。

2. **开头字节相等是必要条件但不充分。** 校验二进制传输时，应把收到的字节
   与源做**完整内容哈希（md5/sha256）**对比——而不只是前 N 字节。损坏可能
   发生在流的任何位置，而 HTTP 层的问题（chunked 编码 bug、压缩尾部数据、
   range 拼接错误）经常损坏**尾部**而头部完好。

3. **编译成 WASM 的 C++ 报错信息无法调试。** ORT Web 的
   `INVALID_PROTOBUF` 来自编译为 WASM 的 C++；没有源码级堆栈，唯一的诊断
   信息就是错误码 + 一条泛化消息。当运行时说「输入已损坏」时，**要相信
   它**——对输入做端到端字节校验（浏览器上下文 → 服务端响应 → 磁盘文件），
   而不是只信任其中某一层。

4. **用真实浏览器（CDP）调试浏览器端故障。** 内置浏览器（Electron
   webview）和真实 Chrome/Edge 都能复现服务端 bug，但只有通过 CDP 驱动的
   真实浏览器能让你执行任意 `Runtime.evaluate` 来算 md5、检查缓冲区、对比
   字节——IAB 的 `evaluate` 被一个副作用守卫锁死，连只读的 `localStorage`
   访问都被挡。对任何「Python 里正常、浏览器里失败」的 bug，用
   `msedge.exe --remote-debugging-port=9222` + `chrome-remote-interface`
   驱动真实浏览器，检查页面实际收到的字节。

5. **中间件管线是全局变换器。** 任何经 `r.Use(...)` 挂载的东西都会跑在
   **每一条**路由上，包括公开的二进制下载路由（`/models/*`、
   `/api/recordings/{id}/download`）。压缩中间件尤其必须针对二进制响应
   测试，而不只是 JSON/HTML。既有的 `TestStreamingGzip_SkipsVideo` 测试
   检查了视频的 `Content-Encoding` 头被清掉，但没有检查响应**体**里有没有
   多余字节——正是这个缺口让 bug 溜上了线。

## 验证（Banana Pi M5，真实 Edge 浏览器）

通过 CDP（`msedge.exe --remote-debugging-port=9222`）驱动一个真实的
Edge 150 实例访问 M5 部署：

- **md5 一致**：`curl -H "Accept-Encoding: gzip" --compressed
  /models/yolo11n.onnx | md5sum` = `df8e6fcc...` = 磁盘 md5（修复前是
  `ee7b0660...`）。
- **ORT 会话创建**：`ort.InferenceSession.create(buf, {executionProviders:
  ['wasm']})` → `SESSION OK inputs=["images"]`（之前是 `ERROR_CODE: 7`）。
- **实时视图 AI 指示器**：视通相机（H.265，在线）在状态栏显示**「AI
  就绪」**（之前是 "AI ✗ ERROR_CODE 7"）。截图确认视频在播、绿色 AI 就绪
  徽章可见。
- 所有既有 `TestStreamingGzip_*` 测试仍然通过；新增了回归测试。

## 相关

- Issue #109 —— 原始报告（"AI detection fails to load model: protobuf
  parsing failed"）。
- `internal/middleware/compress.go` —— 修复。
- `internal/middleware/compress_issue_test.go` —— 回归测试。
- [microsoft/onnxruntime#13117](https://github.com/microsoft/onnxruntime/issues/13117)
  —— ORT 维护者的（正确但不完整的）指引：「检查服务器是否正确地提供该
  文件」。本复盘是该指引在本代码库的具体化。
- `docs/en/troubleshooting.md` / `docs/zh/troubleshooting.md` →
  「AI Detection」小节 —— 面向用户的症状 + 修复步骤。
