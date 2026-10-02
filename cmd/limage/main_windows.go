//go:build windows

package main

import (
	_ "embed"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	comdlg32 = syscall.NewLazyDLL("comdlg32.dll")
	comctl32 = syscall.NewLazyDLL("comctl32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")

	pRegisterClassExW         = user32.NewProc("RegisterClassExW")
	pCreateWindowExW          = user32.NewProc("CreateWindowExW")
	pDefWindowProcW           = user32.NewProc("DefWindowProcW")
	pShowWindow               = user32.NewProc("ShowWindow")
	pUpdateWindow             = user32.NewProc("UpdateWindow")
	pGetMessageW              = user32.NewProc("GetMessageW")
	pTranslateMessage         = user32.NewProc("TranslateMessage")
	pDispatchMessageW         = user32.NewProc("DispatchMessageW")
	pPostMessageW             = user32.NewProc("PostMessageW")
	pPostQuitMessage          = user32.NewProc("PostQuitMessage")
	pLoadCursorW              = user32.NewProc("LoadCursorW")
	pCreateIconFromResourceEx = user32.NewProc("CreateIconFromResourceEx")
	pBeginPaint               = user32.NewProc("BeginPaint")
	pEndPaint                 = user32.NewProc("EndPaint")
	pGetClientRect            = user32.NewProc("GetClientRect")
	pInvalidateRect           = user32.NewProc("InvalidateRect")
	pMessageBoxW              = user32.NewProc("MessageBoxW")
	pSetWindowTextW           = user32.NewProc("SetWindowTextW")
	pGetWindowTextW           = user32.NewProc("GetWindowTextW")
	pGetWindowTextLen         = user32.NewProc("GetWindowTextLengthW")
	pSendMessageW             = user32.NewProc("SendMessageW")
	pMoveWindow               = user32.NewProc("MoveWindow")
	pEnableWindow             = user32.NewProc("EnableWindow")
	pDestroyWindow            = user32.NewProc("DestroyWindow")
	pSetForeground            = user32.NewProc("SetForegroundWindow")
	pSetFocus                 = user32.NewProc("SetFocus")
	pIsIconic                 = user32.NewProc("IsIconic")
	pSetCapture               = user32.NewProc("SetCapture")
	pReleaseCapture           = user32.NewProc("ReleaseCapture")
	pIsChild                  = user32.NewProc("IsChild")
	pSetCursor                = user32.NewProc("SetCursor")
	pGetCursorPos             = user32.NewProc("GetCursorPos")
	pGetDoubleClickTime       = user32.NewProc("GetDoubleClickTime")
	pGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	pGetAncestor              = user32.NewProc("GetAncestor")
	pScreenToClient           = user32.NewProc("ScreenToClient")
	pDrawTextW                = user32.NewProc("DrawTextW")
	pFillRect                 = user32.NewProc("FillRect")

	pGetModuleHandleW       = kernel32.NewProc("GetModuleHandleW")
	pStretchDIBits          = gdi32.NewProc("StretchDIBits")
	pGetStockObject         = gdi32.NewProc("GetStockObject")
	pCreateDIBSection       = gdi32.NewProc("CreateDIBSection")
	pGetSysColor            = user32.NewProc("GetSysColor")
	pDeleteObject           = gdi32.NewProc("DeleteObject")
	pCreatePen              = gdi32.NewProc("CreatePen")
	pCreateSolidBrush       = gdi32.NewProc("CreateSolidBrush")
	pRoundRect              = gdi32.NewProc("RoundRect")
	pCreateFontW            = gdi32.NewProc("CreateFontW")
	pSelectObject           = gdi32.NewProc("SelectObject")
	pMoveToEx               = gdi32.NewProc("MoveToEx")
	pLineTo                 = gdi32.NewProc("LineTo")
	pEllipse                = gdi32.NewProc("Ellipse")
	pSetTextColor           = gdi32.NewProc("SetTextColor")
	pSetBkMode              = gdi32.NewProc("SetBkMode")
	pCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	pDeleteDC               = gdi32.NewProc("DeleteDC")
	pBitBlt                 = gdi32.NewProc("BitBlt")
	pSetStretchBltMode      = gdi32.NewProc("SetStretchBltMode")
	pSaveDC                 = gdi32.NewProc("SaveDC")
	pRestoreDC              = gdi32.NewProc("RestoreDC")
	pIntersectClipRect      = gdi32.NewProc("IntersectClipRect")

	pGetOpenFileNameW = comdlg32.NewProc("GetOpenFileNameW")
	pGetSaveFileNameW = comdlg32.NewProc("GetSaveFileNameW")
	pInitCommonCtrls  = comctl32.NewProc("InitCommonControlsEx")
	pShellExecuteW    = shell32.NewProc("ShellExecuteW")
)

const (
	WM_CREATE        = 0x0001
	WM_DESTROY       = 0x0002
	WM_SIZE          = 0x0005
	WM_PAINT         = 0x000F
	WM_ERASEBKGND    = 0x0014
	WM_CLOSE         = 0x0010
	WM_COMMAND       = 0x0111
	WM_HSCROLL       = 0x0114
	WM_NOTIFY        = 0x004E
	WM_GETMINMAXINFO = 0x0024
	WM_SETCURSOR     = 0x0020
	WM_KEYDOWN       = 0x0100
	WM_CHAR          = 0x0102
	WM_MOUSEMOVE     = 0x0200
	WM_LBUTTONDOWN   = 0x0201
	WM_LBUTTONUP     = 0x0202
	WM_LBUTTONDBLCLK = 0x0203
	WM_RBUTTONDOWN   = 0x0204
	WM_RBUTTONUP     = 0x0205
	WM_MOUSEWHEEL    = 0x020A
	WM_EXITSIZEMOVE  = 0x0232
	WM_SETFONT       = 0x0030
	WM_SETICON       = 0x0080
	WM_USER          = 0x0400
	WS_POPUP         = 0x80000000
	WS_CHILD         = 0x40000000
	WS_CLIPCHILDREN  = 0x02000000
	WS_VISIBLE       = 0x10000000
	WS_DISABLED      = 0x08000000
	WS_TABSTOP       = 0x00010000
	WS_GROUP         = 0x00020000
	WS_BORDER        = 0x00800000
	WS_EX_APPWINDOW  = 0x00040000

	WS_OVERLAPPEDWINDOW = 0x00CF0000
	WS_CAPTION          = 0x00C00000
	WS_SYSMENU          = 0x00080000
	CW_USEDEFAULT       = 0x80000000
	SW_HIDE             = 0
	SW_SHOW             = 5
	SW_RESTORE          = 9
	COLOR_WINDOW        = 5
	COLOR_BTNFACE       = 15
	IDC_ARROW           = 32512
	IDC_HAND            = 32649
	IDC_WAIT            = 32514
	IDC_CROSS           = 32515
	IDC_SIZENWSE        = 32642
	IDC_SIZENESW        = 32643
	IDC_SIZEWE          = 32644
	IDC_SIZENS          = 32645
	IDC_SIZEALL         = 32646
	VK_RETURN           = 0x0D
	VK_ESCAPE           = 0x1B
	VK_SPACE            = 0x20
	VK_Q                = 0x51
	VK_W                = 0x57
	VK_E                = 0x45
	VK_R                = 0x52
	VK_X                = 0x58
	VK_Y                = 0x59
	VK_Z                = 0x5A
	VK_F                = 0x46
	VK_A                = 0x41
	VK_D                = 0x44
	VK_NUMPAD0          = 0x60
	VK_NUMPAD9          = 0x69
	VK_MULTIPLY         = 0x6A
	VK_ADD              = 0x6B
	VK_SEPARATOR        = 0x6C
	VK_SUBTRACT         = 0x6D
	VK_DECIMAL          = 0x6E
	VK_DIVIDE           = 0x6F
	VK_HOME             = 0x24
	VK_LEFT             = 0x25
	VK_UP               = 0x26
	VK_RIGHT            = 0x27
	VK_DOWN             = 0x28
	VK_PRIOR            = 0x21
	VK_NEXT             = 0x22
	VK_END              = 0x23
	VK_INSERT           = 0x2D
	VK_DELETE           = 0x2E
	VK_CLEAR            = 0x0C
	GA_ROOTOWNER        = 3

	BS_PUSHBUTTON      = 0x00000000
	BS_DEFPUSHBUTTON   = 0x00000001
	BS_AUTOCHECKBOX    = 0x00000003
	BS_GROUPBOX        = 0x00000007
	BS_AUTORADIOBUTTON = 0x00000009
	BS_BITMAP          = 0x00000080
	BS_PUSHLIKE        = 0x00001000
	BS_MULTILINE       = 0x00002000
	CBS_DROPDOWNLIST   = 0x0003
	CBS_HASSTRINGS     = 0x0200
	ES_AUTOHSCROLL     = 0x0080
	ES_READONLY        = 0x0800

	BM_GETCHECK  = 0x00F0
	BM_SETCHECK  = 0x00F1
	BM_SETIMAGE  = 0x00F7
	IMAGE_BITMAP = 0
	BST_CHECKED  = 1
	CB_ADDSTRING = 0x0143
	CB_GETCURSEL = 0x0147
	CB_SETCURSEL = 0x014E

	TCM_FIRST       = 0x1300
	TCM_INSERTITEMW = TCM_FIRST + 62
	TCM_GETCURSEL   = TCM_FIRST + 11
	TCN_FIRST       = -550
	TCN_SELCHANGE   = TCN_FIRST - 1
	TCIF_TEXT       = 0x0001

	MB_OK               = 0
	MB_ICONINFORMATION  = 0x40
	MB_ICONERROR        = 0x10
	MB_YESNOCANCEL      = 0x03
	MB_YESNO            = 0x04
	IDYES               = 6
	IDNO                = 7
	IDCANCEL            = 2
	OFN_EXPLORER        = 0x00080000
	OFN_FILEMUSTEXIST   = 0x1000
	OFN_HIDEREADONLY    = 0x4
	OFN_OVERWRITEPROMPT = 0x2
	DIB_RGB_COLORS      = 0
	SRCCOPY             = 0x00CC0020
	BI_RGB              = 0
	DEFAULT_GUI_FONT    = 17
	WHITE_BRUSH         = 0
	HOLLOW_BRUSH        = 5
	PS_SOLID            = 0
	PS_DOT              = 2
	TRANSPARENT         = 1
	HALFTONE            = 4
	CS_DBLCLKS          = 0x0008
	DT_LEFT             = 0x0000
	DT_CENTER           = 0x0001
	DT_RIGHT            = 0x0002
	DT_VCENTER          = 0x0004
	DT_SINGLELINE       = 0x0020
	DT_WORDBREAK        = 0x0010
	ICON_SMALL          = 0
	ICON_BIG            = 1

	ICC_WIN95_CLASSES = 0x000000FF

	ID_OPEN    = 1001
	ID_SAVE    = 1002
	ID_COLOR   = 1003
	ID_FIXED   = 1004
	ID_MARK    = 1005
	ID_DATA    = 1006
	ID_PARAMS  = 1007
	ID_ZOOM    = 1008
	ID_ORIGIN  = 1009
	ID_MAIL    = 1010
	ID_UPDATE  = 1011
	ID_ABOUT   = 1012
	ID_COMPARE = 1013
	ID_VOLUME  = 1014

	APP_NAME        = "SeisForge Studio"
	APP_VERSION     = "1.10.2"
	APP_PROJECT_URL = "https://github.com/DavidLiu-code/SeisForge-Studio"

	IDP_OK       = 2001
	IDP_APPLY    = 2002
	IDP_CANCEL   = 2003
	IDP_TABS     = 2004
	IDP_GAIN     = 2005
	IDP_LIMIT    = 2006
	IDP_MIN      = 2007
	IDP_MAX      = 2008
	IDP_FIX      = 2009
	IDP_FIXW     = 2010
	IDP_FIXH     = 2011
	IDP_WIGGLE   = 2012
	IDP_DISPMODE = 2013

	IDM_RESETCOL = 3001
)

