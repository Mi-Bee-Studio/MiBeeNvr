#!/usr/bin/env bash
#
# MiBee NVR — Unified online installer.
#
# Auto-selects the fastest reachable registry (GitHub ghcr vs Alibaba Cloud
# ACR) so mainland-China NAS users get ACR's fast anonymous pull and overseas
# users get ghcr, with no manual choice. Works across NAS platforms (fnOS /
# QNAP / Synology / ZSpace / iStoreOS) and generic Linux.
#
# Host networking is REQUIRED: ONVIF WS-Discovery uses UDP multicast, which
# Docker's default bridge blocks — camera auto-discovery fails without it.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/main/deploy/install-online.sh | bash
#
#   # Pin version / data dir / port:
#   ./install-online.sh --tag 0.10.1 --data-dir /volume1/docker/mibee-nvr/data --port 9090
#
#   # Force a specific registry (skip auto-detect). Pass the full image prefix
#   # without the tag:
#   ./install-online.sh --registry ghcr.io/mi-bee-studio/mibeenvr
#   ./install-online.sh --registry registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr
#
# Requires: bash, docker (daemon running), curl.
set -eu

CONTAINER="mibee-nvr"
GHCR_PREFIX="ghcr.io/mi-bee-studio/mibeenvr"
ACR_PREFIX="registry.cn-hangzhou.aliyuncs.com/mickeybeehome/mibee-nvr"
TAG="0.10.1"
DATA_DIR=""
PORT="9090"
REGISTRY=""          # empty = auto-detect

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }

# ---- arg parsing ----
while [ $# -gt 0 ]; do
    case "$1" in
        --tag)        TAG="$2"; shift 2 ;;
        --data-dir)   DATA_DIR="$2"; shift 2 ;;
        --port)       PORT="$2"; shift 2 ;;
        --registry)   REGISTRY="$2"; shift 2 ;;
        -h|--help)
            sed -n '3,25p' "$0" | sed 's/^# \{0,1\}//'
            exit 0 ;;
        *) error "未知参数: $1（用 --help 查看用法）"; exit 1 ;;
    esac
done

# ---- preflight ----
command -v docker >/dev/null 2>&1 || { error "未找到 docker。请先安装 Docker / Container Station / Container Manager。"; exit 1; }
docker info >/dev/null 2>&1 || { error "docker daemon 未运行，请先启动 Docker 服务。"; exit 1; }
command -v curl >/dev/null 2>&1 || { error "未找到 curl。"; exit 1; }

# ---- arch (diagnostic only; the multi-arch manifest auto-selects at pull time) ----
# NOTE: armv7l/armhf map to armv7 here, NOT arm64 — a 64-bit binary cannot run on
# a 32-bit ARM userspace. See issue #312.
detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64|x64) echo "amd64" ;;
        aarch64|arm64)    echo "arm64" ;;
        armv7l|armhf)     echo "armv7" ;;
        *) echo "unknown-$(uname -m)" ;;
    esac
}
ARCH="$(detect_arch)"
info "检测到架构: ${ARCH}（多架构 manifest 会自动匹配）"

# ---- registry auto-detect: race ghcr vs ACR, pick the faster reachable one ----
# Probes each registry's /v2/ endpoint concurrently; the multi-arch image lives
# behind the same manifest list at both, so reachability+latency is a fair proxy.
probe() {  # probe <url>  → prints latency seconds or "fail"
    curl -s -o /dev/null -w '%{time_total}' \
         --connect-timeout 3 --max-time 6 "$1" 2>/dev/null || echo "fail"
}

choose_registry() {
    # User-forced registry wins (they pass the full image prefix).
    [ -n "$REGISTRY" ] && { echo "$REGISTRY"; return; }

    info "并发探测最优 registry（GitHub ghcr vs 阿里云 ACR）..."
    local tmp; tmp="$(mktemp -d)"
    ( probe "https://ghcr.io/v2/"                            > "$tmp/ghcr" ) &
    ( probe "https://registry.cn-hangzhou.aliyuncs.com/v2/"  > "$tmp/acr"  ) &
    wait
    local g a; g="$(cat "$tmp/ghcr")"; a="$(cat "$tmp/acr")"
    rm -rf "$tmp"

    if [ "$g" = "fail" ] && [ "$a" = "fail" ]; then
        error "两个 registry 都不可达，请检查网络。"
        error "可手动指定: --registry ghcr.io/mi-bee-studio/mibeenvr"
        exit 1
    fi
    if [ "$g" = "fail" ]; then echo "$ACR_PREFIX";  info "ghcr 不可达 → 用阿里云 ACR"; return; fi
    if [ "$a" = "fail" ]; then echo "$GHCR_PREFIX"; info "阿里云 ACR 不可达 → 用 ghcr"; return; fi
    # both reachable → lower latency wins (ACR tie-break since CN users benefit)
    if awk -v a="$a" -v g="$g" 'BEGIN{exit !(a < g)}'; then
        echo "$ACR_PREFIX";  info "阿里云 ACR 更快（${a}s < ${g}s）→ 用 ACR"
    else
        echo "$GHCR_PREFIX"; info "ghcr 更快（${g}s ≤ ${a}s）→ 用 ghcr"
    fi
}

