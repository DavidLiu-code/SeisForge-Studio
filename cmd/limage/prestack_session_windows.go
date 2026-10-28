//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
	prestacksession "github.com/DavidLiu-code/SeisForge-Studio/internal/prestacksession"
)

var (
	prestackPendingSession        *prestacksession.Snapshot
	prestackPendingSessionCompare *prestacksession.Snapshot
)

func prestackSessionSource(ds *dataset.SeismicDataset) (prestacksession.SourceIdentity, error) {
	if ds == nil || strings.TrimSpace(ds.Path) == "" {
		return prestacksession.SourceIdentity{}, fmt.Errorf("没有打开的叠前数据")
	}
	path, err := filepath.Abs(ds.Path)
	if err != nil {
		return prestacksession.SourceIdentity{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return prestacksession.SourceIdentity{}, err
	}
	return prestacksession.SourceIdentity{Path: filepath.Clean(path), Size: st.Size(), ModTimeNS: st.ModTime().UnixNano()}, nil
}

func prestackSessionFilter(mask uint32) prestackcore.CompareFilterSet {
	return prestackcore.CompareFilterSet{Matched: mask&1 != 0, AOnly: mask&2 != 0, BOnly: mask&4 != 0, Ambiguous: mask&8 != 0, Invalid: mask&16 != 0}
}

func prestackSessionMask(f prestackcore.CompareFilterSet) uint32 {
	var mask uint32
	if f.Matched {
		mask |= 1
	}
	if f.AOnly {
		mask |= 2
	}
	if f.BOnly {
		mask |= 4
	}
	if f.Ambiguous {
		mask |= 8
	}
	if f.Invalid {
		mask |= 16
	}
	return mask
}

func prestackPaletteName(index int) string {
	if index >= 0 && index < len(paletteNames) {
		return paletteNames[index]
	}
	return ""
}

func prestackPaletteIndex(name string) int {
	for i, value := range paletteNames {
		if value == name {
			return i
		}
	}
	return prestackState.palette
}

func capturePrestackSessionState() (prestacksession.SessionState, error) {
	a, err := prestackSessionSource(prestackState.dataset)
	if err != nil {
		return prestacksession.SessionState{}, err
	}
	selection := prestackState.selection
	selection.Keys = append([]prestackcore.GatherKey(nil), selection.Keys...)
	b, berr := prestackSessionSource(prestackState.compareBDataset)
	var bptr *prestacksession.SourceIdentity
	if berr == nil {
		bptr = &b
	} else if prestackState.compareBDataset != nil {
		return prestacksession.SessionState{}, berr
	}
	var bm *prestackcore.HeaderMapping
	if prestackState.compareBDataset != nil {
		m := prestackState.compareBMapping
		bm = &m
	}
	return prestacksession.SessionState{
		Page: prestackState.page, ASource: a, AMapping: prestackState.mapping, Selection: selection,
		Display:  prestacksession.DisplayState{Mode: map[bool]string{true: "wiggle", false: "image"}[prestackState.display == 1], Palette: prestackPaletteName(prestackState.palette), WiggleDecimation: prestackState.wiggleDecimation, GainPercent: prestackState.gain, AGC: prestackState.agc, RangeEnabled: prestackState.compareUseLimits, RangeMin: prestackState.compareLimitMin, RangeMax: prestackState.compareLimitMax, SampleStart: prestackState.sampleFirst, SampleEnd: prestackState.sampleLast, ViewFirst: int64(prestackState.viewFirst), ViewLast: int64(prestackState.viewLast)},
		Compare:  prestacksession.CompareState{Enabled: prestackState.compareBDataset != nil || prestackState.compareBLoading, BSource: bptr, BMapping: bm, Filter: prestackSessionFilter(prestackState.compareFilterMask), Mode: "readonly", SampleStart: prestackState.compareViewSampleFirst, SampleEnd: prestackState.compareViewSampleLast, ViewXMin: float64(prestackState.compareViewFirst), ViewXMax: float64(prestackState.compareViewLast), ViewYMin: float64(prestackState.compareViewSampleFirst), ViewYMax: float64(prestackState.compareViewSampleLast), AxisCompatible: prestackState.compareBAxis.Compatible, AxisReason: prestackState.compareBAxis.Reason},
		QC:       prestacksession.QCState{CurrentType: prestackState.selection.Type.String(), CurrentKey: prestackState.selection.Key.String()},
		Geometry: prestacksession.GeometryState{Layers: []string{"source", "receiver", "midpoint", "bin", "fold"}, BoundsValid: prestackState.mapBounds.Valid, BoundsXMin: prestackState.mapBounds.XMin, BoundsXMax: prestackState.mapBounds.XMax, BoundsYMin: prestackState.mapBounds.YMin, BoundsYMax: prestackState.mapBounds.YMax, LinkActive: prestackState.linkIndex >= 0},
		Mapping:  prestacksession.MappingState{Target: map[bool]string{true: "B", false: "A"}[prestackState.mappingTargetB]},
	}, nil
}

func savePrestackSessionDialog(owner uintptr, defaultName string) string {
	filter := []uint16{'S', 'e', 'i', 's', 'F', 'o', 'r', 'g', 'e', ' ', '(', '*', '.', 'j', 's', 'o', 'n', ')', 0, '*', '.', 'j', 's', 'o', 'n', 0, '*', '.', '*', 0, 0}
	buf := make([]uint16, 32768)
	copy(buf, syscall.StringToUTF16(defaultName))
	of := OPENFILENAME{LStructSize: uint32(unsafe.Sizeof(OPENFILENAME{})), HwndOwner: owner, NFilterIndex: 1, LpstrFilter: uintptr(unsafe.Pointer(&filter[0])), LpstrFile: uintptr(unsafe.Pointer(&buf[0])), NMaxFile: uint32(len(buf)), Flags: OFN_EXPLORER | OFN_OVERWRITEPROMPT, LpstrDefExt: uintptr(unsafe.Pointer(u16("json")))}
	if ok, _, _ := pGetSaveFileNameW.Call(uintptr(unsafe.Pointer(&of))); ok != 0 {
		return syscall.UTF16ToString(buf)
	}
	return ""
}

func openPrestackSessionDialog(owner uintptr) string {
	filter := []uint16{'S', 'e', 'i', 's', 'F', 'o', 'r', 'g', 'e', ' ', '(', '*', '.', 'j', 's', 'o', 'n', ')', 0, '*', '.', 'j', 's', 'o', 'n', 0, '*', '.', '*', 0, 0}
	buf := make([]uint16, 32768)
	of := OPENFILENAME{LStructSize: uint32(unsafe.Sizeof(OPENFILENAME{})), HwndOwner: owner, NFilterIndex: 1, LpstrFilter: uintptr(unsafe.Pointer(&filter[0])), LpstrFile: uintptr(unsafe.Pointer(&buf[0])), NMaxFile: uint32(len(buf)), Flags: OFN_EXPLORER | OFN_FILEMUSTEXIST, LpstrDefExt: uintptr(unsafe.Pointer(u16("json")))}
	if ok, _, _ := pGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&of))); ok != 0 {
		return syscall.UTF16ToString(buf)
	}
	return ""
}

