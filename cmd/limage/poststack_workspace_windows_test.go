//go:build windows

package main

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"unicode/utf8"
	"unsafe"
)

func TestPoststackPathCaptionFitsMeasuredWidth(t *testing.T) {
	path := filepath.Join("synthetic-fixtures", "中文目录 with spaces", "synthetic-section.sgy")
	measure := func(s string) int {
		width := 0
		for _, r := range s {
			if r > 127 {
				width += 15
			} else {
				width += 7
			}
		}
		return width
	}
	full := "A: " + path
	if got := poststackPathCaption("A", path, measure(full)+4, measure); got != "A: synthetic-section.sgy" {
		t.Fatalf("wide label = %q", got)
	}
	if got := poststackPathCaption("A", path, measure(full)+3, measure); got != "A: synthetic-section.sgy" {
		t.Fatalf("narrow label = %q", got)
	}
	if got := poststackPathCaption("B", "", 400, measure); got != "B: 未加载" {
		t.Fatalf("empty B = %q", got)
	}
	long := path + strings.Repeat("连续字符", 20)
	wrapped := wrapPoststackPathTip(long, 130, measure)
	if strings.ReplaceAll(wrapped, "\r\n", "") != long {
		t.Fatal("wrapping lost path characters")
	}
	for _, line := range strings.Split(wrapped, "\r\n") {
		if measure(line) > 130 {
			t.Fatalf("tooltip line too wide: %q", line)
		}
	}
}

