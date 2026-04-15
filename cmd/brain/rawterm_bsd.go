//go:build darwin || freebsd

package main

import (
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"
)

// enableRawInput puts the terminal into raw-ish mode where we read individual
// bytes. ICANON, ECHO, and ISIG are all disabled so we have full control.
func enableRawInput() (restore func(), err error) {
	fd := int(os.Stdin.Fd())
	var orig syscall.Termios
	if _, _, errno := syscall.Syscall6(
		syscall.SYS_IOCTL, uintptr(fd),
		uintptr(syscall.TIOCGETA), uintptr(unsafe.Pointer(&orig)),
		0, 0, 0,
	); errno != 0 {
		return nil, errno
	}

	raw := orig
	raw.Lflag &^= syscall.ICANON | syscall.ECHO | syscall.ISIG
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0

	if _, _, errno := syscall.Syscall6(
		syscall.SYS_IOCTL, uintptr(fd),
		uintptr(syscall.TIOCSETA), uintptr(unsafe.Pointer(&raw)),
		0, 0, 0,
	); errno != 0 {
		return nil, errno
	}

	fmt.Print("\033[?2004h")
	return func() {
		fmt.Print("\033[?2004l")
		syscall.Syscall6(
			syscall.SYS_IOCTL, uintptr(fd),
			uintptr(syscall.TIOCSETA), uintptr(unsafe.Pointer(&orig)),
			0, 0, 0,
		)
	}, nil
}

func waitForStdinReady(timeout time.Duration) (bool, error) {
	fd := int(os.Stdin.Fd())
	var readfds syscall.FdSet
	fdSet(&readfds, fd)

	var tv *syscall.Timeval
	if timeout >= 0 {
		timeoutVal := syscall.NsecToTimeval(timeout.Nanoseconds())
		tv = &timeoutVal
	}

	if err := syscall.Select(fd+1, &readfds, nil, nil, tv); err != nil {
		if err == syscall.EINTR {
			return false, nil
		}
		return false, err
	}
	return fdIsSet(&readfds, fd), nil
}

type terminalWinsize struct {
	row    uint16
	col    uint16
	xpixel uint16
	ypixel uint16
}

func terminalColumns() int {
	fd := int(os.Stdout.Fd())
	ws := terminalWinsize{}
	if _, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(syscall.TIOCGWINSZ),
		uintptr(unsafe.Pointer(&ws)),
	); errno != 0 || ws.col == 0 {
		return 120
	}
	return int(ws.col)
}

func terminalRows() int {
	fd := int(os.Stdout.Fd())
	ws := terminalWinsize{}
	if _, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(syscall.TIOCGWINSZ),
		uintptr(unsafe.Pointer(&ws)),
	); errno != 0 || ws.row == 0 {
		return 24
	}
	return int(ws.row)
}

func fdSet(set *syscall.FdSet, fd int) {
	wordBits := int(unsafe.Sizeof(uintptr(0)) * 8)
	words := unsafe.Slice((*uintptr)(unsafe.Pointer(set)), int(unsafe.Sizeof(*set))/int(unsafe.Sizeof(uintptr(0))))
	words[fd/wordBits] |= uintptr(1) << (uint(fd) % uint(wordBits))
}

func fdIsSet(set *syscall.FdSet, fd int) bool {
	wordBits := int(unsafe.Sizeof(uintptr(0)) * 8)
	words := unsafe.Slice((*uintptr)(unsafe.Pointer(set)), int(unsafe.Sizeof(*set))/int(unsafe.Sizeof(uintptr(0))))
	return words[fd/wordBits]&(uintptr(1)<<(uint(fd)%uint(wordBits))) != 0
}
