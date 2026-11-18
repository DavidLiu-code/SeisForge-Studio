//go:build windows

package prestack

import (
	"context"
	"syscall"
	"time"
	"unsafe"
)

var metadataMoveFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

// A same-directory atomic replace; never remove the destination first. A
// competing reader may briefly deny delete-sharing on Windows. Bounded retries
// preserve cancellation and leave a usable memory index on a persistent error.
func publishMetadataCache(ctx context.Context, source, target string) error {
	from, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		r, _, callErr := metadataMoveFileEx.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), 0x1|0x8) // REPLACE_EXISTING | WRITE_THROUGH
		if r != 0 {
			return nil
		}
		if attempt == 7 || (callErr != syscall.Errno(32) && callErr != syscall.Errno(33) && callErr != syscall.Errno(5)) {
			return callErr
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
