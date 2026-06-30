# MiBee NVR base runtime image — Alpine + FFmpeg + toolchain
#
# This image is built PERIODICALLY (monthly cron or manual trigger) via
# .github/workflows/base-image.yml, NOT on every release.
# It eliminates the need for QEMU in the release Docker build.
#
# Contains: su-exec, tzdata, ffmpeg (+ ffprobe), xz
# FFmpeg is the ONLY third-party binary dependency in the project.

FROM alpine:3.21
RUN apk add --no-cache su-exec tzdata ffmpeg xz
