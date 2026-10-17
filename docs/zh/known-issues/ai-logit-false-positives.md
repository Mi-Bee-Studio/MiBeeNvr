# 已知问题：AI 检测 "person:100% everywhere" —— logit 爆炸绕过置信度阈值

> **状态**：✅ **已修复**（PR #193）。本文档保留作复盘 / 回归参考。
> **影响范围**：浏览器端 AI 检测（`parseYoloOutput`）——所有相机、所有模型
> （yolo11n / yolo11s）、所有置信度阈值均受影响。
> **影响（修复前）**：直播宫格显示几十到几百个 "100%" 置信度的幻影 `person`
> 框，铺满整个画面——背景、墙面、树叶、天空。每一次调参尝试（类别过滤、
> 置信度 0.5→0.98、模型 nano→s、EMA/maxAge）看起来都毫无作用。真实的人
> 偶尔被检出，却被错分为非 `person` 类（黄框）；背景反倒被自信地标成
> `person`（绿框）。
> **症状看起来完全等同于**：欠训练的模型、不适合该场景的模型、类别过滤
> bug、EMA 平滑问题——这些方向全都排查过、"修复"过、部署过，之后才找到
> 真实原因。

## 问题

在监控宫格上启用 AI 检测后，无论场景内容如何，每个相机瓦片都铺满了 100%
置信度的绿色 `person` 框。调试覆盖层（`AiOverlay` 画布上的 `data-ai-debug`）
揭示了 worker 的实际输出：

```
camera 0 (78 dets): person:100% | person:100% | person:100% | ...  (×78)
camera 2 (260 dets): person:100% | person:100% | person:100% | ... (×260)
```

每一条检测都是 `classId=0`（`person`），`sigmoid→100%`。背景噪声中从未出现
其他类别。与此同时，真实走过场景的人偶尔被框成**黄色**（某个"其他"COCO
类，`classId > 25`），而不是绿色——也就是说，模型最强的*正确*检测甚至没被
标为 `person`。

## 为什么这个问题极难诊断

症状（「AI 把一切都框成 person」）直指「模型不适合这个场景」。但模型/参数
的每一个杠杆都拉过了，毫无效果：

| 尝试的杠杆 | 尝试的值 | 结果 |
|-----------|----------|------|
| 类别过滤（`enabled_classes`） | 全部 80 类 → `["person"]` → `["airplane"]` | 「还是很多绿框」；airplane 过滤证明过滤确实生效（框减少了），但 person 类噪声依然存在 |
| 置信度阈值 | 0.5 → 0.7 → 0.9 → 0.95 → **0.98** | 「还是差不多」——连 0.98 都没能减少绿框 |
| 模型 | yolo11n → yolo11s（mAP 0.703 → 0.746） | 「还是很多」——精度更高的模型没有任何区别 |
| EMA / maxAge | 15 → 8 → 3 → 1 | 缩短了陈旧框的*滞留时间*，但没有减少*源头*数量 |

**这不是**模型质量问题的铁证：**0.98 的置信度阈值没有任何可见效果**。在
正常工作的流水线里，0.98 应该滤掉几乎一切。它没起作用这个事实意味着阈值
比较在执行之前就已经被绕过了。

## 根因：`parseYoloOutput` 中叠加的两个缺陷

### 缺陷 1 —— 置信度阈值在 sigmoid 之后应用，毫无区分度

YOLOv11 默认的 ONNX 导出输出**原始类别 logit**（无界实数，可以是很大的
正值）。原代码：

```js
// inference.ts (BROKEN)
const score = sigmoid(rawScore);   // normalize logit → probability
if (score > maxScore) { ... }
if (maxScore < confidenceThreshold) continue;
```

`sigmoid` 把任何正 logit 都映射到 `> 0.5`。于是一个 `person` logit 微弱为正
（比如 `0.3`）的背景块变成 `sigmoid(0.3) = 0.57`，轻松越过 `0.5` 阈值。
阈值要到约 `0.9` 以上才开始有区分度——而即便如此，缺陷 2 仍把它彻底击穿。

### 缺陷 2 —— logit 没有上界（真正的杀手）

