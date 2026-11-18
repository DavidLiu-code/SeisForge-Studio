//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// One real mode determines cursor, drag routing, and the check states. The
// crosshair/fixed-cache toggles are independent of this exclusive group.
type poststackCanvasTool int

const (
	poststackToolIdle poststackCanvasTool = iota
	poststackToolRect
	poststackToolEllipse
	poststackToolLine
	poststackToolZoom
	poststackToolTrace
)

func poststackAnnotationKind(tool poststackCanvasTool) int {
	if tool >= poststackToolRect && tool <= poststackToolLine {
		return int(tool - poststackToolRect)
	}
	return -1
}

func syncPoststackCompareToolButtons() {
	buttons := [...]struct {
		hwnd uintptr
		tool poststackCanvasTool
	}{{cc.zoom, poststackToolZoom}, {cc.traceInspect, poststackToolTrace}, {cc.rectTool, poststackToolRect}, {cc.ellipseTool, poststackToolEllipse}, {cc.lineTool, poststackToolLine}}
	for _, b := range buttons {
		if b.hwnd != 0 && pSendMessageWCheck(b.hwnd) != (compareTool == b.tool) {
			pSendMessageW.Call(b.hwnd, BM_SETCHECK, uintptr(boolToInt(compareTool == b.tool)), 0)
		}
	}
}

func cancelPoststackCompareToolDrag(release bool) {
	if !compareZoomDragging && !compareAnnotDragging && !compareFilterPanDragging {
		return
	}
	compareZoomDragging, compareAnnotDragging, compareFilterPanDragging = false, false, false
	if release && compareHwnd != 0 {
		if capture, _, _ := displayGetCapture.Call(); capture == compareHwnd {
			pReleaseCapture.Call()
		}
	}
	if compareHwnd != 0 {
		invalidateCompareOverlayAll()
	}
}

func setPoststackCompareTool(tool poststackCanvasTool) {
	cancelPoststackCompareToolDrag(true)
	compareTool = tool
	syncPoststackCompareToolButtons()
	if compareHwnd != 0 && len(compareA.bgra) != 0 {
		setPoststackCompareToolCursor(compareHwnd)
	}
	if cc.status == 0 {
		return
	}
	switch tool {
	case poststackToolZoom:
		setText(cc.status, "Zoom：在剖面中拖框放大；A/B/差按数据坐标同步，Esc 退出。")
	case poststackToolTrace:
		setText(cc.status, "单道分析：单击 A、B 或差值图选择一道；Esc 退出选道。")
	case poststackToolRect, poststackToolEllipse, poststackToolLine:
		setText(cc.status, "标注模式：在任一剖面中拖动；A/B/差按数据坐标同步，Esc 退出。")
	default:
		setText(cc.status, "导航模式：使用工具栏选择框选缩放、单道分析或标注。")
	}
}

func togglePoststackCompareTool(tool poststackCanvasTool) {
	if compareTool == tool {
		tool = poststackToolIdle
	}
	setPoststackCompareTool(tool)
}

func poststackCompareToolCursor(tool poststackCanvasTool, panning bool) uintptr {
	if panning {
		return IDC_SIZEALL
	}
	if tool != poststackToolIdle {
		return IDC_CROSS
	}
	return IDC_ARROW
}

func setPoststackCompareToolCursor(owner uintptr) bool {
	if compareAPath == "" || len(compareA.bgra) == 0 {
		return false
	}
	var pt POINT
	if ok, _, _ := pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt))); ok == 0 {
		return false
	}
	pScreenToClient.Call(owner, uintptr(unsafe.Pointer(&pt)))
	if !compareFilterPanDragging {
		inside := false
		for panel := 0; panel < comparePanelCount(); panel++ {
			inside = inside || comparePointInRect(int(pt.X), int(pt.Y), comparePanelRect(panel))
		}
		if !inside {
			return false
		}
	}
	c, _, _ := pLoadCursorW.Call(0, poststackCompareToolCursor(compareTool, compareFilterPanDragging))
	if c == 0 {
		return false
	}
	pSetCursor.Call(c)
	return true
}

// Esc on a focused toolbar pushbutton belongs to its owner; Esc on an
// EDIT or COMBOBOX remains native, as do all keys in owned settings windows.
func translatePoststackToolEscape(target, owner uintptr, key uintptr) bool {
	if key != VK_ESCAPE || target == 0 || owner == 0 || target == owner {
		return false
	}
	if child, _, _ := pIsChild.Call(owner, target); child == 0 {
		return false
	}
	var buf [64]uint16
	n, _, _ := poststackGetClassName.Call(target, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 || n >= uintptr(len(buf)) || !poststackShortcutFocus(target, owner, syscall.UTF16ToString(buf[:n]), false) {
		return false
	}
	if owner == compareHwnd && compareTool == poststackToolIdle && !compareFilterPanDragging {
		return false
	}
	if owner == hwnd && !zoomMode {
		return false
	}
	pSendMessageW.Call(owner, WM_KEYDOWN, key, 0)
	return true
}
