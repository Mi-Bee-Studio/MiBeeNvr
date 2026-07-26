#!/bin/sh
set -e

# MiBee NVR Docker Entrypoint
# Automatically fixes /data ownership and drops privileges.
#
# Environment variables:
#   NVR_UID       — UID to run as (default: 1000)
#   NVR_GID       — GID to run as (default: 1000)
#   NVR_DATA_DIR  — Storage directory inside container (default: /data)

NVR_UID="${NVR_UID:-1000}"
NVR_GID="${NVR_GID:-1000}"
NVR_DATA_DIR="${NVR_DATA_DIR:-/data}"

# --- Validate storage directory ---
if [ ! -d "$NVR_DATA_DIR" ]; then
    echo "[entrypoint] ERROR: Storage directory does not exist: $NVR_DATA_DIR"
    echo "[entrypoint] Did you forget to mount a volume?"
    echo "[entrypoint] Example: docker run -v ./data:$NVR_DATA_DIR ..."
    exit 1
fi

# --- Root setup: fix ownership then drop privileges ---
if [ "$(id -u)" = "0" ]; then
    # Ensure data directory is writable
    if ! chown -R "${NVR_UID}:${NVR_GID}" "$NVR_DATA_DIR" 2>/dev/null; then
        echo "[entrypoint] chown failed, ensuring $NVR_DATA_DIR is writable via chmod"
        chmod -R a+rw "$NVR_DATA_DIR" 2>/dev/null || true
    fi

    echo "[entrypoint] running as UID ${NVR_UID} GID ${NVR_GID}"
    echo "[entrypoint] storage: $NVR_DATA_DIR ($(ls -ld "$NVR_DATA_DIR"))"

    # Check for common storage path misconfiguration
    CONFIG_FILE="$NVR_DATA_DIR/mibee-nvr.yaml"
    if [ -f "$CONFIG_FILE" ]; then
        CONFIG_ROOT=$(grep -E '^\s+root_dir:' "$CONFIG_FILE" 2>/dev/null | sed 's/.*root_dir:\s*//' | tr -d '"' | tr -d "'")
        if [ -n "$CONFIG_ROOT" ] && [ "$CONFIG_ROOT" != "$NVR_DATA_DIR" ]; then
            echo "[entrypoint] WARNING: config storage.root_dir=$CONFIG_ROOT but Docker volume is at $NVR_DATA_DIR"
            echo "[entrypoint] The app will auto-fix this on startup."
        fi
    fi

    # Seed the ONNX AI model from the image cache on first start.
    # The model is baked into the image at /opt/mibee-nvr/models/ (issue #109:
    # avoids the host needing to download it from GitHub's flaky CDN at runtime).
    # We copy it into the writable data volume only when missing or undersized,
    # so this is a one-time cost and never overwrites a user-supplied model.
    # NOTE: stat sizes are captured into variables FIRST, then compared. POSIX sh
    # (Alpine ash / Debian dash) mis-parses a `&&` chain whose command-substitution
    # contains its own `||` (it reports "Syntax error: ... expecting 'then'"), so
    # we avoid nesting them.
    MODEL_SRC="/opt/mibee-nvr/models/yolo11n.onnx"
    MODEL_DST_DIR="$NVR_DATA_DIR/models"
    MODEL_DST="$MODEL_DST_DIR/yolo11n.onnx"
    # 5242880 = 5MB, matches minModelSize in cmd/mibee-nvr/download_model.go.
    SRC_SIZE=$(stat -c%s "$MODEL_SRC" 2>/dev/null || echo 0)
    DST_SIZE=$(stat -c%s "$MODEL_DST" 2>/dev/null || echo 0)
    if [ -f "$MODEL_SRC" ] && [ "$SRC_SIZE" -ge 5242880 ] && [ "$DST_SIZE" -lt 5242880 ]; then
        echo "[entrypoint] seeding AI model from image cache → $MODEL_DST"
        mkdir -p "$MODEL_DST_DIR"
        cp "$MODEL_SRC" "$MODEL_DST"
        chown "${NVR_UID}:${NVR_GID}" "$MODEL_DST" 2>/dev/null || true
    fi

    # Drop privileges and exec the binary
    exec su-exec "${NVR_UID}:${NVR_GID}" /usr/local/bin/mibee-nvr "$@"
fi

# Already non-root, run directly
exec /usr/local/bin/mibee-nvr "$@"
