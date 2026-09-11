//go:build linux

package api

import "syscall"

// diskUsageFor 返回路径所在文件系统的总容量与剩余字节(statfs)。
// Linux-only: syscall.Statfs 在非 Linux 平台不存在,见 diskusage_other.go。
func diskUsageFor(path string) (total, free uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	bs := uint64(st.Bsize)
	total = st.Blocks * bs
	free = st.Bavail * bs // 可用按非 root 视角(Bavail),与 df 一致
	return total, free, true
}
