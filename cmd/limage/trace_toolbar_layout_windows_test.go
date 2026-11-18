//go:build windows

package main

import (
	"runtime"
	"testing"
	"unsafe"
)

func TestTraceToolbarLabelsCenterInTheirNativeRows(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	common := INITCOMMONCONTROLSEX{DwSize: uint32(unsafe.Sizeof(INITCOMMONCONTROLSEX{})), DwICC: ICC_WIN95_CLASSES}
	pInitCommonCtrls.Call(uintptr(unsafe.Pointer(&common)))
	owner := createCtrl(0, "STATIC", "Trace toolbar test", WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN, 0, 0, 1250, 700, 0)
	if owner == 0 {
		t.Fatal("could not create native trace toolbar owner")
	}
	defer pDestroyWindow.Call(owner)
	oldOwner, oldUI, oldPage, oldFont := traceAnalysisHwnd, traceAnalysisUI, traceAnalysisPage, hFont
	oldDraft, oldScope := traceAnalysisManualDraft, traceAnalysisWindow
	oldStyle, oldContentTop := traceScientificStyle, traceScientificContentTop
	oldPrefs, oldLoaded := displayPrefs, displayPrefsLoaded
	defer func() {
		if tools := traceAnalysisUI.scientific.tools; tools != nil {
			tools.close()
		}
		traceAnalysisHwnd, traceAnalysisUI, traceAnalysisPage, hFont = oldOwner, oldUI, oldPage, oldFont
		traceAnalysisManualDraft, traceAnalysisWindow = oldDraft, oldScope
		traceScientificStyle, traceScientificContentTop = oldStyle, oldContentTop
		displayPrefs, displayPrefsLoaded = oldPrefs, oldLoaded
	}()
	traceAnalysisHwnd, traceAnalysisPage = owner, 3
	hFont, _, _ = pGetStockObject.Call(DEFAULT_GUI_FONT)
	displayPrefsLoaded, displayPrefs.TraceStyle = true, nil
	traceAnalysisWindow = traceAnalysisScope{mode: traceAnalysisManual, firstUS: 900000, lastUS: 1200000}
	createTraceAnalysisUI()
	u := &traceAnalysisUI.scientific
	getStyle := user32.NewProc("GetWindowLongW")
	for _, c := range []struct {
		name string
		h    uintptr
	}{{"trace number", traceAnalysisUI.numberLabel}, {"analysis window", traceAnalysisUI.windowLabel},
		{"header source", traceAnalysisUI.sourceLabel}, {"start seconds", u.scopeStartLabel}, {"end seconds", u.scopeEndLabel}} {
		style, _, _ := getStyle.Call(c.h, ^uintptr(15)) // GWL_STYLE
		if style&0x021f != 0x0200 {                     // SS_CENTERIMAGE | SS_LEFT (type 0)
			t.Errorf("%s is not vertically centered and left-aligned: style=%#x", c.name, style)
		}
	}
	for _, width := range []int{1200, 720} {
		dpi := displayDPI(owner)
		layoutTraceScientificControls(displayPixelDIP(float64(width), dpi), 600)
		for _, pair := range [][2]uintptr{{traceAnalysisUI.numberLabel, traceAnalysisUI.number}, {traceAnalysisUI.windowLabel, traceAnalysisUI.window}, {u.scopeStartLabel, u.scopeFirst}, {u.scopeEndLabel, u.scopeLast}} {
			caption, control := displayNativeRect(pair[0]), displayNativeRect(pair[1])
			if d := caption.Top + caption.Bottom - control.Top - control.Bottom; d < -2 || d > 2 {
				t.Errorf("%d DIP: %q is not centered on its control: caption=%+v control=%+v", width, getText(pair[0]), caption, control)
			}
		}
		for _, pair := range [][2]uintptr{{u.scopeStartLabel, u.scopeFirst}, {u.scopeEndLabel, u.scopeLast}} {
			style, _, _ := getStyle.Call(pair[1], ^uintptr(15))
			if style&0x0004 != 0 || style&(ES_AUTOHSCROLL|WS_TABSTOP) != ES_AUTOHSCROLL|WS_TABSTOP {
				t.Errorf("%d DIP: time input no longer behaves as a native single-line edit", width)
			}
			caption := displayNativeRect(pair[0])
			char, _, _ := pSendMessageW.Call(pair[1], 0x00D6, 0, 0) // EM_POSFROMCHAR, standard EDIT
			pt := POINT{X: int32(int16(char)), Y: int32(int16(char >> 16))}
			user32.NewProc("ClientToScreen").Call(pair[1], uintptr(unsafe.Pointer(&pt)))
			height := traceScientificTextSize(pair[1], "0").Height
			tolerance := int32(2 * displayPixelDIP(2, dpi))
			if d := 2*pt.Y + int32(height) - caption.Top - caption.Bottom; d < -tolerance || d > tolerance {
				t.Errorf("%d DIP: the visible number is not centered on %q (delta %.1f px)", width, getText(pair[0]), float64(d)/2)
			}
		}
	}
}
