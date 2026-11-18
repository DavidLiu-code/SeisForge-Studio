//go:build windows

package main

var settingsWindowOwner = user32.NewProc("GetWindow")
var settingsWindowEnabled = user32.NewProc("IsWindowEnabled")

const settingsToolWindowStyle = 0x00000080 // WS_EX_TOOLWINDOW

// Explicit opening may show or restore this tool window, never its owner or
// the other tools. In particular do not SW_RESTORE an already visible tool.
func showOwnedSettingsWindow(dialog uintptr) {
	if dialog == 0 {
		return
	}
	if minimized, _, _ := pIsIconic.Call(dialog); minimized != 0 {
		pShowWindow.Call(dialog, SW_RESTORE)
	} else if visible, _, _ := poststackIsWindowVisible.Call(dialog); visible == 0 {
		pShowWindow.Call(dialog, SW_SHOW)
	}
	pSetForeground.Call(dialog)
}

// Let Win32 select the next active top-level window. Set only keyboard focus
// after Win32 has already selected the exact recorded owner; never activate,
// reveal or restore an owner (or a different app) as a closing side effect.
func closeOwnedSettingsWindow(dialog, owner uintptr) {
	if dialog == 0 || dialog == owner {
		return
	}
	const gwOwner = 4
	nativeOwner, _, _ := settingsWindowOwner.Call(dialog, gwOwner)
	activeBefore, _, _ := pGetForegroundWindow.Call()
	closed, _, _ := pDestroyWindow.Call(dialog)
	if closed == 0 || owner == 0 || owner != nativeOwner || activeBefore != dialog {
		return
	}
	activeAfter, _, _ := pGetForegroundWindow.Call()
	visible, _, _ := poststackIsWindowVisible.Call(owner)
	minimized, _, _ := pIsIconic.Call(owner)
	enabled, _, _ := settingsWindowEnabled.Call(owner)
	if activeAfter == owner && visible != 0 && minimized == 0 && enabled != 0 {
		pSetFocus.Call(owner)
	}
}
