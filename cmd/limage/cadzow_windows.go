//go:build windows

package main

// Legacy callback names remain for the existing Compare message routes.
// External fields are descriptor-driven; the trusted host filter additionally
// exposes conditional fields and its actual response preview.
import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/algorithms/registry"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
	api "github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi"
)

const (
	IDCZ_ALGORITHM       = 5618
	IDCZ_RESULTS         = 5620
	IDCZ_EXPORT_RESULTS  = 5621
	IDCZ_PREVIEW         = 5622
	IDCZ_START           = 5611
	IDCZ_CANCEL          = 5612
	IDCZ_STATUS          = 5614
	IDCZ_AUTODETECT      = 5623
	IDCZ_ADDMODULE       = 5624
	IDCZ_TIMEOUT         = 5625
	IDCZ_MANAGE          = 5626
	IDCZ_TABS            = 5627
	IDCZ_PAGE_PREV       = 5628
	IDCZ_PAGE_NEXT       = 5629
	IDCZ_LOG             = 5630
	IDCZ_FAILURE_LOG     = 5631
	IDCZ_FILTER_RESPONSE = 5632
	IDCZ_COPY_INPUT      = 5633
	IDCZ_CLOSE           = 5634
	IDCZ_PARAMETER       = 5700
	WM_CADZOW_DONE       = WM_USER + 206
	algorithmCommonTips  = 3 // leave input, status, and result tooltips unchanged
)

type algorithmParameterControl struct {
	Descriptor                            api.ParameterDescriptor
	Path                                  []string
	Label, Value, End, Browse, Hint, Unit uintptr
	Height                                int
	Common                                bool
	Compact                               bool
	Wide                                  bool // a common input that fills the entire row
	CommonOrder                           int
	Omit                                  bool // an inapplicable host-built-in field; keep its draft control
}
type cadzowControls struct {
	algorithm, timeout, start, cancel, close, status                   uintptr
	results, preview, exportResults, metadata, copyInput               uintptr
	resultsLabel, timeoutLabel, noResults                              uintptr
	algorithmIDs                                                       []string
	parameters                                                         []algorithmParameterControl
	moduleIssues                                                       string
	body, version, manage, metadataDetail, commonTitle                 uintptr
	tabs, pagePrev, pageNext, pageCount, log, progress, filterResponse uintptr
	failureTitle, failureReason, failureLog                            uintptr
	shownFailure                                                       algorithmFailureNotice
	rebuilding                                                         bool
	pendingParameters, hasFormal                                       bool
	inputTraces, inputSamples, inputIntervalUS                         int
	inputPath                                                          string
	previewInfo                                                        uintptr
	tab, activeTab                                                     algorithmTab
	tabPages                                                           [5]int
	visibleTabs                                                        []algorithmTab
	moduleID, moduleKey                                                string
	tooltip                                                            uintptr
	tips                                                               [algorithmCommonTips + 4]poststackToolbarPathTip
}

type algorithmFieldDraft struct {
	value, end string
	selected   uintptr
}

type algorithmDialogDraft struct {
	fields   map[string]algorithmFieldDraft
	tab      algorithmTab
	tabPages [5]int
}

var algorithmDrafts = map[string]algorithmDialogDraft{}
var algorithmLastModule, algorithmTimeoutDraft string
var algorithmSettingsDraftSaved bool
var algorithmStatus, algorithmLog, algorithmRecordPath string
var algorithmProgress int
var algorithmStatusSource string
var algorithmLogWindow, algorithmLogEdit uintptr

var (
	cadzowHwnd, cadzowOwner uintptr
	cz                      cadzowControls
	poststackAlgorithms     = registry.New(APP_VERSION)
	poststackModuleRoots    []string
	poststackModuleTrust    = map[string]bool{}
	algorithmUser32         = syscall.NewLazyDLL("user32.dll")
	algorithmDPI            = algorithmUser32.NewProc("GetDpiForWindow")
	algorithmWorkArea       = algorithmUser32.NewProc("SystemParametersInfoW")
	algorithmGetWindowRect  = algorithmUser32.NewProc("GetWindowRect")
	algorithmCreateMenu     = algorithmUser32.NewProc("CreatePopupMenu")
	algorithmAppendMenu     = algorithmUser32.NewProc("AppendMenuW")
	algorithmTrackMenu      = algorithmUser32.NewProc("TrackPopupMenu")
	algorithmDestroyMenu    = algorithmUser32.NewProc("DestroyMenu")
)

