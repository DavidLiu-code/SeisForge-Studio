//go:build windows

package main

import (
	"log"
	"os"
	"syscall"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
)

const (
	algorithmOwnerDraw       = 0x000B
	algorithmInitialWidth    = 690
	algorithmMinWidth        = 400
	algorithmMinHeight       = 330
	algorithmGutter          = 16
	algorithmGap             = 8
	algorithmControlHeight   = 28
	algorithmWMDrawItem      = 0x002B
	algorithmWMDPIChanged    = 0x02E0
	algorithmWMControlStatic = 0x0138
	algorithmClipSiblings    = 0x04000000 // WS_CLIPSIBLINGS
	algorithmNoRedraw        = 0x0008     // SWP_NOREDRAW
	algorithmNoCopyBits      = 0x0100     // SWP_NOCOPYBITS
	algorithmShowWindow      = 0x0040     // SWP_SHOWWINDOW
	algorithmHideWindow      = 0x0080     // SWP_HIDEWINDOW
	algorithmRedrawFrame     = 0x0400     // RDW_FRAME
)

var (
	algorithmMonitorFromWindow = user32.NewProc("MonitorFromWindow")
	algorithmMonitorFromRect   = user32.NewProc("MonitorFromRect")
	algorithmMonitorInfo       = user32.NewProc("GetMonitorInfoW")
	algorithmAdjustWindow      = user32.NewProc("AdjustWindowRectExForDpi")
	algorithmAdjustLegacy      = user32.NewProc("AdjustWindowRectEx")
	algorithmSetThreadDPI      = user32.NewProc("SetThreadDpiAwarenessContext")
	algorithmGetWindowDPI      = user32.NewProc("GetWindowDpiAwarenessContext")
	algorithmIsDialogMessage   = user32.NewProc("IsDialogMessageW")
	algorithmGetFocus          = user32.NewProc("GetFocus")
	algorithmDrawFocus         = user32.NewProc("DrawFocusRect")
	algorithmSysColorBrush     = user32.NewProc("GetSysColorBrush")
	algorithmSetBkColor        = gdi32.NewProc("SetBkColor")
	algorithmGetWindowStyle    = user32.NewProc("GetWindowLongW")
	algorithmGetParent         = user32.NewProc("GetParent")
	algorithmIsZoomed          = user32.NewProc("IsZoomed")
	algorithmIsIconic          = user32.NewProc("IsIconic")
	algorithmSetSubclass       = comctl32.NewProc("SetWindowSubclass")
	algorithmDefSubclass       = comctl32.NewProc("DefSubclassProc")
	algorithmRemoveSubclass    = comctl32.NewProc("RemoveWindowSubclass")
	algorithmBodyCallback      uintptr
	algorithmDialogFont        uintptr
	algorithmDialogBold        uintptr
	algorithmDialogFontDPI     int
	algorithmDialogSized       bool
	algorithmDialogSizing      bool
	algorithmNormalWindow      RECT
	algorithmDialogColumns     int
	algorithmProgressBox       algorithmBox
)

type algorithmBox struct{ x, y, w, h int }

type algorithmDialogLayout struct {
	header, body, status, progress, cancel, preview, run, close algorithmBox
	failure                                                     algorithmBox
}

// Keep a separate, full-width status lane. Failure replaces the normal text
// within that lane instead of reserving a second blank area in every state.
func layoutAlgorithmDialog(w, h int) algorithmDialogLayout {
	return layoutAlgorithmDialogWithHint(w, h, false)
}

