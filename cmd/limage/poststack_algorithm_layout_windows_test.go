//go:build windows

package main

import (
	"errors"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
	api "github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi"
)

var testAlgorithmButtonClass sync.Once
var testAlgorithmAuxiliaryCloseClasses sync.Once

func TestAlgorithmDialogRepaintAcrossTabsPagesAndResize(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if cadzowHwnd != 0 || cadzowCancel != nil {
		t.Fatal("unexpected active algorithm dialog")
	}
	const class = "SeisForgeAlgorithmButtonNativeTest"
	hinst, _, _ := pGetModuleHandleW.Call(0)
	testAlgorithmButtonClass.Do(func() {
		registerClass(class, syscall.NewCallback(cadzowWndProc), COLOR_WINDOW+1, hinst, 0)
	})
	h, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(u16(class))), 0,
		WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN, 0, 0, 660, 440, 0, 0, hinst, 0)
	if h == 0 {
		t.Fatal("could not create hidden algorithm test window")
	}
	oldSaved, oldTimeout := algorithmSettingsDraftSaved, algorithmTimeoutDraft
	defer func() {
		pDestroyWindow.Call(h)
		algorithmSettingsDraftSaved, algorithmTimeoutDraft = oldSaved, oldTimeout
	}()
	cadzowHwnd = h
	cz = cadzowControls{body: createAlgorithmPageBody()}
	if cz.body == 0 {
		t.Fatal("could not create clipped page region")
	}
	child := func(parent uintptr, class, text string, style uint32, id int) uintptr {
		control := createCtrl(parent, class, text, WS_CHILD|algorithmClipSiblings|style, 0, 0, 1, 1, id)
		if control == 0 {
			t.Fatalf("could not create %s", class)
		}
		return control
	}
	cz.start = child(h, "BUTTON", "运行处理", algorithmOwnerDraw, IDCZ_START)
	cz.progress = child(h, "msctls_progress32", "", 0, 0)
	cz.status = child(h, "EDIT", "", 0x0804, IDCZ_STATUS) // ES_READONLY | ES_MULTILINE
	cz.failureTitle = child(h, "STATIC", "", 0, 0)
	cz.failureReason = child(h, "STATIC", "", 0, 0)
	cz.failureLog = child(h, "BUTTON", "查看详细日志", BS_PUSHBUTTON, IDCZ_FAILURE_LOG)
	cz.tabs = child(cz.body, "SysTabControl32", "", WS_TABSTOP, IDCZ_TABS)
	cz.pageNext = child(cz.body, "BUTTON", "下一页", WS_TABSTOP, IDCZ_PAGE_NEXT)
	cz.previewInfo = child(cz.body, "STATIC", "正式结果 · 2273 道 × 351 样点", 0, 0)
	cz.results = child(cz.body, "COMBOBOX", "", CBS_DROPDOWNLIST|CBS_HASSTRINGS, IDCZ_RESULTS)
	for i := 0; i < 12; i++ {
		cz.parameters = append(cz.parameters, algorithmParameterControl{
			Common: i < 4, CommonOrder: i, Descriptor: api.ParameterDescriptor{Type: "integer"},
			Label: child(cz.body, "STATIC", "时间子窗", 0, 0), Value: child(cz.body, "EDIT", "未提交的参数草稿", WS_BORDER, IDCZ_PARAMETER+i*3),
		})
	}
	layoutCadzowWindow("")
	if algorithmWindowStyle(cz.results)&WS_VISIBLE != 0 || algorithmWindowStyle(cz.progress)&WS_VISIBLE != 0 {
		t.Fatal("inactive results control or idle progress is visible")
	}
	cadzowCancel = func() {}
	updateCadzowAlgorithmUI()
	progressRunning := algorithmWindowStyle(cz.progress)&WS_VISIBLE != 0
	cadzowCancel = nil
	updateCadzowAlgorithmUI()
	if !progressRunning || algorithmWindowStyle(cz.progress)&WS_VISIBLE != 0 {
		t.Fatal("progress does not follow the worker lifecycle")
	}
	windowRect := func(control uintptr) (r RECT) {
		algorithmGetWindowRect.Call(control, uintptr(unsafe.Pointer(&r)))
		return
	}
	// Exercise a user-sized and moved frame, not the initial dimensions.
	setAlgorithmWindowBounds(h, 40, 60, algorithmPx(585), algorithmPx(500))
	layoutCadzowWindow("")
	outer, anchoredFooter, columns := windowRect(h), windowRect(cz.start), algorithmDialogColumns
	oldFailure, oldStatus, oldSource, oldProgress := algorithmFailure, algorithmStatus, algorithmStatusSource, algorithmProgress
	t.Cleanup(func() {
		algorithmFailure, algorithmStatus, algorithmStatusSource, algorithmProgress = oldFailure, oldStatus, oldSource, oldProgress
	})
	setPoststackAlgorithmFailure(poststackAlgorithmAttempt{ID: "test.example", Name: "Example"}, errors.New("exit status 1; configuration rejected"), true, false, false)
	if got := getText(cz.failureTitle); got != "处理失败 · Example" || algorithmWindowStyle(cz.failureLog)&WS_VISIBLE == 0 || cz.activeTab == algorithmTabResults {
		t.Fatalf("failure not visible independently of the inactive results: %q (%v)", got, cz.activeTab)
	}
	if windowRect(h) != outer || windowRect(cz.start) != anchoredFooter || algorithmDialogColumns != columns {
		t.Fatal("showing error changed the window, footer or columns")
	}
	algorithmFailure = algorithmFailureNotice{}
	longStatus := "临时预览完成 · Example\r\nInline 1870 · 203 道 × 46 样点 · 1086–1176 ms\r\nB 剖面已显示；未加入正式结果列表。"
	setAlgorithmStatus(longStatus, 100)
	if getText(cz.status) != longStatus || algorithmWindowStyle(cz.status)&0x0804 != 0x0804 {
		t.Fatal("status is not a native read-only multiline edit retaining all 3 lines")
	}
	if windowRect(h) != outer || windowRect(cz.start) != anchoredFooter {
		t.Fatal("updating long status changed the window or footer")
	}
	restoreCallerDPI := setAlgorithmDPIContext(^uintptr(0)) // Simulate a different owner DPI awareness.
	revealAlgorithmResults()                                // successful completion uses this exact path
	restoreCallerDPI()
	if cz.activeTab != algorithmTabResults || algorithmWindowStyle(cz.previewInfo)&WS_VISIBLE == 0 {
		t.Fatal("revealing results did not show the results control")
	}
	if got := windowRect(h); got != outer || windowRect(cz.start) != anchoredFooter || algorithmDialogColumns != columns {
		t.Fatalf("revealing a result moved/resized the user frame or changed its grid: before %v columns %d, after %v columns %d",
			outer, columns, got, algorithmDialogColumns)
	}
	index := slices.Index(cz.visibleTabs, algorithmTabSettings)
	pSendMessageW.Call(cz.tabs, algorithmTabSetCur, uintptr(index), 0)
	native := NMHDR{HwndFrom: cz.tabs, IdFrom: IDCZ_TABS, Code: TCN_SELCHANGE}
	pSendMessageW.Call(cz.body, WM_NOTIFY, IDCZ_TABS, uintptr(unsafe.Pointer(&native)))
	if cz.activeTab != algorithmTabSettings || algorithmWindowStyle(cz.previewInfo)&WS_VISIBLE != 0 {
		t.Fatal("native settings tab did not swap page controls")
	}
	if windowRect(h) != outer || windowRect(cz.start) != anchoredFooter || algorithmDialogColumns != columns {
		t.Fatal("toggling run settings changed the user frame, footer or grid")
	}
	// Constrain the window enough to exercise discrete pages and one-column layout.
	setAlgorithmWindowBounds(h, 0, 0, algorithmPx(520), algorithmPx(400))
	pSendMessageW.Call(h, WM_SIZE, 0, 0)
	selectAlgorithmTab(algorithmTabAdvanced)
	if algorithmWindowStyle(cz.body)&WS_VSCROLL != 0 || algorithmWindowStyle(cz.pageNext)&WS_VISIBLE == 0 {
		t.Fatal("constrained page does not use buttons without a vertical scroll bar")
	}
	footer := windowRect(cz.start)
	pSendMessageW.Call(cz.body, WM_COMMAND, IDCZ_PAGE_NEXT, cz.pageNext)
	if cz.tabPages[algorithmTabAdvanced] != 1 || footer != windowRect(cz.start) || algorithmWindowStyle(cz.parameters[4].Value)&WS_VISIBLE != 0 {
		t.Fatal("native pagination did not replace only the body rows")
	}
	if got := getText(cz.parameters[0].Value); got != "未提交的参数草稿" {
		t.Fatalf("layout changed parameter text: %q", got)
	}

	// Paint both real HWND backgrounds into an offscreen GDI surface. The
	// old body acknowledged WM_ERASEBKGND without clearing the stale pixels.
	dc, _, _ := pCreateCompatibleDC.Call(0)
	if dc == 0 {
		t.Fatal("could not create GDI fixture DC")
	}
	defer pDeleteDC.Call(dc)
	bmi := BITMAPINFO{Header: BITMAPINFOHEADER{Size: uint32(unsafe.Sizeof(BITMAPINFOHEADER{})), Width: 16, Height: -16, Planes: 1, BitCount: 32, Compression: BI_RGB}}
	var bits uintptr
	bitmap, _, _ := pCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&bmi)), DIB_RGB_COLORS, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 || bits == 0 {
		t.Fatal("could not create GDI fixture bitmap")
	}
	defer pDeleteObject.Call(bitmap)
	oldBitmap, _, _ := pSelectObject.Call(dc, bitmap)
	defer pSelectObject.Call(dc, oldBitmap)
	pixels := unsafe.Slice((*uint32)(unsafe.Pointer(bits)), 16*16)
	flush := gdi32.NewProc("GdiFlush")
	color, _, _ := pGetSysColor.Call(COLOR_WINDOW)
	want := uint32((color&0xff)<<16 | color&0xff00 | (color>>16)&0xff)
	for _, target := range []uintptr{h, cz.body} {
		for i := range pixels {
			pixels[i] = want ^ 0x00ffffff
		}
		pSendMessageW.Call(target, WM_ERASEBKGND, dc, 0)
		flush.Call()
		for i, got := range pixels {
			if got&0x00ffffff != want {
				t.Fatalf("background left stale pixel %d on HWND %#x: %#x want %#x", i, target, got, want)
			}
		}
	}
}

