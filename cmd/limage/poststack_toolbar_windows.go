//go:build windows

package main

import (
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

const (
	poststackTTFIdIsHWND = 0x0001
	poststackTTFSubclass = 0x0010
	poststackTTSNoPrefix = 0x0002
	poststackTTMAddToolW = WM_USER + 50
	poststackTTMDelToolW = WM_USER + 51
	poststackTTMMaxWidth = WM_USER + 24
	poststackTTMPop      = WM_USER + 28
	// A path label must hit-test for the tooltip, never wrap, and preserve '&'.
	poststackPathLabelStyle = 0x0100 | 0x4000 | 0x0080 // SS_NOTIFY | SS_ENDELLIPSIS | SS_NOPREFIX
)

var poststackToolbarTipText = map[int]string{
	ID_OPEN:             "打开 A 数据",
	ID_SAVE:             "保存当前 A 剖面图像",
	IDCMP_PALETTE:       "快速选择色表（E 下一色表，R 上一色表）；一致模式联动，独立模式通过各面板色标编辑",
	ID_FIXED:            "固定显示：切换窗口缩放时的缓存优先模式",
	ID_MARK:             "打开显示设置的色标页",
	ID_DATA:             "查看数据信息",
	ID_PARAMS:           "显示参数：数据增益 0–49%；Q +1%，W −1%",
	ID_ZOOM:             "框选缩放：在剖面上拖动选择区域",
	ID_ORIGIN:           "复位到完整视图",
	ID_MAIL:             "反馈问题或建议",
	ID_UPDATE:           "检查更新",
	ID_ABOUT:            "关于 SeisForge Studio",
	ID_COMPARE:          "进入 A/B 对比，或返回 A 的单幅剖面",
	ID_VOLUME:           "打开三维体视图",
	IDCMP_B:             "加载文件作为 B",
	IDCMP_CADZOW:        "打开独立算法处理窗口，不会立即运行",
	IDCMP_CLOSE_B:       "关闭当前 B，保留 A",
	IDCMP_DIFF:          "显示或隐藏 A−B 差值",
	IDCMP_SPECTRUM:      "频谱分析：查看当前剖面的频谱",
	IDCMP_EXPORT:        "导出当前剖面",
	IDCMP_TRACE_INSPECT: "单道分析：在剖面中选取一道；Esc 退出选道",
}

// The drawn short captions never replace the full native accessible names.
var poststackVectorLabels = map[int]string{
	ID_OPEN: "打开 A 数据", ID_SAVE: "保存当前 A 剖面图像", ID_FIXED: "固定显示",
	ID_MARK: "色标设置", ID_DATA: "查看数据信息", ID_PARAMS: "显示参数",
	ID_ZOOM: "框选缩放", ID_ORIGIN: "复位视图", ID_MAIL: "反馈问题或建议", ID_UPDATE: "检查更新", ID_ABOUT: "关于 SeisForge Studio",
	ID_COMPARE: "进入 A/B 剖面对比", ID_VOLUME: "打开三维体视图",
	IDCMP_SPECTRUM: "频谱分析", IDCMP_TRACE_INSPECT: "单道分析", IDCMP_EXPORT: "导出当前剖面",
	IDCMP_B: "加载文件作为 B", IDCMP_CADZOW: "打开独立算法处理窗口", IDCMP_CLOSE_B: "关闭当前 B，保留 A",
	IDCMP_DIFF: "显示或隐藏 A−B 差值", IDCMP_REFRESH: "复位当前视图范围", IDCMP_AUTOGEOM: "打开几何设置",
}

const (
	poststackButtonSelected = 0x0001 // Separate from the latched native check state.
	poststackButtonDisabled = 0x0004
	poststackButtonFocus    = 0x0010
	poststackButtonGlyphDIP = 14
	poststackButtonPadDIP   = 4
	poststackPushToggle     = BS_AUTOCHECKBOX | BS_PUSHLIKE
)

const (
	poststackGlyphPrevious = 100_000 + iota
	poststackGlyphNext
	poststackGlyphGo
	poststackGlyphCopy
	poststackGlyphClose
	poststackGlyphRun
	poststackGlyphPreview
)

var (
	poststackFocusRect       = user32.NewProc("DrawFocusRect")
	poststackToolGetFocus    = user32.NewProc("GetFocus")
	poststackToolIsEnabled   = user32.NewProc("IsWindowEnabled")
	poststackToolTrackMouse  = user32.NewProc("TrackMouseEvent")
	poststackToolSetSubclass = comctl32.NewProc("SetWindowSubclass")
	poststackToolDefSubclass = comctl32.NewProc("DefSubclassProc")
	poststackToolDelSubclass = comctl32.NewProc("RemoveWindowSubclass")
	poststackToolGetStyle    = user32.NewProc("GetWindowLongW")
	poststackToolDrawFrame   = user32.NewProc("DrawFrameControl")
	poststackToolCallback    uintptr
	poststackHoveredTool     uintptr // one window can own the hover at any time (UI thread only)
	poststackCustomCaptions  = make(map[uintptr]poststackButtonCaption)
)

type poststackButtonCaption struct {
	text   string
	native bool // draw the live native name, e.g. while an algorithm is running
}

func poststackToolToggle(id int) bool {
	switch id {
	case ID_FIXED, ID_ZOOM, ID_COMPARE, IDCMP_DIFF, IDCMP_TRACE_INSPECT:
		return true
	}
	return false
}

func poststackToolIconOnly(id int) bool {
	switch id {
	case ID_OPEN, ID_SAVE, ID_FIXED, ID_MARK, ID_DATA, ID_PARAMS, ID_ZOOM, ID_ORIGIN, ID_MAIL, ID_UPDATE, ID_ABOUT:
		return true
	}
	return false
}

func poststackToolAccessibleCaption(id int, compare bool) string {
	if id == ID_COMPARE && compare {
		return "返回 A 单幅剖面"
	}
	if caption := poststackVectorLabels[id]; caption != "" {
		return caption
	}
	return poststackToolbarCaption(id, compare)
}

func poststackVectorButtonWidth(id int, compare bool, dpi int, measure func(string) int) int {
	if poststackToolIconOnly(id) {
		return max(32, displayPixelDIP(poststackButtonGlyphDIP, dpi)+2*displayPixelDIP(poststackButtonPadDIP, dpi)+4)
	}
	return poststackToolButtonTextWidth(poststackToolbarCaption(id, compare), dpi, measure)
}

func poststackToolButtonTextWidth(caption string, dpi int, measure func(string) int) int {
	// Use the exact icon, gap and edge clearance also used by paint. The
	// minimum covers the legacy baseline for short single-character captions.
	textWidth := poststackToolbarButtonWidth(caption, measure) - 14
	return max(44, textWidth+displayPixelDIP(poststackButtonGlyphDIP, dpi)+3*displayPixelDIP(poststackButtonPadDIP, dpi))
}

func createPoststackToolButton(owner uintptr, id int, style uint32, x, y, width, height int) uintptr {
	return newPoststackToolButton(owner, id, id, nil, poststackToolAccessibleCaption(id, false), style, x, y, width, height)
}

func createPoststackToolButtonWithAction(owner uintptr, controlID, iconID int, caption, accessibleName string, style uint32, x, y, width, height int) uintptr {
	if accessibleName == "" {
		accessibleName = caption
	}
	return newPoststackToolButton(owner, controlID, iconID, &poststackButtonCaption{text: caption}, accessibleName, style, x, y, width, height)
}

// Use the same raised/sunken chrome as the toolbar while retaining a native,
// mutable label (and native keyboard/accessible semantics) for dialog actions.
// iconID == 0 gives a text-only button with identical chrome and feedback.
func createPoststackNamedToolButton(owner uintptr, controlID, iconID int, label string, style uint32, x, y, width, height int) uintptr {
	return newPoststackToolButton(owner, controlID, iconID, &poststackButtonCaption{native: true}, label, style, x, y, width, height)
}

// Button identity and accessibility come from the native control; an owner
// may reuse the artwork without adopting that artwork's toolbar action.
func newPoststackToolButton(owner uintptr, controlID, iconID int, caption *poststackButtonCaption, accessibleName string, style uint32, x, y, width, height int) uintptr {
	h := createCtrl(owner, "BUTTON", accessibleName, WS_CHILD|WS_TABSTOP|poststackToolbarClipSiblings|style, x, y, width, height, controlID)
	if poststackInstallToolPainter(h, iconID) && caption != nil {
		poststackCustomCaptions[h] = *caption
	}
	return h
}

func poststackInstallToolPainter(h uintptr, id int) bool {
	if h == 0 || id != 0 && poststackVectorLabels[id] == "" && (id < poststackGlyphPrevious || id > poststackGlyphPreview) {
		return false
	}
	if poststackToolCallback == 0 {
		poststackToolCallback = syscall.NewCallback(poststackToolSubclassProc)
	}
	ok, _, _ := poststackToolSetSubclass.Call(h, poststackToolCallback, 1, uintptr(id))
	return ok != 0
}

func poststackToolSubclassProc(h uintptr, msg uint32, wp, lp, subclass, ref uintptr) uintptr {
	id := int(ref)
	invalidate := false
	switch msg {
	case WM_MOUSEMOVE:
		if poststackHoveredTool != h {
			track := struct {
				Size, Flags uint32
				Window      uintptr
				HoverTime   uint32
			}{Flags: 2 /* TME_LEAVE */, Window: h}
			track.Size = uint32(unsafe.Sizeof(track))
			if v, _, _ := poststackToolTrackMouse.Call(uintptr(unsafe.Pointer(&track))); v != 0 {
				if poststackHoveredTool != 0 {
					pInvalidateRect.Call(poststackHoveredTool, 0, 0)
				}
				poststackHoveredTool, invalidate = h, true
			}
		}
	case 0x02a3: // WM_MOUSELEAVE
		if poststackHoveredTool == h {
			poststackHoveredTool, invalidate = 0, true
		}
	case 0x0018: // WM_SHOWWINDOW also ends hover when switching toolbar modes.
		if wp == 0 && poststackHoveredTool == h {
			poststackHoveredTool = 0
		}
	case 0x0082: // WM_NCDESTROY
		if poststackHoveredTool == h {
			poststackHoveredTool = 0
		}
		delete(poststackCustomCaptions, h)
		poststackToolDelSubclass.Call(h, poststackToolCallback, subclass)
	case WM_PAINT, 0x0318: // WM_PRINTCLIENT (e.g. native snapshots)
		// Preserve native button commands, check messages, accessibility,
		// focus and keyboard behavior. Only its painting is replaced.
		var ps PAINTSTRUCT
		dc := wp
		if msg == WM_PAINT {
			dc, _, _ = pBeginPaint.Call(h, uintptr(unsafe.Pointer(&ps)))
			defer pEndPaint.Call(h, uintptr(unsafe.Pointer(&ps)))
		}
		if dc != 0 {
			var r RECT
			pGetClientRect.Call(h, uintptr(unsafe.Pointer(&r)))
			state := uint32(0)
			if native, _, _ := pSendMessageW.Call(h, 0x00f2 /* BM_GETSTATE */, 0, 0); native&4 != 0 {
				state |= poststackButtonSelected
			}
			if enabled, _, _ := poststackToolIsEnabled.Call(h); enabled == 0 {
				state |= poststackButtonDisabled
			}
			if focus, _, _ := poststackToolGetFocus.Call(); focus == h {
				if ui, _, _ := pSendMessageW.Call(h, 0x0129 /* WM_QUERYUISTATE */, 0, 0); ui&1 == 0 { // UISF_HIDEFOCUS
					state |= poststackButtonFocus
				}
			}
			drawPoststackToolButton(h, id, dc, r, state)
		}
		return 0
	case BM_SETCHECK, 0x00f3 /* BM_SETSTATE */, WM_LBUTTONDOWN, WM_LBUTTONUP, WM_KEYDOWN, 0x0101, /* WM_KEYUP */
		0x0007 /* WM_SETFOCUS */, 0x0008 /* WM_KILLFOCUS */, 0x000a /* WM_ENABLE */, 0x0215, /* WM_CAPTURECHANGED */
		0x001F /* WM_CANCELMODE */, 0x000C /* WM_SETTEXT */, 0x0128 /* WM_UPDATEUISTATE */ :
		invalidate = true
	}
	ret, _, _ := poststackToolDefSubclass.Call(h, uintptr(msg), wp, lp)
	if invalidate {
		pInvalidateRect.Call(h, 0, 0)
	}
	return ret
}

const (
	poststackDFCButton    = 4
	poststackDFCSButton   = 0x0010
	poststackDFCSInactive = 0x0100
	poststackDFCSPushed   = 0x0200
	poststackDFCSHot      = 0x1000
)

func poststackButtonFrame(state uint32, checked, hovered bool) (flags uintptr, depressed bool) {
	flags = poststackDFCSButton
	depressed = checked || state&poststackButtonSelected != 0 && state&poststackButtonDisabled == 0
	if depressed {
		flags |= poststackDFCSPushed
	}
	if state&poststackButtonDisabled != 0 {
		flags |= poststackDFCSInactive
	} else if hovered && !depressed {
		flags |= poststackDFCSHot
	}
	return flags, depressed
}

func drawPoststackToolButton(hwnd uintptr, id int, dc uintptr, r RECT, state uint32) {
	customCaption, hasCustomCaption := poststackCustomCaptions[hwnd]
	if !hasCustomCaption && poststackVectorLabels[id] == "" {
		return
	}
	style, _, _ := poststackToolGetStyle.Call(hwnd, ^uintptr(15)) // GWL_STYLE
	checked := style&BS_PUSHLIKE != 0 && pSendMessageWCheck(hwnd)
	hovered := hwnd != 0 && poststackHoveredTool == hwnd
	flags, depressed := poststackButtonFrame(state, checked, hovered)
	frame := r
	poststackToolDrawFrame.Call(dc, uintptr(unsafe.Pointer(&frame)), poststackDFCButton, flags)
	ink, _, _ := pGetSysColor.Call(18 /* COLOR_BTNTEXT */)
	if state&poststackButtonDisabled != 0 {
		ink, _, _ = pGetSysColor.Call(17 /* COLOR_GRAYTEXT */)
	}
	dpi := displayDPI(hwnd)
	if flags&poststackDFCSHot != 0 {
		// Classic DrawFrameControl ignores HOT in some themes. A visible
		// inset underline marks hover without replacing the raised frame.
		pad := int32(displayPixelDIP(5, dpi))
		accent, _, _ := pGetSysColor.Call(13 /* COLOR_HIGHLIGHT */)
		drawHomeLine(dc, int(r.Left+pad), int(r.Bottom-4), int(r.Right-pad), int(r.Bottom-4), accent, 1)
	}
	size := min(int(r.Bottom-r.Top)-displayPixelDIP(5, dpi), displayPixelDIP(poststackButtonGlyphDIP, dpi))
	if id == 0 {
		size = 0
	}
	if id != 0 && size < 6 {
		return
	}
	caption := poststackToolbarCaption(id, compareWorkspaceActive)
	if hasCustomCaption {
		caption = customCaption.text
		if customCaption.native {
			caption = getText(hwnd)
		}
	} else if poststackToolIconOnly(id) {
		caption = ""
	}
	font, _, _ := pSendMessageW.Call(hwnd, 0x0031 /* WM_GETFONT */, 0, 0)
	if font == 0 {
		font = hFont
	}
	oldFont, _, _ := pSelectObject.Call(dc, font)
	var textBounds RECT
	if caption != "" {
		pDrawTextW.Call(dc, uintptr(unsafe.Pointer(u16(caption))), ^uintptr(0), uintptr(unsafe.Pointer(&textBounds)), DT_SINGLELINE|0x0400|0x0800)
	}
	pSelectObject.Call(dc, oldFont)
	gap := displayPixelDIP(poststackButtonPadDIP, dpi)
	contentWidth := size
	if caption != "" {
		contentWidth += int(textBounds.Right)
		if id != 0 {
			contentWidth += gap
		}
	}
	left := int(r.Left) + max(gap, (int(r.Right-r.Left)-contentWidth)/2)
	top := int(r.Top) + (int(r.Bottom-r.Top)-size)/2
	pressedOffset := 0
	if depressed {
		pressedOffset = displayPixelDIP(1, dpi)
		left, top = left+pressedOffset, top+pressedOffset
	}
	line := func(a, b, c, d int) {
		drawHomeLine(dc, left+a*size/12, top+b*size/12, left+c*size/12, top+d*size/12, ink, max(1, size/12))
	}
	path := func(pts ...int) {
		for i := 2; i < len(pts); i += 2 {
			line(pts[i-2], pts[i-1], pts[i], pts[i+1])
		}
	}
	switch id {
	case poststackGlyphPrevious:
		path(8, 2, 3, 6, 8, 10)
	case poststackGlyphNext:
		path(4, 2, 9, 6, 4, 10)
	case poststackGlyphGo:
		line(1, 6, 10, 6)
		path(6, 2, 10, 6, 6, 10)
		line(12, 1, 12, 11)
	case poststackGlyphCopy:
		path(2, 8, 2, 1, 8, 1, 8, 3)
		path(5, 4, 11, 4, 11, 11, 5, 11, 5, 4)
	case poststackGlyphClose:
		line(2, 2, 10, 10)
		line(10, 2, 2, 10)
	case poststackGlyphRun:
		path(3, 1, 10, 6, 3, 11, 3, 1)
	case poststackGlyphPreview:
		path(0, 6, 3, 3, 6, 2, 9, 3, 12, 6, 9, 9, 6, 10, 3, 9, 0, 6)
		path(6, 4, 8, 6, 6, 8, 4, 6, 6, 4)
	case ID_SAVE:
		path(1, 1, 9, 1, 11, 3, 11, 11, 1, 11, 1, 1)
		path(3, 1, 3, 5, 9, 5, 9, 1)
		path(3, 11, 3, 7, 9, 7, 9, 11)
	case ID_FIXED:
		// Fixed-display cache, not a pan tool: show a pinned image frame.
		path(0, 2, 7, 2, 7, 11, 0, 11, 0, 2)
		path(1, 9, 3, 7, 4, 8, 6, 6)
		path(8, 1, 12, 1, 11, 5, 9, 5, 8, 1)
		line(10, 5, 10, 9)
	case ID_MARK:
		// A true colorbar icon: a vertical amplitude ramp with its own tick
		// marks, not the older generic marker/action bitmap.
		tones := [4]uintptr{rgbRef(31, 95, 178), rgbRef(140, 183, 224), rgbRef(239, 173, 135), rgbRef(176, 58, 57)}
		if state&poststackButtonDisabled != 0 {
			tones = [4]uintptr{rgbRef(154, 154, 154), rgbRef(179, 179, 179), rgbRef(196, 196, 196), rgbRef(162, 162, 162)}
		}
		for i, color := range tones {
			band := RECT{Left: int32(left + size*2/12), Top: int32(top + size*(1+i*3)/12), Right: int32(left + size*7/12), Bottom: int32(top + size*min(12, 4+i*3)/12)}
			drawHomeRoundRect(dc, band, color, color, 0)
		}
		path(2, 1, 7, 1, 7, 12, 2, 12, 2, 1)
		line(7, 2, 11, 2)
		line(7, 6, 10, 6)
		line(7, 10, 11, 10)
	case ID_DATA:
		path(2, 0, 8, 0, 11, 3, 11, 12, 2, 12, 2, 0)
		path(8, 0, 8, 3, 11, 3)
		line(4, 5, 9, 5)
		line(4, 7, 9, 7)
		line(4, 9, 7, 9)
	case ID_ZOOM:
		path(3, 0, 7, 0, 10, 3, 10, 7, 7, 10, 3, 10, 0, 7, 0, 3, 3, 0)
		line(9, 9, 12, 12)
		line(3, 5, 7, 5)
		line(5, 3, 5, 7)
	case ID_MAIL:
		path(0, 2, 12, 2, 12, 10, 0, 10, 0, 2, 6, 7, 12, 2)
	case ID_UPDATE:
		line(6, 0, 6, 8)
		path(3, 5, 6, 8, 9, 5)
		path(1, 8, 1, 11, 11, 11, 11, 8)
	case ID_ABOUT:
		path(4, 0, 8, 0, 12, 4, 12, 8, 8, 12, 4, 12, 0, 8, 0, 4, 4, 0)
		line(6, 5, 6, 9)
		line(6, 3, 7, 3)
	case ID_COMPARE:
		if compareWorkspaceActive {
			path(5, 2, 1, 6, 5, 10)
			line(1, 6, 12, 6)
		} else {
			path(0, 2, 5, 2, 5, 10, 0, 10, 0, 2)
			path(7, 2, 12, 2, 12, 10, 7, 10, 7, 2)
			path(1, 7, 2, 5, 4, 6)
			path(8, 7, 9, 5, 11, 6)
		}
	case ID_VOLUME:
		path(6, 0, 12, 3, 12, 9, 6, 12, 0, 9, 0, 3, 6, 0)
		path(0, 3, 6, 6, 12, 3)
		line(6, 6, 6, 12)
	case ID_OPEN, IDCMP_B:
		path(0, 10, 0, 3, 5, 3, 6, 5, 11, 5, 11, 6)
		path(0, 10, 2, 6, 12, 6, 11, 10, 0, 10)
	case ID_PARAMS, IDCMP_CADZOW:
		line(1, 3, 11, 3)
		line(1, 6, 11, 6)
		line(1, 9, 11, 9)
		line(4, 1, 4, 5)
		line(8, 4, 8, 8)
		line(5, 7, 5, 11)
	case IDCMP_CLOSE_B:
		path(1, 1, 11, 1, 11, 11, 1, 11, 1, 1)
		line(4, 4, 8, 8)
		line(8, 4, 4, 8)
	case IDCMP_DIFF:
		path(1, 2, 11, 2, 11, 10, 1, 10, 1, 2)
		line(3, 6, 9, 6)
	case ID_ORIGIN, IDCMP_REFRESH:
		path(2, 4, 4, 2, 8, 2, 11, 5, 11, 8, 8, 11, 5, 11, 2, 9)
		path(1, 1, 1, 5, 5, 5)
	case IDCMP_AUTOGEOM:
		path(1, 1, 11, 1, 11, 11, 1, 11, 1, 1)
		line(6, 1, 6, 11)
		line(1, 6, 11, 6)
		path(2, 9, 5, 6, 8, 7, 10, 3)
	case IDCMP_SPECTRUM:
		path(1, 1, 1, 11, 12, 11)
		path(2, 9, 4, 5, 5, 7, 7, 2, 9, 6, 12, 8)
	case IDCMP_TRACE_INSPECT:
		line(6, 0, 6, 12)
		path(6, 0, 8, 2, 3, 4, 10, 6, 2, 8, 7, 10, 6, 12)
	case IDCMP_EXPORT:
		path(1, 2, 1, 11, 9, 11, 9, 8)
		line(4, 5, 12, 5)
		path(9, 2, 12, 5, 9, 8)
	}
	if caption != "" {
		tr := r
		tr.Left = int32(left + size)
		if id != 0 {
			tr.Left += int32(gap)
		}
		tr.Top += int32(pressedOffset)
		tr.Bottom += int32(pressedOffset)
		tr.Right -= int32(gap)
		drawHomeText(dc, font, ink, caption, tr, DT_LEFT|DT_SINGLELINE|0x0004|0x0800)
	}
	if state&poststackButtonFocus != 0 && state&poststackButtonDisabled == 0 {
		fr := r
		pad := int32(displayPixelDIP(3, dpi))
		fr.Left += pad
		fr.Top += pad
		fr.Right -= pad
		fr.Bottom -= pad
		poststackFocusRect.Call(dc, uintptr(unsafe.Pointer(&fr)))
	}
}

type poststackToolInfoW struct {
	size, flags    uint32
	owner, tool    uintptr
	rect           RECT
	instance       uintptr
	text           *uint16
	param, reserve uintptr
}

func makePoststackToolInfo(owner, tool uintptr, text *uint16) poststackToolInfoW {
	return poststackToolInfoW{
		// TOOLINFO v2 omits the reserved pointer. It is accepted by both the
		// classic common controls and the newer visual-styles implementation.
		size: uint32(unsafe.Offsetof(poststackToolInfoW{}.reserve)), flags: poststackTTFIdIsHWND | poststackTTFSubclass,
		owner: owner, tool: tool, text: text,
	}
}

type poststackToolbarPathTip struct {
	control uintptr
	path    string
	text    *uint16
	pin     runtime.Pinner
}

type poststackToolbarResources struct {
	tooltip  uintptr
	text     runtime.Pinner
	pathTips [5]poststackToolbarPathTip
}

func poststackPathCaption(role, path string, _ int, _ func(string) int) string {
	if path == "" {
		return role + ": 未加载"
	}
	// The caption is always the basename; the separate tooltip has the path.
	return role + ": " + filepath.Base(path)
}

// Native word wrapping may not split an uninterrupted path. Force line
// breaks when needed, preferably after a separator, without dropping text.
func wrapPoststackPathTip(path string, width int, measure func(string) int) string {
	if measure == nil || width <= 0 || measure(path) <= width {
		return path
	}
	runes := []rune(path)
	var out strings.Builder
	for len(runes) > 0 {
		end, boundary := 1, 0
		for i, r := range runes {
			if i > 0 && measure(string(runes[:i+1])) > width {
				break
			}
			end = i + 1
			if r == '\\' || r == '/' {
				boundary = end
			}
		}
		if end < len(runes) && boundary >= end/2 && boundary > 0 {
			end = boundary
		}
		out.WriteString(string(runes[:end]))
		runes = runes[end:]
		if len(runes) > 0 {
			out.WriteString("\r\n")
		}
	}
	return out.String()
}

func (r *poststackToolbarResources) createTooltips(owner uintptr) {
	r.createTooltipWindow(owner)
	for _, c := range compareToolbarControls() {
		description, ok := poststackToolbarTipText[c.id]
		if c.hwnd == 0 || !ok {
			continue
		}
		r.addControlTip(owner, c.hwnd, description)
	}
}

func (r *poststackToolbarResources) createTooltipWindow(owner uintptr) {
	if r.tooltip != 0 {
		return
	}
	const exTopmostNoActivate = 0x00000008 | 0x08000000
	r.tooltip, _, _ = pCreateWindowExW.Call(exTopmostNoActivate, uintptr(unsafe.Pointer(u16("tooltips_class32"))), 0,
		WS_POPUP|poststackTTSNoPrefix, 0, 0, 0, 0, owner, 0, 0, 0)
	if r.tooltip == 0 {
		return
	}
	// Enable multiline tips; long paths should not widen off the display.
	pSendMessageW.Call(r.tooltip, poststackTTMMaxWidth, 0, 680)
}

func (r *poststackToolbarResources) addControlTip(owner, control uintptr, description string) {
	if r == nil || r.tooltip == 0 || control == 0 {
		return
	}
	text := u16(description)
	// Keep UTF-16 memory pinned until the native tooltip is destroyed.
	r.text.Pin(text)
	info := makePoststackToolInfo(owner, control, text)
	pSendMessageW.Call(r.tooltip, poststackTTMAddToolW, 0, uintptr(unsafe.Pointer(&info)))
}

func (r *poststackToolbarResources) setPathTip(owner uintptr, slot int, control uintptr, path string) {
	if r == nil || r.tooltip == 0 || control == 0 {
		return
	}
	tip := &r.pathTips[slot]
	if tip.control == control && tip.path == path {
		return
	}
	if tip.text != nil {
		info := makePoststackToolInfo(owner, tip.control, nil)
		pSendMessageW.Call(r.tooltip, poststackTTMDelToolW, 0, uintptr(unsafe.Pointer(&info)))
		tip.pin.Unpin()
		tip.text, tip.path, tip.control = nil, "", 0
	}
	if path == "" {
		return
	}
	font, _, _ := pSendMessageW.Call(r.tooltip, 0x0031 /* WM_GETFONT */, 0, 0)
	if font == 0 {
		font, _, _ = pGetStockObject.Call(DEFAULT_GUI_FONT)
	}
	measure, release := poststackToolbarFontMeasure(font)
	tip.text = u16(wrapPoststackPathTip(path, 640, measure))
	release()
	tip.pin.Pin(tip.text)
	info := makePoststackToolInfo(owner, control, tip.text)
	if ok, _, _ := pSendMessageW.Call(r.tooltip, poststackTTMAddToolW, 0, uintptr(unsafe.Pointer(&info))); ok == 0 {
		tip.pin.Unpin()
		tip.text = nil
		return
	}
	tip.control, tip.path = control, path
}

func (r *poststackToolbarResources) popTooltip() {
	if r != nil && r.tooltip != 0 {
		pSendMessageW.Call(r.tooltip, poststackTTMPop, 0, 0)
	}
}

// Paths can be ellipsized in the toolbar; copy always uses the original path,
// never the wrapping inserted for the hover tooltip.
func showPoststackPathMenu(owner, control, lParam uintptr) bool {
	var path string
	switch control {
	case cc.aLabel:
		path = compareAPath
	case cc.bLabel:
		path = compareBPath
	default:
		return false
	}
	if control == 0 || path == "" {
		return false
	}
	p := POINT{X: int32(int16(lParam)), Y: int32(int16(lParam >> 16))}
	if p.X == -1 && p.Y == -1 {
		var bounds RECT
		pGetClientRect.Call(control, uintptr(unsafe.Pointer(&bounds)))
		p = POINT{X: bounds.Right / 2, Y: bounds.Bottom / 2}
		displayClientToScreen.Call(control, uintptr(unsafe.Pointer(&p)))
	}
	menu, _, _ := displayCreatePopup.Call()
	if menu == 0 {
		return true
	}
	defer displayDestroyMenu.Call(menu)
	cc.toolbar.popTooltip()
	displayAppendMenu.Call(menu, 0, 1, uintptr(unsafe.Pointer(u16("复制完整文件路径"))))
	command, _, _ := displayTrackPopup.Call(menu, 0x0102 /* TPM_RETURNCMD | TPM_RIGHTBUTTON */, uintptr(p.X), uintptr(p.Y), 0, owner, 0)
	if command == 1 && !copyPoststackPath(owner, path) {
		setText(cc.status, "无法写入剪贴板；可悬停查看完整文件路径。")
	}
	return true
}

func copyPoststackPath(owner uintptr, path string) bool {
	value, err := syscall.UTF16FromString(path)
	if err != nil {
		return false
	}
	h, _, _ := traceGlobalAlloc.Call(2 /* GMEM_MOVEABLE */, uintptr(len(value)*2))
	if h == 0 {
		return false
	}
	defer func() {
		if h != 0 {
			traceGlobalFree.Call(h)
		}
	}()
	p, _, _ := traceGlobalLock.Call(h)
	if p == 0 {
		return false
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(value)), value)
	traceGlobalUnlock.Call(h)
	if ok, _, _ := traceClipboardOpen.Call(owner); ok == 0 {
		return false
	}
	defer traceClipboardClose.Call()
	if ok, _, _ := traceClipboardEmpty.Call(); ok == 0 {
		return false
	}
	if ok, _, _ := traceClipboardSet.Call(traceCFUnicodeText, h); ok == 0 {
		return false
	}
	h = 0 // ownership transferred to the clipboard
	return true
}

func (r *poststackToolbarResources) close() {
	if r == nil {
		return
	}
	if r.tooltip != 0 {
		pDestroyWindow.Call(r.tooltip)
		r.tooltip = 0
	}
	r.text.Unpin()
	for i := range r.pathTips {
		tip := &r.pathTips[i]
		tip.pin.Unpin()
		tip.text, tip.path, tip.control = nil, "", 0
	}
}
