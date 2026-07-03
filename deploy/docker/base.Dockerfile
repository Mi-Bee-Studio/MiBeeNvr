# MiBee NVR base runtime image — Alpine + FFmpeg + toolchain
#
# This image is built PERIODICALLY (monthly cron or manual trigger) via
# .github/workflows/base-image.yml, NOT on every release.
# It eliminates the need for QEMU in the release Docker build.
#
# Contains: su-exec, tzdata, ffmpeg (+ ffprobe), xz
#
# FFmpeg is an OPTIONAL dependency, included here so Docker users get
# out-of-the-box transcoding. The NVR runs fully without it — FFmpeg only
# powers H.265↔H.264 transcoding (storage optimization + live relay transcode).
# All other features (recording, playback, live streaming, timelapse, merge)
# are pure Go and need no external binary.

FROM alpine:3.21
RUN apk add --no-cache su-exec tzdata ffmpeg xz
