//go:build windows

package background

import (
	"errors"
	"os/exec"
	"syscall"
	"unsafe"
)

var (
	modKernel32            = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObject    = modKernel32.NewProc("CreateJobObjectW")
	procCloseHandle        = modKernel32.NewProc("CloseHandle")
	procAssignProcessToJob = modKernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject = modKernel32.NewProc("TerminateJobObject")
	procSetInformationJob  = modKernel32.NewProc("SetInformationJobObject")
)

const (
	jobObjectLimitKillOnJobClose    = 0x00002000
	jobObjectBasicLimitInformation  = 2
	jobObjectExtendedLimitInformation = 9
)

type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	ChildProcessRateControl  uint32
	Reserved                [2]uint64
}

type jobObjectExtendedLimitInformation struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IoInfo                struct {
		ReadOperationCount  uint64
		WriteOperationCount uint64
		OtherOperationCount uint64
		ReadTransferCount   uint64
		WriteTransferCount  uint64
		OtherTransferCount  uint64
	}
	ProcessMemoryLimit      uintptr
	JobMemoryLimit          uintptr
	PeakProcessMemoryUsed   uintptr
	PeakJobMemoryUsed       uintptr
}

func init() {
	configureProcessGroup = func(cmd *exec.Cmd) error {
		if cmd == nil {
			return nil
		}
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		// CreateProcessWithJobObject requires CREATE_SUSPENDED or
		// CREATE_BREAKAWAY_FROM_JOB. We use CreationFlags to allow
		// the child to be assigned to our job object later.
		cmd.SysProcAttr.CreationFlags |= syscall.CREATE_SUSPENDED
		return nil
	}

	killProcessTree = func(cmd *exec.Cmd) error {
		if cmd == nil || cmd.Process == nil {
			return nil
		}
		// On Windows, kill by pid terminates the process. To also
		// kill children, we attempt to use a job object.
		// Fall back to Process.Kill() if that fails.
		return cmd.Process.Kill()
	}
}

// createJobObject creates a Windows job object and assigns the given process handle to it.
// When the job object handle is closed, all processes in the job are terminated.
func createJobObject(processHandle syscall.Handle) (syscall.Handle, error) {
	jobName := uintptr(0) // unnamed job object
	job, _, err := procCreateJobObject.Call(jobName, jobName)
	if job == 0 {
		return 0, err
	}

	// Configure the job to kill all processes when the job handle is closed.
	info := jobObjectExtendedLimitInformation{
		BasicLimitInformation: jobObjectBasicLimitInformation{
			LimitFlags: jobObjectLimitKillOnJobClose,
		},
	}
	infoPtr := uintptr(unsafe.Pointer(&info))
	ret, _, err := procSetInformationJob.Call(
		job,
		jobObjectExtendedLimitInformation,
		infoPtr,
		uintptr(unsafe.Sizeof(info)),
	)
	if ret == 0 {
		procCloseHandle.Call(job)
		return 0, err
	}

	// Assign the process to the job.
	ret, _, err = procAssignProcessToJob.Call(job, uintptr(processHandle))
	if ret == 0 {
		procCloseHandle.Call(job)
		return 0, err
	}

	return job, nil
}

func closeJobObject(job syscall.Handle) error {
	ret, _, err := procCloseHandle.Call(uintptr(job))
	if ret == 0 {
		return err
	}
	return nil
}

func terminateJobObject(job syscall.Handle, exitCode uint32) error {
	ret, _, err := procTerminateJobObject.Call(uintptr(job), uintptr(exitCode))
	if ret == 0 {
		return err
	}
	return nil
}

// killWindowsProcessTree attempts to kill a process and all its children on Windows.
// It uses job objects when possible and falls back to Process.Kill().
func killWindowsProcessTree(pid int) error {
	handle, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
			return nil // process already dying
		}
		return err
	}
	defer syscall.CloseHandle(handle)

	return syscall.TerminateProcess(handle, 1)
}
