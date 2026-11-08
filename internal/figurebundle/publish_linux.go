//go:build linux

package figurebundle

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

func publishNewDirectory(source, target string) error {
	// Linux renameat2(RENAME_NOREPLACE) is the directory equivalent of an
	// atomic create. Plain os.Rename can replace a concurrently created empty
	// directory, so an existence check plus Rename is not sufficient.
	var trap uintptr
	switch runtime.GOARCH {
	case "amd64":
		trap = 316
	case "386":
		trap = 353
	case "arm":
		trap = 382
	case "arm64", "loong64", "riscv64":
		trap = 276
	case "mips", "mipsle":
		trap = 4351
	case "mips64", "mips64le":
		trap = 5311
	case "ppc64", "ppc64le":
		trap = 357
	case "s390x":
		trap = 347
	default:
		return fmt.Errorf("atomic non-replacing bundle publication unsupported on %s", runtime.GOARCH)
	}
	from, err := syscall.BytePtrFromString(source)
	if err != nil {
		return err
	}
	to, err := syscall.BytePtrFromString(target)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(trap, ^uintptr(99), uintptr(unsafe.Pointer(from)), ^uintptr(99), uintptr(unsafe.Pointer(to)), 1, 0)
	if errno != 0 {
		return fmt.Errorf("publish figure bundle without replacement: %w", errno)
	}
	return nil
}