func algorithmScale() int {
	if cadzowHwnd != 0 && algorithmDPI.Find() == nil {
		d, _, _ := algorithmDPI.Call(cadzowHwnd)
		if d >= 96 && d <= 384 {
			return int(d)
		}
	}
	return 96
}
func algorithmPx(n int) int { return n * algorithmScale() / 96 }
func showCadzowDialog() {
	pollPoststackWorkerCompletion()
	if cadzowHwnd != 0 {
		syncPoststackParameterContext()
		showOwnedSettingsWindow(cadzowHwnd)
		traceAlgorithmWindowLayout("激活已有窗口")
		return
	}
	if compareHwnd == 0 || !compareWorkspaceActive {
		return
	}
	cadzowOwner = compareHwnd
	restoreDPI := beginAlgorithmDPIContext()
	defer restoreDPI()
	h, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(u16("SeisForge64Cadzow"))), uintptr(unsafe.Pointer(u16("算法处理 — SeisForge Studio"))), WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN, uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 680, 430, compareHwnd, 0, 0, 0)
	if h == 0 {
		cadzowOwner = 0
		return
	}
	cadzowHwnd = h
	createCadzowUI()
	if cz.body == 0 {
		pDestroyWindow.Call(h)
		message(compareHwnd, "算法处理", "无法创建参数区域。", MB_OK|MB_ICONINFORMATION)
		return
	}
	placeAlgorithmWindowOnOpen()
	showOwnedSettingsWindow(h)
	pUpdateWindow.Call(h)
}
func createCadzowUI() {
	cz = cadzowControls{}
	cz.body = createAlgorithmPageBody()
	if cz.body == 0 {
		return
	}
	control := func(parent uintptr) func(string, string, uint32, int) uintptr {
		return func(class, text string, style uint32, id int) uintptr {
			if class == "BUTTON" && style&0xf == BS_PUSHBUTTON {
				return createPoststackNamedToolButton(parent, id, 0, text, style, 0, 0, 100, 24)
			}
			return createCtrl(parent, class, text, WS_CHILD|algorithmClipSiblings|style, 0, 0, 100, 24, id)
		}
	}
	top, body := control(cadzowHwnd), control(cz.body)
	const comboStyle = WS_TABSTOP | CBS_DROPDOWNLIST | CBS_HASSTRINGS | WS_VSCROLL
	const buttonStyle = WS_TABSTOP | BS_PUSHBUTTON
	const editStyle = WS_TABSTOP | WS_BORDER | ES_AUTOHSCROLL
	const ellipsis = 0x4000 | 0x0080 | 0x000C // SS_ENDELLIPSIS | SS_NOPREFIX | SS_LEFTNOWORDWRAP
	cz.algorithm = top("COMBOBOX", "", comboStyle, IDCZ_ALGORITHM)
	cz.version = top("STATIC", "", ellipsis, 0)
	cz.manage = top("BUTTON", "模块管理 ▾", buttonStyle, IDCZ_MANAGE)
	cz.filterResponse = top("BUTTON", "响应预览", buttonStyle, IDCZ_FILTER_RESPONSE)
	cz.metadata = top("STATIC", "", ellipsis|0x0100, 0) // SS_NOTIFY for tooltip
	cz.copyInput = top("BUTTON", "复制路径", buttonStyle, IDCZ_COPY_INPUT)
	cz.metadataDetail = top("STATIC", "", ellipsis, 0)
	cz.commonTitle = body("STATIC", "常用参数", ellipsis, 0)
	cz.tabs = body("SysTabControl32", "", WS_TABSTOP, IDCZ_TABS)
	cz.pagePrev = body("BUTTON", "上一页", buttonStyle, IDCZ_PAGE_PREV)
	cz.pageNext = body("BUTTON", "下一页", buttonStyle, IDCZ_PAGE_NEXT)
	cz.pageCount = body("STATIC", "", 0x0001, 0) // SS_CENTER
	if !algorithmSettingsDraftSaved {
		algorithmTimeoutDraft = "30"
	}
	cz.timeoutLabel = body("STATIC", "超时（分钟）", 0, 0)
	cz.timeout = body("EDIT", algorithmTimeoutDraft, editStyle, IDCZ_TIMEOUT)
	const statusStyle = WS_TABSTOP | 0x0004 | 0x0800 // ES_MULTILINE | ES_READONLY
	cz.status = top("EDIT", "", statusStyle, IDCZ_STATUS)
	cz.progress = top("msctls_progress32", "", 0, 0)
	pSendMessageW.Call(cz.progress, 0x406, 0, 100) // PBM_SETRANGE32
	cz.preview = createPoststackNamedToolButton(cadzowHwnd, IDCZ_PREVIEW, poststackGlyphPreview, "预览当前窗口", BS_PUSHBUTTON, 0, 0, 100, 24)
	cz.start = createPoststackNamedToolButton(cadzowHwnd, IDCZ_START, poststackGlyphRun, "运行处理", BS_PUSHBUTTON, 0, 0, 100, 24)
	cz.cancel = top("BUTTON", "取消运行", buttonStyle, IDCZ_CANCEL)
	cz.close = top("BUTTON", "关闭", buttonStyle, IDCZ_CLOSE)
	cz.failureTitle = top("STATIC", "", 0x0080|0x000C, 0) // SS_NOPREFIX | SS_LEFTNOWORDWRAP
	cz.failureReason = top("STATIC", "", 0x0080, 0)       // SS_LEFT wraps normally
	cz.failureLog = top("BUTTON", "查看详细日志", buttonStyle, IDCZ_FAILURE_LOG)
	cz.resultsLabel = body("STATIC", "正式结果", 0, 0)
	cz.results = body("COMBOBOX", "", comboStyle, IDCZ_RESULTS)
	cz.noResults = body("STATIC", "暂无正式结果", ellipsis, 0)
	cz.exportResults = body("BUTTON", "导出结果 SEG-Y", buttonStyle, IDCZ_EXPORT_RESULTS)
	cz.log = body("BUTTON", "查看日志", buttonStyle, IDCZ_LOG)
	cz.previewInfo = body("STATIC", "", ellipsis|0x0100, 0)
	refreshPoststackModules()
	refreshPoststackResultChoices()
	syncPoststackParameterContext()
}

func refreshPoststackModules() {
	previous := selectedCadzowAlgorithmID()
	if previous == "" {
		previous = algorithmLastModule
	}
	if previous == "" {
		// Start a fresh session on the public module; an explicitly selected
		// external module continues to be remembered for subsequent openings.
		previous = poststack.BandpassAlgorithmID
	}
	dtUS := 2000
	if compareTD != nil && compareTD.fa != nil {
		dtUS = compareTD.fa.Info.SampleIntervalUS
	}
	// The public host module and external modules share one descriptor-driven
	// selector. Scanning a user's directory cannot replace a trusted builtin.
	builtinErr := poststackAlgorithms.RegisterBuiltin(poststack.BandpassDescriptor(dtUS))
	roots := append([]string(nil), poststackModuleRoots...)
	if root, err := registry.DefaultDirectory(); err == nil {
		roots = append(roots, root)
	}
	if executable, err := os.Executable(); err == nil {
		portable := filepath.Join(filepath.Dir(executable), "algorithms")
		if info, err := os.Stat(portable); err == nil && info.IsDir() {
			roots = append(roots, portable)
		}
	}
	issues, err := poststackAlgorithms.Scan(context.Background(), roots)
	cz.moduleIssues = ""
	if builtinErr != nil {
		cz.moduleIssues = builtinErr.Error()
	} else if err != nil {
		cz.moduleIssues = err.Error()
	} else if len(issues) > 0 {
		cz.moduleIssues = fmt.Sprintf("%d 个模块不可用：%s — %s", len(issues), issues[0].Path, issues[0].Message)
	}
	pSendMessageW.Call(cz.algorithm, CB_RESETCONTENT, 0, 0)
	cz.algorithmIDs = nil
	selected := 0
	for i, m := range poststackAlgorithms.Modules() {
		d := m.Descriptor()
		cz.algorithmIDs = append(cz.algorithmIDs, d.ID)
		pSendMessageW.Call(cz.algorithm, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(d.Name))))
		if d.ID == previous {
			selected = i
		}
	}
	if len(cz.algorithmIDs) == 0 {
		pSendMessageW.Call(cz.algorithm, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16("未安装算法模块"))))
	}
	pSendMessageW.Call(cz.algorithm, CB_SETCURSEL, uintptr(selected), 0)
	rebuildAlgorithmParameters()
}
func selectedCadzowAlgorithmID() string {
	if cz.algorithm == 0 {
		return ""
	}
	i, _, _ := pSendMessageW.Call(cz.algorithm, CB_GETCURSEL, 0, 0)
	if i >= uintptr(len(cz.algorithmIDs)) {
		return ""
	}
	return cz.algorithmIDs[i]
}