type POINT struct{ X, Y int32 }
type RECT struct{ Left, Top, Right, Bottom int32 }
type MSG struct {
	Hwnd           uintptr
	Message        uint32
	_              uint32
	WParam, LParam uintptr
	Time           uint32
	Pt             POINT
	LPrivate       uint32
}
type PAINTSTRUCT struct {
	Hdc                uintptr
	Erase              int32
	RcPaint            RECT
	Restore, IncUpdate int32
	Reserved           [32]byte
}
type WNDCLASSEX struct {
	CbSize, Style                            uint32
	WndProc                                  uintptr
	CbClsExtra, CbWndExtra                   int32
	HInstance, HIcon, HCursor, HbrBackground uintptr
	MenuName, ClassName                      *uint16
	HIconSm                                  uintptr
}
type BITMAPINFOHEADER struct {
	Size                         uint32
	Width, Height                int32
	Planes, BitCount             uint16
	Compression                  uint32
	SizeImage                    uint32
	XPelsPerMeter, YPelsPerMeter int32
	ClrUsed, ClrImportant        uint32
}
type BITMAPINFO struct{ Header BITMAPINFOHEADER }
type OPENFILENAME struct {
	LStructSize                    uint32
	_                              uint32
	HwndOwner, HInstance           uintptr
	LpstrFilter, LpstrCustomFilter uintptr
	NMaxCustFilter, NFilterIndex   uint32
	LpstrFile                      uintptr
	NMaxFile                       uint32
	_pad1                          uint32
	LpstrFileTitle                 uintptr
	NMaxFileTitle                  uint32
	_pad2                          uint32
	LpstrInitialDir, LpstrTitle    uintptr
	Flags                          uint32
	NFileOffset, NFileExtension    uint16
	LpstrDefExt                    uintptr
	LCustData                      uintptr
	LpfnHook                       uintptr
	LpTemplateName                 uintptr
	PvReserved                     uintptr
	DwReserved, FlagsEx            uint32
}
type INITCOMMONCONTROLSEX struct {
	DwSize, DwICC uint32
}
type TCITEM struct {
	Mask, DwState, DwStateMask uint32
	PszText                    *uint16
	CchTextMax, IImage         int32
	LParam                     uintptr
}
type NMHDR struct {
	HwndFrom uintptr
	IdFrom   uintptr
	Code     int32
	_        uint32
}
type MINMAXINFO struct {
	PtReserved, PtMaxSize, PtMaxPosition, PtMinTrackSize, PtMaxTrackSize POINT
}

type paramControls struct {
	tabs                          uintptr
	gain, limit, minEdit, maxEdit uintptr
	minOriginal, maxOriginal      uintptr
	fix, fixW, fixH               uintptr
	wiggle, displayMode           uintptr
	pages                         [3][]uintptr
}

var (
	hwnd            uintptr
	hFont           uintptr
	limageIconLarge uintptr
	limageIconSmall uintptr
	sf              *segy.File
	indices         []byte
	bgra            []byte
	imageW          int
	imageH          int
	lastStats       segy.RenderStats

	paletteIndex                       = 2 // legacy "黑灰白": positive black, zero grey, negative white
	clipPercent                        = 99.0
	agc                                = false
	gainPercent                        = 0.0
	renderDisplayMode segy.DisplayMode = segy.DisplayAdaptive
	useLimits                          = false
	limitMin                           = -1.0
	limitMax                           = 1.0
	traceStart        int64            = 0
	traceEnd          int64            = -1
	traceStep         int64            = 1
	sampleStart                        = 0
	sampleEnd                          = -1

	// Origin is the range explicitly accepted in 数据加载. Zoom never expands
	// beyond this cached range, so restoring the view cannot accidentally read
	// an entire very large SEG-Y volume.
	originTraceStart  int64 = 0
	originTraceEnd    int64 = -1
	originTraceStep   int64 = 1
	originSampleStart       = 0
	originSampleEnd         = -1
	originValid             = false

	zoomMode                   = false
	zoomDragging               = false
	zoomStartX, zoomStartY     int
	zoomCurrentX, zoomCurrentY int
	statusBase                 string
	crossCursor                uintptr

	btnOpen, btnSave, comboColor, btnFixed, btnMark, btnData, btnParams     uintptr
	btnZoom, btnOrigin, btnMail, btnUpdate, btnAbout, btnCompare, btnVolume uintptr
	statusLabel, progressBar                                                uintptr

	paramHwnd     uintptr
	paramOwner    uintptr
	pc            paramControls
	markHwnd      uintptr
	legacyBitmaps []uintptr
)

const (
	legacyToolbarHeight = 30
	legacyStatusHeight  = 16
	legacyAxisLeft      = 44
	legacyAxisRight     = 44
	legacyAxisTop       = 38
	legacyAxisBottom    = 34
)

// Names and anchor colors are recovered verbatim from the original Fimage v1.5.1
// TComboBox DFM and the 60-dword table at VA 0x4E72E8. TColor is 0x00BBGGRR.
var paletteNames = []string{
	"红灰青", "白灰黑", "黑灰白", "蓝灰红", "蓝灰褐1", "绿灰褐1", "蓝灰褐2", "绿灰褐2", "蓝灰橙", "绿灰橙",
	"绿灰褐3", "蓝灰褐3", "蓝黄褐", "绿灰红", "红黄蓝", "红蓝黄", "黄红绿", "绿红蓝", "灰黄橙", "灰绿黄",
}

var legacyAnchors = [20][3]uint32{
	{0x00800000, 0x00F0F0F0, 0x001818B5}, {0x00000000, 0x00808080, 0x00FFFFFF},
	{0x00FFFFFF, 0x00808080, 0x00000000}, {0x005000E0, 0x00F0F0F0, 0x00C04000},
	{0x003F5B72, 0x00F0F0F0, 0x00C04000}, {0x003F5B72, 0x00F0F0F0, 0x003F5B06},
	{0x000040A0, 0x00F0F0F0, 0x00C04000}, {0x000040A0, 0x00F0F0F0, 0x003F5B06},
	{0x000080FF, 0x00F0F0F0, 0x00C04000}, {0x000080FF, 0x00F0F0F0, 0x003F5B06},
	{0x003F5B72, 0x00B0B0B0, 0x003F5B06}, {0x003F5B72, 0x00B0B0B0, 0x00C04000},
	{0x003F5B72, 0x0020B0D0, 0x00C04000}, {0x005000E0, 0x00F0F0F0, 0x003F5B06},
	{0x00C04000, 0x0020B0D0, 0x005000E0}, {0x0020B0D0, 0x00C04000, 0x005000E0},
	{0x003F5B06, 0x005000E0, 0x0020B0D0}, {0x00C04000, 0x005000E0, 0x003F5B06},
	{0x000080FF, 0x0020B0D0, 0x00F0F0F0}, {0x0020B0D0, 0x003F5B06, 0x00F0F0F0},
}

type rgb struct{ r, g, b byte }

func u16(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }
func filterUTF16() []uint16 {
	s := "SEG-Y files (*.sgy;*.segy)\x00*.sgy;*.segy\x00All files (*.*)\x00*.*\x00\x00"
	r := make([]uint16, 0, len(s))
	for _, c := range s {
		r = append(r, uint16(c))
	}
	return r
}
func message(owner uintptr, title, text string, flags uintptr) {
	pMessageBoxW.Call(owner, uintptr(unsafe.Pointer(u16(text))), uintptr(unsafe.Pointer(u16(title))), flags)
}
func openFeedbackEmail(owner uintptr) {
	target := APP_PROJECT_URL + "/issues/new/choose"
	r, _, _ := pShellExecuteW.Call(
		owner,
		uintptr(unsafe.Pointer(u16("open"))),
		uintptr(unsafe.Pointer(u16(target))),
		0, 0, 1,
	)
	if r <= 32 {
		message(owner, "信息反馈", "请访问项目页面反馈问题：\n"+APP_PROJECT_URL, MB_OK|MB_ICONINFORMATION)
	}
}
func setMainTitle(s string)       { pSetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(u16(s)))) }
func setText(h uintptr, s string) { pSetWindowTextW.Call(h, uintptr(unsafe.Pointer(u16(s)))) }
func getText(h uintptr) string {
	n, _, _ := pGetWindowTextLen.Call(h)
	buf := make([]uint16, int(n)+2)
	pGetWindowTextW.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}
func setFont(h uintptr) {
	if h != 0 && hFont != 0 {
		pSendMessageW.Call(h, WM_SETFONT, hFont, 1)
	}
}

func createCtrl(parent uintptr, class, text string, style uint32, x, y, w, height int, id int) uintptr {
	hwndCtrl, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(u16(class))), uintptr(unsafe.Pointer(u16(text))),
		uintptr(style), uintptr(x), uintptr(y), uintptr(w), uintptr(height), parent, uintptr(id), 0, 0)
	setFont(hwndCtrl)
	return hwndCtrl
}

func makeLegacyBitmap(index int) uintptr {
	if index < 0 || index >= len(legacyIconPixels) {
		return 0
	}
	bmi := BITMAPINFO{Header: BITMAPINFOHEADER{Size: uint32(unsafe.Sizeof(BITMAPINFOHEADER{})), Width: 16, Height: -16, Planes: 1, BitCount: 32, Compression: BI_RGB, SizeImage: 16 * 16 * 4}}
	var bits uintptr
	hbm, _, _ := pCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&bmi)), DIB_RGB_COLORS, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hbm == 0 || bits == 0 {
		return 0
	}
	sys, _, _ := pGetSysColor.Call(COLOR_BTNFACE)
	br, bg, bb := byte(sys&0xff), byte((sys>>8)&0xff), byte((sys>>16)&0xff)
	dst := unsafe.Slice((*byte)(unsafe.Pointer(bits)), 16*16*4)
	for i, v := range legacyIconPixels[index] {
		r, g, b := byte((v>>16)&0xff), byte((v>>8)&0xff), byte(v&0xff)
		if byte(v>>24) == 0 {
			r, g, b = br, bg, bb
		}
		j := i * 4
		dst[j], dst[j+1], dst[j+2], dst[j+3] = b, g, r, 0
	}
	legacyBitmaps = append(legacyBitmaps, hbm)
	return hbm
}

func createIconButtonStyled(parent uintptr, x int, id int, imageIndex int, buttonStyle uint32) uintptr {
	h := createCtrl(parent, "BUTTON", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|buttonStyle|BS_BITMAP, x, 2, 27, 22, id)
	if bm := makeLegacyBitmap(imageIndex); bm != 0 {
		pSendMessageW.Call(h, BM_SETIMAGE, IMAGE_BITMAP, bm)
	}
	return h
}