func layoutAlgorithmDialogWithHint(w, h int, hint bool) algorithmDialogLayout {
	g := minInt(algorithmGutter, maxInt(4, w/12))
	inner := maxInt(1, w-2*g)
	header := 65
	if hint && h >= 285 {
		header = 83
	}
	footer, statusH, progressOffset := 101, 55, 58
	if h < 285 {
		footer, statusH, progressOffset = 94, 51, 54
	}
	footerTop := maxInt(0, h-footer)
	buttonY := h - 35
	cancelW, previewW, runW, closeW := 92, 132, 120, 64
	if inner < cancelW+previewW+runW+closeW+3*algorithmGap {
		w := maxInt(4, inner-3*algorithmGap)
		closeW, cancelW, previewW = w*13/100, w*23/100, w*34/100
		runW = w - closeW - cancelW - previewW
	}
	closeX := w - g - closeW
	runX := closeX - algorithmGap - runW
	previewX := runX - algorithmGap - previewW
	cancelX := previewX - algorithmGap - cancelW
	status := algorithmBox{g, footerTop + 1, inner, statusH}
	return algorithmDialogLayout{
		header:   algorithmBox{0, 0, w, minInt(header, footerTop)},
		body:     algorithmBox{0, minInt(header, footerTop), w, maxInt(0, footerTop-header-2)},
		failure:  status,
		status:   status,
		progress: algorithmBox{g, footerTop + progressOffset, inner, 5},
		cancel:   algorithmBox{cancelX, buttonY, cancelW, algorithmControlHeight},
		preview:  algorithmBox{previewX, buttonY, previewW, algorithmControlHeight},
		run:      algorithmBox{runX, buttonY, runW, algorithmControlHeight},
		close:    algorithmBox{closeX, buttonY, closeW, algorithmControlHeight},
	}
}

// Use the smallest initial client height that shows the common grid and a
// complete results page. Narrow or DPI-constrained areas still use pagination.
func algorithmInitialClientHeight(w int, params []algorithmParameterControl, hint bool) int {
	commonCount := 0
	for _, p := range params {
		if p.Common && !p.Omit && p.Descriptor.Type != "group" {
			commonCount++
		}
	}
	for h := algorithmMinHeight; h <= 520; h += 2 {
		root := layoutAlgorithmDialogWithHint(w, h, hint)
		tabs := layoutAlgorithmTabs(w, root.body.h, params, algorithmTabResults, 0)
		if len(tabs.common) == commonCount && len(tabs.rows) == 3 {
			return h
		}
	}
	return 520
}

type algorithmParameterPlacement struct {
	index int
	box   algorithmBox
}

func beginAlgorithmDPIContext() func() {
	return setAlgorithmDPIContext(^uintptr(3)) // PER_MONITOR_AWARE_V2, -4
}

// Result callbacks execute on the Compare owner, whose DPI awareness can
// differ from this child window. Measure the algorithm HWND in its own
// awareness context so the same physical width always takes the same branch.
func enterAlgorithmWindowDPIContext(h uintptr) func() {
	if h != 0 && algorithmGetWindowDPI.Find() == nil {
		if context, _, _ := algorithmGetWindowDPI.Call(h); context != 0 {
			return setAlgorithmDPIContext(context)
		}
	}
	return func() {}
}

func setAlgorithmDPIContext(context uintptr) func() {
	if algorithmSetThreadDPI.Find() != nil {
		return func() {}
	}
	previous, _, _ := algorithmSetThreadDPI.Call(context)
	return func() {
		if previous != 0 {
			algorithmSetThreadDPI.Call(previous)
		}
	}
}

func createAlgorithmPageBody() uintptr {
	// Initialize on the UI thread after package initialization; the subclass
	// procedure uses the same callback identity to remove itself at destruction.
	if algorithmBodyCallback == 0 {
		algorithmBodyCallback = syscall.NewCallback(algorithmBodyWndProc)
	}
	hinst, _, _ := pGetModuleHandleW.Call(0)
	const exControlParent = 0x00010000
	h, _, _ := pCreateWindowExW.Call(exControlParent, uintptr(unsafe.Pointer(u16("STATIC"))), 0,
		WS_CHILD|WS_VISIBLE|WS_CLIPCHILDREN|algorithmClipSiblings, 0, 0, 1, 1, cadzowHwnd, 0, hinst, 0)
	if h == 0 {
		return 0
	}
	installed, _, _ := algorithmSetSubclass.Call(h, algorithmBodyCallback, 1, 0)
	if installed == 0 {
		pDestroyWindow.Call(h)
		return 0
	}
	return h
}