func TestAlgorithmOwnerDrawButtonsDispatchNativeClick(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if cadzowHwnd != 0 || cadzowCancel != nil {
		t.Fatal("unexpected active algorithm dialog")
	}
	const class = "SeisForgeAlgorithmButtonNativeTest"
	hinst, _, _ := pGetModuleHandleW.Call(0)
	testAlgorithmButtonClass.Do(func() {
		registerClass(class, syscall.NewCallback(cadzowWndProc), COLOR_WINDOW+1, hinst, 0)
	})
	h, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(u16(class))), 0,
		WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN, 0, 0, 660, 440, 0, 0, hinst, 0)
	if h == 0 {
		t.Fatal("could not create hidden algorithm test window")
	}
	oldTD, oldWorkspace := compareTD, compareWorkspaceActive
	oldStatus, oldStatusSource, oldProgress := algorithmStatus, algorithmStatusSource, algorithmProgress
	defer func() {
		pDestroyWindow.Call(h)
		compareTD, compareWorkspaceActive = oldTD, oldWorkspace
		algorithmStatus, algorithmStatusSource, algorithmProgress = oldStatus, oldStatusSource, oldProgress
	}()
	compareTD, compareWorkspaceActive = nil, false // exercise command routing without running an algorithm
	cadzowHwnd = h
	cz.status = createCtrl(h, "STATIC", "就绪", WS_CHILD|WS_VISIBLE, 0, 0, 140, 28, IDCZ_STATUS)
	cz.preview = createCtrl(h, "BUTTON", "预览当前窗口", WS_CHILD|WS_VISIBLE|WS_TABSTOP|algorithmOwnerDraw, 160, 32, 120, 28, IDCZ_PREVIEW)
	cz.start = createCtrl(h, "BUTTON", "运行处理", WS_CHILD|WS_VISIBLE|WS_TABSTOP|algorithmOwnerDraw, 290, 32, 112, 28, IDCZ_START)
	cz.body = createAlgorithmPageBody()
	if cz.body == 0 {
		t.Fatal("could not create clipped page region")
	}
	layoutCadzowWindow("")
	var client RECT
	pGetClientRect.Call(h, uintptr(unsafe.Pointer(&client)))
	plan := layoutAlgorithmDialog(int(client.Right)*96/algorithmScale(), int(client.Bottom)*96/algorithmScale())
	hitTest := user32.NewProc("ChildWindowFromPointEx")
	for _, action := range []struct {
		button   uintptr
		position algorithmBox
	}{{cz.start, plan.run}, {cz.preview, plan.preview}} {
		b := action.button
		if b == 0 {
			t.Fatal("could not create owner-draw button")
		}
		r := action.position
		// POINT is an eight-byte by-value argument on Win64.
		x, y := algorithmPx(r.x+r.w/2), algorithmPx(r.y+r.h/2)
		point := uintptr(uint64(uint32(x)) | uint64(uint32(y))<<32)
		if hit, _, _ := hitTest.Call(h, point, 0); hit != b {
			t.Fatalf("footer button is covered at (%d,%d): hit %#x, expected %#x", x, y, hit, b)
		}
		algorithmStatus = "就绪"
		pEnableWindow.Call(b, 1)
		pSendMessageW.Call(b, 0x00F5, 0, 0) // BM_CLICK sends native BN_CLICKED to parent.
		if want := "请进入比并等待 A 图像完成"; algorithmStatus != want || getText(cz.status) != want {
			t.Fatalf("native BUTTON click was not routed: status=%q control=%q", algorithmStatus, getText(cz.status))
		}
	}
}

