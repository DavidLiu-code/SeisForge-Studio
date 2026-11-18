//go:build windows

package main

import (
	"runtime"
	"testing"
	"unsafe"
)

func TestPoststackToolbarNativeFrameStates(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		state                uint32
		checked, hover, down bool
		want                 uintptr
	}{
		{"raised", 0, false, false, false, poststackDFCSButton},
		{"mouse or key held", poststackButtonSelected, false, true, true, poststackDFCSButton | poststackDFCSPushed},
		{"latched after release", 0, true, true, true, poststackDFCSButton | poststackDFCSPushed},
		{"hover", 0, false, true, false, poststackDFCSButton | poststackDFCSHot},
		{"keyboard focus is independent of push", poststackButtonFocus, false, false, false, poststackDFCSButton},
		{"disabled ignores hover and temporary push", poststackButtonDisabled | poststackButtonSelected, false, true, false, poststackDFCSButton | poststackDFCSInactive},
	} {
		if flags, down := poststackButtonFrame(tc.state, tc.checked, tc.hover); flags != tc.want || down != tc.down {
			t.Errorf("%s: flags=%#x down=%t, want %#x down=%t", tc.name, flags, down, tc.want, tc.down)
		}
	}
	// Native off-screen painting must actually change the frame, not just its
	// flag. No screenshot automation, focus, or display resolution is needed.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	owner := createCtrl(0, "STATIC", "native frames", WS_OVERLAPPEDWINDOW, 0, 0, 100, 100, 0)
	if owner == 0 {
		t.Fatal("could not create native test owner")
	}
	defer pDestroyWindow.Call(owner)
	button := createPoststackNamedToolButton(owner, 1, 0, "", poststackPushToggle, 0, 0, 48, 32)
	if button == 0 {
		t.Fatal("could not create native toggle")
	}
	dc, _, _ := pCreateCompatibleDC.Call(0)
	if dc == 0 {
		t.Fatal("could not create compatible DC")
	}
	defer pDeleteDC.Call(dc)
	bmi := BITMAPINFO{Header: BITMAPINFOHEADER{Size: uint32(unsafe.Sizeof(BITMAPINFOHEADER{})), Width: 48, Height: -32, Planes: 1, BitCount: 32, Compression: BI_RGB}}
	var bits uintptr
	bmp, _, _ := pCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&bmi)), DIB_RGB_COLORS, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == 0 {
		t.Fatal("could not create DIB")
	}
	defer pDeleteObject.Call(bmp)
	old, _, _ := pSelectObject.Call(dc, bmp)
	defer pSelectObject.Call(dc, old)
	buf := unsafe.Slice((*uint32)(unsafe.Pointer(bits)), 48*32)
	flush := gdi32.NewProc("GdiFlush")
	edges := func() [4]uint32 {
		pSendMessageW.Call(button, 0x0318 /* WM_PRINTCLIENT */, dc, 4 /* PRF_CLIENT */)
		flush.Call()
		return [4]uint32{buf[0*48+24], buf[1*48+24], buf[30*48+24], buf[31*48+24]}
	}
	raised := edges()
	pSendMessageW.Call(button, 0x00f3 /* BM_SETSTATE */, 1, 0)
	held := edges()
	pSendMessageW.Call(button, 0x00f3 /* BM_SETSTATE */, 0, 0)
	if raised == held || raised != edges() {
		t.Fatalf("physical press did not depress and then restore the native bevel: raised=%#x held=%#x", raised, held)
	}
	pSendMessageW.Call(button, BM_SETCHECK, BST_CHECKED, 0)
	if !pSendMessageWCheck(button) || held != edges() {
		t.Fatal("checked state failed to hold the same native depression after release")
	}
}

func TestPoststackToolbarExclusiveModeAndEscape(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	h := createCtrl(0, "STATIC", "native mode test", WS_OVERLAPPEDWINDOW, 0, 0, 100, 100, 0)
	if h == 0 {
		t.Fatal("could not create test owner")
	}
	defer pDestroyWindow.Call(h)
	old, oldControls, oldTool := compareHwnd, cc, compareTool
	defer func() { compareHwnd, cc, compareTool = old, oldControls, oldTool }()
	compareHwnd, cc = h, compareControls{}
	makeButton := func(id int) uintptr {
		return createPoststackNamedToolButton(h, id, 0, "", poststackPushToggle, 0, 0, 40, 22)
	}
	cc.zoom, cc.rectTool, cc.ellipseTool, cc.lineTool = makeButton(ID_ZOOM), makeButton(IDCMP_RECTTOOL), makeButton(IDCMP_ELLIPSETOOL), makeButton(IDCMP_LINETOOL)
	cc.traceInspect, cc.crosshair = makeButton(IDCMP_TRACE_INSPECT), makeButton(IDCMP_CROSSHAIR)
	pSendMessageW.Call(cc.crosshair, BM_SETCHECK, BST_CHECKED, 0)
	for _, tool := range []poststackCanvasTool{poststackToolZoom, poststackToolTrace, poststackToolRect, poststackToolEllipse, poststackToolLine} {
		setPoststackCompareTool(tool)
		if compareTool != tool || poststackCompareToolCursor(tool, false) != IDC_CROSS {
			t.Fatalf("routed tool/cursor differs from the requested mode %v", tool)
		}
		for _, pair := range []struct {
			h    uintptr
			mode poststackCanvasTool
		}{{cc.zoom, poststackToolZoom}, {cc.traceInspect, poststackToolTrace}, {cc.rectTool, poststackToolRect}, {cc.ellipseTool, poststackToolEllipse}, {cc.lineTool, poststackToolLine}} {
			if pSendMessageWCheck(pair.h) != (pair.mode == tool) {
				t.Fatalf("mode %v has stale button %v", tool, pair.mode)
			}
		}
		compareWndProc(h, WM_KEYDOWN, VK_ESCAPE, 0)
		if compareTool != poststackToolIdle || !pSendMessageWCheck(cc.crosshair) || pSendMessageWCheck(cc.zoom) || pSendMessageWCheck(cc.traceInspect) || pSendMessageWCheck(cc.rectTool) || pSendMessageWCheck(cc.ellipseTool) || pSendMessageWCheck(cc.lineTool) {
			t.Fatalf("Esc left a selected tool or changed the independent crosshair, after mode %v", tool)
		}
	}
}
