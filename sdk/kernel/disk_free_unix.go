//go:build linux || darwin || freebsd
// +build linux darwin freebsd

package kernel

import "syscall"

// diskFreeBytes 返回 path 所在分区的可用字节数(unix:linux/darwin/freebsd)。
// 用 syscall.Statfs 走 statfs(2) 系统调用。
func diskFreeBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}