func createIconButton(parent uintptr, x int, id int, imageIndex int) uintptr {
	return createIconButtonStyled(parent, x, id, imageIndex, BS_PUSHBUTTON)
}

func showControls(a []uintptr, show bool) {
	cmd := uintptr(SW_HIDE)
	if show {
		cmd = SW_SHOW
	}
	for _, h := range a {
		pShowWindow.Call(h, cmd)
	}
}

func openDialog(owner uintptr) string {
	buf := make([]uint16, 32768)
	fil := filterUTF16()
	ofn := OPENFILENAME{LStructSize: uint32(unsafe.Sizeof(OPENFILENAME{})), HwndOwner: owner,
		LpstrFilter: uintptr(unsafe.Pointer(&fil[0])), NFilterIndex: 1, LpstrFile: uintptr(unsafe.Pointer(&buf[0])),
		NMaxFile: uint32(len(buf)), Flags: OFN_EXPLORER | OFN_FILEMUSTEXIST | OFN_HIDEREADONLY}
	r, _, _ := pGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}
func saveDialog(owner uintptr) string {
	buf := make([]uint16, 32768)
	fil := []uint16{'B', 'M', 'P', ' ', 'i', 'm', 'a', 'g', 'e', ' ', '(', '*', '.', 'b', 'm', 'p', ')', 0, '*', '.', 'b', 'm', 'p', 0, 0}
	ofn := OPENFILENAME{LStructSize: uint32(unsafe.Sizeof(OPENFILENAME{})), HwndOwner: owner,
		LpstrFilter: uintptr(unsafe.Pointer(&fil[0])), NFilterIndex: 1, LpstrFile: uintptr(unsafe.Pointer(&buf[0])),
		NMaxFile: uint32(len(buf)), Flags: OFN_EXPLORER | OFN_OVERWRITEPROMPT, LpstrDefExt: uintptr(unsafe.Pointer(u16("bmp")))}
	r, _, _ := pGetSaveFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

func saveSegyDialog(owner uintptr, defaultName string) string {
	buf := make([]uint16, 32768)
	if defaultName != "" {
		u := syscall.StringToUTF16(defaultName)
		if len(u) > len(buf) {
			u = u[:len(buf)]
		}
		copy(buf, u)
	}
	fil := []uint16{'S', 'E', 'G', '-', 'Y', ' ', 's', 'e', 'c', 't', 'i', 'o', 'n', ' ', '(', '*', '.', 's', 'g', 'y', ';', '*', '.', 's', 'e', 'g', 'y', ')', 0, '*', '.', 's', 'g', 'y', ';', '*', '.', 's', 'e', 'g', 'y', 0, 0}
	of := OPENFILENAME{LStructSize: uint32(unsafe.Sizeof(OPENFILENAME{})), HwndOwner: owner,
		LpstrFilter: uintptr(unsafe.Pointer(&fil[0])), NFilterIndex: 1, LpstrFile: uintptr(unsafe.Pointer(&buf[0])),
		NMaxFile: uint32(len(buf)), Flags: OFN_EXPLORER | OFN_OVERWRITEPROMPT, LpstrDefExt: uintptr(unsafe.Pointer(u16("sgy")))}
	r, _, _ := pGetSaveFileNameW.Call(uintptr(unsafe.Pointer(&of)))
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

func askYesNoCancel(owner uintptr, title, text string) int {
	r, _, _ := pMessageBoxW.Call(owner, uintptr(unsafe.Pointer(u16(text))), uintptr(unsafe.Pointer(u16(title))), MB_YESNOCANCEL|MB_ICONINFORMATION)
	return int(r)
}

func askYesNo(owner uintptr, title, text string) int {
	r, _, _ := pMessageBoxW.Call(owner, uintptr(unsafe.Pointer(u16(text))), uintptr(unsafe.Pointer(u16(title))), MB_YESNO|MB_ICONINFORMATION)
	return int(r)
}

func clientRect(h uintptr) RECT {
	var r RECT
	pGetClientRect.Call(h, uintptr(unsafe.Pointer(&r)))
	return r
}
func clientSize(h uintptr) (int, int) {
	r := clientRect(h)
	return int(r.Right - r.Left), int(r.Bottom - r.Top)
}
func viewArea() (x, y, w, h int) {
	cw, ch := clientSize(hwnd)
	return 0, legacyToolbarHeight, maxInt(cw, 2), maxInt(ch-legacyToolbarHeight-legacyStatusHeight, 2)
}

// imageArea returns the actual seismic plotting rectangle.  The original
// Fimage reserved space on all four sides of the data for trace/time axes.
func imageArea() (x, y, w, h int) {
	cw, ch := clientSize(hwnd)
	x = legacyAxisLeft
	y = legacyToolbarHeight + legacyAxisTop
	w = cw - legacyAxisLeft - legacyAxisRight
	h = ch - legacyToolbarHeight - legacyStatusHeight - legacyAxisTop - legacyAxisBottom
	if w < 2 {
		w = 2
	}
	if h < 2 {
		h = 2
	}
	return
}

func setStatusBase(s string) {
	statusBase = s
	if statusLabel != 0 {
		setText(statusLabel, s)
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func pointInImage(px, py int) bool {
	x, y, w, h := imageArea()
	return px >= x && px < x+w && py >= y && py < y+h
}

// invalidateMainImage limits transient zoom/palette repainting to the seismic
// canvas. Invalidating the whole legacy window also asks native toolbar
// buttons to participate in the update cycle, which is visible as a flash
// when the user clicks or drags on the image.
func invalidateMainImage(erase bool) {
	if hwnd == 0 {
		return
	}
	x, y, w, h := imageArea()
	r := RECT{Left: int32(x), Top: int32(y), Right: int32(x + w), Bottom: int32(y + h)}
	bg := uintptr(0)
	if erase {
		bg = 1
	}
	pInvalidateRect.Call(hwnd, uintptr(unsafe.Pointer(&r)), bg)
}

func mousePoint(lParam uintptr) (int, int) {
	x := int(int16(uint16(lParam & 0xffff)))
	y := int(int16(uint16((lParam >> 16) & 0xffff)))
	return x, y
}

// coordinateAtPixel maps the displayed image back to the exact trace/sample
// range represented by lastStats. It does not read any sample from disk.
func coordinateAtPixel(px, py int) (traceNo int64, sample int, timeMS float64, ok bool) {
	if sf == nil || !pointInImage(px, py) {
		return 0, 0, 0, false
	}
	x, y, w, h := imageArea()
	step := lastStats.TraceStep
	if step < 1 {
		step = 1
	}
	count := (lastStats.TraceEnd-lastStats.TraceStart)/step + 1
	if count < 1 {
		count = 1
	}
	idx := int64(0)
	if w > 1 && count > 1 {
		idx = int64(math.Round(float64(px-x) * float64(count-1) / float64(w-1)))
	}
	if idx < 0 {
		idx = 0
	}
	if idx >= count {
		idx = count - 1
	}
	traceNo = lastStats.TraceStart + idx*step + 1
	ns := lastStats.SampleEnd - lastStats.SampleStart + 1
	if ns < 1 {
		ns = 1
	}
	si := 0
	if h > 1 && ns > 1 {
		si = int(math.Round(float64(py-y) * float64(ns-1) / float64(h-1)))
	}
	if si < 0 {
		si = 0
	}
	if si >= ns {
		si = ns - 1
	}
	sample = lastStats.SampleStart + si
	timeMS = float64(sample) * float64(sf.Info.SampleIntervalUS) / 1000.0
	return traceNo, sample, timeMS, true
}

func updateMouseStatus(px, py int) {
	if tr, sm, tm, ok := coordinateAtPixel(px, py); ok {
		mode := ""
		if zoomMode {
			mode = "   [Zoom: drag to select]"
		}
		setText(statusLabel, fmt.Sprintf(" Trace %d   Sample %d   Time %s%s", tr, sm, formatAdaptiveTimeMS(tm), mode))
	} else if statusBase != "" {
		setText(statusLabel, statusBase)
	}
}

func setZoomMode(on bool) {
	zoomMode = on
	if btnZoom != 0 {
		v := uintptr(0)
		if on {
			v = BST_CHECKED
		}
		pSendMessageW.Call(btnZoom, BM_SETCHECK, v, 0)
	}
	if !on && zoomDragging {
		zoomDragging = false
		pReleaseCapture.Call()
		invalidateMainImage(false)
	}
	if crossCursor != 0 && on {
		pSetCursor.Call(crossCursor)
	}
}

func storeOriginRange() {
	originTraceStart, originTraceEnd, originTraceStep = traceStart, traceEnd, traceStep
	originSampleStart, originSampleEnd = sampleStart, sampleEnd
	originValid = sf != nil
}

func restoreOriginRange() {
	if sf == nil {
		return
	}
	if originValid {
		traceStart, traceEnd, traceStep = originTraceStart, originTraceEnd, originTraceStep
		sampleStart, sampleEnd = originSampleStart, originSampleEnd
	} else {
		traceStart, traceEnd, traceStep = 0, sf.Info.TraceCount-1, 1
		sampleStart, sampleEnd = 0, sf.Info.SamplesPerTrace-1
	}
	zoomDragging = false
	setZoomMode(false)
	rerender()
}

func applyZoomRect(x0, y0, x1, y1 int) {
	if sf == nil {
		return
	}
	x, y, w, h := imageArea()
	if w < 2 || h < 2 {
		return
	}
	x0 = clampInt(x0, x, x+w-1)
	x1 = clampInt(x1, x, x+w-1)
	y0 = clampInt(y0, y, y+h-1)
	y1 = clampInt(y1, y, y+h-1)
	if x1 < x0 {
		x0, x1 = x1, x0
	}
	if y1 < y0 {
		y0, y1 = y1, y0
	}
	// The original x86 handler ignored selections smaller than about 3 pixels.
	if x1-x0 < 3 || y1-y0 < 3 {
		return
	}

	step := lastStats.TraceStep
	if step < 1 {
		step = 1
	}
	count := (lastStats.TraceEnd-lastStats.TraceStart)/step + 1
	if count < 1 {
		count = 1
	}
	leftF := float64(x0-x) / float64(w-1)
	rightF := float64(x1-x) / float64(w-1)
	i0 := int64(math.Floor(leftF * float64(maxInt64(count-1, 0))))
	i1 := int64(math.Ceil(rightF * float64(maxInt64(count-1, 0))))
	if i0 < 0 {
		i0 = 0
	}
	if i1 >= count {
		i1 = count - 1
	}

	ns := lastStats.SampleEnd - lastStats.SampleStart + 1
	if ns < 1 {
		ns = 1
	}
	topF := float64(y0-y) / float64(h-1)
	botF := float64(y1-y) / float64(h-1)
	s0 := int(math.Floor(topF * float64(maxInt(ns-1, 0))))
	s1 := int(math.Ceil(botF * float64(maxInt(ns-1, 0))))
	if s0 < 0 {
		s0 = 0
	}
	if s1 >= ns {
		s1 = ns - 1
	}

	oldTS, oldTE, oldStep := traceStart, traceEnd, traceStep
	oldSS, oldSE := sampleStart, sampleEnd
	traceStart = lastStats.TraceStart + i0*step
	traceEnd = lastStats.TraceStart + i1*step
	traceStep = step
	sampleStart = lastStats.SampleStart + s0
	sampleEnd = lastStats.SampleStart + s1
	if traceEnd < traceStart || sampleEnd < sampleStart || !rerender() {
		traceStart, traceEnd, traceStep = oldTS, oldTE, oldStep
		sampleStart, sampleEnd = oldSS, oldSE
	}
}

func tcolor(v uint32) rgb { return rgb{byte(v & 0xff), byte((v >> 8) & 0xff), byte((v >> 16) & 0xff)} }
func lerp(a, b byte, i int) byte {
	// Exact legacy routine uses factor i/128 for i=0..127.
	v := float64(a) + float64(int(b)-int(a))*float64(i)/128.0
	if v < 0 {
		v = 0
	}
	if v > 255 {
		v = 255
	}
	return byte(math.Round(v))
}

var displayModeNames = []string{"快速像素", "平滑插值", "自适应"}

func displayModeLabel(m segy.DisplayMode) string {
	switch m {
	case segy.DisplaySmooth:
		return "平滑"
	case segy.DisplayAdaptive:
		return "自适应"
	default:
		return "快速"
	}
}

func currentPalette() [256]rgb {
	idx := paletteIndex
	if idx < 0 || idx >= len(legacyAnchors) {
		idx = 2
	}
	a := legacyAnchors[idx]
	// The original table is stored end/mid/start; routine 0x408610 reverses it.
	start, mid, end := tcolor(a[2]), tcolor(a[1]), tcolor(a[0])
	var p [256]rgb
	for i := 0; i < 128; i++ {
		p[i] = rgb{lerp(start.r, mid.r, i), lerp(start.g, mid.g, i), lerp(start.b, mid.b, i)}
		p[128+i] = rgb{lerp(mid.r, end.r, i), lerp(mid.g, end.g, i), lerp(mid.b, end.b, i)}
	}
	return p
}
func applyPalette() {
	if len(indices) == 0 {
		return
	}
	pal := currentPalette()
	bgra = make([]byte, len(indices)*4)
	for i, v := range indices {
		c := pal[int(v)]
		j := i * 4
		bgra[j] = c.b
		bgra[j+1] = c.g
		bgra[j+2] = c.r
		bgra[j+3] = 0
	}
	invalidateMainImage(false)
	if markHwnd != 0 {
		pInvalidateRect.Call(markHwnd, 0, 1)
	}
	if compareHwnd != 0 {
		refreshComparePaletteOnly()
	}
}

func loadFile(path string) {
	// Compatibility helper: choose the full range. The normal v3 workflow uses
	// the reconstructed 数据加载 dialog so the user can crop before sample I/O.
	f, err := segy.Open(path)
	if err != nil {
		message(hwnd, APP_NAME+" - Open error", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	info := f.Info
	f.Close()
	traceStart = 0
	traceEnd = info.TraceCount - 1
	traceStep = 1
	sampleStart = 0
	sampleEnd = info.SamplesPerTrace - 1
	useLimits = false
	gainPercent = 0
	if loadSelectedFile(path) {
		storeOriginRange()
		workspaceSyncAFromMain()
		completeWorkspaceOpen()
	}
}
func enableDataControls(on bool) {
	for _, h := range []uintptr{btnSave, comboColor, btnFixed, btnMark, btnData, btnParams, btnZoom, btnOrigin, btnCompare, btnVolume} {
		v := uintptr(0)
		if on {
			v = 1
		}
		pEnableWindow.Call(h, v)
	}
}
func rerender() bool {
	if sf == nil {
		return false
	}
	_, _, w, h := imageArea()
	if w > 2200 {
		w = 2200
	}
	if h > 1800 {
		h = 1800
	}
	p, st, err := sf.RenderWithOptions(segy.RenderOptions{Width: w, Height: h, AGC: agc, ClipPercent: clipPercent, GainPercent: gainPercent,
		UseValueLimits: useLimits, MinValue: limitMin, MaxValue: limitMax, TraceStart: traceStart, TraceEnd: traceEnd, TraceStep: traceStep, SampleStart: sampleStart, SampleEnd: sampleEnd, DisplayMode: renderDisplayMode})
	if err != nil {
		message(hwnd, APP_NAME+" - Render error", err.Error(), MB_OK|MB_ICONERROR)
		return false
	}
	indices = p
	imageW = w
	imageH = h
	lastStats = st
	if !useLimits {
		limitMin = st.MapMin
		limitMax = st.MapMax
	}
	applyPalette()
	setStatusBase(fmt.Sprintf(" Traces %d-%d step %d | samples %d-%d | display %.5g..%.5g | gain %.0f%% | %s", st.TraceStart+1, st.TraceEnd+1, st.TraceStep, st.SampleStart, st.SampleEnd, st.MapMin, st.MapMax, gainPercent, paletteNames[paletteIndex]))
	updateParamRangeFields()
	return true
}

func rgbRef(r, g, b byte) uintptr {
	return uintptr(uint32(r) | uint32(g)<<8 | uint32(b)<<16)
}

func niceFloatStep(raw float64) float64 {
	if raw <= 0 || math.IsNaN(raw) || math.IsInf(raw, 0) {
		return 1
	}
	p := math.Pow(10, math.Floor(math.Log10(raw)))
	f := raw / p
	var n float64
	switch {
	case f <= 1:
		n = 1
	case f <= 2:
		n = 2
	case f <= 5:
		n = 5
	default:
		n = 10
	}
	return n * p
}

func niceIntStep(raw float64) int64 {
	v := int64(math.Round(niceFloatStep(raw)))
	if v < 1 {
		return 1
	}
	return v
}

func drawLine(hdc uintptr, x1, y1, x2, y2 int) {
	pMoveToEx.Call(hdc, uintptr(x1), uintptr(y1), 0)
	pLineTo.Call(hdc, uintptr(x2), uintptr(y2))
}

func drawAxisText(hdc uintptr, text string, left, top, right, bottom int, flags uint32) {
	r := RECT{Left: int32(left), Top: int32(top), Right: int32(right), Bottom: int32(bottom)}
	pDrawTextW.Call(hdc, uintptr(unsafe.Pointer(u16(text))), uintptr(^uint32(0)), uintptr(unsafe.Pointer(&r)), uintptr(flags|DT_SINGLELINE))
}

// formatAdaptiveTimeMS keeps seismic time labels compact. Values at or above
// one second are shown in seconds; sub-second values stay in milliseconds.
// This avoids long labels such as "1500.000 ms" overlapping seismic panels.
func formatAdaptiveTimeMS(ms float64) string {
	a := math.Abs(ms)
	if a >= 1000.0 {
		s := ms / 1000.0
		if math.Abs(s-math.Round(s)) < 0.0005 {
			return fmt.Sprintf("%.0f s", s)
		}
		return fmt.Sprintf("%.2f s", s)
	}
	if math.Abs(ms-math.Round(ms)) < 0.05 {
		return fmt.Sprintf("%.0f ms", ms)
	}
	if a >= 100.0 {
		return fmt.Sprintf("%.1f ms", ms)
	}
	return fmt.Sprintf("%.2f ms", ms)
}

func formatAdaptiveTimeRangeMS(ms0, ms1 float64) string {
	if math.Max(math.Abs(ms0), math.Abs(ms1)) >= 1000.0 {
		return fmt.Sprintf("%.2f–%.2f s", ms0/1000.0, ms1/1000.0)
	}
	return fmt.Sprintf("%s–%s", formatAdaptiveTimeMS(ms0), formatAdaptiveTimeMS(ms1))
}

func drawAxes(hdc uintptr) {
	if sf == nil || imageW <= 0 || imageH <= 0 {
		return
	}
	x, y, w, h := imageArea()
	right := x + w - 1
	bottom := y + h - 1

	// Original Fimage uses a bright blue rectangular data frame and blue ticks.
	pen, _, _ := pCreatePen.Call(PS_SOLID, 1, rgbRef(0, 0, 255))
	oldPen, _, _ := pSelectObject.Call(hdc, pen)
	drawLine(hdc, x, y, right, y)
	drawLine(hdc, right, y, right, bottom)
	drawLine(hdc, right, bottom, x, bottom)
	drawLine(hdc, x, bottom, x, y)

	// Trace coordinates (top and bottom).  Use approximately ten divisions,
	// exactly reproducing the familiar 1, 51, 101, ... pattern for 500 traces.
	step := lastStats.TraceStep
	if step < 1 {
		step = 1
	}
	count := (lastStats.TraceEnd-lastStats.TraceStart)/step + 1
	if count < 1 {
		count = 1
	}
	tickIndexStep := niceIntStep(float64(maxInt64(count-1, 1)) / 10.0)
	seenLast := false
	for idx := int64(0); idx < count; idx += tickIndexStep {
		px := x
		if count > 1 {
			px = x + int(math.Round(float64(idx)*float64(w-1)/float64(count-1)))
		}
		drawLine(hdc, px, y, px, y-6)
		drawLine(hdc, px, bottom, px, bottom+6)
		traceNo := lastStats.TraceStart + idx*step + 1
		label := strconv.FormatInt(traceNo, 10)
		drawAxisText(hdc, label, px-28, y-23, px+29, y-7, DT_CENTER)
		drawAxisText(hdc, label, px-28, bottom+7, px+29, bottom+25, DT_CENTER)
		if idx == count-1 {
			seenLast = true
		}
	}
	if !seenLast && count > 1 {
		idx := count - 1
		px := right
		drawLine(hdc, px, y, px, y-6)
		drawLine(hdc, px, bottom, px, bottom+6)
		traceNo := lastStats.TraceStart + idx*step + 1
		label := strconv.FormatInt(traceNo, 10)
		drawAxisText(hdc, label, px-34, y-23, px+18, y-7, DT_CENTER)
		drawAxisText(hdc, label, px-34, bottom+7, px+18, bottom+25, DT_CENTER)
	}

	// Time coordinates on both sides.  Fimage displays seconds on the main
	// image even though the load dialog accepts milliseconds.
	dtSec := float64(sf.Info.SampleIntervalUS) / 1e6
	t0 := float64(lastStats.SampleStart) * dtSec
	t1 := float64(lastStats.SampleEnd) * dtSec
	if t1 < t0 {
		t0, t1 = t1, t0
	}
	timeStep := niceFloatStep((t1 - t0) / 6.0)
	if timeStep <= 0 {
		timeStep = 1
	}
	first := math.Ceil((t0-1e-12)/timeStep) * timeStep
	drawTimeTick := func(tv float64) {
		py := y
		if t1 > t0 {
			py = y + int(math.Round((tv-t0)/(t1-t0)*float64(h-1)))
		}
		if py < y {
			py = y
		}
		if py > bottom {
			py = bottom
		}
		drawLine(hdc, x, py, x-6, py)
		drawLine(hdc, right, py, right+6, py)
		label := fmt.Sprintf("%.2f", tv)
		drawAxisText(hdc, label, 2, py-9, x-9, py+10, DT_RIGHT|DT_VCENTER)
		drawAxisText(hdc, label, right+9, py-9, right+legacyAxisRight-2, py+10, DT_LEFT|DT_VCENTER)
	}
	// Always show exact start, then nice intermediate ticks, then exact end.
	drawTimeTick(t0)
	lastDrawn := t0
	for tv := first; tv <= t1+timeStep*1e-8; tv += timeStep {
		if math.Abs(tv-t0) < timeStep*0.05 || math.Abs(tv-t1) < timeStep*0.05 {
			continue
		}
		drawTimeTick(tv)
		lastDrawn = tv
	}
	_ = lastDrawn
	if math.Abs(t1-t0) > 1e-12 {
		drawTimeTick(t1)
	}

	pSelectObject.Call(hdc, oldPen)
	if pen != 0 {
		pDeleteObject.Call(pen)
	}
}

func paintMain() {
	var ps PAINTSTRUCT
	hdc, _, _ := pBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	// Clear the whole seismic view first so old coordinate labels never remain
	// after selecting a different cached range or resizing the window.
	vx, vy, vw, vh := viewArea()
	white, _, _ := pGetStockObject.Call(WHITE_BRUSH)
	vr := RECT{Left: int32(vx), Top: int32(vy), Right: int32(vx + vw), Bottom: int32(vy + vh)}
	pFillRect.Call(hdc, uintptr(unsafe.Pointer(&vr)), white)
	if hFont != 0 {
		pSelectObject.Call(hdc, hFont)
	}
	pSetBkMode.Call(hdc, TRANSPARENT)
	pSetTextColor.Call(hdc, rgbRef(0, 0, 0))
	if len(bgra) > 0 && imageW > 0 && imageH > 0 {
		x, y, w, h := imageArea()
		bmi := BITMAPINFO{Header: BITMAPINFOHEADER{Size: uint32(unsafe.Sizeof(BITMAPINFOHEADER{})), Width: int32(imageW), Height: -int32(imageH), Planes: 1, BitCount: 32, Compression: BI_RGB, SizeImage: uint32(len(bgra))}}
		pSetStretchBltMode.Call(hdc, HALFTONE)
		pStretchDIBits.Call(hdc, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 0, 0, uintptr(imageW), uintptr(imageH), uintptr(unsafe.Pointer(&bgra[0])), uintptr(unsafe.Pointer(&bmi)), DIB_RGB_COLORS, SRCCOPY)
		drawAxes(hdc)
		if zoomDragging {
			x0, x1 := zoomStartX, zoomCurrentX
			y0, y1 := zoomStartY, zoomCurrentY
			if x1 < x0 {
				x0, x1 = x1, x0
			}
			if y1 < y0 {
				y0, y1 = y1, y0
			}
			pen, _, _ := pCreatePen.Call(PS_SOLID, 1, rgbRef(255, 0, 0))
			old, _, _ := pSelectObject.Call(hdc, pen)
			drawLine(hdc, x0, y0, x1, y0)
			drawLine(hdc, x1, y0, x1, y1)
			drawLine(hdc, x1, y1, x0, y1)
			drawLine(hdc, x0, y1, x0, y0)
			pSelectObject.Call(hdc, old)
			if pen != 0 {
				pDeleteObject.Call(pen)
			}
		}
	}
	pEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
}

func saveBMP(path string) error {
	if imageW == 0 || imageH == 0 || len(bgra) == 0 {
		return fmt.Errorf("no image loaded")
	}
	row := imageW * 3
	stride := (row + 3) &^ 3
	dataSize := stride * imageH
	off := 54
	total := off + dataSize
	b := make([]byte, total)
	b[0] = 'B'
	b[1] = 'M'
	binary.LittleEndian.PutUint32(b[2:6], uint32(total))
	binary.LittleEndian.PutUint32(b[10:14], uint32(off))
	binary.LittleEndian.PutUint32(b[14:18], 40)
	binary.LittleEndian.PutUint32(b[18:22], uint32(imageW))
	binary.LittleEndian.PutUint32(b[22:26], uint32(imageH))
	binary.LittleEndian.PutUint16(b[26:28], 1)
	binary.LittleEndian.PutUint16(b[28:30], 24)
	binary.LittleEndian.PutUint32(b[34:38], uint32(dataSize))
	for y := 0; y < imageH; y++ {
		srcY := imageH - 1 - y
		dst := off + y*stride
		for x := 0; x < imageW; x++ {
			j := (srcY*imageW + x) * 4
			b[dst+x*3] = bgra[j]
			b[dst+x*3+1] = bgra[j+1]
			b[dst+x*3+2] = bgra[j+2]
		}
	}
	return os.WriteFile(path, b, 0644)
}

func createMainUI() {
	// Pixel positions and ImageIndex values below are recovered from the original
	// Fimage v1.5.1 TToolBar/TImageList. The combo retains its 66-pixel width.
	btnOpen = createIconButton(hwnd, 11, ID_OPEN, 0)
	btnSave = createIconButton(hwnd, 38, ID_SAVE, 1)
	comboColor = createCtrl(hwnd, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 73, 2, 66, 260, ID_COLOR)
	for _, n := range paletteNames {
		pSendMessageW.Call(comboColor, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(n))))
	}
	pSendMessageW.Call(comboColor, CB_SETCURSEL, uintptr(paletteIndex), 0)
	btnFixed = createIconButtonStyled(hwnd, 147, ID_FIXED, 20, BS_AUTOCHECKBOX|BS_PUSHLIKE)
	pSendMessageW.Call(btnFixed, BM_SETCHECK, BST_CHECKED, 0)
	btnMark = createIconButton(hwnd, 174, ID_MARK, 5)
	btnData = createIconButton(hwnd, 201, ID_DATA, 21)
	btnParams = createIconButton(hwnd, 228, ID_PARAMS, 23)
	btnZoom = createIconButtonStyled(hwnd, 264, ID_ZOOM, 19, BS_AUTOCHECKBOX|BS_PUSHLIKE)
	btnOrigin = createIconButton(hwnd, 291, ID_ORIGIN, 18)
	btnMail = createIconButton(hwnd, 327, ID_MAIL, 13)
	btnUpdate = createIconButton(hwnd, 354, ID_UPDATE, 10)
	btnAbout = createIconButton(hwnd, 381, ID_ABOUT, 17)
	btnCompare = createCtrl(hwnd, "BUTTON", "比", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 408, 2, 27, 22, ID_COMPARE)
	btnVolume = createCtrl(hwnd, "BUTTON", "体", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 438, 2, 27, 22, ID_VOLUME)
	statusLabel = createCtrl(hwnd, "STATIC", "", WS_CHILD|WS_VISIBLE, 8, 0, 240, 16, 0)
	progressBar = createCtrl(hwnd, "msctls_progress32", "", WS_CHILD|WS_VISIBLE, 0, 0, 181, 14, 0)
	enableDataControls(false)
	layoutMain()
}
func layoutMain() {
	w, h := clientSize(hwnd)
	if statusLabel != 0 {
		pMoveWindow.Call(statusLabel, 4, uintptr(maxInt(h-18, 0)), uintptr(maxInt(w-195, 10)), 18, 1)
	}
	if progressBar != 0 {
		pMoveWindow.Call(progressBar, uintptr(maxInt(w-185, 0)), uintptr(maxInt(h-16, 0)), 181, 14, 1)
	}
}

func showParamDialog() {
	if sf == nil {
		message(hwnd, "参数", "请先打开SEG-Y数据。", MB_OK|MB_ICONINFORMATION)
		return
	}
	if paramHwnd != 0 {
		pSetForeground.Call(paramHwnd)
		return
	}
	paramOwner = hwnd
	if compareHwnd != 0 {
		paramOwner = compareHwnd
	}
	pEnableWindow.Call(paramOwner, 0)
	h, _, _ := pCreateWindowExW.Call(1, uintptr(unsafe.Pointer(u16("Limage64Param"))), uintptr(unsafe.Pointer(u16("参数"))), WS_POPUP|WS_CAPTION|WS_SYSMENU,
		uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 390, 390, paramOwner, 0, 0, 0)
	if h == 0 {
		pEnableWindow.Call(paramOwner, 1)
		return
	}
	paramHwnd = h
	createParamUI()
	pShowWindow.Call(h, SW_SHOW)
	pUpdateWindow.Call(h)
}
func addPageCtrl(page int, h uintptr) { pc.pages[page] = append(pc.pages[page], h) }
func createParamUI() {
	pc = paramControls{}
	pc.tabs = createCtrl(paramHwnd, "SysTabControl32", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP, 5, 9, 361, 280, IDP_TABS)
	for i, n := range []string{"坐标设置", "其它设置", "波形显示"} {
		ti := TCITEM{Mask: TCIF_TEXT, PszText: u16(n)}
		pSendMessageW.Call(pc.tabs, TCM_INSERTITEMW, uintptr(i), uintptr(unsafe.Pointer(&ti)))
	}

	// Tab 1: coordinate / gain settings (layout follows recovered DFM proportions).
	g := createCtrl(paramHwnd, "BUTTON", "横纵坐标：", WS_CHILD|WS_VISIBLE|BS_GROUPBOX, 13, 52, 337, 137, 0)
	addPageCtrl(0, g)
	labels := []struct {
		t    string
		x, y int
	}{{"X起始坐标：", 25, 78}, {"X终止坐标：", 25, 104}, {"Y起始坐标：", 191, 78}, {"Y终止坐标：", 191, 104}, {"横坐标间隔：", 25, 130}, {"纵坐标间隔：", 191, 130}}
	for _, q := range labels {
		h := createCtrl(paramHwnd, "STATIC", q.t, WS_CHILD|WS_VISIBLE, q.x, q.y, 72, 18, 0)
		addPageCtrl(0, h)
		e := createCtrl(paramHwnd, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_BORDER|ES_AUTOHSCROLL, q.x+74, q.y-2, 65, 20, 0)
		addPageCtrl(0, e)
	}
	g2 := createCtrl(paramHwnd, "BUTTON", "", WS_CHILD|WS_VISIBLE|BS_GROUPBOX, 13, 199, 337, 54, 0)
	addPageCtrl(0, g2)
	rt := createCtrl(paramHwnd, "BUTTON", "时间域", WS_CHILD|WS_VISIBLE|BS_AUTORADIOBUTTON|WS_GROUP, 29, 211, 80, 18, 0)
	addPageCtrl(0, rt)
	pSendMessageW.Call(rt, BM_SETCHECK, BST_CHECKED, 0)
	rd := createCtrl(paramHwnd, "BUTTON", "深度域", WS_CHILD|WS_VISIBLE|BS_AUTORADIOBUTTON, 29, 232, 80, 18, 0)
	addPageCtrl(0, rd)
	lg := createCtrl(paramHwnd, "STATIC", "数据增益（%）：", WS_CHILD|WS_VISIBLE, 171, 213, 90, 18, 0)
	addPageCtrl(0, lg)
	pc.gain = createCtrl(paramHwnd, "EDIT", fmt.Sprintf("%.4g", gainPercent), WS_CHILD|WS_VISIBLE|WS_BORDER|ES_AUTOHSCROLL, 261, 210, 65, 20, IDP_GAIN)
	addPageCtrl(0, pc.gain)
	note := createCtrl(paramHwnd, "STATIC", "（增益数值增减对应原版快捷键 Q / W）", WS_CHILD|WS_VISIBLE, 150, 235, 185, 18, 0)
	addPageCtrl(0, note)

	// Tab 2: exact recovered "其它设置" and functional amplitude range limiting.
	boxFix := createCtrl(paramHwnd, "BUTTON", "", WS_CHILD|BS_GROUPBOX, 13, 49, 337, 59, 0)
	addPageCtrl(1, boxFix)
	pc.fix = createCtrl(paramHwnd, "BUTTON", "固定图像尺寸大小", WS_CHILD|BS_AUTOCHECKBOX, 29, 61, 145, 18, IDP_FIX)
	addPageCtrl(1, pc.fix)
	lh := createCtrl(paramHwnd, "STATIC", "图像高度：", WS_CHILD, 188, 61, 72, 18, 0)
	addPageCtrl(1, lh)
	pc.fixH = createCtrl(paramHwnd, "EDIT", "", WS_CHILD|WS_BORDER|WS_DISABLED, 261, 58, 65, 20, IDP_FIXH)
	addPageCtrl(1, pc.fixH)
	lw := createCtrl(paramHwnd, "STATIC", "图像宽度：", WS_CHILD, 188, 86, 72, 18, 0)
	addPageCtrl(1, lw)
	pc.fixW = createCtrl(paramHwnd, "EDIT", "", WS_CHILD|WS_BORDER|WS_DISABLED, 261, 83, 65, 20, IDP_FIXW)
	addPageCtrl(1, pc.fixW)
	boxRange := createCtrl(paramHwnd, "BUTTON", "数值限定：", WS_CHILD|BS_GROUPBOX, 13, 116, 337, 161, 0)
	addPageCtrl(1, boxRange)
	pc.limit = createCtrl(paramHwnd, "BUTTON", "限定数据范围", WS_CHILD|BS_AUTOCHECKBOX, 29, 137, 120, 18, IDP_LIMIT)
	addPageCtrl(1, pc.limit)
	if useLimits {
		pSendMessageW.Call(pc.limit, BM_SETCHECK, BST_CHECKED, 0)
	}
	for _, q := range []struct {
		t       string
		x, y, w int
	}{{"最小值", 187, 183, 45}, {"最大值", 275, 183, 45}, {"原始范围：", 108, 205, 70}, {"限制范围：", 108, 237, 70}} {
		h := createCtrl(paramHwnd, "STATIC", q.t, WS_CHILD, q.x, q.y, q.w, 18, 0)
		addPageCtrl(1, h)
	}
	pc.minOriginal = createCtrl(paramHwnd, "EDIT", "", WS_CHILD|WS_BORDER|ES_READONLY, 173, 201, 65, 20, 0)
	addPageCtrl(1, pc.minOriginal)
	pc.maxOriginal = createCtrl(paramHwnd, "EDIT", "", WS_CHILD|WS_BORDER|ES_READONLY, 261, 201, 65, 20, 0)
	addPageCtrl(1, pc.maxOriginal)
	st := uint32(WS_CHILD | WS_BORDER | ES_AUTOHSCROLL)
	if !useLimits {
		st |= WS_DISABLED
	}
	pc.minEdit = createCtrl(paramHwnd, "EDIT", fmt.Sprintf("%.8g", limitMin), st, 173, 233, 65, 20, IDP_MIN)
	addPageCtrl(1, pc.minEdit)
	pc.maxEdit = createCtrl(paramHwnd, "EDIT", fmt.Sprintf("%.8g", limitMax), st, 261, 233, 65, 20, IDP_MAX)
	addPageCtrl(1, pc.maxEdit)
	updateParamRangeFields()

	// Tab 3: display mode + recovered waveform options. Keep every control
	// owned by page 2 so inactive tabs never leak text into the coordinate page.
	dispBox := createCtrl(paramHwnd, "BUTTON", "放大显示方式：", WS_CHILD|BS_GROUPBOX, 13, 47, 337, 78, 0)
	addPageCtrl(2, dispBox)
	pc.displayMode = createCtrl(paramHwnd, "COMBOBOX", "", WS_CHILD|WS_TABSTOP|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 28, 68, 120, 180, IDP_DISPMODE)
	for _, n := range displayModeNames {
		pSendMessageW.Call(pc.displayMode, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(n))))
	}
	pSendMessageW.Call(pc.displayMode, CB_SETCURSEL, uintptr(renderDisplayMode), 0)
	addPageCtrl(2, pc.displayMode)
	help := createCtrl(paramHwnd, "STATIC", "自适应：仅在放大时自动平滑（推荐）", WS_CHILD, 164, 70, 170, 35, 0)
	addPageCtrl(2, help)

	tbox := createCtrl(paramHwnd, "BUTTON", "类型设定：", WS_CHILD|BS_GROUPBOX, 13, 132, 337, 57, 0)
	addPageCtrl(2, tbox)
	for i, q := range []struct {
		t    string
		x, y int
	}{{"波形图", 31, 151}, {"变面积图", 31, 169}, {"波形变面积", 118, 151}, {"彩色图", 118, 169}} {
		h := createCtrl(paramHwnd, "BUTTON", q.t, WS_CHILD|BS_AUTORADIOBUTTON, q.x, q.y, 90, 18, 0)
		addPageCtrl(2, h)
		if i == 3 {
			pSendMessageW.Call(h, BM_SETCHECK, BST_CHECKED, 0)
		}
	}

	vbox := createCtrl(paramHwnd, "BUTTON", "范围设定：", WS_CHILD|BS_GROUPBOX, 13, 195, 337, 82, 0)
	addPageCtrl(2, vbox)
	for i, t := range []string{"道距：", "时间：", "振幅："} {
		y := 213 + i*20
		l := createCtrl(paramHwnd, "STATIC", t, WS_CHILD, 35, y+2, 48, 18, 0)
		addPageCtrl(2, l)
		e := createCtrl(paramHwnd, "EDIT", "0", WS_CHILD|WS_BORDER|ES_AUTOHSCROLL, 230, y, 57, 18, 0)
		addPageCtrl(2, e)
	}

	ok := createCtrl(paramHwnd, "BUTTON", "确定", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_DEFPUSHBUTTON, 48, 304, 65, 25, IDP_OK)
	apply := createCtrl(paramHwnd, "BUTTON", "应用", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 152, 304, 65, 25, IDP_APPLY)
	cancel := createCtrl(paramHwnd, "BUTTON", "取消", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 256, 304, 65, 25, IDP_CANCEL)
	_ = ok
	_ = apply
	_ = cancel
	setParamPage(0)
}
func setParamPage(page int) {
	for i := 0; i < 3; i++ {
		showControls(pc.pages[i], i == page)
	}
}
func updateParamRangeFields() {
	if paramHwnd == 0 {
		return
	}
	if pc.minOriginal != 0 {
		setText(pc.minOriginal, fmt.Sprintf("%.8g", lastStats.ObservedMin))
		setText(pc.maxOriginal, fmt.Sprintf("%.8g", lastStats.ObservedMax))
	}
	if pc.minEdit != 0 && !useLimits {
		setText(pc.minEdit, fmt.Sprintf("%.8g", lastStats.MapMin))
		setText(pc.maxEdit, fmt.Sprintf("%.8g", lastStats.MapMax))
	}
}
func applyParamValues() bool {
	if pc.gain != 0 {
		if v, err := strconv.ParseFloat(getText(pc.gain), 64); err == nil {
			if v < 0 || v > 49 {
				message(paramHwnd, "参数", "数据增益应在 0–49% 之间。原版 Q/W 每次调整 1%。", MB_OK|MB_ICONERROR)
				return false
			}
			gainPercent = v
		} else {
			message(paramHwnd, "参数", "数据增益必须为数值。", MB_OK|MB_ICONERROR)
			return false
		}
	}
	if pc.displayMode != 0 {
		r, _, _ := pSendMessageW.Call(pc.displayMode, CB_GETCURSEL, 0, 0)
		if int(r) >= 0 && int(r) < len(displayModeNames) {
			renderDisplayMode = segy.DisplayMode(r)
		}
	}
	useLimits = pc.limit != 0 && isChecked(pc.limit)
	if useLimits {
		lo, e1 := strconv.ParseFloat(getText(pc.minEdit), 64)
		hi, e2 := strconv.ParseFloat(getText(pc.maxEdit), 64)
		if e1 != nil || e2 != nil || hi <= lo {
			message(paramHwnd, "参数", "限制范围必须满足：最大值 > 最小值。", MB_OK|MB_ICONERROR)
			return false
		}
		limitMin, limitMax = lo, hi
	}
	rerender()
	if compareHwnd != 0 {
		refreshCompareGainOrLimits()
	}
	if volumeHwnd != 0 {
		refreshVolumeGainOrLimits()
	}
	return true
}
func isChecked(h uintptr) bool {
	r, _, _ := pSendMessageW.Call(h, BM_GETCHECK, 0, 0)
	return r == BST_CHECKED
}

func showMarkWindow() {
	if sf == nil {
		return
	}
	if markHwnd != 0 {
		pSetForeground.Call(markHwnd)
		return
	}
	h, _, _ := pCreateWindowExW.Call(1, uintptr(unsafe.Pointer(u16("Limage64Mark"))), uintptr(unsafe.Pointer(u16("色标"))), WS_POPUP|WS_CAPTION|WS_SYSMENU,
		uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 132, 420, hwnd, 0, 0, 0)
	if h == 0 {
		return
	}
	markHwnd = h
	createCtrl(h, "BUTTON", "缺省颜色设置", WS_CHILD|WS_VISIBLE|BS_PUSHBUTTON, 32, 350, 85, 26, IDM_RESETCOL)
	pShowWindow.Call(h, SW_SHOW)
	pUpdateWindow.Call(h)
}
func paintMark(h uintptr) {
	var ps PAINTSTRUCT
	hdc, _, _ := pBeginPaint.Call(h, uintptr(unsafe.Pointer(&ps)))
	pal := currentPalette()
	w, hg := 25, 313
	px := make([]byte, w*hg*4)
	for y := 0; y < hg; y++ {
		idx := int(math.Round(float64(y) * 255 / float64(hg-1)))
		c := pal[idx]
		for x := 0; x < w; x++ {
			j := (y*w + x) * 4
			px[j] = c.b
			px[j+1] = c.g
			px[j+2] = c.r
		}
	}
	bmi := BITMAPINFO{Header: BITMAPINFOHEADER{Size: uint32(unsafe.Sizeof(BITMAPINFOHEADER{})), Width: int32(w), Height: -int32(hg), Planes: 1, BitCount: 32, Compression: BI_RGB, SizeImage: uint32(len(px))}}
	pStretchDIBits.Call(hdc, 24, 16, uintptr(w), uintptr(hg), 0, 0, uintptr(w), uintptr(hg), uintptr(unsafe.Pointer(&px[0])), uintptr(unsafe.Pointer(&bmi)), DIB_RGB_COLORS, SRCCOPY)
	draw := func(y int, s string) {
		r := RECT{Left: 54, Top: int32(y), Right: 124, Bottom: int32(y + 18)}
		pDrawTextW.Call(hdc, uintptr(unsafe.Pointer(u16(s))), uintptr(^uint32(0)), uintptr(unsafe.Pointer(&r)), DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	}
	draw(10, fmt.Sprintf("%.5g", lastStats.MapMax))
	draw(162, "0")
	draw(311, fmt.Sprintf("%.5g", lastStats.MapMin))
	pEndPaint.Call(h, uintptr(unsafe.Pointer(&ps)))
}

func cyclePalette(delta int) {
	if len(paletteNames) == 0 {
		return
	}
	paletteIndex = (paletteIndex + delta) % len(paletteNames)
	if paletteIndex < 0 {
		paletteIndex += len(paletteNames)
	}
	if comboColor != 0 {
		pSendMessageW.Call(comboColor, CB_SETCURSEL, uintptr(paletteIndex), 0)
	}
	applyPalette()
	if sf != nil {
		setStatusBase(fmt.Sprintf(" Range %.6g .. %.6g   Display %.6g .. %.6g   Gain %.0f%%   Palette: %s", lastStats.ObservedMin, lastStats.ObservedMax, lastStats.MapMin, lastStats.MapMax, gainPercent, paletteNames[paletteIndex]))
	}
}

func changeGainPercent(delta float64) {
	if sf == nil {
		return
	}
	// Exact shortcut direction and step recovered from FormKeyDown at
	// 0x4079D0: Q adds 1.0, W subtracts 1.0. The legacy display
	// algorithm treats this value as symmetric percentile clipping.
	v := math.Round(gainPercent) + delta
	if v < 0 {
		v = 0
	}
	if v > 49 {
		v = 49
	}
	if v == gainPercent {
		return
	}
	gainPercent = v
	if pc.gain != 0 {
		setText(pc.gain, formatNumber(gainPercent))
	}
	if lc.gain != 0 {
		setText(lc.gain, formatNumber(gainPercent))
	}
	rerender()
	if compareHwnd != 0 {
		refreshCompareGainOrLimits()
	}
	if volumeHwnd != 0 {
		refreshVolumeGainOrLimits()
		updateVolumeStatusLine(fmt.Sprintf("Gain %.0f%%", gainPercent))
	}
}

func resetDisplay() {
	// “还原” now restores the range accepted in 数据加载 rather than the
	// entire SEG-Y. This preserves the range-first I/O design for huge files.
	restoreOriginRange()
}

func wndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_COMMAND:
		id := int(wParam & 0xffff)
		switch id {
		case ID_OPEN:
			showLoadDialog("")
		case ID_SAVE:
			if p := saveDialog(hwnd); p != "" {
				if err := saveBMP(p); err != nil {
					message(hwnd, "Save error", err.Error(), MB_OK|MB_ICONERROR)
				}
			}
		case ID_COLOR:
			if (wParam>>16)&0xffff == 1 {
				r, _, _ := pSendMessageW.Call(comboColor, CB_GETCURSEL, 0, 0)
				if int(r) >= 0 && int(r) < len(paletteNames) {
					paletteIndex = int(r)
					applyPalette()
					setStatusBase(fmt.Sprintf(" Range %.6g .. %.6g   Display %.6g .. %.6g   Gain %.0f%%   Palette: %s", lastStats.ObservedMin, lastStats.ObservedMax, lastStats.MapMin, lastStats.MapMax, gainPercent, paletteNames[paletteIndex]))
				}
			}
		case ID_MARK:
			showMarkWindow()
		case ID_DATA:
			if sf != nil {
				message(hwnd, "数据信息", sf.Info.String()+fmt.Sprintf("\n\nCurrent display traces: %d - %d (step %d)\nSamples: %d - %d\nObserved range: %.8g .. %.8g\nDisplay gain: %.0f%%", lastStats.TraceStart+1, lastStats.TraceEnd+1, lastStats.TraceStep, lastStats.SampleStart, lastStats.SampleEnd, lastStats.ObservedMin, lastStats.ObservedMax, gainPercent), MB_OK|MB_ICONINFORMATION)
			}
		case ID_PARAMS:
			showParamDialog()
		case ID_ORIGIN:
			resetDisplay()
		case ID_FIXED:
			// Checked = legacy fixed-image behavior (stretch cached bitmap only).
			// Unchecked = re-render once after a resize finishes.
			if statusBase != "" {
				if isChecked(btnFixed) {
					setText(statusLabel, statusBase+"   [Fixed]")
				} else {
					setText(statusLabel, statusBase+"   [Fit on resize]")
				}
			}
			pInvalidateRect.Call(hwnd, 0, 1)
		case ID_ZOOM:
			setZoomMode(isChecked(btnZoom))
			if statusBase != "" {
				if zoomMode {
					setText(statusLabel, statusBase+"   [Zoom: drag a rectangle]")
				} else {
					setText(statusLabel, statusBase)
				}
			}
		case ID_MAIL:
			openFeedbackEmail(hwnd)
		case ID_UPDATE:
			message(hwnd, "检查更新", APP_NAME+" 当前为本地 x64 版本。\n\n项目主页："+APP_PROJECT_URL, MB_OK|MB_ICONINFORMATION)
		case ID_COMPARE:
			showCompareWindow()
		case ID_VOLUME:
			showVolumeWindow()
		case ID_ABOUT:
			message(hwnd, "About "+APP_NAME, APP_NAME+"  v"+APP_VERSION+"\nSeismic Visualization, Reconstruction & Enhancement\n震铸地震数据处理平台\n\nNative Windows x64 SEG-Y visualization, QC, reconstruction and enhancement workspace.\n\nProject: "+APP_PROJECT_URL+"\n\n"+licenseSummary(), MB_OK|MB_ICONINFORMATION)
		}
		return 0
	case WM_SETCURSOR:
		if zoomMode && crossCursor != 0 {
			pSetCursor.Call(crossCursor)
			return 1
		}
	case WM_LBUTTONDOWN:
		if zoomMode && sf != nil {
			mx, my := mousePoint(lParam)
			if pointInImage(mx, my) {
				x, y, w, hh := imageArea()
				zoomStartX = clampInt(mx, x, x+w-1)
				zoomCurrentX = zoomStartX
				zoomStartY = clampInt(my, y, y+hh-1)
				zoomCurrentY = zoomStartY
				zoomDragging = true
				pSetCapture.Call(hwnd)
				invalidateMainImage(false)
				return 0
			}
		}
	case WM_MOUSEMOVE:
		mx, my := mousePoint(lParam)
		if zoomDragging {
			x, y, w, hh := imageArea()
			zoomCurrentX = clampInt(mx, x, x+w-1)
			zoomCurrentY = clampInt(my, y, y+hh-1)
			invalidateMainImage(false)
		}
		updateMouseStatus(mx, my)
		return 0
	case WM_LBUTTONUP:
		if zoomDragging {
			mx, my := mousePoint(lParam)
			x, y, w, hh := imageArea()
			zoomCurrentX = clampInt(mx, x, x+w-1)
			zoomCurrentY = clampInt(my, y, y+hh-1)
			zoomDragging = false
			pReleaseCapture.Call()
			x0, y0, x1, y1 := zoomStartX, zoomStartY, zoomCurrentX, zoomCurrentY
			invalidateMainImage(false)
			applyZoomRect(x0, y0, x1, y1)
			return 0
		}
	case WM_EXITSIZEMOVE:
		if sf != nil && btnFixed != 0 && !isChecked(btnFixed) {
			rerender()
		}
		return 0
	case WM_GETMINMAXINFO:
		if lParam != 0 {
			mmi := (*MINMAXINFO)(unsafe.Pointer(lParam))
			// Keep enough width for the recovered 406-pixel legacy toolbar.
			mmi.PtMinTrackSize.X = 500
			mmi.PtMinTrackSize.Y = 320
		}
		return 0
	case WM_SIZE:
		layoutMain()
		pInvalidateRect.Call(hwnd, 0, 1)
		return 0
	case WM_PAINT:
		paintMain()
		return 0
	case WM_DESTROY:
		if sf != nil {
			sf.Close()
		}
		for _, bm := range legacyBitmaps {
			if bm != 0 {
				pDeleteObject.Call(bm)
			}
		}
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return r
}

func paramWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_COMMAND:
		id := int(wParam & 0xffff)
		switch id {
		case IDP_LIMIT:
			on := isChecked(pc.limit)
			v := uintptr(0)
			if on {
				v = 1
			}
			pEnableWindow.Call(pc.minEdit, v)
			pEnableWindow.Call(pc.maxEdit, v)
			if on && !useLimits {
				setText(pc.minEdit, fmt.Sprintf("%.8g", lastStats.MapMin))
				setText(pc.maxEdit, fmt.Sprintf("%.8g", lastStats.MapMax))
			}
		case IDP_FIX:
			on := isChecked(pc.fix)
			v := uintptr(0)
			if on {
				v = 1
			}
			pEnableWindow.Call(pc.fixW, v)
			pEnableWindow.Call(pc.fixH, v)
		case IDP_APPLY:
			applyParamValues()
		case IDP_OK:
			if applyParamValues() {
				pDestroyWindow.Call(h)
			}
		case IDP_CANCEL:
			pDestroyWindow.Call(h)
		case IDP_WIGGLE:
			if isChecked(pc.wiggle) {
				message(h, "显示方式", "请在“波形显示”页选择：快速像素 / 平滑插值 / 自适应。", MB_OK|MB_ICONINFORMATION)
			}
		}
		return 0
	case WM_NOTIFY:
		n := (*NMHDR)(unsafe.Pointer(lParam))
		if n.Code == TCN_SELCHANGE {
			r, _, _ := pSendMessageW.Call(pc.tabs, TCM_GETCURSEL, 0, 0)
			if int(r) >= 0 && int(r) < 3 {
				setParamPage(int(r))
			}
			return 0
		}
	case WM_CLOSE:
		pDestroyWindow.Call(h)
		return 0
	case WM_DESTROY:
		paramHwnd = 0
		pc = paramControls{}
		if paramOwner != 0 {
			pEnableWindow.Call(paramOwner, 1)
			pSetForeground.Call(paramOwner)
		}
		paramOwner = 0
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return r
}
func markWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_COMMAND:
		if int(wParam&0xffff) == IDM_RESETCOL {
			paletteIndex = 2
			comparePaletteIndex = 2
			pSendMessageW.Call(comboColor, CB_SETCURSEL, 2, 0)
			if cc.palette != 0 {
				pSendMessageW.Call(cc.palette, CB_SETCURSEL, 2, 0)
			}
			applyPalette()
			return 0
		}
	case WM_PAINT:
		paintMark(h)
		return 0
	case WM_CLOSE:
		pDestroyWindow.Call(h)
		return 0
	case WM_DESTROY:
		markHwnd = 0
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return r
}

