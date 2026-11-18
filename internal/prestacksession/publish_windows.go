//go:build windows

package prestacksession

import (
	"fmt"
	"syscall"
	"unsafe"
)

var moveFileExSession = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func publishSnapshot(source, target string, overwrite bool) error {
	if !overwrite {
		// MoveFileEx without REPLACE_EXISTING is the exclusive publication
		// operation; no check-then-rename race is introduced here.
	}
	from, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	flags := uintptr(0x8) // MOVEFILE_WRITE_THROUGH
	if overwrite {
		flags |= 0x1 // MOVEFILE_REPLACE_EXISTING
	}
	ok, _, callErr := moveFileExSession.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), flags)
	if ok == 0 {
		return fmt.Errorf("publish prestack session: %w", callErr)
	}
	return nil
}