func TestAlgorithmDialogReleasesOwnedFonts(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if algorithmDialogFont != 0 || algorithmDialogBold != 0 || cadzowHwnd != 0 {
		t.Fatal("unexpected retained algorithm window GDI state")
	}
	defer releaseAlgorithmDialogLayout()
	getObjectType := gdi32.NewProc("GetObjectType")
	flush := gdi32.NewProc("GdiFlush")
	resources := user32.NewProc("GetGuiResources")
	readCount := func() uintptr {
		flush.Call()                              // DeleteObject may be batched on this Windows GUI thread.
		n, _, _ := resources.Call(^uintptr(0), 0) // current process, GR_GDIOBJECTS
		return n
	}
	refreshAlgorithmFonts(96)
	for _, font := range []uintptr{algorithmDialogFont, algorithmDialogBold} {
		if kind, _, _ := getObjectType.Call(font); kind != 6 { // OBJ_FONT
			t.Fatalf("expected owned HFONT, got type %d", kind)
		}
	}
	refreshAlgorithmFonts(144) // Warm the system font cache for both DPIs.
	releaseAlgorithmDialogLayout()
	baseline := readCount()
	for range 100 {
		refreshAlgorithmFonts(96)
		refreshAlgorithmFonts(144)
		releaseAlgorithmDialogLayout()
	}
	if got := readCount(); got > baseline+4 {
		t.Fatalf("100 DPI/close cycles grew GDI count from %d to %d", baseline, got)
	}
}