func algorithmBodyWndProc(h uintptr, msg uint32, w, l, _, _ uintptr) uintptr {
	switch msg {
	case WM_COMMAND, WM_NOTIFY, algorithmWMControlStatic:
		parent, _, _ := algorithmGetParent.Call(h)
		r, _, _ := pSendMessageW.Call(parent, uintptr(msg), w, l)
		return r
	case WM_PAINT:
		paintAlgorithmBackground(h)
		return 0
	case WM_ERASEBKGND:
		return eraseAlgorithmBackground(h, w)
	case 0x0084: // WM_NCHITTEST: do not pass body input to controls underneath.
		r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), w, l)
		return r
	case 0x0082: // WM_NCDESTROY
		algorithmRemoveSubclass.Call(h, algorithmBodyCallback, 1)
	}
	r, _, _ := algorithmDefSubclass.Call(h, uintptr(msg), w, l)
	return r
}

func fillAlgorithmBackground(dc uintptr, r *RECT) {
	brush, _, _ := algorithmSysColorBrush.Call(COLOR_WINDOW)
	pFillRect.Call(dc, uintptr(unsafe.Pointer(r)), brush)
}

func paintAlgorithmBackground(h uintptr) {
	var ps PAINTSTRUCT
	dc, _, _ := pBeginPaint.Call(h, uintptr(unsafe.Pointer(&ps)))
	fillAlgorithmBackground(dc, &ps.RcPaint)
	if h == cadzowHwnd {
		drawAlgorithmFailureCard(dc)
	}
	pEndPaint.Call(h, uintptr(unsafe.Pointer(&ps)))
}

func eraseAlgorithmBackground(h, dc uintptr) uintptr {
	var client RECT
	pGetClientRect.Call(h, uintptr(unsafe.Pointer(&client)))
	fillAlgorithmBackground(dc, &client)
	return 1
}

func algorithmMonitorWorkArea(m uintptr) (RECT, bool) {
	type monitorInfo struct {
		size       uint32
		area, work RECT
		flags      uint32
	}
	var mi monitorInfo
	mi.size = uint32(unsafe.Sizeof(mi))
	if m != 0 {
		if ok, _, _ := algorithmMonitorInfo.Call(m, uintptr(unsafe.Pointer(&mi))); ok != 0 {
			return mi.work, true
		}
	}
	return RECT{}, false
}

func algorithmMonitorArea(h uintptr) RECT {
	m, _, _ := algorithmMonitorFromWindow.Call(h, 2)
	if area, ok := algorithmMonitorWorkArea(m); ok {
		return area
	}
	area := RECT{Right: 1200, Bottom: 900}
	algorithmWorkArea.Call(0x30, 0, uintptr(unsafe.Pointer(&area)), 0)
	return area
}

func algorithmOuterSize(w, h int) (int, int) {
	r := RECT{Right: int32(algorithmPx(w)), Bottom: int32(algorithmPx(h))}
	if algorithmAdjustWindow.Find() == nil {
		algorithmAdjustWindow.Call(uintptr(unsafe.Pointer(&r)), WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN, 0, 0, uintptr(algorithmScale()))
	} else {
		algorithmAdjustLegacy.Call(uintptr(unsafe.Pointer(&r)), WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN, 0, 0)
	}
	return int(r.Right - r.Left), int(r.Bottom - r.Top)
}

