//go:build windows
// +build windows

package kernel

import (
	"syscall"
	"unsafe"
)

// diskFreeBytes 返回 path 所在分区的可用字节数(Windows)。
// 用 kernel32.GetDiskFreeSpaceExW (Win32 API),不依赖 golang.org/x/sys。
//
// 三个 out 参数:
//   - lpFreeBytesAvailableToCaller: 当前进程可用字节(考虑配额)
//   - lpTotalNumberOfBytes: 分区总字节
//   - lpTotalNumberOfFreeBytes: 分区总可用字节(忽略配额)
//
// 我们返回 lpFreeBytesAvailableToCaller,语义最贴近 unix 的 st.Bavail*Bsize。
func diskFreeBytes(path string) (uint64, error) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetDiskFreeSpaceExW")

	utf16Path, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}

	var freeBytesAvailable, totalBytes, totalFreeBytes uint64

	r1, _, lastErr := proc.Call(
		uintptr(unsafe.Pointer(utf16Path)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)
	if r1 == 0 {
		return 0, lastErr
	}
	return freeBytesAvailable, nil
}
