# Eval replay — 门控/评分评估框架(#639)

对"录像门控/活动评分"类改动的**准入门槛**:任何 scorer/gate 参数变更的 PR
必须附 corpus 回放对比表(判据:静态段 TL 占比不劣化、真实活动段不漏、
雨天/低码率段全速时长显著下降)。

## 组成

- `mibee-nvr eval-replay --corpus <manifest.json>` — CLI 子命令(`cmd/mibee-nvr/eval_replay.go`)
  - 默认:scorer 回放(每段 score / medianP / confidence / effective)
  - `--gate`:门控回放(每段 TL 占比 / 切换次数 / 学到的噪声地板)
  - `--gate --spike N --noisefloor-bytes N --autonoise=false --videoexit=false`:
    候选配置 vs 默认配置并排对比
- `internal/recorder/replay.go` — `ReplayAdaptive` 门控回放接口
- corpus manifest(JSON 数组,路径相对 manifest 所在目录):

```json
[
  {"path": "/mnt/data/nvr/cam-xxx/202608/30/10/seg.mp4", "camera": "视通-9楼", "label": "rain"}
]
```

## 标签约定

| label | 含义 | 期望形状 |
|---|---|---|
| `rain` | 雨天/积水反光的持续扰动 | 门控改进后 TL 占比显著上升 |
| `lowbitrate` | 相机码率坍缩段(夜间 IR) | scorer 置信度→0;门控不被抖动打回全速 |
| `static` | 已知静态无人 | TL 占比保持高位(≥现状) |
| `active` | 已知真实活动(人/车) | 门控能退出全速;score 有效 |

## 语料来源与登记

- 语料文件**不进 git**——引用现网(M5)保留的录像样本,manifest 按需生成;
- 每批样本在下方登记(日期 / 相机 / 标签 / 采集人):
  - 2026-08-31: 视通-9楼 雨天白天 / 七楼门外 夜间低码率 / 工作室 静态 —— 7 天回归窗口首批(待生成 manifest);
- 12h 回归巡检的实测数字应回填到对应标签的备注,校准离线结论。

## 生成 manifest(在 M5 上)

```bash
# 例:视通保留的 3 天里每小时抽 1 段,标 rain(雨期采集)
ssh mickey@192.168.63.30 'python3 - <<EOF
import sqlite3, json, random
db = sqlite3.connect("file:/mnt/data/nvr/mibee-nvr.db?mode=ro", uri=True)
rows = db.execute("""select file_path from recordings
  where camera_id="cam-5b24e253-0808-499d-bf4c-dbe9a8b2e3aa" order by started_at""").fetchall()
print(json.dumps([{"path": r[0], "camera": "shitong", "label": "rain"} for r in rows], indent=1))
EOF' > rain.json
```
