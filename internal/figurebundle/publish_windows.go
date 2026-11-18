//go:build windows

package figurebundle

import (
	"fmt"
	"syscall"
	"unsafe"
)

var moveFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func nativeMove(source, target string, overwrite bool) error {
	from, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	flags := uintptr(0x8) // WRITE_THROUGH; no replace unless explicitly authorized.
	if overwrite {
		flags |= 0x1
	}
	ok, _, callErr := moveFileEx.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), flags)
	if ok == 0 {
		return fmt.Errorf("publish figure: %w", callErr)
	}
	return nil
}

func publishNewDirectory(source, target string) error { return nativeMove(source, target, false) }
func publishFile(source, target string, overwrite bool) error {
	return nativeMove(source, target, overwrite)
}
