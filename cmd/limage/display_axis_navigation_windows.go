//go:build windows

package main

import (
	"fmt"
	"math"
	"time"
	"unsafe"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
)

// An axis gesture is independent of the settings draft and of image tools.
// Mouse motion only updates an explicitly labelled preview. The existing
// viewport adapter is called once on release, never once per pointer event.
type displayAxisNavigation struct {
	key, identity, version string
	axis                   displaycore.Axis
	plot                   RECT
	vertical               bool
	start                  int
	threshold              int
	quantum                float64
	coords                 []int32
	candidate              displaycore.Range
	dragging               bool
}

var displayAxisNavigations = make(map[uintptr]*displayAxisNavigation)
var displayAxisDragEnd = make(map[uintptr]displayAxisDragCompletion)

type displayAxisDragCompletion struct {
	x, y  int
	until time.Time
}

func displayNavigationHit(owner uintptr, x, y int) (string, displaycore.Axis, RECT, bool, bool) {
	key, id, color := displayHit(owner, x, y, false)
	if key == "" || color || id == "" {
		return "", displaycore.Axis{}, RECT{}, false, false
	}
	o := displayOwners[owner]
	e := o.surfaces[key]
	if !e.s.LiveViewport || e.s.Apply == nil || displayWithin(e.s.Plot, x, y) {
		return "", displaycore.Axis{}, RECT{}, false, false
	}
	for i, axis := range e.s.Axes {
		if axis.ID == id {
			axis = displayAxisFor(owner, key, axis)
			if axis.StyleOnly || axis.HideAxis || !axis.Data.Valid() || !axis.View.Valid() {
				break
			}
			return key, axis, e.s.Plot, i == 1, true
		}
	}
	return "", displaycore.Axis{}, RECT{}, false, false
}

func displayNavigationQuantum(owner uintptr, axis displaycore.Axis) float64 {
	if axis.ID != "time" {
		if displayAxisIsDistance(axis) {
			return 0
		}
		if axis.ID == "trace" || axis.ID == "inline" || axis.ID == "crossline" {
			return 1
		}
		return 0
	}
	var dtUS int
	if owner == compareHwnd {
		dtUS = compareDtAUS
		if compareTD != nil && compareTD.fa != nil {
			dtUS = compareTD.fa.Info.SampleIntervalUS
		}
	} else if owner == hwnd && sf != nil {
		dtUS = sf.Info.SampleIntervalUS
	}
	if dtUS <= 0 {
		return 0
	}
	timeAxis := displaycore.Axis{Unit: axis.CanonicalUnit(), BaseUnit: "ms"}
	return timeAxis.DisplayValue(float64(dtUS) / 1000)
}

func displayNavigationBegin(owner uintptr, x, y int) bool {
	delete(displayAxisDragEnd, owner)
	key, axis, plot, vertical, ok := displayNavigationHit(owner, x, y)
	if !ok {
		return false
	}
	e := displayOwners[owner].surfaces[key]
	n := &displayAxisNavigation{key: key, identity: e.s.Identity, version: e.s.StatsVersion,
		axis: axis, plot: plot, vertical: vertical, start: x,
		threshold: max(4, 4*displayDPI(owner)/96), quantum: displayNavigationQuantum(owner, axis), candidate: axis.View}
	if vertical {
		n.start = y
	} else if owner == compareHwnd && compareMode != 2 && !displayAxisIsDistance(axis) {
		for panel := 0; panel < 3; panel++ {
			if key != seismicCompareKey(panel) {
				continue
			}
			p := seismicComparePanel(panel)
			if p == nil {
				break
			}
			coords := p.coords
			// A partial B occupies only part of the shared viewport. Its axis
			// still spans A; do not stretch B's ranks over that complete span.
			if _, _, partial := seismicComparePartial(panel, false); partial {
				coords = compareA.coords
			}
			n.coords = append([]int32(nil), coords...)
			break
		}
	}
	displayAxisNavigations[owner] = n
	pSetFocus.Call(owner)
	pSetCapture.Call(owner)
	return true
}