func TestAlgorithmLayoutPinnedFooterAtDPI(t *testing.T) {
	for _, dims := range [][2]int{{660, 430}, {660, 600}, {540, 340}, {400, 330}, {400, 265}} {
		w, h := dims[0], dims[1]
		l := layoutAlgorithmDialog(w, h)
		if l.body.h < 20 || l.body.y < l.header.h {
			t.Fatalf("no body at %dx%d: %+v", w, h, l)
		}
		for _, dpi := range []int{96, 144, 192} {
			convert := func(x int) int { return x * dpi / 96 }
			actions := []algorithmBox{l.status, l.progress, l.cancel, l.preview, l.run, l.close}
			for i, a := range actions {
				if a.x < 0 || a.x+a.w > w || a.y+a.h > h || a.y < l.body.y+l.body.h || a.w < 1 || a.h < 1 {
					t.Fatalf("out-of-bounds footer at %dx%d: %+v", w, h, a)
				}
				for _, b := range actions[i+1:] {
					if convert(a.x) < convert(b.x+b.w) && convert(b.x) < convert(a.x+a.w) &&
						convert(a.y) < convert(b.y+b.h) && convert(b.y) < convert(a.y+a.h) {
						t.Fatalf("overlapping footer at %dx%d/%d DPI: %+v, %+v", w, h, dpi, a, b)
					}
				}
			}
			if l.preview.h != l.run.h || l.run.x-l.preview.x-l.preview.w != algorithmGap {
				t.Fatalf("inconsistent buttons at %dx%d: %+v", w, h, l)
			}
		}
	}
}