func prestackSessionIdentityMatches(id prestacksession.SourceIdentity, path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Size() == id.Size && st.ModTime().UnixNano() == id.ModTimeNS
}

func savePrestackSessionSnapshot() {
	state, err := capturePrestackSessionState()
	if err != nil {
		message(prestackHwnd, "保存流程", err.Error(), MB_OK|MB_ICONINFORMATION)
		return
	}
	snapshot, err := prestacksession.CapturePrestackSessionSnapshot(state)
	if err != nil {
		message(prestackHwnd, "保存流程", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	path := savePrestackSessionDialog(prestackHwnd, "prestack-session.json")
	if path == "" {
		return
	}
	if err := prestacksession.Save(path, snapshot, false); err != nil {
		message(prestackHwnd, "保存流程", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	setPrestackStatus("叠前流程已保存：" + path)
}

func resolvePrestackSessionSource(id prestacksession.SourceIdentity, label string, allowPrompt bool) string {
	if prestackSessionIdentityMatches(id, id.Path) {
		return id.Path
	}
	if !allowPrompt {
		return ""
	}
	path := openDialog(prestackHwnd)
	if path == "" {
		return ""
	}
	if !prestackSessionIdentityMatches(id, path) {
		message(prestackHwnd, label, "文件大小或修改时间与快照不一致，已取消恢复。", MB_OK|MB_ICONERROR)
		return ""
	}
	return path
}

func openPrestackSessionSnapshot() {
	path := openPrestackSessionDialog(prestackHwnd)
	if path == "" {
		return
	}
	snapshot, err := prestacksession.Load(path)
	if err != nil {
		message(prestackHwnd, "打开流程", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	aPath := resolvePrestackSessionSource(snapshot.A, "打开流程", true)
	if aPath == "" {
		message(prestackHwnd, "打开流程", "无法定位或验证 A 文件，当前工作区未改变。", MB_OK|MB_ICONINFORMATION)
		return
	}
	if prestackPendingSession != nil {
		prestackPendingSession = nil
	}
	clone := snapshot.Clone()
	if !strings.EqualFold(filepath.Clean(aPath), filepath.Clean(snapshot.A.Path)) {
		clone.A.Path = aPath
	}
	prestackPendingSession = &clone
	if !openApplicationPath(aPath, workspaceModePrestack) {
		prestackPendingSession = nil
		return
	}
	setPrestackStatus("正在恢复叠前流程…")
}

func applyPrestackSessionDisplay(s prestacksession.Snapshot) {
	prestackState.palette = prestackPaletteIndex(s.Display.Palette)
	prestackState.display = 0
	if strings.EqualFold(s.Display.Mode, "wiggle") {
		prestackState.display = 1
	}
	prestackState.wiggleDecimation = s.Display.WiggleDecimation
	prestackState.gain = s.Display.GainPercent
	prestackState.agc = s.Display.AGC
	prestackState.compareUseLimits = s.Display.RangeEnabled
	prestackState.compareLimitMin, prestackState.compareLimitMax = s.Display.RangeMin, s.Display.RangeMax
	prestackState.sampleFirst, prestackState.sampleLast = s.Display.SampleStart, s.Display.SampleEnd
	prestackState.viewFirst, prestackState.viewLast = int(s.Display.ViewFirst), int(s.Display.ViewLast)
	prestackState.compareFilterMask = prestackSessionMask(s.Compare.Filter)
	prestackState.compareViewFirst, prestackState.compareViewLast = int(s.Compare.ViewXMin), int(s.Compare.ViewXMax)
	prestackState.compareViewSampleFirst, prestackState.compareViewSampleLast = s.Compare.SampleStart, s.Compare.SampleEnd
	setText(prestackUI.gain, fmt.Sprintf("%.0f%%", prestackState.gain))
	pSendMessageW.Call(prestackUI.palette, CB_SETCURSEL, uintptr(prestackState.palette), 0)
	pSendMessageW.Call(prestackUI.display, CB_SETCURSEL, uintptr(prestackState.display), 0)
	pSendMessageW.Call(prestackUI.wiggleDecim, CB_SETCURSEL, uintptr(clampInt(prestackState.wiggleDecimation, 0, 4)), 0)
	pSendMessageW.Call(prestackUI.agc, BM_SETCHECK, map[bool]uintptr{true: BST_CHECKED, false: 0}[prestackState.agc], 0)
	setText(prestackUI.compareRangeMin, fmt.Sprintf("%g", prestackState.compareLimitMin))
	setText(prestackUI.compareRangeMax, fmt.Sprintf("%g", prestackState.compareLimitMax))
	for i, c := range []uintptr{prestackUI.compareFilterMatched, prestackUI.compareFilterAOnly, prestackUI.compareFilterBOnly, prestackUI.compareFilterAmbiguous, prestackUI.compareFilterInvalid} {
		pSendMessageW.Call(c, BM_SETCHECK, map[bool]uintptr{true: BST_CHECKED, false: 0}[prestackState.compareFilterMask&(1<<uint(i)) != 0], 0)
	}
}

func restorePrestackSessionAfterIndex(s prestacksession.Snapshot) {
	prestackState.mapping = s.AMapping
	prestackState.offsetBinSize = s.Selection.OffsetBinSize
	prestackState.cmpBinSize = s.Selection.CMPBin.Size
	prestackState.azimuthBinSize = s.Selection.AzimuthBinSize
	applyPrestackSessionDisplay(s)
	saved := s.Selection
	saved.Keys = append([]prestackcore.GatherKey(nil), s.Selection.Keys...)
	prestackState.selection = saved
	selectPrestackGatherType(saved.Type)
	prestackState.selection = saved
	target := 0
	for i, key := range prestackState.keys {
		if key == saved.Key {
			target = i
			break
		}
	}
	if len(prestackState.keys) > 0 {
		prestackState.keyIndex = target
		pSendMessageW.Call(prestackUI.key, CB_SETCURSEL, uintptr(target), 0)
	}
	applyPrestackGatherSelection()
	prestackState.sampleFirst, prestackState.sampleLast = s.Display.SampleStart, s.Display.SampleEnd
	prestackState.viewFirst, prestackState.viewLast = int(s.Display.ViewFirst), int(s.Display.ViewLast)
	setPrestackPage(s.Page)
	layoutPrestackControls()
}

func applyPendingPrestackSessionCompare() {
	if prestackPendingSessionCompare == nil {
		return
	}
	s := prestackPendingSessionCompare.Clone()
	prestackPendingSessionCompare = nil
	prestackState.compareFilterMask = prestackSessionMask(s.Compare.Filter)
	prestackState.compareUseLimits = s.Display.RangeEnabled
	prestackState.compareLimitMin, prestackState.compareLimitMax = s.Display.RangeMin, s.Display.RangeMax
	if prestackState.page != s.Page {
		setPrestackPage(s.Page)
	}
	applyPrestackSessionDisplay(s)
	startPrestackCompareRender()
}
