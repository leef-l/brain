//go:build !windows

package tool

import (
	"os"
	"strings"
	"syscall"
)

func resolveSignal(name string) os.Signal {
	switch strings.ToUpper(name) {
	case "KILL", "SIGKILL", "9":
		return syscall.SIGKILL
	case "STOP", "SIGSTOP":
		return syscall.SIGSTOP
	case "CONT", "SIGCONT":
		return syscall.SIGCONT
	case "HUP", "SIGHUP", "1":
		return syscall.SIGHUP
	case "USR1", "SIGUSR1":
		return syscall.SIGUSR1
	case "USR2", "SIGUSR2":
		return syscall.SIGUSR2
	default:
		return syscall.SIGTERM
	}
}
