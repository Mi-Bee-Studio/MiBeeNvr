# ---- Stage 1: Build frontend SPA ----
FROM node:22-slim AS frontend

WORKDIR /build/web

# Install dependencies first (layer cache)
COPY web/package.json web/package-lock.json ./
RUN npm ci

# Copy frontend source and build
COPY web/ ./
RUN npm run build

# ---- Stage 2: Build Go binary ----
FROM golang:1.26-bookworm AS backend

WORKDIR /build

# Cache go module downloads
COPY go.mod go.sum ./
RUN go mod download

# Copy Go source
COPY cmd/ ./cmd/
COPY internal/ ./internal/

# Copy built frontend into the embed directory
COPY --from=frontend /build/web/dist ./internal/ui/static/

# Static build — no C dependencies
ENV CGO_ENABLED=0
RUN go build -ldflags="-s -w" -o /mibee-nvr ./cmd/mibee-nvr/

# ---- Stage 3: Minimal runtime image ----
FROM gcr.io/distroless/static-debian12:nonroot

# Default data and config directories
# These can be overridden via volume mounts
ENV NVR_DATA_DIR=/data

COPY --from=backend /mibee-nvr /usr/local/bin/mibee-nvr

# Non-root user (distroless nonroot UID 65534)
USER nonroot:nonroot

# Config is expected at /data/mibee-nvr.yaml via volume mount
# Recordings stored in /data by default (configurable in YAML)
VOLUME ["/data"]

EXPOSE 9090

ENTRYPOINT ["mibee-nvr"]
CMD ["-config", "/data/mibee-nvr.yaml"]
