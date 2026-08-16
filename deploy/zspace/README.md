# MiBee NVR — 极空间 Docker Compose 模板

极空间（ZSPACE）Docker 编排一键导入模板，无需手抄 Compose。

## 导入步骤

1. 下载本目录的 `docker-compose.yml`。
2. 极空间 **Docker → 编排 → 创建编排**，名称 `mibee-nvr`。
3. 粘贴文件内容（或上传文件），按需修改存储路径。
4. **创建/启动**。多架构镜像自动按 CPU 架构拉取。
5. 浏览器打开 `http://<极空间IP>:9090` 完成初始化向导。

## 按需调整

- **存储位置**：把 volume 左侧改成空间充足的目录（建议 HDD）。
- **端口冲突**（9090 被占）：取消注释 `NVR_LISTEN_PORT=8080` ——
  或之后在 Web UI 里改（设置 → 通用，#270）。
- **镜像源**：默认走阿里云镜像（国内免登录拉取）；海外可换回 ghcr。

## 为什么用 host 网络

ONVIF 自动发现依赖 UDP 组播，Docker bridge 模式不转发；
`network_mode: host` 解决。host 模式下不要同时写 `ports:`。

完整文档：[docs/zh/deployment-zspace.md](../../docs/zh/deployment-zspace.md)
