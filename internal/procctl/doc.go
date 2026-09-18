// Package procctl centralizes the platform-specific parts of child-process
// control used by the external-ffmpeg helpers (transcoding queue, timelapse
// merges, live transcoder): putting children in their own process group so
// cleanup kills reach grandchildren, lowering their scheduling priority, and
// killing the whole group.
//
// Unix builds use POSIX process groups; Windows has no equivalent, so its
// variants degrade to direct-child semantics. This keeps the desktop
// (windows/darwin) builds of the NVR compilable and sane while the production
// targets (linux) keep exact behavior.
package procctl