// Only creation owns the initial rectangle. Parameter rebuilding, result
// completion and folding only lay out the existing client rectangle.
func placeAlgorithmWindowOnOpen() {
	if cadzowHwnd == 0 || cz.body == 0 || algorithmDialogSized {
		return
	}
	monitorOwner := cadzowHwnd
	if cadzowOwner != 0 {
		monitorOwner = cadzowOwner
	}
	area := algorithmMonitorArea(monitorOwner)
	initialHeight := algorithmInitialClientHeight(algorithmInitialWidth, cz.parameters, cz.moduleID == poststack.BandpassAlgorithmID)
	width, height := algorithmOuterSize(algorithmInitialWidth, initialHeight)
	var anchor RECT
	algorithmGetWindowRect.Call(monitorOwner, uintptr(unsafe.Pointer(&anchor)))
	if saved := algorithmNormalWindow; saved.Right > saved.Left && saved.Bottom > saved.Top {
		monitor, _, _ := algorithmMonitorFromRect.Call(uintptr(unsafe.Pointer(&saved)), 2)
		if work, ok := algorithmMonitorWorkArea(monitor); ok {
			area = work
		}
		anchor = saved
		width, height = int(saved.Right-saved.Left), int(saved.Bottom-saved.Top)
	}
	width, height = minInt(width, int(area.Right-area.Left)), minInt(height, int(area.Bottom-area.Top))
	x := int(anchor.Left+anchor.Right)/2 - width/2
	y := int(anchor.Top+anchor.Bottom)/2 - height/2
	x = maxInt(int(area.Left), minInt(x, int(area.Right)-width))
	y = maxInt(int(area.Top), minInt(y, int(area.Bottom)-height))
	algorithmDialogSized = true
	setAlgorithmWindowBounds(cadzowHwnd, x, y, width, height)
	layoutCadzowWindow("打开")
}

func rememberAlgorithmWindowPlacement() {
	if cadzowHwnd == 0 || !algorithmDialogSized {
		return
	}
	restoreDPI := enterAlgorithmWindowDPIContext(cadzowHwnd)
	defer restoreDPI()
	zoomed, _, _ := algorithmIsZoomed.Call(cadzowHwnd)
	iconic, _, _ := algorithmIsIconic.Call(cadzowHwnd)
	if zoomed != 0 || iconic != 0 {
		return
	}
	var r RECT
	if ok, _, _ := algorithmGetWindowRect.Call(cadzowHwnd, uintptr(unsafe.Pointer(&r))); ok != 0 && r.Right > r.Left && r.Bottom > r.Top {
		algorithmNormalWindow = r
	}
}

// Opt-in diagnostics for reproducing an unexpected move or layout transition;
// no sample, input path, module parameter or progress spam is logged.
func traceAlgorithmWindowLayout(event string) {
	if event == "" || cadzowHwnd == 0 || os.Getenv("SEISFORGE_DEBUG_ALGORITHM_LAYOUT") != "1" {
		return
	}
	restoreDPI := enterAlgorithmWindowDPIContext(cadzowHwnd)
	defer restoreDPI()
	var outer, client RECT
	algorithmGetWindowRect.Call(cadzowHwnd, uintptr(unsafe.Pointer(&outer)))
	pGetClientRect.Call(cadzowHwnd, uintptr(unsafe.Pointer(&client)))
	log.Printf("algorithm layout %s: outer=%+v client=%dx%d dpi=%d common_columns=%d tab=%d",
		event, outer, client.Right-client.Left, client.Bottom-client.Top, algorithmScale(), algorithmDialogColumns, cz.activeTab)
}

func setAlgorithmWindowBounds(h uintptr, x, y, w, height int) {
	wasSizing := algorithmDialogSizing
	algorithmDialogSizing = true // SetWindowPos synchronously sends WM_SIZE; the caller lays out once.
	defer func() { algorithmDialogSizing = wasSizing }()
	pSetWindowPos.Call(h, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(height), SWP_NOZORDER|SWP_NOACTIVATE|algorithmNoCopyBits)
}

func algorithmWindowStyle(h uintptr) uintptr {
	style, _, _ := algorithmGetWindowStyle.Call(h, ^uintptr(15)) // GWL_STYLE (-16)
	return style
}

func algorithmMoveBox(h uintptr, b algorithmBox) { algorithmShowBox(h, b, true) }