// Flatten groups only for presentation. Values remain nested according to the
// manifest, and validation is always performed by the shared public API.
func algorithmParameterRows(schema []api.ParameterDescriptor, parent []string) []algorithmParameterControl {
	var out []algorithmParameterControl
	for _, p := range schema {
		path := append(append([]string(nil), parent...), p.ID)
		height := 52
		if p.Type == "group" {
			height = 28
		}
		out = append(out, algorithmParameterControl{Descriptor: p, Path: path, Height: height})
		if p.Type == "group" {
			children := append([]api.ParameterDescriptor(nil), p.Parameters...)
			if defaults, ok := p.Default.(map[string]any); ok {
				for i := range children {
					if value, exists := defaults[children[i].ID]; exists {
						children[i].Default = value
					}
				}
			}
			out = append(out, algorithmParameterRows(children, path)...)
		}
	}
	return out
}

func markCommonAlgorithmParameters(rows []algorithmParameterControl) {
	hasHints := false
	for _, row := range rows {
		hasHints = hasHints || row.Descriptor.UI != nil
	}
	var candidates []int
	for i := range rows {
		p := &rows[i]
		// Do not tear parameters out of their nested groups or crowd file/range controls.
		if len(p.Path) != 1 || p.Descriptor.Type == "group" || p.Descriptor.Type == "file" && !p.Wide || p.Descriptor.Type == "range" {
			continue
		}
		if !hasHints || p.Descriptor.UI != nil && p.Descriptor.UI.Section == api.ParameterUICommon {
			candidates = append(candidates, i)
		}
	}
	if hasHints {
		sort.SliceStable(candidates, func(i, j int) bool {
			return rows[candidates[i]].Descriptor.UI.Order < rows[candidates[j]].Descriptor.UI.Order
		})
	}
	limit := 4
	// The public temporal filter deliberately uses a compact three-row grid.
	// Other modules keep the standard four-parameter common area.
	if len(candidates) > 0 && rows[candidates[0]].Compact {
		limit = 6
	}
	for order, i := range candidates[:minInt(len(candidates), limit)] {
		rows[i].Common, rows[i].CommonOrder = true, order
	}
}

func algorithmDraftKey(p algorithmParameterControl) string { return strings.Join(p.Path, "/") }

func applyAlgorithmFieldDraft(p algorithmParameterControl, field algorithmFieldDraft) {
	switch p.Descriptor.Type {
	case "boolean":
		pSendMessageW.Call(p.Value, BM_SETCHECK, field.selected, 0)
	case "enum":
		pSendMessageW.Call(p.Value, CB_SETCURSEL, field.selected, 0)
	default:
		setText(p.Value, field.value)
	}
	if p.End != 0 {
		setText(p.End, field.end)
	}
}

func saveAlgorithmDialogDraft() {
	if cz.timeout != 0 {
		algorithmTimeoutDraft = getText(cz.timeout)
		algorithmSettingsDraftSaved = true
	}
	if cz.moduleKey == "" {
		return
	}
	draft := algorithmDialogDraft{fields: map[string]algorithmFieldDraft{}, tab: cz.tab, tabPages: cz.tabPages}
	for _, p := range cz.parameters {
		if p.Value == 0 {
			continue
		}
		value := algorithmFieldDraft{value: getText(p.Value)}
		if p.End != 0 {
			value.end = getText(p.End)
		}
		switch p.Descriptor.Type {
		case "enum":
			value.selected, _, _ = pSendMessageW.Call(p.Value, CB_GETCURSEL, 0, 0)
		case "boolean":
			value.selected, _, _ = pSendMessageW.Call(p.Value, BM_GETCHECK, 0, 0)
		}
		draft.fields[algorithmDraftKey(p)] = value
	}
	algorithmDrafts[cz.moduleKey] = draft
}

