//go:build windows

package term

import (
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"
)

const (
	winEnableProcessedInput        = 0x0001
	winEnableLineInput             = 0x0002
	winEnableEchoInput             = 0x0004
	winEnableWindowInput           = 0x0008
	winEnableMouseInput            = 0x0010
	winEnableQuickEditMode         = 0x0040
	winEnableExtendedFlags         = 0x0080
	winEnableVirtualTerminalInput  = 0x0200
	winEnableProcessedOutput       = 0x0001
	winEnableVirtualTermProcessing = 0x0004
	winUTF8CodePage                = 65001
)

var (
	winKernel32                    = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleMode             = winKernel32.NewProc("SetConsoleMode")
	procGetConsoleScreenBufferInfo = winKernel32.NewProc("GetConsoleScreenBufferInfo")
	procGetConsoleCP               = winKernel32.NewProc("GetConsoleCP")
	procGetConsoleOutputCP         = winKernel32.NewProc("GetConsoleOutputCP")
	procSetConsoleCP               = winKernel32.NewProc("SetConsoleCP")
	procSetConsoleOutputCP         = winKernel32.NewProc("SetConsoleOutputCP")
)

type windowsConsoleState struct {
	inputHandle  syscall.Handle
	outputHandle syscall.Handle
	inputMode    uint32
	outputMode   uint32
	inputCP      uint32
	outputCP     uint32
}

type windowsCoord struct {
	X int16
	Y int16
}

type windowsSmallRect struct {
	Left   int16
	Top    int16
	Right  int16
	Bottom int16
}

type windowsConsoleScreenBufferInfo struct {
	Size              windowsCoord
	CursorPosition    windowsCoord
	Attributes        uint16
	Window            windowsSmallRect
	MaximumWindowSize windowsCoord
}

func EnableRawInput() (restore func(), err error) {
	state, err := captureWindowsConsoleState()
	if err != nil {
		return nil, err
	}

	rawInputMode := state.inputMode
	rawInputMode &^= winEnableEchoInput |
		winEnableProcessedInput |
		winEnableLineInput |
		winEnableMouseInput |
		winEnableQuickEditMode |
		winEnableWindowInput
	rawInputMode |= winEnableExtendedFlags | winEnableVirtualTerminalInput

	rawOutputMode := state.outputMode | winEnableProcessedOutput | winEnableVirtualTermProcessing

	if err := setConsoleMode(state.inputHandle, rawInputMode); err != nil {
		return nil, err
	}
	if err := setConsoleMode(state.outputHandle, rawOutputMode); err != nil {
		_ = setConsoleMode(state.inputHandle, state.inputMode)
		return nil, err
	}
	if err := setConsoleCP(winUTF8CodePage); err != nil {
		_ = setConsoleMode(state.outputHandle, state.outputMode)
		_ = setConsoleMode(state.inputHandle, state.inputMode)
		return nil, err
	}
	if err := setConsoleOutputCP(winUTF8CodePage); err != nil {
		_ = setConsoleCP(state.inputCP)
		_ = setConsoleMode(state.outputHandle, state.outputMode)
		_ = setConsoleMode(state.inputHandle, state.inputMode)
		return nil, err
	}

	fmt.Print("\033[?2004h")
	return func() {
		fmt.Print("\033[?2004l")
		_ = setConsoleOutputCP(state.outputCP)
		_ = setConsoleCP(state.inputCP)
		_ = setConsoleMode(state.outputHandle, state.outputMode)
		_ = setConsoleMode(state.inputHandle, state.inputMode)
	}, nil
}

func WaitForStdinReady(timeout time.Duration) (bool, error) {
	handle := syscall.Handle(os.Stdin.Fd())

	waitMillis := uint32(syscall.INFINITE)
	if timeout >= 0 {
		waitMillis = uint32(timeout / time.Millisecond)
	}

	event, err := syscall.WaitForSingleObject(handle, waitMillis)
	if err != nil {
		return false, err
	}
	switch event {
	case syscall.WAIT_OBJECT_0:
		return true, nil
	case syscall.WAIT_TIMEOUT:
		return false, nil
	default:
		return false, syscall.EINVAL
	}
}

func TerminalColumns() int {
	info, err := getConsoleScreenBufferInfo(syscall.Handle(os.Stdout.Fd()))
	if err != nil {
		return 120
	}
	width := int(info.Window.Right-info.Window.Left) + 1
	if width <= 0 {
		return 120
	}
	return width
}

func TerminalRows() int {
	info, err := getConsoleScreenBufferInfo(syscall.Handle(os.Stdout.Fd()))
	if err != nil {
		return 24
	}
	height := int(info.Window.Bottom-info.Window.Top) + 1
	if height <= 0 {
		return 24
	}
	return height
}

func captureWindowsConsoleState() (*windowsConsoleState, error) {
	state := &windowsConsoleState{
		inputHandle:  syscall.Handle(os.Stdin.Fd()),
		outputHandle: syscall.Handle(os.Stdout.Fd()),
	}
	if err := syscall.GetConsoleMode(state.inputHandle, &state.inputMode); err != nil {
		return nil, err
	}
	if err := syscall.GetConsoleMode(state.outputHandle, &state.outputMode); err != nil {
		return nil, err
	}

	var err error
	if state.inputCP, err = getConsoleCP(); err != nil {
		return nil, err
	}
	if state.outputCP, err = getConsoleOutputCP(); err != nil {
		return nil, err
	}
	return state, nil
}

func setConsoleMode(handle syscall.Handle, mode uint32) error {
	r1, _, e1 := procSetConsoleMode.Call(uintptr(handle), uintptr(mode))
	if r1 == 0 {
		if e1 != nil {
			return e1
		}
		return syscall.EINVAL
	}
	return nil
}

func getConsoleScreenBufferInfo(handle syscall.Handle) (*windowsConsoleScreenBufferInfo, error) {
	var info windowsConsoleScreenBufferInfo
	r1, _, e1 := procGetConsoleScreenBufferInfo.Call(uintptr(handle), uintptr(unsafe.Pointer(&info)))
	if r1 == 0 {
		if e1 != nil {
			return nil, e1
		}
		return nil, syscall.EINVAL
	}
	return &info, nil
}

func getConsoleCP() (uint32, error) {
	r1, _, e1 := procGetConsoleCP.Call()
	if r1 == 0 {
		if e1 != nil {
			return 0, e1
		}
		return 0, syscall.EINVAL
	}
	return uint32(r1), nil
}

func getConsoleOutputCP() (uint32, error) {
	r1, _, e1 := procGetConsoleOutputCP.Call()
	if r1 == 0 {
		if e1 != nil {
			return 0, e1
		}
		return 0, syscall.EINVAL
	}
	return uint32(r1), nil
}

func setConsoleCP(cp uint32) error {
	r1, _, e1 := procSetConsoleCP.Call(uintptr(cp))
	if r1 == 0 {
		if e1 != nil {
			return e1
		}
		return syscall.EINVAL
	}
	return nil
}

func setConsoleOutputCP(cp uint32) error {
	r1, _, e1 := procSetConsoleOutputCP.Call(uintptr(cp))
	if r1 == 0 {
		if e1 != nil {
			return e1
		}
		return syscall.EINVAL
	}
	return nil
}