func TestAlgorithmTabsPagesReachWholeRowsAndKeepCommonGrid(t *testing.T) {
	params := []algorithmParameterControl{
		{Common: true, CommonOrder: 20}, {Common: true, CommonOrder: 10},
		{Common: true, CommonOrder: 40}, {Common: true, CommonOrder: 30},
		{Descriptor: api.ParameterDescriptor{Type: "group"}}, {Path: []string{"group", "member"}},
	}
	for range 14 {
		params = append(params, algorithmParameterControl{Path: []string{"group", "member"}})
	}
	wide := layoutAlgorithmTabs(760, 350, params, algorithmTabSettings, 0)
	if len(wide.common) != 4 || wide.commonTitle.h == 0 || !slices.Equal(wide.tabs, []algorithmTab{algorithmTabAdvanced, algorithmTabSettings, algorithmTabResults}) {
		t.Fatalf("common grid and native tabs missing: %+v", wide)
	}
	for i, want := range []int{1, 0, 3, 2} {
		if wide.common[i].index != want {
			t.Fatalf("unexpected common order: %v", wide.common)
		}
	}
	a, b, c, d := wide.common[0].box, wide.common[1].box, wide.common[2].box, wide.common[3].box
	if a.y != b.y || c.y != d.y || a.x != c.x || b.x != d.x || a.x+a.w+algorithmGap != b.x || a.y+a.h > c.y {
		t.Fatalf("not a 2×2 grid: %v", wide.common)
	}
	for _, dpi := range []int{96, 120, 144, 192} {
		for _, size := range [][2]int{{760, 735}, {720, 520}, {480, 410}, {400, 330}, {400, 265}} {
			w, h := size[0], size[1]
			root := layoutAlgorithmDialog(w, h)
			all := make(map[int]int)
			base := layoutAlgorithmTabs(w, root.body.h, params, algorithmTabSettings, 0)
			for _, p := range base.common {
				all[p.index]++
			}
			for _, tab := range base.tabs {
				first := layoutAlgorithmTabs(w, root.body.h, params, tab, 0)
				for page := 0; page < first.pageCount; page++ {
					l := layoutAlgorithmTabs(w, root.body.h, params, tab, page)
					if l.tab != base.tab || !slices.Equal(l.common, base.common) || len(l.rows) == 0 {
						t.Fatalf("page changed header/grid or hid all rows at %v and %d DPI: %+v", size, dpi, l)
					}
					for _, p := range l.parameters {
						toPx := func(x int) int { return x * dpi / 96 }
						if toPx(p.box.y) < toPx(l.panel.y) || toPx(p.box.y+p.box.h) > toPx(l.panel.y+l.panel.h) {
							t.Fatalf("row clipped at %v and %d DPI: %+v", size, dpi, p)
						}
						all[p.index]++
					}
				}
			}
			for i := range params {
				if all[i] != 1 {
					t.Fatalf("field %d appears %d times across pages at %v %d DPI", i, all[i], size, dpi)
				}
			}
		}
	}
	noop := layoutAlgorithmTabs(660, 350, nil, algorithmTabAdvanced, 0)
	if noop.commonTitle.h != 0 || slices.Contains(noop.tabs, algorithmTabAdvanced) {
		t.Fatalf("no-op creates empty advanced parameter space: %+v", noop)
	}
}