一帧花屏 / 部分损坏的解码帧（H.265 流常见——偶发的解码错误喂给模型的是
噪声而不是像素）会让模型输出**大得离谱的 logit**。故障相机上观测到的值：

```
person raw logits: 62932, 15351, 11356, 2620, 15324, ...
```

`sigmoid(62932)` 在浮点精度下就是 `1.0`。`sigmoid(2620)` 也是 `1.0`。所以
**每一个** logit 爆炸的框都读作 `100%` 置信度，**任何阈值（除非 >1.0）
都无法拒绝它们**。一帧损坏帧就能在一台相机上画出 260 个幻影 `person:100%`
框。这就是 0.98「毫无作用」的原因。

## 修复（PR #193，`inference.ts:parseYoloOutput`）

```js
// Find the best (highest-logit) class.
let maxRaw = -Infinity;
let maxClass = 0;
for (let j = 0; j < numClasses; j++) {
  const raw = data[offset + 4 + j];
  if (raw > maxRaw) { maxRaw = raw; maxClass = j; }
}

// Sanity cap: a well-formed YOLO detection has a class logit in roughly
// -10..+15. Values in the thousands (corrupted/garbled decoded frames) produce
// exploding logits whose sigmoid is indistinguishable from 1.0, swamping any
// threshold and painting dozens of phantom "person:100%" boxes. Reject them as
// decode artifacts.
const MAX_VALID_LOGIT = 15;
if (maxRaw > MAX_VALID_LOGIT) continue;

// Sigmoid to normalize, THEN threshold (a 0.95 threshold ≈ logit 2.94).
const maxScore = sigmoid(maxRaw);
if (maxScore < confidenceThreshold) continue;
```

两处改动：
1. **logit 上界（`MAX_VALID_LOGIT = 15`）**——丢弃损坏帧产生的 logit 爆炸框。
   真实的强检测位于 logit ~5–12（`sigmoid` 0.99–0.99999），远低于上界；只有
   解码伪影框（logit 数以千计）被拒绝。
2. **保留 sigmoid 后阈值比较**（语义清晰：阈值是概率）——上界已经堵住了
   sigmoid 饱和这个漏洞。

## 验证（M5，yolo11n，修复后）

借助调试覆盖层读取 worker 的实际输出：

| 相机 | 修复前 | 修复后 |
|------|--------|--------|
| cam 0（7-8楼转角） | 78 × `person:100%` | `no detections`（空场景） |
| cam 2（视通） | 260 × `person:100%` | `no detections`（空场景） |

空场景现在正确地产生**零个框**。置信度阈值（0.5）终于有了区分度：背景噪声
（logit <0 → sigmoid <0.5）被拒绝；真实检测（logit 5–12 → sigmoid 0.99+）
通过。

## 诊断最终如何破局

突破口是一个**临时调试覆盖层**：把 worker 返回的 `Detection[]`（label +
置信度）渲染进 DOM（`data-ai-debug`），让数据可以通过 DOM 快照读取，而不是
不透明的画布。实际数字一可见——`person:100%` 重复 78–260 次，「去掉
sigmoid」实验揭示原始 logit 高达 **62932**——logit 爆炸的根因一目了然。之前
每次调参都是盲调，因为画布只显示*框*，不显示底层的置信度值或 logit。

**教训**：当阈值「毫无作用」时，要怀疑被比较的值在上游已经饱和 / 损坏——
先在比较之前测量原始数值，别一味调高阈值。

## 相关

- PR #193 —— 修复（`MAX_VALID_LOGIT` 上界 + sigmoid 语义澄清）
- `inference.test.ts` —— 回归测试：logit 爆炸（`62932`）被拒绝，正常强检测
  （`logit 6` → `sigmoid 0.998`）被保留
- 姊妹复盘：`known-issues/ai-onnx-gzip-trailer.md`（#109）——又一个可见症状
  （「模型加载失败」）把诊断带偏、远离真实（中间件）原因的案例
- 调试覆盖层技术（`AiOverlay` → `data-ai-debug` DOM 节点）是一个可复用的
  模式：当画布像素不可读时，用它检查 worker 返回的检测数据；该代码在合并
  前已删除，但在此记录供日后调参时复用
