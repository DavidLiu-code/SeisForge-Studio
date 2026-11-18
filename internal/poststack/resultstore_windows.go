//go:build windows

package poststack

import (
	"os"
	"syscall"
	"unsafe"
)

var poststackMoveFileExW = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

// A single MoveFileEx operation is the publication point. In particular, never
// remove dst before replacing it, and do not use a preflight existence check as
// protection against a concurrent publisher.
func publishResultFile(src, dst string, overwrite bool) error {
	source, err := syscall.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	target, err := syscall.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	flags := uintptr(0x8) // MOVEFILE_WRITE_THROUGH; files share a directory/volume.
	if overwrite {
		flags |= 0x1
	} // MOVEFILE_REPLACE_EXISTING
	ok, _, callErr := poststackMoveFileExW.Call(uintptr(unsafe.Pointer(source)), uintptr(unsafe.Pointer(target)), flags)
	if ok == 0 {
		return &os.LinkError{Op: "publish", Old: src, New: dst, Err: callErr}
	}
	return nil
}