func algorithmDefaultText(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
func rebuildAlgorithmParameters() {
	id := selectedCadzowAlgorithmID()
	m, installed := poststackAlgorithms.Get(id)
	key := "no-module"
	if installed {
		key = m.Fingerprint()
	}
	if key == cz.moduleKey {
		updateCadzowAlgorithmUI()
		return
	}
	cz.rebuilding = true
	defer func() { cz.rebuilding = false }()
	if id != poststack.BandpassAlgorithmID {
		if response := temporalFilterResponseOwners[cadzowHwnd]; response != 0 {
			pDestroyWindow.Call(response)
		}
	}
	saveAlgorithmDialogDraft()
	cz.moduleID, cz.moduleKey = id, key
	if installed {
		algorithmLastModule = id
		setText(cz.version, "版本 "+m.Descriptor().Version)
	} else {
		setText(cz.version, "")
	}
	draft := algorithmDrafts[key]
	cz.tab, cz.tabPages = draft.tab, draft.tabPages
	for i := algorithmCommonTips; i < len(cz.tips); i++ {
		setAlgorithmTooltip(i, cz.tips[i].control, "")
	}
	for _, p := range cz.parameters {
		for _, h := range []uintptr{p.Label, p.Value, p.End, p.Browse, p.Hint, p.Unit} {
			if h != 0 {
				pDestroyWindow.Call(h)
			}
		}
	}
	cz.parameters = nil
	if installed {
		cz.parameters = algorithmParameterRows(m.Descriptor().Parameters, nil)
		if m.IsBuiltin() && id == poststack.BandpassAlgorithmID {
			for i := range cz.parameters {
				p := &cz.parameters[i]
				p.Compact = p.Descriptor.UI != nil && p.Descriptor.UI.Section == api.ParameterUICommon
			}
		}
		markCommonAlgorithmParameters(cz.parameters)
		for i := range cz.parameters {
			p := &cz.parameters[i]
			d := p.Descriptor
			name := d.Name
			extra := ""
			if i := strings.Index(name, "（0="); i >= 0 && strings.HasSuffix(name, "）") {
				extra, name = name[i+len("（"):len(name)-len("）")], name[:i]
			}
			if name == "" {
				name = d.ID
			}
			if d.Required {
				name += " *"
			}
			p.Label = createCtrl(cz.body, "STATIC", name, WS_CHILD|algorithmClipSiblings|0x4000, 0, 0, 100, 22, 0)
			if d.Type == "group" {
				continue
			}
			style := uint32(WS_CHILD | algorithmClipSiblings | WS_TABSTOP | WS_BORDER | ES_AUTOHSCROLL)
			class := "EDIT"
			text := algorithmDefaultText(d.Default)
			if d.Type == "enum" {
				class = "COMBOBOX"
				style = WS_CHILD | algorithmClipSiblings | WS_TABSTOP | CBS_DROPDOWNLIST | CBS_HASSTRINGS | WS_VSCROLL
				text = ""
			}
			if d.Type == "boolean" {
				class = "BUTTON"
				style = WS_CHILD | algorithmClipSiblings | WS_TABSTOP | BS_AUTOCHECKBOX
				text = "启用"
			}
			p.Value = createCtrl(cz.body, class, text, style, 0, 0, 100, 24, IDCZ_PARAMETER+i*3)
			if d.Type == "enum" {
				selected := 0
				for j, o := range d.Options {
					label := o
					if id == poststack.BandpassAlgorithmID {
						label = temporalFilterEnumLabel(d.ID, o)
					}
					pSendMessageW.Call(p.Value, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
					if o == algorithmDefaultText(d.Default) {
						selected = j
					}
				}
				pSendMessageW.Call(p.Value, CB_SETCURSEL, uintptr(selected), 0)
			}
			if d.Type == "boolean" && d.Default == true {
				pSendMessageW.Call(p.Value, BM_SETCHECK, BST_CHECKED, 0)
			}
			if d.Type == "range" {
				lo, hi := "", ""
				if d.Default != nil {
					v, _ := api.NormalizeParameters([]api.ParameterDescriptor{d}, nil)
					if r, ok := v[d.ID].(api.RangeValue); ok {
						lo = strconv.FormatFloat(r.Min, 'g', -1, 64)
						hi = strconv.FormatFloat(r.Max, 'g', -1, 64)
					}
				}
				setText(p.Value, lo)
				p.End = createCtrl(cz.body, "EDIT", hi, WS_CHILD|algorithmClipSiblings|WS_BORDER|ES_AUTOHSCROLL|WS_TABSTOP, 0, 0, 100, 24, IDCZ_PARAMETER+i*3+1)
			}
			if d.Type == "file" {
				p.Browse = createPoststackNamedToolButton(cz.body, IDCZ_PARAMETER+i*3+2, 0, "…", BS_PUSHBUTTON, 0, 0, 28, 24)
			}
			if old, ok := draft.fields[algorithmDraftKey(*p)]; ok {
				applyAlgorithmFieldDraft(*p, old)
			}
			hint := ""
			switch {
			case d.Unit != "" && (d.Type == "integer" || d.Type == "number" || d.Type == "range") && len([]rune(d.Unit)) <= 8:
				p.Unit = createCtrl(cz.body, "STATIC", d.Unit, WS_CHILD|algorithmClipSiblings|0x0080, 0, 0, 50, 22, 0)
			default:
				hint = d.Unit
			}
			if extra != "" {
				hint += "  " + extra
			}
			if d.Min != nil {
				hint += fmt.Sprintf("  最小 %g", *d.Min)
			}
			if d.Max != nil {
				hint += fmt.Sprintf("  最大 %g", *d.Max)
			}
			if d.Step != nil {
				hint += fmt.Sprintf("  步长 %g", *d.Step)
			}
			if d.Description != "" {
				hint += "  " + d.Description
			}
			p.Hint = createCtrl(cz.body, "STATIC", strings.TrimSpace(hint), WS_CHILD|algorithmClipSiblings|0x0080, 0, 0, 500, 30, 0) // SS_NOPREFIX; wraps normally
			if p.Common {
				if d.Default != nil && algorithmDefaultText(d.Default) != "" {
					hint += "  默认 " + algorithmDefaultText(d.Default)
				}
				if hint = strings.TrimSpace(hint); hint != "" {
					setAlgorithmTooltip(algorithmCommonTips+p.CommonOrder, p.Value, name+"\r\n"+hint)
				}
			}
		}
	}
	cz.rebuilding = false
	refreshPoststackOutputPreview()
	updateCadzowAlgorithmUI()
	layoutCadzowWindow("重建参数")
}

func poststackCompareInputPreparing() bool {
	return comparePrepareCancel != nil || (compareAlgorithmGeometryPending != 0 && compareAlgorithmGeometryPending == atomic.LoadInt64(&compareGeomGen))
}

func updateCadzowAlgorithmUI() {
	_, ok := poststackAlgorithms.Get(selectedCadzowAlgorithmID())
	preparing := poststackCompareInputPreparing()
	ready := ok && cadzowCancel == nil && !preparing && compareTD != nil && compareTD.fa != nil && compareTD.ga != nil && compareWorkspaceActive && compareMode != 2
	for _, h := range []uintptr{cz.start, cz.preview} {
		if h != 0 {
			pEnableWindow.Call(h, uintptr(boolToInt(ready)))
		}
	}
	if cz.cancel != 0 {
		pEnableWindow.Call(cz.cancel, uintptr(boolToInt(cadzowCancel != nil && !poststackTaskCancelling())))
	}
	if cz.manage != 0 {
		pEnableWindow.Call(cz.manage, uintptr(boolToInt(cadzowCancel == nil)))
	}
	refreshAlgorithmProgressVisibility()
	if cz.start != 0 {
		label := "运行处理"
		if cadzowCancel != nil {
			label = "处理中…"
		}
		if getText(cz.start) != label {
			setText(cz.start, label)
		}
	}
	if cz.status != 0 {
		status := algorithmStatus
		if status == "" {
			switch {
			case !ok && cz.moduleIssues != "":
				status = cz.moduleIssues
			case !ok:
				status = "未安装算法模块"
			case preparing:
				status = "正在准备 A/B 几何…"
			case !ready:
				status = "当前剖面不可处理"
			default:
				status = "就绪"
			}
		}
		if cadzowCancel == nil && !cz.rebuilding && cz.pendingParameters {
			status += " · 参数有更改，尚未运行"
		}
		if getText(cz.status) != status {
			setText(cz.status, status)
			setAlgorithmTooltip(1, cz.status, status)
		}
		pSendMessageW.Call(cz.progress, 0x402, uintptr(algorithmProgress), 0) // PBM_SETPOS
	}
	updateAlgorithmFailureUI()
}

func setAlgorithmStatus(status string, percent int) {
	changed := status != algorithmStatus || algorithmStatusSource != compareAPath
	algorithmStatus, algorithmStatusSource = status, compareAPath
	if percent >= 0 {
		algorithmProgress = maxInt(0, minInt(100, percent))
	}
	updateCadzowAlgorithmUI()
	if changed && compareWorkspaceActive && compareAPath != "" && cc.status != 0 {
		setText(cc.status, "算法处理："+strings.ReplaceAll(status, "\r\n", " | "))
		if !compareActiveB() {
			invalidateCompareBase()
		}
	}
}

func resetAlgorithmLog() {
	algorithmLog, algorithmRecordPath = "", ""
	refreshAlgorithmLogWindow()
}

func setAlgorithmLog(message, recordPath string) {
	if message != "" {
		runes := []rune(algorithmLog + message + "\r\n")
		const limit = 16000
		if len(runes) > limit {
			runes = runes[len(runes)-limit:]
		}
		algorithmLog = string(runes)
	}
	if recordPath != "" {
		algorithmRecordPath = recordPath
	}
	refreshAlgorithmLogWindow()
}

func refreshAlgorithmLogWindow() {
	if algorithmLogEdit == 0 {
		return
	}
	logText := algorithmLog
	if failure := visibleAlgorithmFailure(); failure.details != "" {
		logText = failure.details + "\r\n\r\n" + logText
	}
	if algorithmRecordPath != "" {
		logText = "运行记录：" + algorithmRecordPath + "\r\n\r\n" + logText
	}
	if logText == "" {
		logText = "暂无运行日志。"
	}
	setText(algorithmLogEdit, logText)
}

func showAlgorithmLogWindow() {
	if cadzowHwnd == 0 {
		return
	}
	if algorithmLogWindow != 0 {
		showOwnedSettingsWindow(algorithmLogWindow)
		return
	}
	h, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(u16("SeisForgeAlgorithmLog"))), uintptr(unsafe.Pointer(u16("算法处理日志"))), WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN, uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 570, 350, cadzowHwnd, 0, 0, 0)
	if h == 0 {
		return
	}
	algorithmLogWindow = h
	algorithmLogEdit = createCtrl(h, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_VSCROLL|ES_READONLY|traceESMultiline|traceESAutoVScroll|WS_TABSTOP, 0, 0, 1, 1, 0)
	pSendMessageW.Call(algorithmLogEdit, 0x00C5, 2<<20, 0) // EM_SETLIMITTEXT: runner retains up to 1 MiB stderr
	refreshAlgorithmLogWindow()
	if width, height := clientSize(h); width > 0 && height > 0 {
		pMoveWindow.Call(algorithmLogEdit, 0, 0, uintptr(width), uintptr(height), 1)
	}
	showOwnedSettingsWindow(h)
}

