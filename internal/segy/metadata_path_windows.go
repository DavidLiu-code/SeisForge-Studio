//go:build windows

package segy

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

var metadataGetFinalPathName = syscall.NewLazyDLL("kernel32.dll").NewProc("GetFinalPathNameByHandleW")

func metadataCanonicalPath(file *os.File, _ string) (string, error) {
	// Resolve the already-open file's final path without requiring directory
	// enumeration rights on every ancestor (filepath.EvalSymlinks does so on
	// Windows to normalize case). No extra handle or amplitude read is needed.
	buffer := make([]uint16, 512)
	for {
		n, _, err := metadataGetFinalPathName.Call(file.Fd(), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0)
		runtime.KeepAlive(file)
		if n == 0 {
			return "", fmt.Errorf("resolve SEG-Y metadata path: %w", err)
		}
		if n >= uintptr(len(buffer)) {
			if n > 32768 {
				return "", fmt.Errorf("resolved SEG-Y path exceeds Windows path limit")
			}
			buffer = make([]uint16, int(n)+1)
			continue
		}
		path := syscall.UTF16ToString(buffer[:n])
		if strings.HasPrefix(path, `\\?\UNC\`) {
			path = `\\` + path[len(`\\?\UNC\`):]
		} else {
			path = strings.TrimPrefix(path, `\\?\`)
		}
		return filepath.Clean(path), nil
	}
}
