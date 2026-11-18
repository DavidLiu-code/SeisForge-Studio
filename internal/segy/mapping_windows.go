//go:build windows

package segy

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

const (
	pageReadOnly = 0x02
	fileMapRead  = 0x0004
)

var prefetchVirtualMemory = syscall.NewLazyDLL("kernel32.dll").NewProc("PrefetchVirtualMemory")

type windowsMemoryRangeEntry struct {
	VirtualAddress uintptr
	NumberOfBytes  uintptr
}

func platformMapReadOnly(file *os.File, size int64) ([]byte, func() error, error) {
	if file == nil || size <= 0 || uint64(size) > uint64(^uint(0)>>1) {
		return nil, nil, errors.New("invalid read-only mapping size")
	}
	handle, err := syscall.CreateFileMapping(syscall.Handle(file.Fd()), nil, pageReadOnly, 0, 0, nil)
	if err != nil {
		return nil, nil, err
	}
	address, err := syscall.MapViewOfFile(handle, fileMapRead, 0, 0, 0)
	if err != nil {
		_ = syscall.CloseHandle(handle)
		return nil, nil, err
	}
	data := unsafe.Slice((*byte)(unsafe.Pointer(address)), int(size))
	closeView := func() error {
		unmapErr := syscall.UnmapViewOfFile(address)
		closeErr := syscall.CloseHandle(handle)
		return errors.Join(unmapErr, closeErr)
	}
	return data, closeView, nil
}

func platformPrefetchMapped(data []byte, ranges []mappedReadRange) bool {
	if len(data) == 0 || len(ranges) == 0 {
		return false
	}
	entries := make([]windowsMemoryRangeEntry, 0, len(ranges))
	base := uintptr(unsafe.Pointer(&data[0]))
	for _, selected := range ranges {
		if selected.Offset < 0 || selected.Length <= 0 || selected.Offset+selected.Length > int64(len(data)) {
			continue
		}
		entries = append(entries, windowsMemoryRangeEntry{VirtualAddress: base + uintptr(selected.Offset), NumberOfBytes: uintptr(selected.Length)})
	}
	if len(entries) == 0 {
		return false
	}
	process, err := syscall.GetCurrentProcess()
	if err != nil {
		return false
	}
	result, _, _ := prefetchVirtualMemory.Call(uintptr(process), uintptr(len(entries)), uintptr(unsafe.Pointer(&entries[0])), 0)
	return result != 0
}