func algorithmLogWndProc(h uintptr, msg uint32, w, l uintptr) uintptr {
	switch msg {
	case WM_SIZE:
		if h == algorithmLogWindow && algorithmLogEdit != 0 {
			pMoveWindow.Call(algorithmLogEdit, 0, 0, l&0xffff, (l>>16)&0xffff, 1)
		}
		return 0
	case WM_CLOSE:
		if h == algorithmLogWindow {
			closeOwnedSettingsWindow(h, cadzowHwnd)
		}
		return 0
	case WM_DESTROY:
		if h == algorithmLogWindow {
			algorithmLogWindow, algorithmLogEdit = 0, 0
		}
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), w, l)
	return r
}
func readAlgorithmParameters() (registry.Module, map[string]any, error) {
	m, ok := poststackAlgorithms.Get(selectedCadzowAlgorithmID())
	if !ok {
		return m, nil, fmt.Errorf("未知或未安装的算法；不会回退为 Copy")
	}
	values := map[string]any{}
	response, method := selectedAlgorithmFilterDesign()
	for _, p := range cz.parameters {
		d := p.Descriptor
		if m.IsBuiltin() && m.Descriptor().ID == poststack.BandpassAlgorithmID && !poststack.FilterParameterApplies(d.ID, response, method) {
			continue
		}
		current := values
		for _, key := range p.Path[:len(p.Path)-1] {
			child, ok := current[key].(map[string]any)
			if !ok {
				child = map[string]any{}
				current[key] = child
			}
			current = child
		}
		if d.Type == "group" {
			if _, exists := current[d.ID]; !exists {
				current[d.ID] = map[string]any{}
			}
			continue
		}
		text := strings.TrimSpace(getText(p.Value))
		if text == "" && !d.Required && d.Default == nil && d.Type != "boolean" && d.Type != "enum" {
			continue
		}
		var v any
		var err error
		switch d.Type {
		case "integer":
			v, err = strconv.ParseInt(text, 10, 64)
		case "number":
			v, err = strconv.ParseFloat(text, 64)
		case "boolean":
			b, _, _ := pSendMessageW.Call(p.Value, BM_GETCHECK, 0, 0)
			v = b == BST_CHECKED
		case "enum":
			i, _, _ := pSendMessageW.Call(p.Value, CB_GETCURSEL, 0, 0)
			if i >= uintptr(len(d.Options)) {
				err = fmt.Errorf("请选择选项")
			} else {
				v = d.Options[i]
			}
		case "range":
			var lo, hi float64
			lo, err = strconv.ParseFloat(text, 64)
			if err == nil {
				hi, err = strconv.ParseFloat(strings.TrimSpace(getText(p.End)), 64)
			}
			v = api.RangeValue{Min: lo, Max: hi}
		default:
			v = text
		}
		if err != nil {
			return m, nil, fmt.Errorf("%s: %w", d.Name, err)
		}
		current[d.ID] = v
	}
	v, err := api.NormalizeParameters(m.Descriptor().Parameters, values)
	return m, v, err
}
func syncPoststackParameterContext() {
	if cadzowHwnd == 0 || cadzowCancel != nil {
		return
	}
	if algorithmStatusSource != compareAPath {
		algorithmStatus, algorithmProgress, algorithmStatusSource = "", 0, compareAPath
		algorithmFailure = algorithmFailureNotice{}
		resetAlgorithmLog()
	}
	traces, _, line := cadzowCurrentProfile()
	if cz.inputPath != compareAPath {
		cz.inputPath = compareAPath
		setText(cz.copyInput, "复制路径")
	}
	nt, ns := len(traces), maxInt(0, compareSampleEnd-compareSampleStart+1)
	cz.inputTraces, cz.inputSamples, cz.inputIntervalUS = nt, ns, 0
	direction, timeRange := "当前剖面", "时间范围不可用"
	if nt > 0 && compareTD != nil && compareTD.fa != nil {
		first, last := compareSampleStart, compareSampleEnd
		switch {
		case compareTD.traceOrderFallback:
			direction = "道序剖面"
		case compareMode == 0:
			direction = fmt.Sprintf("Inline %d", line)
		case compareMode == 1:
			direction = fmt.Sprintf("Crossline %d", line)
		}
		dt := compareTD.fa.Info.SampleIntervalUS
		cz.inputIntervalUS = dt
		if origin, err := compareTD.fa.TraceTimeOriginUS(traces[0]); err == nil && dt > 0 {
			// The visible time axis follows the first trace; per-trace delays remain in the captured artifact.
			start, end := origin+int64(first)*int64(dt), origin+int64(last)*int64(dt)
			timeRange = fmt.Sprintf("首道 %.7g–%.7g ms", float64(start)/1000, float64(end)/1000)
		}
	}
	short, full := algorithmInputSummary(compareAPath, direction, nt, ns, timeRange)
	setText(cz.metadata, short)
	setAlgorithmTooltip(0, cz.metadata, full)
	pEnableWindow.Call(cz.copyInput, uintptr(boolToInt(compareAPath != "")))
	if cz.inputIntervalUS > 0 {
		setText(cz.metadataDetail, fmt.Sprintf("频率 (Hz)：0 < 截止频率 < Nyquist %.7g；双边频率需下界小于上界。", 5e5/float64(cz.inputIntervalUS)))
	} else {
		setText(cz.metadataDetail, "频率 (Hz)：须低于当前数据的 Nyquist 频率。")
	}
	refreshPoststackOutputPreview()
	updateCadzowAlgorithmUI()
	if cz.moduleID == poststack.BandpassAlgorithmID {
		refreshFormalTemporalResponse(false)
	}
}