func TestAlgorithmNumericControlsRemainCompact(t *testing.T) {
	for _, w := range []int{330, 390, 750, 1080} {
		for _, kind := range []string{"integer", "number", "range", "file", "string"} {
			label, input := algorithmParameterWidths(w, kind, kind == "file", kind == "range")
			total := label + algorithmGap + input
			if kind == "file" {
				total += 37
			}
			if kind == "range" {
				total += algorithmGap + input
			}
			if total > w || input < 100 {
				t.Fatalf("%s width %d: field %d label %d total %d", kind, w, input, label, total)
			}
			if (kind == "number" || kind == "integer") && input > 160 {
				t.Fatalf("numeric input stretched: %d", input)
			}
		}
	}
}

func TestAlgorithmAdvancedPageKeepsAWholeSchemaGroupWhenItFits(t *testing.T) {
	group := func(id string) algorithmParameterControl {
		return algorithmParameterControl{Path: []string{id}, Descriptor: api.ParameterDescriptor{Type: "group"}}
	}
	member := func(g, id string) algorithmParameterControl {
		return algorithmParameterControl{Path: []string{g, id}, Descriptor: api.ParameterDescriptor{Type: "integer"}}
	}
	params := []algorithmParameterControl{group("first"), member("first", "a"), member("first", "b"), group("second"), member("second", "a"), member("second", "b")}
	first := layoutAlgorithmTabs(720, 255, params, algorithmTabAdvanced, 0)
	second := layoutAlgorithmTabs(720, 255, params, algorithmTabAdvanced, 1)
	if first.pageCount != 2 || !slices.Equal(first.rows, []int{0, 1, 2}) || !slices.Equal(second.rows, []int{3, 4, 5}) {
		t.Fatalf("groups split despite room on separate pages: %v / %v", first.rows, second.rows)
	}
}

func TestAlgorithmCompletionStatusUsesActualResultScope(t *testing.T) {
	result := &generatedPoststackResult{
		Algorithm: "internal.module.id", DisplayName: "带通滤波（零相位）", Line: 1870, Preview: true,
		Section: &poststack.SectionResult{SampleStart: 10, SampleEnd: 55, TraceIndices: make([]int64, 203),
			Metadata: poststack.Metadata{Direction: "inline", SampleIntervalUS: 2000, TimeOriginUS: 1086000}},
	}
	if got := poststackResultScope(result); got != "Inline 1870 · 203 道 × 46 样点 · 首道 1086–1176 ms" {
		t.Fatalf("result page actual geometry: %q", got)
	}
	if got := poststackResultStatus(result, "B 剖面已显示"); got != "预览完成 · B 剖面已显示 · 尚未加入正式结果" {
		t.Fatalf("footer duplicates the scope or does not distinguish preview: %q", got)
	}
	result.Preview = false
	if got := poststackResultStatus(result, "正在加载 B 剖面"); got != "处理完成 · 正在加载 B 剖面 · 已加入正式结果" {
		t.Fatalf("formal result status: %q", got)
	}
}