func refreshAlgorithmProgressVisibility() {
	b := algorithmProgressBox
	if cz.progress == 0 || cadzowHwnd == 0 || b.h <= 0 {
		return
	}
	visible := cadzowCancel != nil
	if (algorithmWindowStyle(cz.progress)&WS_VISIBLE != 0) == visible {
		return
	}
	algorithmShowBox(cz.progress, b, visible)
	r := RECT{Left: int32(algorithmPx(b.x)), Top: int32(algorithmPx(b.y)), Right: int32(algorithmPx(b.x + b.w)), Bottom: int32(algorithmPx(b.y + b.h))}
	pRedrawWindow.Call(cadzowHwnd, uintptr(unsafe.Pointer(&r)), 0, RDW_INVALIDATE|RDW_ERASE|RDW_ALLCHILDREN)
}

func algorithmShowBox(h uintptr, b algorithmBox, visible bool) {
	if h == 0 {
		return
	}
	// The layout may move several siblings through the same screen region.
	// Do not paint (or copy old bits) until every child is in its final place.
	flags := uintptr(SWP_NOZORDER | SWP_NOACTIVATE | algorithmNoRedraw | algorithmNoCopyBits)
	wasVisible := algorithmWindowStyle(h)&WS_VISIBLE != 0
	if !visible {
		if wasVisible {
			pSetWindowPos.Call(h, 0, 0, 0, 0, 0, flags|algorithmHideWindow|0x0003) // SWP_NOMOVE | SWP_NOSIZE
		}
		return
	}
	if !wasVisible {
		flags |= algorithmShowWindow
	}
	pSetWindowPos.Call(h, 0, uintptr(algorithmPx(b.x)), uintptr(algorithmPx(b.y)), uintptr(maxInt(1, algorithmPx(b.w))), uintptr(maxInt(1, algorithmPx(b.h))), flags)
}

