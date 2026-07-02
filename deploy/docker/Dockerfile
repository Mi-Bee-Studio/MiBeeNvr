# syntax=docker/dockerfile:1

# ---- Stage 1: Build frontend SPA (native, no QEMU) ----
FROM --platform=$BUILDPLATFORM node:22-slim AS frontend

WORKDIR /build/web

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

# Persistent data: recordings, database, config
VOLUME ["/data"]

COPY --from=backend /mibee-nvr /usr/local/bin/mibee-nvr
COPY --chmod=0755 deploy/docker/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

EXPOSE 9090

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["mibee-nvr", "health"]

ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["-config", "/data/mibee-nvr.yaml"]
