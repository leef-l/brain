//go:build windows

package tool

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

// windowsSandbox implements CommandSandbox on Windows using Job Objects.
//
// Windows lacks mount namespaces, so there is no true filesystem isolation.
// We provide:
//   - Job Object with KILL_ON_JOB_CLOSE: all child processes die when brain exits
//   - Job Object with ACTIVE_PROCESS limit: prevents fork bombs
//   - Restricted environment: only essential variables propagated
//   - Working directory locked to sandbox primary
//   - Path-level checks via SandboxTool for filesystem safety
//
// Network: Windows requires admin for per-process firewall rules.
// We do NOT attempt network isolation. The permission layer handles
// network-related commands via user confirmation.
type windowsSandbox struct {
	sb  *Sandbox
	cfg *SandboxConfig
}

func newPlatformSandbox(sb *Sandbox, cfg *SandboxConfig) CommandSandbox {
	return &windowsSandbox{sb: sb, cfg: cfg}
}

func (w *windowsSandbox) Available() bool {
	return true // Job Objects available on all Windows 7+
}

func (w *windowsSandbox) Run(ctx context.Context, command string, workDir string,
	stdout, stderr io.Writer) (int, error) {

	// Create a Job Object for this command group.
	job, err := createRestrictedJob(100)
	if err != nil {
		return -1, fmt.Errorf("cannot create Job Object: %w", err)
	}
	defer closeHandle(job)

	cmd := exec.CommandContext(ctx, shellName(), shellFlag(), command)

	if workDir != "" {
		cmd.Dir = workDir
	} else if w.sb != nil {
		cmd.Dir = w.sb.Primary()
	}

	cmd.Env = w.restrictedEnv()
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	// CREATE_NEW_PROCESS_GROUP + CREATE_SUSPENDED:
	// Suspended so we can assign the Job before any code runs.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | createSuspended,
	}

	// Start the process in suspended state.
	if err := cmd.Start(); err != nil {
		return -1, err
	}

	// Get process handle from PID via OpenProcess.
	procHandle, err := openProcess(processAllAccess, false, uint32(cmd.Process.Pid))
	if err != nil {
		cmd.Process.Kill()
		return -1, fmt.Errorf("cannot open process: %w", err)
	}
	defer closeHandle(procHandle)

	// Assign to Job Object before resuming.
	if err := assignToJob(job, procHandle); err != nil {
		cmd.Process.Kill()
		return -1, fmt.Errorf("cannot assign Job Object: %w", err)
	}

	// Resume the process.
	if err := ntResumeProcess(procHandle); err != nil {
		cmd.Process.Kill()
		return -1, fmt.Errorf("cannot resume process: %w", err)
	}

	// Wait for completion.
	err = cmd.Wait()
	return exitCodeFromErr(err), nil
}

// restrictedEnv builds a minimal environment.
func (w *windowsSandbox) restrictedEnv() []string {
	var env []string

	for _, key := range []string{
		"SystemRoot", "SystemDrive", "TEMP", "TMP",
		"PATHEXT", "COMSPEC", "WINDIR",
		"USERPROFILE", "APPDATA", "LOCALAPPDATA",
		"PATH",
		"GOPATH", "GOROOT", "GOMODCACHE", "GOCACHE",
		"HOME", "TERM",
		"GIT_EXEC_PATH", "GIT_TEMPLATE_DIR",
	} {
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}

	// Override HOME to sandbox primary.
	if w.sb != nil {
		replaced := false
		for i, e := range env {
			if strings.HasPrefix(e, "HOME=") {
				env[i] = "HOME=" + w.sb.Primary()
				replaced = true
				break
			}
		}
		if !replaced {
			env = append(env, "HOME="+w.sb.Primary())
		}
	}

	return env
}

// ---------------------------------------------------------------------------
// Windows API
// ---------------------------------------------------------------------------

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	ntdll                        = syscall.NewLazyDLL("ntdll.dll")
	procCreateJobObject          = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	procOpenProcess              = kernel32.NewProc("OpenProcess")
	procNtResumeProcess          = ntdll.NewProc("NtResumeProcess")
)

const processAllAccess = 0x001F0FFF

const (
	createSuspended = 0x00000004

	jobObjectExtendedLimitInformation = 9
	jobLimitKillOnJobClose            = 0x00002000
	jobLimitActiveProcess             = 0x00000008
)

type jobObjectExtendedLimitInfo struct {
	BasicLimitInformation jobObjectBasicLimitInfo
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

type jobObjectBasicLimitInfo struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

func createRestrictedJob(maxProcs uint32) (syscall.Handle, error) {
	handle, _, err := procCreateJobObject.Call(0, 0)
	if handle == 0 {
		return 0, fmt.Errorf("CreateJobObject: %v", err)
	}

	info := jobObjectExtendedLimitInfo{}
	info.BasicLimitInformation.LimitFlags = jobLimitKillOnJobClose
	if maxProcs > 0 {
		info.BasicLimitInformation.LimitFlags |= jobLimitActiveProcess
		info.BasicLimitInformation.ActiveProcessLimit = maxProcs
	}

	r, _, err := procSetInformationJobObject.Call(
		handle,
		uintptr(jobObjectExtendedLimitInformation),
		uintptr(unsafe.Pointer(&info)),
		uintptr(unsafe.Sizeof(info)),
	)
	if r == 0 {
		syscall.CloseHandle(syscall.Handle(handle))
		return 0, fmt.Errorf("SetInformationJobObject: %v", err)
	}

	return syscall.Handle(handle), nil
}

func assignToJob(job syscall.Handle, process syscall.Handle) error {
	r, _, err := procAssignProcessToJobObject.Call(uintptr(job), uintptr(process))
	if r == 0 {
		return fmt.Errorf("AssignProcessToJobObject: %v", err)
	}
	return nil
}

func ntResumeProcess(process syscall.Handle) error {
	r, _, _ := procNtResumeProcess.Call(uintptr(process))
	if r != 0 { // NTSTATUS: 0 = success
		return fmt.Errorf("NtResumeProcess: NTSTATUS 0x%x", r)
	}
	return nil
}

func openProcess(access uint32, inheritHandle bool, pid uint32) (syscall.Handle, error) {
	inherit := uintptr(0)
	if inheritHandle {
		inherit = 1
	}
	handle, _, err := procOpenProcess.Call(uintptr(access), inherit, uintptr(pid))
	if handle == 0 {
		return 0, fmt.Errorf("OpenProcess(%d): %v", pid, err)
	}
	return syscall.Handle(handle), nil
}

func closeHandle(h syscall.Handle) {
	syscall.CloseHandle(h)
}

func shellName() string {
	if p := os.Getenv("COMSPEC"); p != "" {
		return p
	}
	return `C:\Windows\System32\cmd.exe`
}
func shellFlag() string { return "/C" }