func layoutCadzowWindow(event string) {
	if cadzowHwnd == 0 || cz.body == 0 {
		return
	}
	restoreDPI := enterAlgorithmWindowDPIContext(cadzowHwnd)
	defer restoreDPI()
	var rc RECT
	pGetClientRect.Call(cadzowHwnd, uintptr(unsafe.Pointer(&rc)))
	dpi := algorithmScale()
	w, h := int(rc.Right)*96/dpi, int(rc.Bottom)*96/dpi
	if w < 2 || h < 2 {
		return
	}
	refreshAlgorithmFonts(dpi)
	filterBuiltin := refreshAlgorithmFilterApplicability()
	root := layoutAlgorithmDialogWithHint(w, h, filterBuiltin)
	x := algorithmGutter
	inner := maxInt(1, w-2*x)
	manageW, versionW := 94, 82
	responseWidth := 0
	if filterBuiltin {
		responseWidth = 82 + algorithmGap
	}
	algorithmMoveBox(cz.algorithm, algorithmBox{x, 12, inner - manageW - versionW - 2*algorithmGap - responseWidth, 220})
	algorithmMoveBox(cz.manage, algorithmBox{w - x - manageW, 12, manageW, algorithmControlHeight})
	algorithmShowBox(cz.filterResponse, algorithmBox{w - x - manageW - responseWidth, 12, 82, algorithmControlHeight}, filterBuiltin)
	algorithmMoveBox(cz.version, algorithmBox{w - x - manageW - versionW - algorithmGap - responseWidth, 17, versionW, 21})
	algorithmMoveBox(cz.metadata, algorithmBox{x, 45, maxInt(1, inner-84), 19})
	algorithmMoveBox(cz.copyInput, algorithmBox{x + inner - 76, 41, 76, 23})
	algorithmShowBox(cz.metadataDetail, algorithmBox{x, 64, inner, 18}, filterBuiltin && root.header.h >= 83)
	algorithmProgressBox = root.progress
	algorithmShowBox(cz.progress, root.progress, cadzowCancel != nil)
	algorithmMoveBox(cz.cancel, root.cancel)
	algorithmMoveBox(cz.preview, root.preview)
	algorithmMoveBox(cz.start, root.run)
	algorithmMoveBox(cz.close, root.close)
	algorithmMoveBox(cz.body, root.body)
	algorithmFailureCard = root.failure
	layoutAlgorithmFailureControls()
	body := layoutAlgorithmTabs(w, root.body.h, cz.parameters, cz.tab, cz.tabPages[cz.tab])
	cz.activeTab = body.active
	if cz.tab == 0 {
		cz.tab = body.active
	}
	algorithmDialogColumns = body.columns
	updateAlgorithmTabs(body)
	place := algorithmShowBox
	place(cz.commonTitle, body.commonTitle, body.commonTitle.h > 0)
	shown := make([]bool, len(cz.parameters))
	measure, doneMeasure := poststackToolbarFontMeasure(algorithmDialogFont)
	defer doneMeasure()
	for _, placement := range append(body.common, body.parameters...) {
		p := &cz.parameters[placement.index]
		shown[placement.index] = true
		b := placement.box
		if p.Descriptor.Type == "group" {
			place(p.Label, algorithmBox{b.x, b.y, b.w, 22}, true)
			continue
		}
		unitW := 0
		if p.Unit != 0 {
			unitW = (measure(p.Descriptor.Unit)*96+dpi-1)/dpi + 9
		}
		labelW, valueW := algorithmParameterWidths(b.w-unitW, p.Descriptor.Type, p.Browse != 0, p.End != 0)
		valueX := b.x + labelW + algorithmGap
		valueH := algorithmControlHeight
		if p.Descriptor.Type == "enum" {
			valueH = 210 // drop-down height; collapsed native control remains one row
		}
		place(p.Label, algorithmBox{b.x, b.y + 5, labelW, 21}, true)
		place(p.Value, algorithmBox{valueX, b.y, valueW, valueH}, true)
		place(p.End, algorithmBox{valueX + valueW + algorithmGap, b.y, valueW, algorithmControlHeight}, true)
		place(p.Browse, algorithmBox{valueX + valueW + 5, b.y, 32, algorithmControlHeight}, true)
		unitX := valueX + valueW + 6
		if p.End != 0 {
			unitX += valueW + algorithmGap
		}
		place(p.Unit, algorithmBox{unitX, b.y + 5, maxInt(1, unitW-6), 21}, unitW > 0)
		place(p.Hint, algorithmBox{b.x, b.y + 29, b.w, b.h - 30}, !p.Compact)
	}
	for i, p := range cz.parameters {
		if !shown[i] {
			for _, control := range []uintptr{p.Label, p.Value, p.End, p.Browse, p.Hint, p.Unit} {
				place(control, algorithmBox{}, false)
			}
		}
	}
	managed := []uintptr{cz.timeoutLabel, cz.timeout, cz.previewInfo, cz.resultsLabel, cz.results, cz.noResults, cz.exportResults, cz.log}
	visible := make(map[uintptr]bool, len(managed))
	show := func(control uintptr, b algorithmBox) {
		visible[control] = true
		place(control, b, true)
	}
	s := body.panel
	for pageRow, row := range body.rows {
		if body.active == algorithmTabSettings {
			y := s.y + pageRow*36 + 2
			show(cz.timeoutLabel, algorithmBox{s.x, y + 5, 112, 22})
			show(cz.timeout, algorithmBox{s.x + 116, y, 104, algorithmControlHeight})
		}
		if body.active == algorithmTabResults {
			y := s.y + pageRow*32 + 2
			switch row {
			case 0:
				show(cz.previewInfo, algorithmBox{s.x, y + 4, s.w, 22})
			case 1:
				if cz.hasFormal {
					show(cz.resultsLabel, algorithmBox{s.x, y + 5, 86, 22})
					show(cz.results, algorithmBox{s.x + 90, y, maxInt(1, s.w-90), 190})
				} else {
					show(cz.noResults, algorithmBox{s.x, y + 5, s.w, 22})
				}
			case 2:
				exportW := minInt(142, (s.w-8)/2)
				show(cz.exportResults, algorithmBox{s.x, y, exportW, algorithmControlHeight})
				show(cz.log, algorithmBox{s.x + exportW + 8, y, minInt(92, (s.w-8)/2), algorithmControlHeight})
			}
		}
	}
	for _, control := range managed {
		if !visible[control] {
			place(control, algorithmBox{}, false)
		}
	}
	// Include the parent areas exposed by moved/hidden controls and all visible
	// descendants. Let the message loop paint after the layout has committed.
	pRedrawWindow.Call(cadzowHwnd, 0, 0, RDW_INVALIDATE|RDW_ERASE|RDW_ALLCHILDREN|algorithmRedrawFrame)
	traceAlgorithmWindowLayout(event)
}