//go:embed limage.ico
var limageICO []byte

func loadEmbeddedLimageIcon(requested int) uintptr {
	if len(limageICO) < 6 || limageICO[0] != 0 || limageICO[1] != 0 || limageICO[2] != 1 || limageICO[3] != 0 {
		return 0
	}
	count := int(binary.LittleEndian.Uint16(limageICO[4:6]))
	best := -1
	bestDelta := int(^uint(0) >> 1)
	for i := 0; i < count; i++ {
		off := 6 + i*16
		if off+16 > len(limageICO) {
			break
		}
		w := int(limageICO[off])
		if w == 0 {
			w = 256
		}
		h := int(limageICO[off+1])
		if h == 0 {
			h = 256
		}
		delta := absInt(w-requested) + absInt(h-requested)
		if delta < bestDelta {
			bestDelta = delta
			best = off
		}
	}
	if best < 0 {
		return 0
	}
	sz := int(binary.LittleEndian.Uint32(limageICO[best+8 : best+12]))
	dataOff := int(binary.LittleEndian.Uint32(limageICO[best+12 : best+16]))
	if sz <= 0 || dataOff < 0 || dataOff+sz > len(limageICO) {
		return 0
	}
	data := limageICO[dataOff : dataOff+sz]
	if len(data) == 0 {
		return 0
	}
	r, _, _ := pCreateIconFromResourceEx.Call(
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), 1, 0x00030000,
		uintptr(requested), uintptr(requested), 0)
	return r
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// showTopLevelWindowRestored preserves a normal/maximized placement but never
// leaves a workspace hidden behind a retained minimized state.
func showTopLevelWindowRestored(h uintptr) {
	if h == 0 {
		return
	}
	iconic, _, _ := pIsIconic.Call(h)
	if iconic != 0 {
		pShowWindow.Call(h, SW_RESTORE)
	} else {
		pShowWindow.Call(h, SW_SHOW)
	}
	pUpdateWindow.Call(h)
	pSetForeground.Call(h)
	pSetFocus.Call(h)
}