func algorithmInputSummary(path, direction string, traces, samples int, timeRange string) (string, string) {
	if path == "" {
		return "输入 A 尚未加载", ""
	}
	scope := fmt.Sprintf("%s · %d 道 × %d 样点 · %s", direction, traces, samples, timeRange)
	return scope + " · A：" + filepath.Base(path), "输入 A：" + path + "\r\n" + scope
}

func setAlgorithmTooltip(slot int, control uintptr, content string) {
	if cadzowHwnd == 0 || control == 0 {
		return
	}
	restoreDPI := enterAlgorithmWindowDPIContext(cadzowHwnd)
	defer restoreDPI()
	tip := &cz.tips[slot]
	if tip.control == control && tip.path == content {
		return
	}
	if tip.text != nil {
		info := makePoststackToolInfo(cadzowHwnd, tip.control, nil)
		pSendMessageW.Call(cz.tooltip, poststackTTMDelToolW, 0, uintptr(unsafe.Pointer(&info)))
		tip.pin.Unpin()
		tip.text, tip.path, tip.control = nil, "", 0
	}
	if content == "" {
		return
	}
	if cz.tooltip == 0 {
		cz.tooltip, _, _ = pCreateWindowExW.Call(0x08000008, uintptr(unsafe.Pointer(u16("tooltips_class32"))), 0, WS_POPUP|poststackTTSNoPrefix, 0, 0, 0, 0, cadzowHwnd, 0, 0, 0)
		if cz.tooltip == 0 {
			return
		}
		pSendMessageW.Call(cz.tooltip, poststackTTMMaxWidth, 0, uintptr(algorithmPx(515)))
	}
	font, _, _ := pSendMessageW.Call(cz.tooltip, 0x0031 /* WM_GETFONT */, 0, 0)
	if font == 0 {
		font, _, _ = pGetStockObject.Call(DEFAULT_GUI_FONT)
	}
	measure, done := poststackToolbarFontMeasure(font)
	tip.text = u16(wrapPoststackPathTip(content, algorithmPx(480), measure))
	done()
	tip.pin.Pin(tip.text)
	info := makePoststackToolInfo(cadzowHwnd, control, tip.text)
	if added, _, _ := pSendMessageW.Call(cz.tooltip, poststackTTMAddToolW, 0, uintptr(unsafe.Pointer(&info))); added == 0 {
		tip.pin.Unpin()
		tip.text = nil
		return
	}
	tip.path, tip.control = content, control
}

func releaseAlgorithmTooltips() {
	if cz.tooltip != 0 {
		pDestroyWindow.Call(cz.tooltip)
		cz.tooltip = 0
	}
	for i := range cz.tips {
		cz.tips[i].pin.Unpin()
		cz.tips[i].text, cz.tips[i].path, cz.tips[i].control = nil, "", 0
	}
}

func refreshAlgorithmTooltipDPI() {
	var texts [len(cz.tips)]string
	var controls [len(cz.tips)]uintptr
	for i := range cz.tips {
		texts[i], controls[i] = cz.tips[i].path, cz.tips[i].control
	}
	releaseAlgorithmTooltips()
	for i, text := range texts {
		if text != "" {
			setAlgorithmTooltip(i, controls[i], text)
		}
	}
}

func refreshPoststackOutputPreview() {
	if cz.previewInfo == 0 {
		return
	}
	r := activeGeneratedPoststackResult()
	info, detail := "暂无当前处理结果。", ""
	if r != nil {
		label := "正式 B"
		if r.Preview {
			label = "临时预览 B"
		}
		info = fmt.Sprintf("%s · %s · %s", label, r.displayName(), poststackResultScope(r))
		detail = info + "\r\n" + r.Name2
		if r.Section.Part1 != "" {
			detail += "\r\n" + r.Name1
		}
	}
	setText(cz.previewInfo, info)
	setAlgorithmTooltip(2, cz.previewInfo, detail)
	refreshAlgorithmPendingParameters(r)
}