func refreshAlgorithmFonts(dpi int) {
	oldRegular, oldBold := uintptr(0), uintptr(0)
	if algorithmDialogFontDPI != dpi || algorithmDialogFont == 0 {
		oldRegular, oldBold = algorithmDialogFont, algorithmDialogBold
		face := u16("Microsoft YaHei UI")
		h := -maxInt(1, 13*dpi/96)
		algorithmDialogFont, _, _ = pCreateFontW.Call(uintptr(h), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(face)))
		algorithmDialogBold, _, _ = pCreateFontW.Call(uintptr(h), 0, 0, 0, 600, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(face)))
		algorithmDialogFontDPI = dpi
	}
	apply := func(font uintptr, controls ...uintptr) {
		if font == 0 {
			return
		}
		for _, control := range controls {
			if control != 0 {
				if current, _, _ := pSendMessageW.Call(control, 0x0031, 0, 0); current != font {
					pSendMessageW.Call(control, WM_SETFONT, font, 0) // repaint once after layout
				}
			}
		}
	}
	apply(algorithmDialogFont, cz.algorithm, cz.version, cz.manage, cz.metadata, cz.metadataDetail, cz.copyInput,
		cz.status, cz.preview, cz.cancel, cz.close, cz.timeout, cz.timeoutLabel,
		cz.resultsLabel, cz.results, cz.previewInfo, cz.noResults, cz.exportResults, cz.log, cz.failureReason, cz.failureLog,
		cz.tabs, cz.pageNext, cz.pagePrev, cz.pageCount)
	apply(algorithmDialogBold, cz.start, cz.commonTitle, cz.failureTitle)
	for _, p := range cz.parameters {
		apply(algorithmDialogFont, p.Label, p.Value, p.End, p.Browse, p.Hint, p.Unit)
	}
	for _, f := range []uintptr{oldRegular, oldBold} {
		if f != 0 {
			pDeleteObject.Call(f)
		}
	}
}

func releaseAlgorithmDialogLayout() {
	// WM_DESTROY reaches the parent before it finishes destroying child
	// controls. Detach owned fonts before releasing their GDI handles.
	controls := []uintptr{cz.algorithm, cz.version, cz.manage, cz.metadata, cz.metadataDetail, cz.copyInput,
		cz.status, cz.preview, cz.cancel, cz.close, cz.timeout, cz.timeoutLabel,
		cz.resultsLabel, cz.results, cz.previewInfo, cz.noResults, cz.exportResults, cz.log,
		cz.start, cz.commonTitle, cz.tabs, cz.pageNext, cz.pagePrev, cz.pageCount, cz.failureTitle, cz.failureReason, cz.failureLog}
	for _, p := range cz.parameters {
		controls = append(controls, p.Label, p.Value, p.End, p.Browse, p.Hint, p.Unit)
	}
	for _, control := range controls {
		if control != 0 {
			pSendMessageW.Call(control, WM_SETFONT, hFont, 0)
		}
	}
	for _, f := range []uintptr{algorithmDialogFont, algorithmDialogBold} {
		if f != 0 {
			pDeleteObject.Call(f)
		}
	}
	algorithmDialogFont, algorithmDialogBold, algorithmDialogFontDPI = 0, 0, 0
	algorithmDialogSized = false
	algorithmDialogSizing = false
	algorithmDialogColumns = 0
	algorithmFailureCard = algorithmBox{}
	algorithmProgressBox = algorithmBox{}
	if algorithmFailureBrush != 0 {
		pDeleteObject.Call(algorithmFailureBrush)
		algorithmFailureBrush = 0
	}
}

