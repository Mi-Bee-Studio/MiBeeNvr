# 发布说明规范（Release Notes Specification）

> 每次发版（打 `vX.Y.Z` tag）后,**手动撰写**发布说明,不要直接采用 CI 自动生成的朴素 PR 列表。
> 本文件是发布说明的格式规范与模板。参考实现:`v0.10.0` / `v0.10.1` 的 release body。

## 为什么不用自动生成的 release notes

`release.yml` 里 `generate_release_notes: true` 会产出一个纯 PR 标题列表。它对维护者有用(快速回顾改动),但**对用户没有价值**——用户不会去读一堆 `fix(xxx): ...` 的提交标题。发布说明应当:

1. **讲用户能感知的变化**(功能/修复/破坏性),不是讲代码层提交。
2. **带品牌**(logo banner)与**导航**(镜像 tag、安装命令、升级指引),降低上手成本。
3. **中英双语**(项目面向国内 NAS 用户 + GitHub 国际用户)。

所以发版流程是:CI 跑完产出二进制/镜像 + 自动 PR 列表 → **维护者手动编辑 release body**,按下述规范重写。

---

## 模板

复制下面的结构,填入实际内容。带 `<>` 的是占位符。

```markdown
# <版本号> — <一句话版本定位>

<p align="center"><img src="https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/<版本tag>/docs/brand/logo-package.svg" width="120" alt="MiBee NVR" /></p>

> <emoji> **<版本号> 发布。** <2-3 句话说明这个版本的性质(正式版/补丁版/预览版)、
> 相对上一版本的核心价值、是否推荐升级。>

> Docker 镜像:`ghcr.io/mi-bee-studio/mibeenvr:<版本号>`(或 `:latest`),多架构 amd64/arm64/armv7。
> <可选:飞牛 fnOS 包 / 其它平台特殊说明>

---

## <emoji> <主题分组 1>(<涉及的 PR 号>)

<一段话说明这组改动的背景与价值,然后列要点。>

- **<要点标题>**(<PR 号>):<用户能感知的变化>。
- ...

---

## <emoji> <主题分组 2>(<PR 号>)

...

---

## ⚠️ 破坏性变更(若有)

<如果本版本有不兼容变更,在这里明确列出 + 升级指引链接。若无,删掉本节。>

---

## 📦 安装 / 升级

​```bash
# Docker(NAS 推荐 host 网络)
docker run -d --network host \
  -v /mnt/data/mibee-nvr:/data \
  -e NVR_DATA_DIR=/data \
  --name mibee-nvr --restart unless-stopped \
  ghcr.io/mi-bee-studio/mibeenvr:<版本号>

# 一键安装(systemd 二进制)
curl -fsSL https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/main/install.sh | sudo bash
​```

<可选:飞牛/特定平台安装提示>

---

## English summary

**<版本号> <性质>** — <一句话英文定位>。

**Highlights:**
- **<要点>** (<PR 号>): <英文说明>。
- ...

**Images:** `ghcr.io/mi-bee-studio/mibeenvr:<版本号>` / `:latest` — multi-arch amd64/arm64/armv7.
```

---

## 撰写要点

### 内容原则

- **面向用户,不是面向提交**:写「修了飞牛桌面打不开」,不写「refactor SecurityHeaders middleware」。
- **分组讲价值**:用主题分组(如「飞牛桌面修复」「并发加固」「AI 误报根治」),每组先讲背景再列要点。
- **PR 号要带**:每个要点末尾标 `<PR 号>`,方便追溯。
- **破坏性变更单独成节**:不兼容变更必须在显眼的 ⚠️ 节里列出,并给升级指引链接。无则删节。
- **中英双语**:中文为主体(国内 NAS 用户为主),末尾 `## English summary` 给精简英文版。

### 格式细节

- **logo banner**:用 raw.githubusercontent 引用**该版本 tag** 的 `docs/brand/logo-package.svg`(不要用 `main`,避免后续 logo 变动影响历史 release)。
- **emoji 分组标题**:用 emoji 让分组醒目(🖥️ 平台 / 🛡️ 安全 / 🐛 修复 / ⚠️ 破坏性 / 📦 安装)。
- **镜像 tag**:正文顶部 blockquote 和底部 English summary 都写明 `<版本号>` 和 `:latest`。
- **安装命令**:Docker + 一键脚本两条,host 网络优先(NAS 场景)。

### 补丁版 vs 正式版

- **补丁版**(`0.10.1` 这类):精简,聚焦修复。通常 1-2 个主题分组 + 其他修复。强调「建议升级」。
- **正式版**(`0.10.0` 这类):完整,覆盖功能/架构/分发/安全/破坏性变更,可作为里程碑回顾。

---

## 发版操作流程

1. **打 tag 前**:按 `docs/private/release-test-checklist.md` 完成实测(gitignored,本地清单)。
2. **打 tag 推送**:`git tag vX.Y.Z && git push origin vX.Y.Z` → 触发 `release.yml`。
3. **等 CI 完成**:确认二进制 + 多架构镜像推送成功。
4. **手动重写 release body**:`gh release edit vX.Y.Z --notes-file <按本规范写的 md>`。
   - CI 自动生成的 PR 列表会被覆盖——这是预期的。需要 PR 清单可从 `gh pr list` 或 compare 链接获取。
5. **上传 NAS 资产**(如涉及):fpk / 各平台 zip,见 `deploy/fnos/build.sh` 与历史发版的资产打包流程。
```