func algorithmParametersDiffer(current, finished map[string]any) bool {
	a, errA := json.Marshal(current)
	b, errB := json.Marshal(finished)
	return errA != nil || errB != nil || string(a) != string(b)
}

func refreshAlgorithmPendingParameters(r *generatedPoststackResult) {
	cz.pendingParameters = false
	if r == nil || r.Record.Parameters == nil || cz.rebuilding || cadzowCancel != nil {
		return
	}
	module, draft, err := readAlgorithmParameters()
	cz.pendingParameters = err != nil || module.Fingerprint() != r.Module.Fingerprint() ||
		algorithmParametersDiffer(draft, r.Record.Parameters)
}
func cadzowCurrentProfile() (traces []int64, coords []int32, actual int32) {
	if compareTD == nil || compareTD.ga == nil || compareMode == 2 {
		return
	}
	t, c, a, err := compareTD.ga.NumericLineTraceNumbers(compareMode, compareLineCoord)
	if err != nil {
		return
	}
	if compareTD.fa != nil {
		start, end, step := compareTraceRangeForGeometry(compareTD.fa)
		if end < start {
			return nil, nil, a
		}
		t, c = filterPhysicalTraceRange(t, c, start, end, step)
	}
	t, c = filterLineRange(t, c, compareLineXMin, compareLineXMax)
	return t, c, a
}
func cadzowCurrentTraces() []int64 { t, _, _ := cadzowCurrentProfile(); return t }
func openAlgorithmFile(owner uintptr, title, matches string) string {
	buf := make([]uint16, 32768)
	filter := utf16.Encode([]rune(matches))
	titleText := u16(title)
	of := OPENFILENAME{LStructSize: uint32(unsafe.Sizeof(OPENFILENAME{})), HwndOwner: owner, LpstrFilter: uintptr(unsafe.Pointer(&filter[0])), NFilterIndex: 1, LpstrFile: uintptr(unsafe.Pointer(&buf[0])), NMaxFile: uint32(len(buf)), LpstrTitle: uintptr(unsafe.Pointer(titleText)), Flags: OFN_EXPLORER | OFN_FILEMUSTEXIST | OFN_HIDEREADONLY | 0x00000008 /* OFN_NOCHANGEDIR */}
	r, _, _ := pGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&of)))
	runtime.KeepAlive(filter)
	runtime.KeepAlive(titleText)
	runtime.KeepAlive(buf)
	runtime.KeepAlive(&of)
	if r != 0 {
		return syscall.UTF16ToString(buf)
	}
	return ""
}
func algorithmOutputNames(source, id string, params map[string]any, at time.Time) (string, string) {
	b, _ := json.Marshal(params)
	hash := sha256.Sum256(b)
	stem := fmt.Sprintf("%s_%s_%s_p%x", cadzowSanitizeToken(strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))), at.Format("20060102_150405"), cadzowSanitizeToken(id), hash[:6])
	return stem + "_Part1.sgy", stem + "_Part2.sgy"
}
func algorithmParameterFingerprint(params map[string]any) string {
	b, _ := json.Marshal(params)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func cadzowSanitizeToken(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 32 && !strings.ContainsRune("<>:\"/\\|?*", r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "value"
	}
	return b.String()
}

func showAlgorithmModuleMenu(h uintptr) {
	if cadzowCancel != nil {
		return
	}
	menu, _, _ := algorithmCreateMenu.Call()
	if menu == 0 {
		return
	}
	defer algorithmDestroyMenu.Call(menu)
	algorithmAppendMenu.Call(menu, 0, IDCZ_AUTODETECT, uintptr(unsafe.Pointer(u16("刷新模块"))))
	algorithmAppendMenu.Call(menu, 0, IDCZ_ADDMODULE, uintptr(unsafe.Pointer(u16("添加模块或外挂包…"))))
	var r RECT
	algorithmGetWindowRect.Call(cz.manage, uintptr(unsafe.Pointer(&r)))
	id, _, _ := algorithmTrackMenu.Call(menu, 0x102, uintptr(r.Left), uintptr(r.Bottom), 0, h, 0) // TPM_RETURNCMD|TPM_RIGHTBUTTON
	if id != 0 {
		pSendMessageW.Call(h, WM_COMMAND, id, 0)
	}
}

func algorithmModuleRootForSelection(path string) (root string, isPack bool, err error) {
	name := filepath.Base(path)
	isPack = strings.EqualFold(name, registry.PackManifestName)
	if !isPack && !strings.EqualFold(name, "manifest.json") {
		return "", false, fmt.Errorf("请选择外挂的 %s 或单算法的 manifest.json", registry.PackManifestName)
	}
	return filepath.Dir(path), isPack, nil
}

func selectAlgorithmModuleIndex(h uintptr, i int) {
	pSendMessageW.Call(cz.algorithm, CB_SETCURSEL, uintptr(i), 0)
	pSendMessageW.Call(h, WM_COMMAND, IDCZ_ALGORITHM|(1<<16), cz.algorithm)
	pSetFocus.Call(cz.algorithm)
}

func selectRecentlyAddedAlgorithmPack(h uintptr, path string) {
	if canonical, err := filepath.EvalSymlinks(path); err == nil {
		path = canonical
	}
	packID := ""
	preferred := -1
	for i, id := range cz.algorithmIDs {
		m, ok := poststackAlgorithms.Get(id)
		if !ok || !strings.EqualFold(filepath.Clean(m.PackManifestPath()), filepath.Clean(path)) {
			continue
		}
		packID = m.PackID()
		if preferred < 0 {
			preferred = i
		}
	}
	if preferred >= 0 {
		selectAlgorithmModuleIndex(h, preferred)
		setAlgorithmStatus("已加载外挂 "+packID+"；可在算法下拉框中切换方法", 0)
	} else if cz.moduleIssues != "" {
		setAlgorithmStatus(cz.moduleIssues, -1)
	} else {
		setAlgorithmStatus("所选外挂没有可用算法，请检查外挂目录。", -1)
	}
}

func revealAlgorithmResults() {
	if cadzowHwnd == 0 {
		return
	}
	cz.tabPages[algorithmTabResults] = 0
	selectAlgorithmTab(algorithmTabResults)
}

func finishAlgorithmDialogClose() {
	if cadzowHwnd != 0 {
		h, owner := cadzowHwnd, cadzowOwner
		traceAlgorithmWindowLayout("关闭")
		closeOwnedSettingsWindow(h, owner)
	}
}

func algorithmOwnsWorker(h uintptr) bool {
	return h != 0 && activePoststackWorker != nil && activePoststackWorker.dialog == h && activePoststackWorker.owner == cadzowOwner
}

func cadzowWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	// A separate HWND can still deliver messages while the next dialog is
	// opening/closing. Never act on its global controls or worker.
	if h != cadzowHwnd {
		r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
		return r
	}
	if result, handled := handleAlgorithmDialogLayout(h, msg, wParam, lParam); handled {
		return result
	}
	switch msg {
	case WM_NOTIFY:
		if lParam != 0 {
			header := (*NMHDR)(unsafe.Pointer(lParam))
			if header.HwndFrom == cz.tabs && header.Code == TCN_SELCHANGE {
				index, _, _ := pSendMessageW.Call(cz.tabs, TCM_GETCURSEL, 0, 0)
				if index < uintptr(len(cz.visibleTabs)) {
					selectAlgorithmTab(cz.visibleTabs[index])
				}
				return 0
			}
		}
	case WM_COMMAND:
		id := int(wParam & 0xffff)
		notification := (wParam >> 16) & 0xffff
		if id >= IDCZ_PARAMETER {
			i := (id - IDCZ_PARAMETER) / 3
			if i < len(cz.parameters) && cadzowCancel == nil {
				p := cz.parameters[i]
				if id == IDCZ_PARAMETER+i*3+2 && p.Browse != 0 {
					matches := "所有文件\x00*.*\x00\x00"
					if path := openAlgorithmFile(h, p.Descriptor.Name, matches); path != "" {
						setText(p.Value, path)
					}
				}
			}
			changed := notification == 1 || notification == 0x0300 ||
				notification == 0 && lParam != 0 && i < len(cz.parameters) && cz.parameters[i].Descriptor.Type == "boolean"
			if changed && !cz.rebuilding && cadzowCancel == nil {
				refreshAlgorithmPendingParameters(activeGeneratedPoststackResult())
				updateCadzowAlgorithmUI()
			}
			if !cz.rebuilding && cadzowCancel == nil && cz.moduleID == poststack.BandpassAlgorithmID {
				if notification == 1 { // CBN_SELCHANGE can alter field applicability.
					layoutCadzowWindow("切换滤波设计")
				}
				if notification == 1 || notification == 0x0300 {
					refreshFormalTemporalResponse(false)
				}
			}
			return 0
		}
		switch id {
		case IDCZ_ALGORITHM:
			if notification == 1 && cadzowCancel == nil {
				algorithmStatus, algorithmProgress = "", 0
				algorithmFailure = algorithmFailureNotice{}
				rebuildAlgorithmParameters()
			}
		case IDCZ_RESULTS:
			if notification == 1 && cadzowCancel == nil {
				selectPoststackResult()
			}
		case IDCZ_MANAGE:
			showAlgorithmModuleMenu(h)
		case IDCZ_FILTER_RESPONSE:
			refreshFormalTemporalResponse(true)
		case IDCZ_AUTODETECT:
			if cadzowCancel == nil {
				refreshPoststackModules()
			}
		case IDCZ_ADDMODULE:
			if cadzowCancel != nil {
				return 0
			}
			matches := "外挂或模块清单\x00" + registry.PackManifestName + ";manifest.json\x00\x00"
			if p := openAlgorithmFile(h, "选择算法外挂或模块清单", matches); p != "" {
				dir, isPack, err := algorithmModuleRootForSelection(p)
				if err != nil {
					setAlgorithmStatus(err.Error(), -1)
					break
				}
				found := false
				for _, root := range poststackModuleRoots {
					found = found || strings.EqualFold(filepath.Clean(root), filepath.Clean(dir))
				}
				if !found {
					poststackModuleRoots = append(poststackModuleRoots, dir)
				}
				refreshPoststackModules()
				if isPack {
					selectRecentlyAddedAlgorithmPack(h, p)
				}
			}
		case IDCZ_PAGE_PREV, IDCZ_PAGE_NEXT:
			advance := -1
			if id == IDCZ_PAGE_NEXT {
				advance = 1
			}
			if cz.activeTab != 0 {
				cz.tabPages[cz.activeTab] += advance
				layoutCadzowWindow("切换参数页")
			}
		case IDCZ_START, IDCZ_PREVIEW:
			startCadzowTask(id == IDCZ_PREVIEW)
		case IDCZ_CANCEL:
			requestPoststackCancel(false)
		case IDCZ_CLOSE:
			pSendMessageW.Call(h, WM_CLOSE, 0, 0)
		case IDCZ_COPY_INPUT:
			if copyPoststackPath(h, cz.inputPath) {
				setText(cz.copyInput, "已复制")
			}
		case IDCZ_LOG, IDCZ_FAILURE_LOG:
			showAlgorithmLogWindow()
		case IDCZ_EXPORT_RESULTS:
			if r := activeGeneratedPoststackResult(); cadzowCancel == nil && r != nil {
				startExportGeneratedSection(r)
			}
		}
		return 0
	case WM_CLOSE:
		if cadzowCancel != nil && algorithmOwnsWorker(h) {
			answer, _, _ := pMessageBoxW.Call(h, uintptr(unsafe.Pointer(u16("处理任务尚未结束。取消并关闭窗口？"))), uintptr(unsafe.Pointer(u16("算法处理"))), MB_YESNO|0x20|0x100)
			if answer == IDYES && cadzowHwnd == h {
				if cadzowCancel != nil && algorithmOwnsWorker(h) {
					requestPoststackCancel(true)
				} else {
					finishAlgorithmDialogClose()
				}
			}
		} else {
			finishAlgorithmDialogClose()
		}
		return 0
	case WM_DESTROY:
		saveAlgorithmDialogDraft()
		if response := temporalFilterResponseOwners[h]; response != 0 {
			pDestroyWindow.Call(response)
		}
		ownedWorker := algorithmOwnsWorker(h)
		releaseAlgorithmTooltips()
		releaseAlgorithmDialogLayout()
		cadzowHwnd, cadzowOwner = 0, 0
		cz = cadzowControls{}
		if ownedWorker {
			cancelCadzowTask()
		}
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return r
}