func (n *displayAxisNavigation) valueAt(pixel int) float64 {
	start, end := int(n.plot.Left), int(n.plot.Right)-1
	if n.vertical {
		start, end = int(n.plot.Top), int(n.plot.Bottom)-1
	}
	if end <= start {
		return n.axis.View.Min
	}
	f := float64(pixel-start) / float64(end-start)
	if n.vertical && !n.axis.Down {
		f = 1 - f
	}
	if !n.vertical && len(n.coords) > 1 {
		if n.axis.Reverse {
			f = 1 - f
		}
		return seismicAcrossAt(n.coords, f)
	}
	return n.axis.ValueAtFraction(f)
}

func (n *displayAxisNavigation) move(pixel int) bool {
	if !n.dragging && absInt(pixel-n.start) < n.threshold {
		return false
	}
	n.dragging = true
	delta := n.valueAt(n.start) - n.valueAt(pixel)
	if n.quantum > 0 {
		delta = math.Round(delta/n.quantum) * n.quantum
	}
	n.candidate = displaycore.DragRange(n.axis.Data, n.axis.View, displaycore.DragWhole, delta)
	return true
}

func displayNavigationCurrent(owner uintptr, n *displayAxisNavigation) bool {
	o := displayOwners[owner]
	if n == nil || o == nil {
		return false
	}
	e := o.surfaces[n.key]
	if e == nil || e.order == 0 || e.s.Identity != n.identity || e.s.StatsVersion != n.version || e.s.Plot != n.plot {
		return false
	}
	for _, axis := range e.s.Axes {
		if axis.ID == n.axis.ID {
			axis = displayAxisFor(owner, n.key, axis)
			return axis.View == n.axis.View && axis.Data == n.axis.Data && axis.Reverse == n.axis.Reverse && axis.Down == n.axis.Down
		}
	}
	return false
}

func displayNavigationRepaint(owner uintptr) {
	if owner == compareHwnd {
		invalidateCompareOverlayAll()
	} else {
		pInvalidateRect.Call(owner, 0, 0)
	}
}

func displayNavigationMove(owner uintptr, x, y int) bool {
	n := displayAxisNavigations[owner]
	if n == nil {
		return false
	}
	if !displayNavigationCurrent(owner, n) {
		displayNavigationCancel(owner)
		return true
	}
	pixel := x
	if n.vertical {
		pixel = y
	}
	if n.move(pixel) {
		displayNavigationRepaint(owner)
	}
	return true
}

func displayNavigationCancel(owner uintptr) bool {
	if displayAxisNavigations[owner] == nil {
		return false
	}
	delete(displayAxisNavigations, owner)
	if captured, _, _ := displayGetCapture.Call(); captured == owner {
		pReleaseCapture.Call()
	}
	displayNavigationRepaint(owner)
	return true
}

var displayGetCapture = user32.NewProc("GetCapture")
var displayNavigationGetCursorPos = user32.NewProc("GetCursorPos")

func displayNavigationEnd(owner uintptr, x, y int) bool {
	n := displayAxisNavigations[owner]
	if n == nil {
		return false
	}
	current := displayNavigationCurrent(owner, n)
	if current {
		pixel := x
		if n.vertical {
			pixel = y
		}
		n.move(pixel)
	}
	displayNavigationCancel(owner)
	if n.dragging {
		ms, _, _ := displayGetDoubleClickTime.Call()
		displayAxisDragEnd[owner] = displayAxisDragCompletion{x: x, y: y, until: time.Now().Add(time.Duration(ms) * time.Millisecond)}
	}
	if !current || !n.dragging || n.candidate == n.axis.View {
		return true
	}
	e := displayOwners[owner].surfaces[n.key]
	s, _ := displayCommitted(owner, n.key)
	for i := range s.Axes {
		s.Axes[i].ViewEdited = s.Axes[i].ID == n.axis.ID
		if s.Axes[i].ViewEdited {
			s.Axes[i].View = n.candidate
		}
	}
	// Direct navigation is not a settings Apply: do not persist a data-bound
	// viewport or overwrite an independently edited dialog draft.
	if err := e.s.Apply(s); err != nil {
		text := "坐标平移未应用：" + err.Error()
		if owner == compareHwnd {
			setText(cc.status, text)
		} else if owner == hwnd {
			setText(statusLabel, text)
		}
	} else {
		displayRefreshEditorLive(owner)
	}
	displayNavigationRepaint(owner)
	return true
}