type algorithmDrawItem struct {
	kind, id, item, action, state uint32
	hwnd, dc                      uintptr
	r                             RECT
	data                          uintptr
}

func handleAlgorithmDialogLayout(h uintptr, msg uint32, w, l uintptr) (uintptr, bool) {
	switch msg {
	case WM_GETMINMAXINFO:
		m := (*MINMAXINFO)(unsafe.Pointer(l))
		area := algorithmMonitorArea(h)
		ww, hh := algorithmOuterSize(algorithmMinWidth, algorithmMinHeight)
		m.PtMinTrackSize = POINT{X: int32(minInt(ww, int(area.Right-area.Left))), Y: int32(minInt(hh, int(area.Bottom-area.Top)))}
		return 0, true
	case WM_SIZE:
		if !algorithmDialogSizing {
			layoutCadzowWindow("")
		}
		return 0, true
	case WM_EXITSIZEMOVE:
		rememberAlgorithmWindowPlacement()
		traceAlgorithmWindowLayout("用户调整")
		return 0, true
	case algorithmWMDPIChanged:
		if l != 0 {
			r := *(*RECT)(unsafe.Pointer(l))
			area := algorithmMonitorArea(h)
			// A window crossing displays belongs to the monitor of the suggested rectangle.
			monitor, _, _ := algorithmMonitorFromRect.Call(uintptr(unsafe.Pointer(&r)), 2)
			if work, ok := algorithmMonitorWorkArea(monitor); ok {
				area = work
			}
			nw, nh := minInt(int(r.Right-r.Left), int(area.Right-area.Left)), minInt(int(r.Bottom-r.Top), int(area.Bottom-area.Top))
			x, y := maxInt(int(area.Left), minInt(int(r.Left), int(area.Right)-nw)), maxInt(int(area.Top), minInt(int(r.Top), int(area.Bottom)-nh))
			setAlgorithmWindowBounds(h, x, y, nw, nh)
		}
		layoutCadzowWindow("DPI 变化")
		refreshAlgorithmTooltipDPI()
		return 0, true
	case algorithmWMControlStatic:
		if l != 0 && (l == cz.failureTitle || l == cz.failureReason) {
			background := algorithmFailureColor()
			algorithmSetBkColor.Call(w, background)
			pSetBkMode.Call(w, 2)
			pSetTextColor.Call(w, rgbRef(121, 31, 39))
			if algorithmFailureBrush == 0 {
				algorithmFailureBrush, _, _ = pCreateSolidBrush.Call(background)
			}
			return algorithmFailureBrush, true
		}
		color, _, _ := pGetSysColor.Call(COLOR_WINDOW)
		algorithmSetBkColor.Call(w, color)
		pSetBkMode.Call(w, 2) // OPAQUE: replacing a label must clear its former text.
		text := rgbRef(35, 39, 46)
		if l == cz.metadataDetail || l == cz.version || l == cz.noResults {
			text = rgbRef(90, 95, 103)
		}
		pSetTextColor.Call(w, text)
		brush, _, _ := algorithmSysColorBrush.Call(COLOR_WINDOW)
		return brush, true
	case WM_PAINT:
		paintAlgorithmBackground(h)
		return 0, true
	case WM_ERASEBKGND:
		return eraseAlgorithmBackground(h, w), true
	}
	return 0, false
}

func translateAlgorithmDialogMessage(m *MSG) bool {
	if cadzowHwnd == 0 || m == nil || m.Message != WM_KEYDOWN || m.WParam != 0x09 { // Tab only; never start a task on Enter implicitly.
		return false
	}
	child, _, _ := pIsChild.Call(cadzowHwnd, m.Hwnd)
	if m.Hwnd != cadzowHwnd && child == 0 {
		return false
	}
	processed, _, _ := algorithmIsDialogMessage.Call(cadzowHwnd, uintptr(unsafe.Pointer(m)))
	return processed != 0
}
