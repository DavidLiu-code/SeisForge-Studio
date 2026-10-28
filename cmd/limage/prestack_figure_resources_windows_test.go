//go:build windows

package main

import (
	"context"
	"os"
	"reflect"
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

// Exercises actual GDI DC/font/bitmap creation and data-driven replay, without
// controlling the user's mouse or creating any visible application window.
func TestPrestackFigureReplayReleasesNativeResources(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	state, payload := figureGatherFixture(t, 800, 600, 1, true)
	resources := syscall.NewLazyDLL("user32.dll").NewProc("GetGuiResources")
	handles := syscall.NewLazyDLL("kernel32.dll").NewProc("GetProcessHandleCount")
	count := func() [3]uint32 {
		gdi, _, _ := resources.Call(^uintptr(0), 0)
		user, _, _ := resources.Call(^uintptr(0), 1)
		native := reflect.New(reflect.TypeOf(uint32(0))).Interface().(*uint32)
		ok, _, _ := handles.Call(^uintptr(0), uintptr(unsafe.Pointer(native)))
		runtime.KeepAlive(native)
		if ok == 0 {
			t.Fatal("GetProcessHandleCount failed")
		}
		return [3]uint32{uint32(gdi), uint32(user), *native}
	}
	if _, err := RenderPrestackFigure(context.Background(), payload, state.dataset.Path, ""); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	before := count()
	for i := 0; i < 16; i++ {
		if _, err := RenderPrestackFigure(context.Background(), payload, state.dataset.Path, ""); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	after := count()
	if after[0] != before[0] || after[1] != before[1] || after[2] > before[2]+2 {
		t.Fatalf("figure replay leaked GDI/USER/native handles: %v -> %v", before, after)
	}
	// Replay is read-only, and the original fixture remains usable.
	if _, err := os.Stat(state.dataset.Path); err != nil {
		t.Fatal("source was changed or removed by replay:", err)
	}
	t.Logf("16 real replay cycles: GDI/USER/native handles %v -> %v", before, after)
}
