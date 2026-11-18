//go:build windows

package main

import (
	"fmt"
	"math"
	"unsafe"
)

const (
	prestackWMKeyUp      = 0x0101
	prestackWMMouseLeave = 0x02a3
	prestackVKControl    = 0x11
)

var pPrestackTrackMouseEvent = user32.NewProc("TrackMouseEvent")

type prestackMouseTracking struct {
	Size, Flags uint32
	Window      uintptr
	HoverTime   uint32
}

// Transient UI state only. A render invalidation drops the preview, so it can
// never label a retained old frame using the next selection's physical rows.
type prestackComparePickPreview struct {
	Active     bool
	Panel, Row int
	Generation int64
}

func prestackComparePickRow(x int, raster RECT, width, count int) int {
	if raster.Right <= raster.Left || width < 1 || count < 1 || x < int(raster.Left) || x >= int(raster.Right) {
		return -1
	}
	column := prestackCompareAxisIndex(x, int(raster.Left), int(raster.Right), 0, width-1)
	return prestackCompareLogicalColumn(column, width, count)
}

func prestackComparePickX(row, count int, raster RECT) int {
	if count <= 1 {
		return int(raster.Left + (raster.Right-raster.Left-1)/2)
	}
	return int(raster.Left) + int(math.Round(float64(row)*float64(raster.Right-raster.Left-1)/float64(count-1)))
}

func prestackComparePickFrameReady(state *prestackSession, panel int) bool {
	if state.page != 3 || state.compareRendering || state.resizeActive || state.resizePending || panel < 0 || panel > 2 || state.compareWidth < 2 || state.compareHeight < 2 {
		return false
	}
	pixels := [3][]byte{state.compareA, state.compareB, state.compareDelta}[panel]
	return len(pixels) == state.compareWidth*state.compareHeight*4
}

func prestackComparePickPanelReady(state *prestackSession, panel int, outer RECT) bool {
	r := prestackCompareRasterRect(outer)
	return prestackComparePickFrameReady(state, panel) && int(r.Right-r.Left) == state.compareWidth && int(r.Bottom-r.Top) == state.compareHeight
}

func prestackComparePickAt(state *prestackSession, panels [3]RECT, x, y int, control bool) prestackComparePickPreview {
	if !control || (state.dragging && !state.ctrlClick) {
		return prestackComparePickPreview{}
	}
	for panel, outer := range panels {
		r := prestackCompareRasterRect(outer)
		if y < int(r.Top) || y >= int(r.Bottom) || !prestackComparePickPanelReady(state, panel, outer) {
			continue
		}
		row := prestackComparePickRow(x, r, state.compareWidth, len(state.compareDisplayRows))
		if row < 0 {
			continue
		}
		if _, _, ok := prestackCompareTraceForPanel(panel, state.compareDisplayRows[row], state.compareBAxis.Compatible); ok {
			return prestackComparePickPreview{Active: true, Panel: panel, Row: row, Generation: state.compareRenderGeneration}
		}
	}
	return prestackComparePickPreview{}
}

func clearPrestackComparePickPreview() {
	if prestackState.comparePickPreview.Active {
		prestackState.comparePickPreview = prestackComparePickPreview{}
		invalidatePrestackScene()
	}
}

func updatePrestackComparePickPreview(x, y int, control bool) {
	next := prestackComparePickAt(&prestackState, prestackComparePanelRects(), x, y, control)
	if next == prestackState.comparePickPreview {
		return
	}
	prestackState.comparePickPreview = next
	if next.Active {
		tracking := prestackMouseTracking{Flags: 2, Window: prestackHwnd} // TME_LEAVE
		tracking.Size = uint32(unsafe.Sizeof(tracking))
		pPrestackTrackMouseEvent.Call(uintptr(unsafe.Pointer(&tracking)))
	}
	// Invalidate only the scene: no control layout, Reader or render worker.
	invalidatePrestackScene()
}

// Observe both transitions even if a toolbar control holds keyboard focus.
// Do not consume Ctrl: native edits/combos retain their standard shortcuts.
func observePrestackCompareControlKey(target uintptr, message uint32, key uintptr) {
	if key != prestackVKControl || (message != WM_KEYDOWN && message != prestackWMKeyUp) || prestackHwnd == 0 {
		return
	}
	if message == prestackWMKeyUp {
		clearPrestackComparePickPreview()
		return
	}
	child, _, _ := pIsChild.Call(prestackHwnd, target)
	if (target != prestackHwnd && child == 0) || prestackTextInput(target) || prestackComboDropdownOpen() {
		return
	}
	var p POINT
	if ok, _, _ := pGetCursorPos.Call(uintptr(unsafe.Pointer(&p))); ok == 0 {
		return
	}
	if ok, _, _ := pScreenToClient.Call(prestackHwnd, uintptr(unsafe.Pointer(&p))); ok != 0 {
		updatePrestackComparePickPreview(int(p.X), int(p.Y), true)
	}
}

func paintPrestackComparePickPreview(hdc uintptr, panels [3]RECT) {
	p := prestackState.comparePickPreview
	if !p.Active || p.Panel < 0 || p.Panel > 2 || p.Generation != prestackState.compareRenderGeneration || !prestackComparePickPanelReady(&prestackState, p.Panel, panels[p.Panel]) || p.Row < 0 || p.Row >= len(prestackState.compareDisplayRows) {
		return
	}
	row := prestackState.compareDisplayRows[p.Row]
	for panel, outer := range panels {
		trace, role, present := prestackCompareTraceForPanel(panel, row, prestackState.compareBAxis.Compatible)
		if !present || !prestackComparePickPanelReady(&prestackState, panel, outer) {
			continue
		}
		r := prestackCompareRasterRect(outer)
		saved, _, _ := pSaveDC.Call(hdc)
		if saved == 0 {
			continue
		}
		pIntersectClipRect.Call(hdc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom))
		x := prestackComparePickX(p.Row, len(prestackState.compareDisplayRows), r)
		// A dark halo keeps the orange trace marker visible on every palette.
		for _, stroke := range []struct {
			width int
			color uintptr
		}{{3, rgbRef(30, 30, 30)}, {1, rgbRef(255, 180, 0)}} {
			pen, _, _ := pCreatePen.Call(0, uintptr(stroke.width), stroke.color)
			if pen == 0 {
				continue
			}
			old, _, _ := pSelectObject.Call(hdc, pen)
			drawLine(hdc, x, int(r.Top), x, int(r.Bottom))
			pSelectObject.Call(hdc, old)
			pDeleteObject.Call(pen)
		}
		label := RECT{Left: r.Left + 3, Top: r.Top + 3, Right: r.Right - 3, Bottom: min(r.Bottom, r.Top+25)}
		if label.Right > label.Left && label.Bottom > label.Top {
			white, _, _ := pGetStockObject.Call(WHITE_BRUSH)
			pFillRect.Call(hdc, uintptr(unsafe.Pointer(&label)), white)
			pSetTextColor.Call(hdc, rgbRef(0, 0, 0))
			pSetBkMode.Call(hdc, TRANSPARENT)
			text := fmt.Sprintf("Ctrl | 逻辑道 %d | %s 物理道 %d", prestackState.compareViewFirst+p.Row+1, role, trace+1)
			pDrawTextW.Call(hdc, uintptr(unsafe.Pointer(u16(text))), uintptr(^uint32(0)), uintptr(unsafe.Pointer(&label)), uintptr(prestackCompareSummaryFlags))
		}
		pRestoreDC.Call(hdc, saved)
	}
}
