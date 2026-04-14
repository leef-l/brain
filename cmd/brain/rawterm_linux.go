//go:build linux

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
		uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&orig)),
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
		uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(&raw)),
		0, 0, 0,
	); errno != 0 {
		return nil, errno
	}

	fmt.Print("\033[?2004h")
	return func() {
		fmt.Print("\033[?2004l")
		syscall.Syscall6(
			syscall.SYS_IOCTL, uintptr(fd),
			uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(&orig)),
			0, 0, 0,
		)
	}, nil
}

func waitForStdinReady(timeout time.Duration) (bool, error) {
	fd := int(os.Stdin.Fd())
	var readfds syscall.FdSet

	readfds.Bits[fd/64] |= 1 << (uint(fd) % 64)

	var tv *syscall.Timeval
	if timeout >= 0 {
		timeoutVal := syscall.NsecToTimeval(timeout.Nanoseconds())
		tv = &timeoutVal
	}

	n, err := syscall.Select(fd+1, &readfds, nil, nil, tv)
	if err != nil {
		if err == syscall.EINTR {
			return false, nil
		}
		return false, err
	}
	return n > 0, nil
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