func TestAlgorithmBuiltInCompactFormAndInputSummary(t *testing.T) {
	d := poststack.BandpassDescriptor(2000)
	rows := make([]algorithmParameterControl, 0, len(d.Parameters))
	for _, p := range d.Parameters {
		rows = append(rows, algorithmParameterControl{Descriptor: p, Path: []string{p.ID},
			Compact: p.UI != nil && p.UI.Section == api.ParameterUICommon})
	}
	markCommonAlgorithmParameters(rows)
	markAlgorithmFilterApplicability(rows, true, poststack.FilterBandpass, poststack.FilterButterworth)
	w := algorithmInitialWidth
	h := algorithmInitialClientHeight(w, rows, true)
	root := layoutAlgorithmDialogWithHint(w, h, true)
	tabs := layoutAlgorithmTabs(w, root.body.h, rows, algorithmTabResults, 0)
	if h >= 500 || len(tabs.common) != 6 || len(tabs.rows) != 3 || slices.Contains(tabs.tabs, algorithmTabAdvanced) {
		t.Fatalf("built-in form isn't compact or common fields are hidden: %dx%d %+v", w, h, tabs)
	}
	if rows[tabs.common[4].index].Descriptor.ID != poststack.FilterOrderParameter || rows[tabs.common[5].index].Descriptor.ID != poststack.FilterPhaseParameter {
		t.Fatalf("order and phase are not in the common grid: %+v", tabs.common)
	}
	markAlgorithmFilterApplicability(rows, true, poststack.FilterLowpass, poststack.FilterElliptic)
	tabs = layoutAlgorithmTabs(w, root.body.h, rows, algorithmTabAdvanced, 0)
	if len(tabs.common) != 5 || !slices.Contains(tabs.tabs, algorithmTabAdvanced) {
		t.Fatalf("inapplicable high edge remains visible or applicable Rp/Rs are inaccessible: %+v", tabs)
	}
	short, full := algorithmInputSummary("E:/survey/sub/source.sgy", "Inline 1870", 203, 46, "首道 1086–1176 ms")
	if strings.Contains(short, "E:/survey") || !strings.Contains(short, "source.sgy") || !strings.Contains(short, "203 道 × 46 样点") || !strings.Contains(full, "E:/survey/sub/source.sgy") {
		t.Fatalf("wrong short/full summary: %q / %q", short, full)
	}
	if algorithmParametersDiffer(map[string]any{"order": int64(4)}, map[string]any{"order": float64(4)}) ||
		!algorithmParametersDiffer(map[string]any{"order": int64(3)}, map[string]any{"order": float64(4)}) {
		t.Fatal("draft comparison did not use normalized values")
	}
}

func TestAlgorithmCloseTargetsOnlyItsOwnNativeWindow(t *testing.T) {
	const class = "SeisForgeAlgorithmButtonNativeTest"
	for _, test := range []struct {
		name, status string
		msg, w       uintptr
	}{
		{"X idle", "", WM_CLOSE, 0},
		{"Close completed", "预览完成", WM_COMMAND, IDCZ_CLOSE},
		{"Alt+F4 failed", "处理失败", 0x0112, 0xf060},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime.LockOSThread()
			t.Cleanup(runtime.UnlockOSThread)
			if cadzowHwnd != 0 || cadzowCancel != nil {
				t.Fatal("unexpected previous algorithm window or worker")
			}
			inst, _, _ := pGetModuleHandleW.Call(0)
			testAlgorithmButtonClass.Do(func() {
				registerClass(class, syscall.NewCallback(cadzowWndProc), COLOR_WINDOW+1, inst, 0)
			})
			owner := makeSettingsTestWindow(t, "STATIC", 0)
			peer := makeSettingsTestWindow(t, "STATIC", 0)
			sibling := makeSettingsTestWindow(t, "STATIC", owner)
			h := makeSettingsTestWindow(t, class, owner)
			cadzowHwnd, cadzowOwner = h, owner
			oldStatus, oldPlacement := algorithmStatus, algorithmNormalWindow
			t.Cleanup(func() { algorithmStatus, algorithmNormalWindow = oldStatus, oldPlacement })
			algorithmStatus = test.status
			pShowWindow.Call(peer, 7) // SW_SHOWMINNOACTIVE
			beforeOwner := readSettingsTestWindowState(owner)
			beforePeer := readSettingsTestWindowState(peer)
			beforeSibling := readSettingsTestWindowState(sibling)
			if beforePeer.minimized == 0 {
				t.Fatal("could not minimize the independent test peer")
			}
			pSendMessageW.Call(h, test.msg, test.w, 0)
			if alive, _, _ := poststackIsWindow.Call(h); alive != 0 || cadzowHwnd != 0 || cadzowOwner != 0 {
				t.Fatal("algorithm dialog was not destroyed")
			}
			if readSettingsTestWindowState(owner) != beforeOwner || readSettingsTestWindowState(peer) != beforePeer ||
				readSettingsTestWindowState(sibling) != beforeSibling || algorithmNormalWindow != oldPlacement {
				t.Fatal("closing the algorithm changed a peer or replayed saved bounds")
			}
		})
	}
}

