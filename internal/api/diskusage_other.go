//go:build !linux

package api

// diskUsageFor 非 Linux 开发机上的桩:报告不可用(handler 输出 status=unknown)。
func diskUsageFor(path string) (total, free uint64, ok bool) {
	return 0, 0, false
}
