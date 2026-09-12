//go:build linux

package main

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// selfThrottle lowers this process's CPU and IO scheduling priority (#748).
// Batch timelapse merges at the NVR's default priority starve online
// recording on saturated ARM + SMR-HDD devices (2026-09-12 incident: load
// 8-13 for 4.5h, recordings dropped 17→10). nice 19 = lowest CPU priority;
// IOPRIO_CLASS_BE level 7 = lowest best-effort IO urgency.
func selfThrottle() error {
	if err := unix.Setpriority(unix.PRIO_PROCESS, 0, 19); err != nil {
		return fmt.Errorf("setpriority nice 19: %w", err)
	}
	// x/sys does not export the IOPRIO_* user constants; they are stable
	// kernel ABI (include/linux/ioprio.h).
	const (
		ioprioWhoProcess = 1
		ioprioClassBE    = 2
		ioprioClassShift = 13
		ioprioLevelLow   = 7
	)
	prio := ioprioClassBE<<ioprioClassShift | ioprioLevelLow
	if _, _, errno := unix.Syscall(unix.SYS_IOPRIO_SET, ioprioWhoProcess, 0, uintptr(prio)); errno != 0 {
		return fmt.Errorf("ioprio_set best-effort 7: %w", errno)
	}
	return nil
}