func TestAlgorithmLogAndResponseCloseLeaveTheirOwnerAlone(t *testing.T) {
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	owner := makeSettingsTestWindow(t, "STATIC", 0)
	const logClass, responseClass = "SeisForgeAlgorithmOwnedLogCloseTest", "SeisForgeAlgorithmOwnedResponseCloseTest"
	inst, _, _ := pGetModuleHandleW.Call(0)
	testAlgorithmAuxiliaryCloseClasses.Do(func() {
		registerClass(logClass, syscall.NewCallback(algorithmLogWndProc), COLOR_WINDOW+1, inst, 0)
		registerClass(responseClass, temporalFilterResponseCallback, COLOR_WINDOW+1, inst, 0)
	})
	logWindow := makeSettingsTestWindow(t, logClass, owner)
	response := makeSettingsTestWindow(t, responseClass, owner)
	oldOwner := cadzowHwnd
	t.Cleanup(func() {
		cadzowHwnd = oldOwner
		algorithmLogWindow, algorithmLogEdit = 0, 0
		delete(temporalFilterResponseOwners, owner)
	})
	cadzowHwnd, algorithmLogWindow = owner, logWindow
	temporalFilterResponseOwners[owner] = response
	beforeOwner, beforeResponse := readSettingsTestWindowState(owner), readSettingsTestWindowState(response)
	pSendMessageW.Call(logWindow, WM_CLOSE, 0, 0)
	if algorithmLogWindow != 0 || readSettingsTestWindowState(owner) != beforeOwner || readSettingsTestWindowState(response) != beforeResponse {
		t.Fatal("log close affected another HWND")
	}
	pSendMessageW.Call(response, WM_KEYDOWN, 0x1b, 0)
	if temporalFilterResponseOwners[owner] != 0 || readSettingsTestWindowState(owner) != beforeOwner {
		t.Fatal("response Escape affected another HWND")
	}
}

func TestAlgorithmWindowReopensAtLastUserPlacement(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if cadzowHwnd != 0 || cadzowCancel != nil {
		t.Fatal("unexpected active algorithm dialog")
	}
	const class = "SeisForgeAlgorithmButtonNativeTest"
	hinst, _, _ := pGetModuleHandleW.Call(0)
	testAlgorithmButtonClass.Do(func() {
		registerClass(class, syscall.NewCallback(cadzowWndProc), COLOR_WINDOW+1, hinst, 0)
	})
	oldPlacement := algorithmNormalWindow
	algorithmNormalWindow = RECT{}
	defer func() {
		if cadzowHwnd != 0 {
			pDestroyWindow.Call(cadzowHwnd)
		}
		algorithmNormalWindow = oldPlacement
	}()
	open := func() uintptr {
		h, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(u16(class))), 0,
			WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN, 0, 0, 600, 400, 0, 0, hinst, 0)
		if h == 0 {
			t.Fatal("could not create test window")
		}
		cadzowHwnd = h
		cz = cadzowControls{body: createAlgorithmPageBody()}
		placeAlgorithmWindowOnOpen()
		return h
	}
	h := open()
	work := algorithmMonitorArea(h)
	width, height := int(work.Right-work.Left), int(work.Bottom-work.Top)
	left, top := work.Left+15, work.Top+25
	w, hPx := minInt(width-16, algorithmPx(500)), minInt(height-26, algorithmPx(360))
	setAlgorithmWindowBounds(h, int(left), int(top), w, hPx)
	var want RECT
	algorithmGetWindowRect.Call(h, uintptr(unsafe.Pointer(&want))) // Win32 enforces the minimum frame.
	if want.Left != left || want.Top != top || want.Right <= want.Left || want.Bottom <= want.Top {
		t.Fatalf("fixture failed to place a valid user rectangle: %v", want)
	}
	pSendMessageW.Call(h, WM_EXITSIZEMOVE, 0, 0)
	finishAlgorithmDialogClose()
	if cadzowHwnd != 0 || algorithmNormalWindow != want {
		t.Fatalf("normal placement not remembered: got %v want %v", algorithmNormalWindow, want)
	}
	h = open()
	var reopened RECT
	algorithmGetWindowRect.Call(h, uintptr(unsafe.Pointer(&reopened)))
	if reopened != want {
		t.Fatalf("reopening moved/resized frame: got %v want %v", reopened, want)
	}
}
