package kernel

import (
	"os/exec"
	"syscall"
)

// setSidecarDeathSignal configures the child process to receive SIGTERM
// when the parent dies. This is a Linux-only feature (PR_SET_PDEATHSIG).
func setSidecarDeathSignal(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGTERM
}
