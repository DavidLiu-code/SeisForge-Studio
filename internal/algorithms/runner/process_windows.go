//go:build windows

package runner

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var algorithmKernel32 = syscall.NewLazyDLL("kernel32.dll")
var algorithmCreateJob = algorithmKernel32.NewProc("CreateJobObjectW")
var algorithmSetJob = algorithmKernel32.NewProc("SetInformationJobObject")
var algorithmAssignJob = algorithmKernel32.NewProc("AssignProcessToJobObject")
var algorithmTerminateJob = algorithmKernel32.NewProc("TerminateJobObject")
var algorithmQueryJob = algorithmKernel32.NewProc("QueryInformationJobObject")
var algorithmSnapshot = algorithmKernel32.NewProc("CreateToolhelp32Snapshot")
var algorithmThreadFirst = algorithmKernel32.NewProc("Thread32First")
var algorithmThreadNext = algorithmKernel32.NewProc("Thread32Next")
var algorithmOpenThread = algorithmKernel32.NewProc("OpenThread")
var algorithmResumeThread = algorithmKernel32.NewProc("ResumeThread")

type algorithmJobBasicLimits struct {
	ProcessUserTime, JobUserTime int64
	Flags                        uint32
	MinWorkingSet, MaxWorkingSet uintptr
	ActiveProcesses              uint32
	Affinity                     uintptr
	Priority, Scheduling         uint32
}
type algorithmJobExtendedLimits struct {
	Basic                                                      algorithmJobBasicLimits
	IO                                                         [6]uint64
	ProcessMemory, JobMemory, PeakProcessMemory, PeakJobMemory uintptr
}
type algorithmThreadEntry struct {
	Size, Usage, ID, Owner      uint32
	BasePriority, DeltaPriority int32
	Flags                       uint32
}

type algorithmJobAccounting struct {
	TotalUserTime, TotalKernelTime, PeriodUserTime, PeriodKernelTime     int64
	PageFaultCount, TotalProcesses, ActiveProcesses, TerminatedProcesses uint32
}

const algorithmTreeShutdownTimeout = 5 * time.Second

// TerminateJobObject is asynchronous. Waiting for the launcher is not enough:
// its descendants may still hold artifact files open. Observe the job's active
// process count before the runner attempts to remove the task directory.
func waitAlgorithmJobEmpty(job syscall.Handle) error {
	deadline := time.Now().Add(algorithmTreeShutdownTimeout)
	for {
		var info algorithmJobAccounting
		ok, _, err := algorithmQueryJob.Call(uintptr(job), 1, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), 0)
		if ok == 0 {
			return fmt.Errorf("query algorithm process-tree shutdown: %w", err)
		}
		if info.ActiveProcesses == 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("algorithm process tree did not stop within %s (%d active)", algorithmTreeShutdownTimeout, info.ActiveProcesses)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func configureAlgorithmCommand(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
}

// Suspend the initial thread until the process has joined a private job. This
// closes the start/assign race in which an external module could otherwise spawn
// workers outside the kill-on-close job. No global process/security settings
// are changed; failure to establish containment is a hard launch failure.
func prepareAlgorithmProcess(cmd *exec.Cmd) (func() error, func() error, error) {
	configureAlgorithmCommand(cmd)
	cmd.SysProcAttr.CreationFlags |= 0x4 // CREATE_SUSPENDED
	h, _, err := algorithmCreateJob.Call(0, 0)
	if h == 0 {
		return nil, nil, fmt.Errorf("create algorithm job: %w", err)
	}
	job := syscall.Handle(h)
	limits := algorithmJobExtendedLimits{}
	limits.Basic.Flags = 0x2000 // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	ok, _, err := algorithmSetJob.Call(h, 9, uintptr(unsafe.Pointer(&limits)), unsafe.Sizeof(limits))
	if ok == 0 {
		syscall.CloseHandle(job)
		return nil, nil, fmt.Errorf("configure algorithm job: %w", err)
	}
	var mu sync.Mutex
	cleanup := func() error {
		mu.Lock()
		defer mu.Unlock()
		if job != 0 {
			// Also stop children left behind by a normally exiting launcher.
			ok, _, terminateErr := algorithmTerminateJob.Call(uintptr(job), 1)
			waitErr := waitAlgorithmJobEmpty(job)
			closeErr := syscall.CloseHandle(job)
			job = 0
			if ok != 0 || waitErr == nil {
				terminateErr = nil
			}
			return errors.Join(terminateErr, waitErr, closeErr)
		}
		return nil
	}
	cmd.Cancel = func() error {
		mu.Lock()
		defer mu.Unlock()
		if job != 0 {
			algorithmTerminateJob.Call(uintptr(job), 1)
		}
		// Assignment might not have finished yet. The suspended launcher must
		// also be killed in that case; it has not executed or spawned children.
		if cmd.Process != nil {
			if e := cmd.Process.Kill(); e != nil && e != os.ErrProcessDone {
				return e
			}
		}
		return nil
	}
	afterStart := func() error {
		mu.Lock()
		defer mu.Unlock()
		process, e := syscall.OpenProcess(0x100|0x1|0x1000, false, uint32(cmd.Process.Pid)) // SET_QUOTA|TERMINATE|QUERY_LIMITED_INFORMATION
		if e != nil {
			return e
		}
		defer syscall.CloseHandle(process)
		ok, _, e := algorithmAssignJob.Call(uintptr(job), uintptr(process))
		if ok == 0 {
			return fmt.Errorf("assign algorithm to job: %w", e)
		}
		return resumeAlgorithmThreads(uint32(cmd.Process.Pid))
	}
	return afterStart, cleanup, nil
}

func resumeAlgorithmThreads(pid uint32) error {
	h, _, err := algorithmSnapshot.Call(0x4, 0) // TH32CS_SNAPTHREAD
	if h == ^uintptr(0) {
		return err
	}
	defer syscall.CloseHandle(syscall.Handle(h))
	entry := algorithmThreadEntry{Size: uint32(unsafe.Sizeof(algorithmThreadEntry{}))}
	ok, _, err := algorithmThreadFirst.Call(h, uintptr(unsafe.Pointer(&entry)))
	resumed := 0
	for ok != 0 {
		if entry.Owner == pid {
			thread, _, e := algorithmOpenThread.Call(0x2, 0, uintptr(entry.ID)) // THREAD_SUSPEND_RESUME
			if thread == 0 {
				return e
			}
			previous, _, e := algorithmResumeThread.Call(thread)
			_ = syscall.CloseHandle(syscall.Handle(thread))
			if uint32(previous) == ^uint32(0) {
				return e
			}
			resumed++
		}
		ok, _, err = algorithmThreadNext.Call(h, uintptr(unsafe.Pointer(&entry)))
	}
	if resumed == 0 {
		return fmt.Errorf("no suspended algorithm thread found: %v", err)
	}
	return nil
}
