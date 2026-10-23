//go:build windows

package proc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// Go's exec.Cmd normally kills only the direct child on cancellation.
// A kill-on-close Windows Job Object manages the entire descendant tree.
// CREATE_SUSPENDED lets us assign the child to the job before any of its
// application code can spawn a grandchild. NtResumeProcess resumes it only
// after assignment succeeds; failure is fail-closed (the child is killed).
const (
	createSuspended                   = 0x00000004
	jobObjectExtendedLimitInformation = 9
	jobObjectLimitKillOnJobClose      = 0x00002000
	processSetQuota                   = 0x0100
	processTerminate                  = 0x0001
	processSuspendResume              = 0x0800
)

type basicJobLimitInformation struct {
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

type jobIOCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type extendedJobLimitInformation struct {
	BasicLimitInformation basicJobLimitInformation
	IoInfo                jobIOCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	createJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	setInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	assignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	terminateJobObject       = kernel32.NewProc("TerminateJobObject")
	ntdll                    = syscall.NewLazyDLL("ntdll.dll")
	ntResumeProcess          = ntdll.NewProc("NtResumeProcess")
)

func winAPIErr(op string, err error) error {
	if err == nil || errors.Is(err, syscall.Errno(0)) {
		err = syscall.EINVAL
	}
	return fmt.Errorf("%s: %w", op, err)
}

func createKillOnCloseJob() (syscall.Handle, error) {
	h, _, err := createJobObjectW.Call(0, 0)
	if h == 0 {
		return 0, winAPIErr("CreateJobObjectW", err)
	}
	job := syscall.Handle(h)
	info := extendedJobLimitInformation{}
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	ok, _, callErr := setInformationJobObject.Call(
		h, jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info),
	)
	if ok == 0 {
		_ = syscall.CloseHandle(job)
		return 0, winAPIErr("SetInformationJobObject", callErr)
	}
	return job, nil
}

func assignToJob(job, process syscall.Handle) error {
	ok, _, err := assignProcessToJobObject.Call(uintptr(job), uintptr(process))
	if ok == 0 {
		return winAPIErr("AssignProcessToJobObject", err)
	}
	return nil
}

func resumeJobProcess(process syscall.Handle) error {
	status, _, _ := ntResumeProcess.Call(uintptr(process))
	if status != 0 {
		return fmt.Errorf("NtResumeProcess: NTSTATUS 0x%x", uint32(status))
	}
	return nil
}

func terminateJob(job syscall.Handle) error {
	ok, _, err := terminateJobObject.Call(uintptr(job), 1)
	if ok == 0 {
		return winAPIErr("TerminateJobObject", err)
	}
	return nil
}

func configure(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createSuspended
}

func cleanup(cmd *exec.Cmd) {} // job ownership is scoped to runManaged.

func runManaged(cmd *exec.Cmd) error {
	// cmd.Run cannot provide a hook between Start and Wait. Explicit Start
	// permits atomic (relative to child code) job assignment and resume.
	job, err := createKillOnCloseJob()
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(job) // kills any remaining descendants, even on success

	// Also kill the direct child if cancellation races with job assignment.
	// It is suspended until assigned, so it cannot spawn unmanaged children.
	var canceled atomic.Bool
	cmd.Cancel = func() error {
		canceled.Store(true)
		err := terminateJob(job)
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return err
	}
	// Keep supporting callers that constructed their own exec.Cmd; do not
	// rely on them knowing the CREATE_SUSPENDED requirement.
	configure(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	fail := func(cause error) error {
		_ = terminateJob(job)
		_ = cmd.Process.Kill() // handles assignment failures (not yet in job)
		_ = cmd.Wait()         // reap the suspended or terminated direct child
		return cause
	}
	process, err := syscall.OpenProcess(processSetQuota|processTerminate|processSuspendResume, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fail(fmt.Errorf("OpenProcess for job assignment: %w", err))
	}
	defer syscall.CloseHandle(process)
	if err := assignToJob(job, process); err != nil {
		return fail(err)
	}
	if canceled.Load() {
		return fail(contextCanceledDuringStart)
	}
	if err := resumeJobProcess(process); err != nil {
		return fail(err)
	}
	return cmd.Wait()
}

// cmd.Wait cannot run before resume/assignment succeeds; a canceled command
// must still be reaped and must never execute uncontained child code.
var contextCanceledDuringStart = fmt.Errorf("subprocess canceled before resume: %w", os.ErrProcessDone)
