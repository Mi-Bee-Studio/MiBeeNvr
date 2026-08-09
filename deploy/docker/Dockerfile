# syntax=docker/dockerfile:1

# ---- Stage 1: Build frontend SPA (native, no QEMU) ----
FROM --platform=$BUILDPLATFORM node:22-slim AS frontend

WORKDIR /build/web

# Optional npm registry mirror (e.g. https://registry.npmmirror.com for mainland
# China builds). Not set by default — CI uses the public registry.
ARG NPM_REGISTRY
RUN if [ -n "$NPM_REGISTRY" ]; then npm config set registry "$NPM_REGISTRY"; fi

# Install dependencies first (layer cache)
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci

# Copy frontend source and build
COPY web/ ./
RUN npm run build

# ---- Stage 2: Build Go binary (cross-compile, no QEMU) ----
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS backend

WORKDIR /build

ARG TARGETARCH
ARG TARGETVARIANT
# Optional Go module proxy (e.g. https://goproxy.cn,direct for mainland China
# builds where proxy.golang.org is blocked). Not set by default — CI uses the
# Go default proxy.
ARG GOPROXY
RUN if [ -n "$GOPROXY" ]; then go env -w GOPROXY="$GOPROXY"; fi

# Cache go module downloads
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# Copy Go source
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY pkg/ ./pkg/

# Copy built frontend into the embed directory
COPY --from=frontend /build/web/dist ./internal/ui/static/

# Cross-compile for the target architecture.
# BuildKit sets TARGETARCH/TARGETVARIANT; we map them to GOARCH/GOARM.
# This stage runs ONCE on the native runner — no QEMU emulation for compilation.
ENV CGO_ENABLED=0
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -e && \
    if [ "$TARGETARCH" = "arm" ]; then \
      export GOARCH=arm GOARM=${TARGETVARIANT#v}; \
    else \
      export GOARCH=$TARGETARCH; \
    fi && \
    go build -trimpath -ldflags="-s -w -X main.appVersion=${VERSION}" -o /mibee-nvr ./cmd/mibee-nvr/

# ---- Stage 2b: Pre-download the ONNX AI model ----
# Docker users get a working AI detection model baked into the image — no
# runtime `download-model` needed, no dependence on the host's network quality.
# The build environment (GitHub Actions runner) has a stable connection, and
# curl's `--retry-all-errors` + `--fail` handle the GitHub release CDN's
# intermittent mid-transfer truncation (issue #109). A truncated/failed download
# FAILS THE BUILD here (explicit failure > a silently corrupt model on disk).
# The model is architecture-independent (a protobuf blob), so a single download
# serves amd64/arm64/armv7.
FROM --platform=$BUILDPLATFORM alpine:3.20 AS model
RUN apk add --no-cache curl
ARG YOLO_MODEL_URL=https://github.com/ultralytics/assets/releases/download/v8.3.0/yolo11n.onnx
RUN mkdir -p /opt/mibee-nvr/models && \
    curl --fail --location --show-error \
         --retry 10 --retry-all-errors --retry-delay 2 \
         --connect-timeout 30 --max-time 600 \
         -o /opt/mibee-nvr/models/yolo11n.onnx \
         "$YOLO_MODEL_URL" && \
    # Reject a truncated download (matches the Go CLI's minModelSize gate).
    test "$(stat -c%s /opt/mibee-nvr/models/yolo11n.onnx)" -ge 5242880 || \
    { echo "ERROR: downloaded model is < 5MB (truncated)"; exit 1; }

# ---- Stage 3: Runtime image ----
# Pre-built base image (Alpine + FFmpeg + ffprobe + xz + su-exec + tzdata).
# Built periodically by .github/workflows/base-image.yml — NOT on every release.
# This eliminates the last QEMU bottleneck: no per-arch `apk add` in release builds.
FROM ghcr.io/mi-bee-studio/mibeenvr-base:v1

# Default data and config directories
# These can be overridden via volume mounts
ENV NVR_DATA_DIR=/data
ENV NVR_UID=1000
ENV NVR_GID=1000
# Marks this as a Docker deployment so the in-app version checker surfaces
# "update the image" guidance (see internal/update/checker.go) instead of the
# bare-binary upgrade instructions.
ENV NVR_DEPLOYMENT=docker

# Persistent data: recordings, database, config
VOLUME ["/data"]

COPY --from=backend /mibee-nvr /usr/local/bin/mibee-nvr
COPY --from=model /opt/mibee-nvr/models/yolo11n.onnx /opt/mibee-nvr/models/yolo11n.onnx
COPY --chmod=0755 deploy/docker/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

EXPOSE 9090

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["mibee-nvr", "health"]

ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["-config", "/data/mibee-nvr.yaml"]