REG_PREFIX="$(choose_registry)"
IMAGE="${REG_PREFIX}:${TAG}"
info "使用镜像: ${IMAGE}"

# ---- data dir ----
if [ -z "$DATA_DIR" ]; then
    cat <<EOF

录像 / 数据库 / 配置 / 模型 会持久化到数据目录。各 NAS 推荐路径:
  Synology DSM : /volume1/docker/mibee-nvr/data
  QNAP QTS     : /share/Container/mibee-nvr/data
  飞牛 fnOS    : /vol1/@appdata/mibee-nvr/data
  极空间 ZOS   : <你的存储路径>/mibee-nvr/data
  iStoreOS     : /mnt/sata1/mibee-nvr/data   (务必用外接存储)
  通用 Linux   : /opt/mibee-nvr/data
EOF
    if [ -t 0 ]; then
        read -rp "数据目录 [$(pwd)/mibee-nvr-data]: " DATA_DIR
        DATA_DIR="${DATA_DIR:-$(pwd)/mibee-nvr-data}"
    else
        DATA_DIR="$(pwd)/mibee-nvr-data"
        warn "非交互模式，数据目录默认 ${DATA_DIR}（可用 --data-dir 指定）"
    fi
fi
mkdir -p "$DATA_DIR"
info "数据目录: ${DATA_DIR}"

# ---- port validation ----
case "$PORT" in
    ''|*[!0-9]*) error "无效端口: ${PORT}"; exit 1 ;;
esac
if [ "$PORT" -lt 1 ] || [ "$PORT" -gt 65535 ]; then error "端口超出范围 (1-65535): ${PORT}"; exit 1; fi
info "Web/API 端口: ${PORT}（host 网络，直接占宿主端口）"

# ---- stop & remove any existing container ----
if docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null | grep -q true; then
    warn "检测到运行中的 ${CONTAINER} 容器，先停止并移除（数据目录不受影响）..."
    docker stop "$CONTAINER" >/dev/null 2>&1 || true
    docker rm   "$CONTAINER" >/dev/null 2>&1 || true
fi

# ---- pull + run ----
info "拉取镜像（多架构，自动匹配 ${ARCH}）..."
# ACR manual pushes sometimes carry a 'v' prefix (v0.10.1) while ghcr/CI use the
# bare version (0.10.1). Try the bare tag first; if ACR doesn't have it, fall
# back to the v-prefixed tag so both naming conventions work.
if [ "$REG_PREFIX" = "$ACR_PREFIX" ] && ! docker pull "$IMAGE" >/dev/null 2>&1; then
    info "ACR 没有 ${TAG}，回退尝试 v${TAG}..."
    IMAGE="${ACR_PREFIX}:v${TAG}"
fi
docker pull "$IMAGE"

info "启动容器（host 网络）..."
# frame-ancestors: allow http/https embedders so NAS desktops can iframe the UI.
# Tighten via mibee-nvr.yaml (security.frame_ancestors) if your threat model needs.
docker run -d \
    --name "$CONTAINER" \
    --restart unless-stopped \
    --network host \
    -e NVR_DATA_DIR=/data \
    -e "NVR_LISTEN_PORT=${PORT}" \
    -e "NVR_FRAME_ANCESTORS='self' http: https:" \
    -v "${DATA_DIR}:/data" \
    "$IMAGE" >/dev/null

# ---- verify ----
sleep 3
if docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null | grep -q true; then
    IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
    IP="${IP:-<NAS-IP>}"
    echo ""
    echo -e "${GREEN}========================================${NC}"
    echo -e "${GREEN}  MiBee NVR 已启动${NC}"
    echo -e "${GREEN}========================================${NC}"
    echo ""
    echo "  Web UI : http://${IP}:${PORT}"
    echo "  镜像   : ${IMAGE}"
    echo "  数据   : ${DATA_DIR}"
    echo "  管理   : docker logs ${CONTAINER} | docker restart ${CONTAINER}"
    echo ""
    echo "  首次访问请在浏览器完成初始化向导。"
    echo "  ONVIF 自动发现依赖 host 网络（已启用）。"
    echo ""
else
    error "容器未能保持运行，查看日志排查: docker logs ${CONTAINER}"
    exit 1
fi
