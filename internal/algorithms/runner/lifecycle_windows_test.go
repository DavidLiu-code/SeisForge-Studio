//go:build windows

package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	api "github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi"
)

func lockChildFile(path string) (func(), error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// Deliberately omit FILE_SHARE_DELETE: a premature RemoveAll would fail.
	h, err := syscall.CreateFile(name, syscall.GENERIC_WRITE, syscall.FILE_SHARE_READ, nil, syscall.CREATE_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return func() { _ = syscall.CloseHandle(h) }, nil
}

func TestTaskCleanupFailureIsReported(t *testing.T) {
	m := fakeModule(t)
	opts := options(t, m)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var release func()
	var lockErr error
	_, err := Run(ctx, m, request("hang"), fakeInput(t), opts, func(e api.ProgressEvent) {
		if e.Type != "started" {
			return
		}
		dirs, globErr := filepath.Glob(filepath.Join(opts.TempRoot, "seisforge-algorithm-*"))
		if globErr != nil || len(dirs) != 1 {
			lockErr = errors.New("task directory unavailable to cleanup-failure fixture")
		} else {
			release, lockErr = lockChildFile(filepath.Join(dirs[0], "host-held.lock"))
		}
		cancel()
	})
	if release != nil {
		release()
	}
	if lockErr != nil {
		t.Fatal(lockErr)
	}
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "remove algorithm task directory") {
		t.Fatalf("cleanup failure was hidden: %v", err)
	}
	records, _ := filepath.Glob(filepath.Join(opts.RecordDirectory, "*.json"))
	if len(records) != 1 {
		t.Fatal("cleanup failure run record missing")
	}
	data, readErr := os.ReadFile(records[0])
	if readErr != nil || !strings.Contains(string(data), "remove algorithm task directory") {
		t.Fatalf("cleanup failure absent from record: %s %v", data, readErr)
	}
	// Only this fixture's externally held lock prevented cleanup. Release it,
	// then remove the exact generated task children and verify recovery.
	dirs, _ := filepath.Glob(filepath.Join(opts.TempRoot, "seisforge-algorithm-*"))
	for _, dir := range dirs {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
	}
	assertNoTasks(t, opts.TempRoot)
}

func processHandles() int {
	var count uint32
	h, _, _ := algorithmKernel32.NewProc("GetCurrentProcess").Call()
	ok, _, _ := algorithmKernel32.NewProc("GetProcessHandleCount").Call(h, uintptr(unsafe.Pointer(&count)))
	if ok == 0 {
		return -1
	}
	return int(count)
}
func assertProcessExited(t *testing.T, pid int) {
	t.Helper()
	h, err := syscall.OpenProcess(0x1000|0x100000, false, uint32(pid))
	if err != nil {
		return
	}
	defer syscall.CloseHandle(h)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status, err := syscall.WaitForSingleObject(h, 10)
		if err == nil && status == 0 {
			return
		}
	}
	t.Fatalf("child process %d survived cancellation", pid)
}