func registerClass(name string, proc uintptr, bg uintptr, hinst, cursor uintptr) {
	cls := u16(name)
	wc := WNDCLASSEX{CbSize: uint32(unsafe.Sizeof(WNDCLASSEX{})), Style: CS_DBLCLKS, WndProc: proc, HInstance: hinst, HIcon: limageIconLarge, HCursor: cursor, HbrBackground: bg, ClassName: cls, HIconSm: limageIconSmall}
	r, _, e := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if r == 0 {
		panic(e)
	}
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func main() {
	runtime.LockOSThread()
	icc := INITCOMMONCONTROLSEX{DwSize: uint32(unsafe.Sizeof(INITCOMMONCONTROLSEX{})), DwICC: ICC_WIN95_CLASSES}
	pInitCommonCtrls.Call(uintptr(unsafe.Pointer(&icc)))
	hinst, _, _ := pGetModuleHandleW.Call(0)
	cursor, _, _ := pLoadCursorW.Call(0, IDC_ARROW)
	crossCursor, _, _ = pLoadCursorW.Call(0, IDC_CROSS)
	hFont, _, _ = pGetStockObject.Call(DEFAULT_GUI_FONT)
	limageIconLarge = loadEmbeddedLimageIcon(32)
	limageIconSmall = loadEmbeddedLimageIcon(16)
	registerClass("Limage64Reconstructed", syscall.NewCallback(wndProc), COLOR_WINDOW+1, hinst, cursor)
	registerClass("Limage64Param", syscall.NewCallback(paramWndProc), COLOR_BTNFACE+1, hinst, cursor)
	registerClass("Limage64Mark", syscall.NewCallback(markWndProc), COLOR_BTNFACE+1, hinst, cursor)
	registerClass("Limage64Load", syscall.NewCallback(loadWndProc), COLOR_BTNFACE+1, hinst, cursor)
	registerClass("Limage64Compare", syscall.NewCallback(compareWndProc), COLOR_BTNFACE+1, hinst, cursor)
	registerClass("Limage64Volume", syscall.NewCallback(volumeWndProc), COLOR_BTNFACE+1, hinst, cursor)
	registerClass("Limage64Crooked", syscall.NewCallback(crookedWndProc), COLOR_WINDOW+1, hinst, cursor)
	registerClass("Limage64Pseudo3D", syscall.NewCallback(pseudoWndProc), COLOR_WINDOW+1, hinst, cursor)
	registerClass("Limage64PseudoExport", syscall.NewCallback(pseudoExportWndProc), COLOR_BTNFACE+1, hinst, cursor)
	registerClass("Limage64CrookedHeaders", syscall.NewCallback(crookedHeaderWndProc), COLOR_BTNFACE+1, hinst, cursor)
	registerClass("Limage64VolumeExport", syscall.NewCallback(volumeExportWndProc), COLOR_BTNFACE+1, hinst, cursor)
	registerClass("Limage64Spectrum", syscall.NewCallback(spectrumWndProc), COLOR_WINDOW+1, hinst, cursor)
	registerClass("Limage64TraceAnalysis", syscall.NewCallback(traceAnalysisWndProc), COLOR_WINDOW+1, hinst, cursor)
	registerClass("Limage64License", syscall.NewCallback(licenseWndProc), COLOR_BTNFACE+1, hinst, cursor)
	if !ensureActivated(hinst) {
		return
	}
	title := u16(APP_NAME + "  v" + APP_VERSION + "  [x64]")
	h, _, e := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(u16("Limage64Reconstructed"))), uintptr(unsafe.Pointer(title)), WS_OVERLAPPEDWINDOW, uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 500, 573, 0, 0, hinst, 0)
	if h == 0 {
		panic(e)
	}
	hwnd = h
	// v1.2: keep the reconstructed legacy window hidden as a compatibility
	// controller for dialogs/toolbar assets. The A/比/差 workspace is now the
	// single visible application entry point.
	createMainUI()
	showCompareWindow()
	if err := initializePhase1Application(); err != nil {
		message(compareHwnd, APP_NAME, err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	if len(os.Args) > 1 {
		if stat, err := os.Stat(os.Args[1]); err == nil && stat.IsDir() {
			openCrookedProjectFolder(os.Args[1])
		} else {
			openHomeFile(os.Args[1], 0)
		}
	}
	var m MSG
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		// These windows are reconstructed as ordinary top-level Win32 windows,
		// not dialog resources. Intercept Enter/Esc here so the behavior matches
		// the original VCL dialogs even while an EDIT control has keyboard focus.
		if m.Message == WM_KEYDOWN {
			// Numeric fields in the 3-D export dialog must accept the physical
			// numeric keypad reliably.  In particular, NumPad-1 becomes VK_END
			// when NumLock is off, which previously looked like a "dead" key in
			// these EDIT controls.  Normalize both NumLock states before any 3-D
			// application shortcut routing can see the message.
			if volumeExportHwnd != 0 && handleVolumeExportNumpadKey(m.Hwnd, m.WParam, m.LParam) {
				continue
			}
			if volumeExportHwnd != 0 {
				child, _, _ := pIsChild.Call(volumeExportHwnd, m.Hwnd)
				if m.Hwnd == volumeExportHwnd || child != 0 {
					if m.WParam == VK_RETURN {
						pSendMessageW.Call(volumeExportHwnd, WM_COMMAND, IDVX_OK, 0)
						continue
					}
					if m.WParam == VK_ESCAPE {
						pSendMessageW.Call(volumeExportHwnd, WM_COMMAND, IDVX_CANCEL, 0)
						continue
					}
				}
			}
			// Compare-window shortcuts are a separate top-level window, so they
			// must be intercepted explicitly rather than via the main-window tree.
			if compareHwnd != 0 {
				child, _, _ := pIsChild.Call(compareHwnd, m.Hwnd)
				if m.Hwnd == compareHwnd || child != 0 {
					switch m.WParam {
					case VK_Q:
						changeGainPercent(1)
						continue
					case VK_W:
						changeGainPercent(-1)
						continue
					case VK_E:
						cycleComparePalette(1)
						continue
					case VK_R:
						cycleComparePalette(-1)
						continue
					}
				}
			}
			if pseudoHwnd != 0 {
				// Keep pseudo-3D shortcuts active while its ListView, buttons or
				// combo boxes own focus, matching the regular 3-D controller.
				child, _, _ := pIsChild.Call(pseudoHwnd, m.Hwnd)
				rootOwner, _, _ := pGetAncestor.Call(m.Hwnd, GA_ROOTOWNER)
				fg, _, _ := pGetForegroundWindow.Call()
				if m.Hwnd == pseudoHwnd || child != 0 || rootOwner == pseudoHwnd || fg == pseudoHwnd {
					if handlePseudoShortcut(m.WParam) {
						continue
					}
				}
			}
			if prestackHwnd != 0 {
				// Route the Prestack shortcuts from the message loop as well.  The
				// gather key, mapping fields and layer buttons are child controls;
				// handling them here keeps Q/W/E/R active regardless of focus.
				child, _, _ := pIsChild.Call(prestackHwnd, m.Hwnd)
				rootOwner, _, _ := pGetAncestor.Call(m.Hwnd, GA_ROOTOWNER)
				fg, _, _ := pGetForegroundWindow.Call()
				prestackKeys := m.Hwnd == prestackHwnd || child != 0 || rootOwner == prestackHwnd || fg == prestackHwnd
				// ComboLBox is an owned top-level popup, so the owner/foreground
				// checks above classify it as Prestack.  Leave its keys alone while
				// any native combo reports an open drop list.
				if prestackKeys && !prestackComboDropdownOpen() && handlePrestackShortcut(m.WParam) {
					continue
				}
			}
			if volumeHwnd != 0 {
				// Route 3-D shortcuts at the message-loop level so they keep working
				// after a combo box, view button or popup list has taken keyboard focus.
				child, _, _ := pIsChild.Call(volumeHwnd, m.Hwnd)
				rootOwner, _, _ := pGetAncestor.Call(m.Hwnd, GA_ROOTOWNER)
				fg, _, _ := pGetForegroundWindow.Call()
				volumeKeys := m.Hwnd == volumeHwnd || child != 0 || rootOwner == volumeHwnd || fg == volumeHwnd
				if volumeKeys {
					if handleVolumeShortcut(m.WParam) {
						continue
					}
					switch m.WParam {
					case VK_Q:
						changeGainPercent(1)
						continue
					case VK_W:
						changeGainPercent(-1)
						continue
					case VK_E:
						if volumeShiftDown() {
							cycleVolumePalette(-1)
						} else {
							cycleVolumePalette(1)
						}
						continue
					}
				}
			}
			// Main-view shortcuts recovered from the original FormKeyDown routine.
			// Q/W = gain +1/-1%; E/R = next/previous color map. Do not
			// steal these keys while a modal reconstructed dialog is active.
			if loadHwnd == 0 && paramHwnd == 0 && markHwnd == 0 && sf != nil {
				child, _, _ := pIsChild.Call(hwnd, m.Hwnd)
				if m.Hwnd == hwnd || child != 0 {
					switch m.WParam {
					case VK_Q:
						changeGainPercent(1)
						continue
					case VK_W:
						changeGainPercent(-1)
						continue
					case VK_E:
						cyclePalette(1)
						continue
					case VK_R:
						cyclePalette(-1)
						continue
					}
				}
			}
			if loadHwnd != 0 {
				child, _, _ := pIsChild.Call(loadHwnd, m.Hwnd)
				if m.Hwnd == loadHwnd || child != 0 {
					if m.WParam == VK_RETURN {
						pSendMessageW.Call(loadHwnd, WM_COMMAND, IDL_OK, 0)
						continue
					}
					if m.WParam == VK_ESCAPE {
						pSendMessageW.Call(loadHwnd, WM_COMMAND, IDL_CANCEL, 0)
						continue
					}
				}
			}
			if paramHwnd != 0 {
				child, _, _ := pIsChild.Call(paramHwnd, m.Hwnd)
				if m.Hwnd == paramHwnd || child != 0 {
					if m.WParam == VK_RETURN {
						pSendMessageW.Call(paramHwnd, WM_COMMAND, IDP_OK, 0)
						continue
					}
					if m.WParam == VK_ESCAPE {
						pSendMessageW.Call(paramHwnd, WM_COMMAND, IDP_CANCEL, 0)
						continue
					}
				}
			}
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}
