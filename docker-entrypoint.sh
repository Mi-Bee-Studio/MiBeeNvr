#!/bin/sh
set -e

# MiBee NVR Docker Entrypoint
# Automatically fixes /data ownership and drops privileges.
#
# Environment variables:
#   NVR_UID  — UID to run as (default: 1000)
#   NVR_GID  — GID to run as (default: 1000)

NVR_UID="${NVR_UID:-1000}"
NVR_GID="${NVR_GID:-1000}"

if [ "$(id -u)" = "0" ]; then
    # Ensure data directory exists
    mkdir -p /data

    # Fix ownership so the app can read/write
    if ! chown -R "${NVR_UID}:${NVR_GID}" /data 2>/dev/null; then
        echo "[entrypoint] chown failed, ensuring /data is writable via chmod"
        chmod -R a+rw /data 2>/dev/null || true
    fi

    echo "[entrypoint] running as UID ${NVR_UID} GID ${NVR_GID}"
    echo "[entrypoint] /data permissions: $(ls -ld /data)"

    # Drop privileges and exec the binary
    exec su-exec "${NVR_UID}:${NVR_GID}" /usr/local/bin/mibee-nvr "$@"
fi

# Already non-root, run directly
exec /usr/local/bin/mibee-nvr "$@"
