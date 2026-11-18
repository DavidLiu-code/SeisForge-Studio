//go:build windows

package mappingprofile

import (
	"fmt"
	"syscall"
	"unsafe"
)

var moveFileExProfile = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func publishProfile(source, target string, overwrite bool) error {
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
	ok, _, callErr := moveFileExProfile.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), flags)
	if ok == 0 {
		return fmt.Errorf("publish mapping profile: %w", callErr)
	}
	return nil
}