func TestPoststackNativePathTipsFollowLabelsAndBReplacement(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	common := INITCOMMONCONTROLSEX{DwSize: uint32(unsafe.Sizeof(INITCOMMONCONTROLSEX{})), DwICC: ICC_WIN95_CLASSES}
	pInitCommonCtrls.Call(uintptr(unsafe.Pointer(&common)))
	parent := createCtrl(0, "STATIC", "Path tooltip regression", WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN, 0, 0, 1120, 520, 0)
	if parent == 0 {
		t.Fatal("could not create native owner")
	}
	defer pDestroyWindow.Call(parent)
	oldOwner, oldUI, oldFont := compareHwnd, cc, hFont
	oldA, oldB, oldActive, oldVisible := compareAPath, compareBPath, compareWorkspaceActive, compareBVisible
	defer func() {
		compareHwnd, cc, hFont = oldOwner, oldUI, oldFont
		compareAPath, compareBPath, compareWorkspaceActive, compareBVisible = oldA, oldB, oldActive, oldVisible
	}()
	compareHwnd, cc = parent, compareControls{}
	hFont, _, _ = pGetStockObject.Call(DEFAULT_GUI_FONT)
	compareAPath = filepath.Join("synthetic-fixtures", strings.Repeat(`中文目录 & with spaces\`, 10), "A&B.sgy")
	compareBPath = filepath.Join("synthetic-fixtures", "中文输出目录", "B.sgy")
	compareWorkspaceActive, compareBVisible = true, true
	createPoststackToolbar()
	defer cc.toolbar.close()
	layoutComparePathLabels()
	if got := getText(cc.aLabel); got != "A: A&B.sgy" {
		t.Fatalf("native A caption = %q", got)
	}
	getTip := func(control uintptr) (string, bool) {
		var buf [1024]uint16
		info := makePoststackToolInfo(parent, control, &buf[0])
		ok, _, _ := pSendMessageW.Call(cc.toolbar.tooltip, WM_USER+53 /* TTM_GETTOOLINFOW */, 0, uintptr(unsafe.Pointer(&info)))
		if ok == 0 {
			return "", false
		}
		info = makePoststackToolInfo(parent, control, &buf[0])
		pSendMessageW.Call(cc.toolbar.tooltip, WM_USER+56 /* TTM_GETTEXTW */, uintptr(len(buf)), uintptr(unsafe.Pointer(&info)))
		return strings.ReplaceAll(syscall.UTF16ToString(buf[:]), "\r\n", ""), true
	}
	check := func(control uintptr, want string) {
		t.Helper()
		if got, ok := getTip(control); !ok || got != want {
			t.Fatalf("tooltip text=%q registered=%v; want %q", got, ok, want)
		}
	}
	check(cc.aLabel, compareAPath)
	check(cc.bLabel, compareBPath)
	pMoveWindow.Call(parent, 0, 0, 1600, 520, 0)
	layoutComparePathLabels()
	check(cc.aLabel, compareAPath)
	if got := getText(cc.bLabel); got != "B: B.sgy" {
		t.Fatalf("wide B caption = %q", got)
	}
	compareBPath = filepath.Join("synthetic-fixtures", "中文输出目录", "processed.sfdata")
	layoutComparePathLabels()
	check(cc.bLabel, compareBPath)
	compareBPath = ""
	layoutComparePathLabels()
	if _, ok := getTip(cc.bLabel); ok {
		t.Fatal("closed B retained a tooltip")
	}
	if got := getText(cc.bLabel); got != "B: 未加载" {
		t.Fatalf("closed B caption=%q", got)
	}
	compareBPath = filepath.Join("synthetic-fixtures", "中文输出目录", "hidden.sfdata")
	compareWorkspaceActive = false
	layoutComparePathLabels()
	if _, ok := getTip(cc.bLabel); ok {
		t.Fatal("hidden B retained a tooltip")
	}
}

func TestPoststackToolbarRequiresExplicitCompareWorkspace(t *testing.T) {
	for _, state := range []struct{ hasA, active, hasB bool }{{false, false, false}, {false, true, true}, {true, false, false}, {true, false, true}} {
		r := poststackToolbarRects(1104, state.hasA, state.active, state.hasB)
		for _, id := range []int{IDCMP_B, IDCMP_CADZOW, IDCMP_CLOSE_B, IDCMP_DIFF, IDCMP_BLABEL} {
			if _, ok := r[id]; ok {
				t.Fatalf("B action %d exposed outside Compare: %+v", id, state)
			}
		}
		if state.hasA {
			if _, ok := r[ID_COMPARE]; !ok {
				t.Fatal("ordinary A section lost 比 entry")
			}
			if _, ok := r[ID_MARK]; !ok {
				t.Fatal("ordinary A section lost the shared color editor entry")
			}
		} else if len(r) != 0 {
			t.Fatal("Home toolbar is not empty")
		}
	}
	r := poststackToolbarRects(1104, true, true, false)
	for _, id := range []int{ID_COMPARE, ID_VOLUME, IDCMP_B, IDCMP_CADZOW, ID_MARK} {
		if _, ok := r[id]; !ok {
			t.Fatalf("missing Compare action %d", id)
		}
	}
	for _, id := range []int{IDCMP_CLOSE_B, IDCMP_DIFF} {
		if _, ok := r[id]; ok {
			t.Fatalf("B unavailable but action %d visible", id)
		}
	}
	r = poststackToolbarRects(1104, true, true, true)
	for _, id := range []int{IDCMP_CLOSE_B, IDCMP_DIFF, ID_MARK} {
		if _, ok := r[id]; !ok {
			t.Fatalf("B ready but action %d hidden", id)
		}
	}
}

func TestPoststackToolbarLayoutBoundsAndNoOverlap(t *testing.T) {
	for _, width := range []int{960, 1104, 1280, 1560, 1920} {
		for _, active := range []bool{false, true} {
			for _, hasB := range []bool{false, true} {
				r := poststackToolbarRects(width, true, active, hasB)
				for _, id := range []int{ID_VOLUME, IDCMP_SPECTRUM, IDCMP_TRACE_INSPECT, IDCMP_EXPORT} {
					if _, ok := r[id]; !ok {
						t.Fatalf("width %d lost action %d (Compare=%v, B=%v)", width, id, active, hasB)
					}
				}
				if _, ok := r[5032]; ok { // retired standalone filter control
					t.Fatal("obsolete standalone filter still occupies the toolbar")
				}
				for id, rect := range r {
					if rect.Left < 0 || rect.Right > int32(width) || rect.Right <= rect.Left || rect.Bottom > poststackToolbarBottom {
						t.Fatalf("width %d action %d outside toolbar: %+v", width, id, rect)
					}
					for other, second := range r {
						if id < other && rect.Left < second.Right && second.Left < rect.Right && rect.Top < second.Bottom && second.Top < rect.Bottom {
							t.Fatalf("width %d overlap %d/%d", width, id, other)
						}
					}
				}
			}
		}
	}
}

func TestPoststackNavigationActionsStayAdjacent(t *testing.T) {
	old := cc
	defer func() { cc = old }()
	cc = compareControls{
		modeLabel: 1, mode: 2, prev: 3, next: 4, slider: 5, timeEdit: 6, timeUnit: 7,
		refresh: 8, autoGeom: 9,
	}
	for _, mode := range []int{0, 1, 2} {
		for _, dpi := range []int{96, 120, 144} {
			measure := func(s string) int { return displayPixelDIP(13*float64(utf8.RuneCountInString(s)), dpi) }
			r := compareNavigationRectsMeasured(960, mode, dpi, measure)
			reset, geometry := r[cc.refresh], r[cc.autoGeom]
			if reset.Right+6 != geometry.Left || geometry.Right > 960 || reset.Top != geometry.Top || reset.Bottom != geometry.Bottom {
				t.Fatalf("mode %d DPI %d: navigation gap or overflow: %+v / %+v", mode, dpi, reset, geometry)
			}
			if len(r) != 7 && mode != 2 || len(r) != 9 && mode == 2 {
				t.Fatalf("mode %d DPI %d: unexpected navigation slot: %v", mode, dpi, r)
			}
		}
	}
}

func TestPoststackVectorButtonsReserveMeasuredTextAndIcons(t *testing.T) {
	for _, dpi := range []int{96, 120, 144} {
		measure := func(s string) int { return displayPixelDIP(12*float64(utf8.RuneCountInString(s)), dpi) }
		for _, active := range []bool{false, true} {
			r := poststackToolbarRectsAtDPI(1104, true, active, active, dpi, measure)
			for _, id := range []int{ID_COMPARE, ID_VOLUME, IDCMP_SPECTRUM, IDCMP_TRACE_INSPECT, IDCMP_EXPORT, IDCMP_B, IDCMP_CADZOW, IDCMP_CLOSE_B, IDCMP_DIFF} {
				rect, visible := r[id]
				if !visible && !active && (id == IDCMP_B || id == IDCMP_CADZOW || id == IDCMP_CLOSE_B || id == IDCMP_DIFF) {
					continue
				}
				caption := poststackToolbarCaption(id, active)
				if !visible || caption == "" || int(rect.Right-rect.Left) < measure(caption)+displayPixelDIP(22, dpi) || rect.Right > 1104 {
					t.Fatalf("DPI %d Compare=%v: vector button %d (%s) does not fit: %+v", dpi, active, id, caption, rect)
				}
			}
		}
	}
}

func TestPoststackToolbarMeasuredCaptionsFitNarrowClient(t *testing.T) {
	// A wider font must increase the button widths before the file labels are laid out.
	const width = 1104
	measure := func(s string) int { return 16 * utf8.RuneCountInString(s) }
	for _, active := range []bool{false, true} {
		for _, hasB := range []bool{false, true} {
			r := poststackToolbarRectsMeasured(width, true, active, hasB, measure)
			for id, rect := range r {
				if rect.Right > width {
					t.Fatalf("control %d exceeds narrow client: %+v", id, rect)
				}
				switch id {
				case IDCMP_PALETTE, IDCMP_ALABEL, IDCMP_BLABEL:
					continue
				}
				if poststackToolIconOnly(id) {
					if rect.Right-rect.Left < 27 || rect.Bottom-rect.Top < 22 {
						t.Fatalf("icon %d has no margin: %+v", id, rect)
					}
					continue
				}
				caption := poststackToolbarCaption(id, active)
				if caption == "" || int(rect.Right-rect.Left) < measure(caption)+14 {
					t.Fatalf("control %d caption %q clipped: %+v", id, caption, rect)
				}
			}
		}
	}
}

func TestPoststackNativeToolbarUsesUnifiedVectorsAndUnicodeTips(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	common := INITCOMMONCONTROLSEX{DwSize: uint32(unsafe.Sizeof(INITCOMMONCONTROLSEX{})), DwICC: ICC_WIN95_CLASSES}
	pInitCommonCtrls.Call(uintptr(unsafe.Pointer(&common)))
	parent := createCtrl(0, "STATIC", "Poststack toolbar regression", WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN, 0, 0, 1120, 520, 0)
	if parent == 0 {
		t.Fatal("could not create hidden native test owner")
	}
	defer pDestroyWindow.Call(parent)
	oldOwner, oldUI, oldFont := compareHwnd, cc, hFont
	oldA, oldB, oldActive, oldBVisible := compareAPath, compareBPath, compareWorkspaceActive, compareBVisible
	defer func() {
		compareHwnd, cc, hFont = oldOwner, oldUI, oldFont
		compareAPath, compareBPath, compareWorkspaceActive, compareBVisible = oldA, oldB, oldActive, oldBVisible
	}()
	compareHwnd, cc = parent, compareControls{}
	hFont, _, _ = pGetStockObject.Call(DEFAULT_GUI_FONT)
	compareAPath, compareBPath, compareBVisible = "A.sgy", "B.sgy", true
	createPoststackToolbar()
	defer cc.toolbar.close()
	if cc.toolbar.tooltip == 0 {
		t.Fatal("could not create native tooltip control")
	}
	count, _, _ := pSendMessageW.Call(cc.toolbar.tooltip, WM_USER+13 /* TTM_GETTOOLCOUNT */, 0, 0)
	if int(count) != len(poststackToolbarTipText) {
		t.Fatalf("registered tooltip count = %d, want %d", count, len(poststackToolbarTipText))
	}
	getWindowLong := user32.NewProc("GetWindowLongW")
	measure, release := poststackToolbarFontMeasure(hFont)
	defer release()
	if measure == nil || measure("数据信息") <= 0 {
		t.Fatal("could not measure the native toolbar font")
	}
	for _, active := range []bool{false, true, false} {
		compareWorkspaceActive = active
		layoutComparePathLabels()
		cw, _ := clientSize(parent)
		rects := poststackToolbarRectsMeasured(cw, true, active, true, measure)
		for _, c := range compareToolbarControls() {
			if c.hwnd == 0 {
				t.Fatalf("native control %d was not created", c.id)
			}
			style, _, _ := getWindowLong.Call(c.hwnd, ^uintptr(15)) // GWL_STYLE = -16
			if style&poststackToolbarClipSiblings == 0 {
				t.Fatalf("control %d can draw across siblings", c.id)
			}
			if _, visible := rects[c.id]; (style&WS_VISIBLE != 0) != visible {
				t.Fatalf("mode %v: visibility differs for control %d", active, c.id)
			}
			switch {
			case poststackToolToggle(c.id):
				if style&0xF != BS_AUTOCHECKBOX || style&BS_PUSHLIKE == 0 {
					t.Fatalf("control %d lost its toggle style: %#x", c.id, style)
				}
			case poststackVectorLabels[c.id] != "":
				if style&0xF != BS_PUSHBUTTON {
					t.Fatalf("control %d lost its native push-button action: %#x", c.id, style)
				}
			}
			if want, ok := poststackToolbarTipText[c.id]; ok {
				var buf [128]uint16
				info := makePoststackToolInfo(parent, c.hwnd, &buf[0])
				pSendMessageW.Call(cc.toolbar.tooltip, WM_USER+56 /* TTM_GETTEXTW */, uintptr(len(buf)), uintptr(unsafe.Pointer(&info)))
				if got := syscall.UTF16ToString(buf[:]); got != want {
					t.Fatalf("mode %v: control %d tooltip %q, want %q", active, c.id, got, want)
				}
			}
			if poststackVectorLabels[c.id] != "" {
				if bitmap, _, _ := pSendMessageW.Call(c.hwnd, 0x00f6 /* BM_GETIMAGE */, IMAGE_BITMAP, 0); bitmap != 0 {
					t.Fatalf("control %d retained a mixed bitmap icon", c.id)
				}
				want := poststackToolAccessibleCaption(c.id, active)
				if got := getText(c.hwnd); got != want || style&BS_BITMAP != 0 {
					t.Fatalf("mode %v: control %d text=%q, want %q (style %#x)", active, c.id, got, want, style)
				}
				if style&WS_VISIBLE != 0 {
					w, _ := clientSize(c.hwnd)
					if w != poststackVectorButtonWidth(c.id, active, displayDPI(parent), measure) {
						t.Fatalf("mode %v: native control %q width %d clips caption", active, want, w)
					}
				}
			}
		}
		want := "进入 A/B 剖面对比"
		if active {
			want = "返回 A 单幅剖面"
		}
		if got := getText(cc.compare); got != want {
			t.Fatalf("mode %v: compare button text=%q, want %q", active, got, want)
		}
		if pSendMessageWCheck(cc.compare) != active {
			t.Fatalf("mode %v: owner paint did not preserve native check state", active)
		}
	}
	cc.toolbar.close()
	if cc.toolbar.tooltip != 0 {
		t.Fatal("native toolbar resources not released")
	}
}

func TestPoststackNativeToggleVectorPaintShowsCheckAndDisabledState(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	common := INITCOMMONCONTROLSEX{DwSize: uint32(unsafe.Sizeof(INITCOMMONCONTROLSEX{})), DwICC: ICC_WIN95_CLASSES}
	pInitCommonCtrls.Call(uintptr(unsafe.Pointer(&common)))
	parent := createCtrl(0, "STATIC", "Poststack vector paint test", WS_OVERLAPPEDWINDOW, 0, 0, 320, 120, 0)
	if parent == 0 {
		t.Fatal("could not create hidden toolbar owner")
	}
	defer pDestroyWindow.Call(parent)
	button := createCtrl(parent, "BUTTON", "显示或隐藏 A−B 差值", WS_CHILD|BS_AUTOCHECKBOX|BS_PUSHLIKE, 0, 0, 80, 25, IDCMP_DIFF)
	if button == 0 {
		t.Fatal("could not create native vector checkbox")
	}
	poststackInstallToolPainter(button, IDCMP_DIFF)
	dc, _, _ := pCreateCompatibleDC.Call(0)
	if dc == 0 {
		t.Fatal("could not create test drawing context")
	}
	defer pDeleteDC.Call(dc)
	bmi := BITMAPINFO{Header: BITMAPINFOHEADER{Size: 40, Width: 80, Height: -25, Planes: 1, BitCount: 32}}
	var bits uintptr
	bmp, _, _ := pCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&bmi)), DIB_RGB_COLORS, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 {
		t.Fatal("could not create test DIB")
	}
	defer pDeleteObject.Call(bmp)
	old, _, _ := pSelectObject.Call(dc, bmp)
	defer pSelectObject.Call(dc, old)
	getPixel := gdi32.NewProc("GetPixel")
	var raised [4]uintptr
	for _, state := range []struct{ checked, disabled bool }{{false, false}, {true, false}, {true, true}} {
		pEnableWindow.Call(button, uintptr(boolToInt(!state.disabled)))
		pSendMessageW.Call(button, BM_SETCHECK, uintptr(boolToInt(state.checked)), 0)
		pSendMessageW.Call(button, 0x0318 /* WM_PRINTCLIENT */, dc, 0)
		var bevel [4]uintptr
		for i, y := range []uintptr{0, 1, 23, 24} {
			bevel[i], _, _ = getPixel.Call(dc, 40, y)
		}
		if !state.checked {
			raised = bevel
		} else if !state.disabled && bevel == raised {
			t.Fatalf("checked native button has the same bevel as the raised button: %x", bevel)
		}
		enabled, _, _ := user32.NewProc("IsWindowEnabled").Call(button)
		if (enabled == 0) != state.disabled || pSendMessageWCheck(button) != state.checked {
			t.Fatalf("checked=%v disabled=%v: native state did not match the requested state", state.checked, state.disabled)
		}
	}
	const customCommand = 30005
	colorbar := createPoststackToolButtonWithAction(parent, customCommand, ID_MARK, "", "独立窗口的色标命令", BS_PUSHBUTTON, 0, 0, 80, 25)
	if colorbar == 0 {
		t.Fatal("could not create shared vector colorbar")
	}
	controlID, _, _ := user32.NewProc("GetDlgCtrlID").Call(colorbar)
	if want := "独立窗口的色标命令"; controlID != customCommand || getText(colorbar) != want {
		t.Fatalf("reused glyph changed action %d or accessible name %q", controlID, getText(colorbar))
	}
	for _, enabled := range []bool{true, false} {
		pEnableWindow.Call(colorbar, uintptr(boolToInt(enabled)))
		pSendMessageW.Call(colorbar, 0x0318 /* WM_PRINTCLIENT */, dc, 0)
		chromatic := 0
		for y := 5; y < 22; y++ {
			for x := 30; x < 51; x++ {
				v, _, _ := getPixel.Call(dc, uintptr(x), uintptr(y))
				if v&255 != v>>8&255 || v>>8&255 != v>>16&255 {
					chromatic++
				}
			}
		}
		if enabled && chromatic < 6 || !enabled && chromatic != 0 {
			t.Fatalf("enabled=%v: colorbar lost ramp or disabled visual: %d chromatic pixels", enabled, chromatic)
		}
	}
	pDestroyWindow.Call(colorbar)
	if _, stale := poststackCustomCaptions[colorbar]; stale {
		t.Fatal("shared button retained dead-window state")
	}
}

func TestPoststackPhysicalTraceRangeFiltersReducedEnd(t *testing.T) {
	traces := []int64{0, 1, 2, 3, 4, 5, 6}
	coords := []int32{100, 101, 102, 103, 104, 105, 106}
	gotTraces, gotCoords := filterPhysicalTraceRange(traces, coords, 0, 3, 1)
	if len(gotTraces) != 4 || gotTraces[0] != 0 || gotTraces[3] != 3 || gotCoords[3] != 103 {
		t.Fatalf("reduced end trace was not applied: traces=%v coords=%v", gotTraces, gotCoords)
	}
	gotTraces, gotCoords = filterPhysicalTraceRange(traces, coords, 1, 6, 2)
	if fmt.Sprint(gotTraces) != "[1 3 5]" || fmt.Sprint(gotCoords) != "[101 103 105]" {
		t.Fatalf("trace interval not preserved: traces=%v coords=%v", gotTraces, gotCoords)
	}
}

func TestPoststackHomeOwnsAllBControls(t *testing.T) {
	old := cc
	defer func() { cc = old }()
	cc = compareControls{loadB: 11, generateB: 12, closeB: 13}
	have := map[uintptr]bool{}
	for _, h := range compareWorkspaceHandles() {
		have[h] = true
	}
	for _, h := range []uintptr{11, 12, 13} {
		if !have[h] {
			t.Fatalf("B handle %d not hidden by Home transition", h)
		}
	}
}

func TestPoststackSharedRangeUsesBothPreparedRasters(t *testing.T) {
	lo, hi := poststackSharedSectionRange([]float64{-2, 1}, []float64{-5, 7, math.NaN()}, 0, false, 0, 0)
	if lo != -5 || hi != 7 {
		t.Fatalf("not shared range: %v..%v", lo, hi)
	}
	lo, hi = poststackSharedSectionRange([]float64{-2, 1}, []float64{4, 7}, 0, true, -1, 1)
	if lo != -1 || hi != 1 {
		t.Fatalf("manual limits changed: %v..%v", lo, hi)
	}
}

func TestPoststackCompareProgressRejectsRetiredOwnerAndGeneration(t *testing.T) {
	for _, c := range []struct {
		owner, currentOwner uintptr
		gen, currentGen     int64
		want                bool
	}{
		{123, 123, 8, 8, true},
		{0, 0, 8, 8, false},
		{122, 123, 8, 8, false},
		{123, 123, 7, 8, false},
		// A reused HWND still cannot accept a previous task's progress.
		{123, 123, 8, 9, false},
	} {
		if got := compareProgressTokenCurrent(c.owner, c.currentOwner, c.gen, c.currentGen); got != c.want {
			t.Fatalf("progress token %+v accepted=%v", c, got)
		}
	}
	if WM_COMPARE_PREPARE_PROGRESS == WM_CADZOW_DONE || WM_COMPARE_EXPORT_PROGRESS == WM_CADZOW_DONE || WM_COMPARE_PREPARE_PROGRESS == WM_COMPARE_EXPORT_PROGRESS {
		t.Fatal("tokenized progress message IDs collide")
	}
}

func TestPoststackComparePrepareCancelInvalidatesQueuedProgress(t *testing.T) {
	oldCancel, oldGen := comparePrepareCancel, atomic.LoadInt64(&compareGen)
	defer func() { comparePrepareCancel = oldCancel; atomic.StoreInt64(&compareGen, oldGen) }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	comparePrepareCancel = cancel
	atomic.StoreInt64(&compareGen, 42)
	cancelPoststackComparePrepare()
	if ctx.Err() != context.Canceled || comparePrepareCancel != nil {
		t.Fatal("prepare context was not canceled and released")
	}
	if compareProgressTokenCurrent(123, 123, 42, atomic.LoadInt64(&compareGen)) {
		t.Fatal("progress queued before prepare cancellation remains current")
	}
	current := atomic.LoadInt64(&compareGen)
	cancelPoststackComparePrepare()
	if atomic.LoadInt64(&compareGen) != current {
		t.Fatal("repeated cancellation changed the completed token")
	}
}

func TestPoststackCompareStaleWorkersCannotReplaceCurrentMailbox(t *testing.T) {
	oldOwner := compareHwnd
	oldGen, oldSliceGen := atomic.LoadInt64(&compareGen), atomic.LoadInt64(&compareSliceGen)
	oldPrepare, oldSlice := comparePendingPrepare, comparePendingSlice
	defer func() {
		compareHwnd = oldOwner
		atomic.StoreInt64(&compareGen, oldGen)
		atomic.StoreInt64(&compareSliceGen, oldSliceGen)
		comparePendingPrepare, comparePendingSlice = oldPrepare, oldSlice
	}()
	compareHwnd = 123
	atomic.StoreInt64(&compareGen, 42)
	atomic.StoreInt64(&compareSliceGen, 45)
	currentPrepare := &comparePrepareResult{owner: 123, gen: 42}
	currentSlice := &compareSliceResult{owner: 123, gen: 45}
	comparePendingPrepare, comparePendingSlice = currentPrepare, currentSlice
	for _, stale := range []*comparePrepareResult{{owner: 123, gen: 41}, {owner: 122, gen: 42}, nil} {
		if previous, accepted := queueComparePrepareResult(stale); accepted || previous != nil || comparePendingPrepare != currentPrepare {
			t.Fatal("stale prepare worker replaced current result")
		}
	}
	for _, stale := range []*compareSliceResult{{owner: 123, gen: 44}, {owner: 122, gen: 45}, nil} {
		if previous, accepted := queueCompareSliceResult(stale); accepted || previous != nil || comparePendingSlice != currentSlice {
			t.Fatal("stale slice worker replaced current result")
		}
	}
	nextPrepare := &comparePrepareResult{owner: 123, gen: 42}
	if previous, accepted := queueComparePrepareResult(nextPrepare); !accepted || previous != currentPrepare || comparePendingPrepare != nextPrepare {
		t.Fatal("valid prepare result was not accepted atomically")
	}
	nextSlice := &compareSliceResult{owner: 123, gen: 45}
	if previous, accepted := queueCompareSliceResult(nextSlice); !accepted || previous != currentSlice || comparePendingSlice != nextSlice {
		t.Fatal("valid slice result was not accepted atomically")
	}
}