var displayGetDoubleClickTime = user32.NewProc("GetDoubleClickTime")

// Win32 may classify the release/next press near a drag endpoint as a double
// click. Do not turn the end of a real pan into an unintended settings popup.
func displayNavigationDoubleClick(owner uintptr, x, y int) bool {
	displayNavigationCancel(owner)
	ended, ok := displayAxisDragEnd[owner]
	delete(displayAxisDragEnd, owner)
	threshold := max(4, 4*displayDPI(owner)/96)
	return ok && time.Now().Before(ended.until) && absInt(x-ended.x) < threshold && absInt(y-ended.y) < threshold
}

func displayNavigationClose(owner uintptr) {
	displayNavigationCancel(owner)
	delete(displayAxisDragEnd, owner)
}

func displayNavigationHover(owner uintptr, x, y int) bool {
	_, axis, _, _, ok := displayNavigationHit(owner, x, y)
	if !ok {
		return false
	}
	label := statusLabel
	if owner == compareHwnd {
		label = cc.status
	}
	if label != 0 {
		text := axis.Title + "：拖动平移范围，双击打开设置"
		if getText(label) != text {
			setText(label, text)
		}
	}
	return true
}

func displayNavigationCursor(owner uintptr) bool {
	var p POINT
	if ok, _, _ := displayNavigationGetCursorPos.Call(uintptr(unsafe.Pointer(&p))); ok == 0 {
		return false
	}
	pScreenToClient.Call(owner, uintptr(unsafe.Pointer(&p)))
	_, _, _, vertical, ok := displayNavigationHit(owner, int(p.X), int(p.Y))
	if n := displayAxisNavigations[owner]; n != nil {
		vertical, ok = n.vertical, true
	}
	if !ok {
		return false
	}
	id := uintptr(32644) // IDC_SIZEWE
	if vertical {
		id = 32645 // IDC_SIZENS
	}
	if cursor, _, _ := pLoadCursorW.Call(0, id); cursor != 0 {
		pSetCursor.Call(cursor)
	}
	return true
}

func displayPaintAxisNavigation(dc, owner uintptr) {
	n := displayAxisNavigations[owner]
	if dc == 0 || n == nil || !n.dragging || !displayNavigationCurrent(owner, n) {
		return
	}
	saved, _, _ := pSaveDC.Call(dc)
	if saved == 0 {
		return
	}
	defer pRestoreDC.Call(dc, saved)
	if hFont != 0 {
		pSelectObject.Call(dc, hFont)
	}
	pSetBkMode.Call(dc, TRANSPARENT)
	pSetTextColor.Call(dc, rgbRef(31, 65, 102))
	dpi := displayDPI(owner)
	pad, height := max(6, 6*dpi/96), max(24, 24*dpi/96)
	r := n.plot
	r.Left += int32(pad)
	r.Top += int32(pad)
	r.Right -= int32(pad)
	r.Bottom = min(r.Bottom, r.Top+int32(height))
	pIntersectClipRect.Call(dc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom))
	displayFill(dc, r, displaycore.RGB{R: 233, G: 243, B: 255})
	text := fmt.Sprintf("范围预览：%s … %s %s；松开应用，Esc 取消", displayNum(n.axis.DisplayValue(n.candidate.Min)), displayNum(n.axis.DisplayValue(n.candidate.Max)), n.axis.Unit)
	drawAxisText(dc, text, int(r.Left)+pad, int(r.Top), int(r.Right)-pad, int(r.Bottom), DT_LEFT|DT_VCENTER)
}
