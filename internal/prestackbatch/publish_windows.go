//go:build windows

package prestackbatch

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32   = syscall.NewLazyDLL("kernel32.dll")
	moveFileEx = kernel32.NewProc("MoveFileExW")
)

func publishPlan(temp, target string, overwrite bool) error {
	if !overwrite {
		if _, err := os.Stat(target); err == nil {
			return ErrPlanExists
		}
	}
	flags := uintptr(0)
	if overwrite {
		flags |= 0x1
	}
	from, err := syscall.UTF16PtrFromString(temp)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	if r, _, e := moveFileEx.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), flags); r == 0 {
		return fmt.Errorf("publish batch plan: %w", e)
	}
	return nil
}
