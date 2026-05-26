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
    chown -R "${NVR_UID}:${NVR_GID}" /data 2>/dev/null || true

    # Drop privileges and exec the binary
    exec su-exec "${NVR_UID}:${NVR_GID}" "$@"
fi

# Already non-root, run directly
exec "$@"
