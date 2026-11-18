//go:build windows

package main

import (
	"runtime"
	"sync"
	"syscall"
	"testing"
	"unsafe"
)

var testLegacyParameterClass sync.Once

type settingsTestWindowState struct {
	rect                        RECT
	visible, minimized, enabled uintptr
}

func readSettingsTestWindowState(h uintptr) settingsTestWindowState {
	visible, _, _ := poststackIsWindowVisible.Call(h)
	minimized, _, _ := pIsIconic.Call(h)
	enabled, _, _ := settingsWindowEnabled.Call(h)
	return settingsTestWindowState{displayNativeRect(h), visible, minimized, enabled}
}

func makeSettingsTestWindow(t *testing.T, class string, owner uintptr) uintptr {
	t.Helper()
	inst, _, _ := pGetModuleHandleW.Call(0)
	h, _, _ := pCreateWindowExW.Call(settingsToolWindowStyle, uintptr(unsafe.Pointer(u16(class))), 0,
		WS_OVERLAPPEDWINDOW, 100, 150, 500, 390, owner, 0, inst, 0)
	if h == 0 {
		t.Fatal("failed to create native settings test window")
	}
	t.Cleanup(func() { pDestroyWindow.Call(h) })
	return h
}

func TestSettingsCloseOnlyDestroysItsOwnNativeDialog(t *testing.T) {
	for _, tc := range []struct {
		name    string
		msg     uintptr
		command uintptr
	}{
		{"X", WM_CLOSE, 0},
		{"cancel", WM_COMMAND, deCancel},
		{"system-close", 0x0112, 0xf060}, // WM_SYSCOMMAND / SC_CLOSE (Alt+F4)
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime.LockOSThread()
			t.Cleanup(runtime.UnlockOSThread)
			displayEnsureWindows()
			owner := makeSettingsTestWindow(t, "STATIC", 0)
			peer := makeSettingsTestWindow(t, "STATIC", 0)
			sibling := makeSettingsTestWindow(t, "STATIC", owner)
			h := makeSettingsTestWindow(t, "SeisForgeDisplaySettings", owner)
			w := &displayEditor{handle: h, owner: owner, key: "deleted-surface", invalid: true}
			displayEditors[h] = w
			t.Cleanup(func() { delete(displayEditors, h) })
			// Cancel must also close a stale editor with no live display surface.
			beforeOwner, beforePeer, beforeSibling := readSettingsTestWindowState(owner), readSettingsTestWindowState(peer), readSettingsTestWindowState(sibling)
			pSendMessageW.Call(h, tc.msg, tc.command, 0)
			if displayEditors[h] != nil {
				t.Fatal("only the intended dialog should receive close")
			}
			if readSettingsTestWindowState(owner) != beforeOwner || readSettingsTestWindowState(peer) != beforePeer || readSettingsTestWindowState(sibling) != beforeSibling {
				t.Fatal("owner or another window changed position, visibility, minimized or enabled state")
			}
		})
	}
}

func TestLegacyParameterCloseDoesNotEnableOrActivateAnOwner(t *testing.T) {
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	const cls = "SeisForgeParametersIsolationTest"
	inst, _, _ := pGetModuleHandleW.Call(0)
	cursor, _, _ := pLoadCursorW.Call(0, IDC_ARROW)
	testLegacyParameterClass.Do(func() {
		registerClass(cls, syscall.NewCallback(paramWndProc), COLOR_BTNFACE+1, inst, cursor)
	})
	owner := makeSettingsTestWindow(t, "STATIC", 0)
	peer := makeSettingsTestWindow(t, "STATIC", 0)
	h := makeSettingsTestWindow(t, cls, owner)
	oldDialog, oldOwner, oldControls := paramHwnd, paramOwner, pc
	oldCompareA, oldLegacyA := paramCompareA, paramLegacyA
	t.Cleanup(func() {
		paramHwnd, paramOwner, pc = oldDialog, oldOwner, oldControls
		paramCompareA, paramLegacyA = oldCompareA, oldLegacyA
	})
	paramHwnd, paramOwner = h, owner
	pEnableWindow.Call(owner, 0) // disabled elsewhere, not by this modeless dialog
	beforeOwner, beforePeer := readSettingsTestWindowState(owner), readSettingsTestWindowState(peer)
	pSendMessageW.Call(h, WM_COMMAND, IDP_CANCEL, 0)
	if paramHwnd != 0 || paramOwner != 0 || readSettingsTestWindowState(owner) != beforeOwner || readSettingsTestWindowState(peer) != beforePeer {
		t.Fatal("legacy parameters changed a window they did not own on close")
	}
}

func TestLegacyParameterDraftRejectsReplacementA(t *testing.T) {
	previousCompare, previousPath, previousOwner, previousSelected := compareHwnd, compareAPath, paramOwner, paramCompareA
	t.Cleanup(func() {
		compareHwnd, compareAPath, paramOwner, paramCompareA = previousCompare, previousPath, previousOwner, previousSelected
	})
	compareHwnd, compareAPath, paramOwner, paramCompareA = 99, "first.sgy", 99, "first.sgy"
	if !parameterDialogSourceCurrent() {
		t.Fatal("a draft for its exact, current owner should be accepted")
	}
	compareAPath = "second.sgy"
	if parameterDialogSourceCurrent() {
		t.Fatal("draft would modify a newly loaded A")
	}
}
