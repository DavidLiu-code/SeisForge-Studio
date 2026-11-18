//go:build windows

package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

type compareHeaderMapping struct{ il, xl int }

func parseCompareHeaderMapping(il, xl string) (compareHeaderMapping, error) {
	a, ea := strconv.Atoi(strings.TrimSpace(il))
	b, eb := strconv.Atoi(strings.TrimSpace(xl))
	if ea != nil || eb != nil || a < 1 || a > 237 || b < 1 || b > 237 {
		return compareHeaderMapping{}, fmt.Errorf("IL / XL 起始字节须为 1–237 的整数（1 起算、有符号 32 位，读取 4 字节）")
	}
	if a == b {
		return compareHeaderMapping{}, fmt.Errorf("IL、XL 不能读取同一个起始字节")
	}
	return compareHeaderMapping{a, b}, nil
}

func (m compareHeaderMapping) swap() compareHeaderMapping { return compareHeaderMapping{m.xl, m.il} }
func (m compareHeaderMapping) valid() bool {
	return m.il >= 1 && m.il <= 237 && m.xl >= 1 && m.xl <= 237 && m.il != m.xl
}

type compareGeometrySourceMapping struct {
	path            string
	applied, opened compareHeaderMapping
}

type compareGeometryRebuild struct {
	aPath, bPath string
	a, b         compareHeaderMapping
	swapA        bool
}

var compareGeometrySourceA, compareGeometrySourceB compareGeometrySourceMapping

func compareSourceMapping(path string, s compareGeometrySourceMapping) (compareHeaderMapping, bool) {
	return s.applied, path != "" && s.path == path && s.applied.valid()
}

func saveCompareSourceMapping(s *compareGeometrySourceMapping, path string, m compareHeaderMapping) {
	if path == "" || !m.valid() {
		return
	}
	if s.path != path || !s.opened.valid() {
		s.opened = m
	}
	s.path, s.applied = path, m
}

func compareEffectiveHeaderMappings() (a, b compareHeaderMapping, ok bool) {
	a, err := parseCompareHeaderMapping(getText(cc.ilByte), getText(cc.xlByte))
	if err != nil {
		setText(cc.status, err.Error())
		return a, b, false
	}
	if compareAutoGeom && a == (compareHeaderMapping{compareAutoILA, compareAutoXLA}) {
		b = compareHeaderMapping{compareAutoILB, compareAutoXLB}
	}
	if saved, has := compareSourceMapping(compareAPath, compareGeometrySourceA); has {
		a = saved
	}
	if saved, has := compareSourceMapping(compareBPath, compareGeometrySourceB); has {
		b = saved
	}
	if !b.valid() {
		// Initial detection failed. Try the standard header for B without
		// treating subsequent A changes as a manual override of B.
		b = compareHeaderMapping{189, 193}
	}
	return a, b, true
}

func compareCurrentPosition(d *compareTimeData) (il, xl int32, trace int64, ok bool) {
	if d == nil || d.ga == nil {
		return 0, 0, 0, false
	}
	g := d.ga
	if compareMode == 2 && compareCrosshairValid {
		il, xl = int32(compareCrosshairWorldY), int32(compareCrosshairWorldX)
		trace, ok = g.TraceAt(il, xl)
		if ok {
			return il, xl, trace, true
		}
	}
	mode := compareMode
	if mode == 2 {
		mode = 0
	}
	traces, coords, actual, err := g.LineTraceNumbers(mode, compareLineCoord)
	if err != nil {
		return 0, 0, 0, false
	}
	if start, end, step := compareTraceRangeForGeometry(d.fa); end >= start {
		traces, coords = filterPhysicalTraceRange(traces, coords, start, end, step)
	}
	var across int32
	if compareCrosshairValid && compareMode != 2 {
		trace, across, ok = nearestCompareLineTrace(traces, coords, compareCrosshairWorldX)
	} else if len(coords) > 0 {
		trace, across, ok = traces[0], coords[0], true
	}
	if mode == 0 {
		return actual, across, trace, ok
	}
	return across, actual, trace, ok
}

func compareGeometryAtTrace(g *segy.GeometryIndex, trace int64) (il, xl int32, ok bool) {
	if g == nil {
		return
	}
	for i, physical := range g.TraceNumbers {
		if trace != physical || i >= len(g.RowOfTrace) || i >= len(g.ColOfTrace) {
			continue
		}
		r, c := g.RowOfTrace[i], g.ColOfTrace[i]
		if r >= 0 && int(r) < len(g.InlineValues) && c >= 0 && int(c) < len(g.CrosslineValues) {
			return g.InlineValues[r], g.CrosslineValues[c], true
		}
	}
	return
}

func compareModeLines(g *segy.GeometryIndex, mode int) []int32 {
	if g == nil {
		return nil
	}
	if mode == 0 {
		return g.InlineValues
	}
	return g.CrosslineValues
}

func compareNavigationLines(d *compareTimeData, mode int) []int32 {
	if d == nil || d.ga == nil || mode < 0 || mode >= 2 {
		return nil
	}
	g := d.ga
	start, end, step := compareTraceRangeForGeometry(d.fa)
	if d.fa == nil || (start == 0 && end >= d.fa.Info.TraceCount-1 && step == 1) {
		return compareModeLines(g, mode)
	}
	key := [3]int64{start, end, step}
	if !d.navValid || d.navKey != key {
		rows, cols := make([]bool, len(g.InlineValues)), make([]bool, len(g.CrosslineValues))
		for i, physical := range g.TraceNumbers {
			if physical < start || physical > end || (physical-start)%step != 0 || i >= len(g.RowOfTrace) || i >= len(g.ColOfTrace) {
				continue
			}
			if r := g.RowOfTrace[i]; r >= 0 && int(r) < len(rows) {
				rows[r] = true
			}
			if c := g.ColOfTrace[i]; c >= 0 && int(c) < len(cols) {
				cols[c] = true
			}
		}
		for axis, mask := range [][]bool{rows, cols} {
			d.navLines[axis] = d.navLines[axis][:0]
			values := compareModeLines(g, axis)
			for i, present := range mask {
				if present {
					d.navLines[axis] = append(d.navLines[axis], values[i])
				}
			}
		}
		d.navKey, d.navValid = key, true
	}
	return d.navLines[mode]
}

func compareLineRank(values []int32, id int32) (int, bool) {
	i := sort.Search(len(values), func(i int) bool { return values[i] >= id })
	return i, i < len(values) && values[i] == id
}

// B sample 0 represents sample offset of A. Intersect windows in A's native
// sample index, never by copying data or fabricating local samples.
func compareSourceSampleOverlap(first, last, countA, offset, countB int) (int, int, bool) {
	if countA < 1 || countB < 1 || offset < 0 || offset >= countA {
		return 0, 0, false
	}
	if last < 0 || last >= countA {
		last = countA - 1
	}
	first = maxInt(first, offset)
	last = minInt(last, offset+minInt(countB, countA-offset)-1)
	return first, last, first <= last
}

func compareDifferenceGeometryKnown(d *compareTimeData) bool {
	return d != nil && (!d.traceOrderFallback && !d.bTraceOrderFallback ||
		d.fb != nil && generatedPoststackTraceOrder(d.fb.Info.Path) != nil)
}
func compareLineCandidates(values []int32, wanted int32) string {
	if len(values) == 0 {
		return "无可用编号"
	}
	idx, exact := compareLineRank(values, wanted)
	if exact || idx == 0 {
		return fmt.Sprint(values[idx])
	}
	if idx == len(values) {
		return fmt.Sprint(values[idx-1])
	}
	return fmt.Sprintf("%d / %d", values[idx-1], values[idx])
}

func compareExactNavigationPosition(g *segy.GeometryIndex, il, xl int32) (int64, error) {
	if g == nil {
		return 0, fmt.Errorf("几何索引尚未就绪")
	}
	if _, ok := compareLineRank(g.InlineValues, il); !ok {
		return 0, fmt.Errorf("不存在 Inline %d；邻近可用编号：%s", il, compareLineCandidates(g.InlineValues, il))
	}
	if _, ok := compareLineRank(g.CrosslineValues, xl); !ok {
		return 0, fmt.Errorf("不存在 Crossline %d；邻近可用编号：%s", xl, compareLineCandidates(g.CrosslineValues, xl))
	}
	if g.DuplicateCount > 0 {
		ri, _ := compareLineRank(g.InlineValues, il)
		ci, _ := compareLineRank(g.CrosslineValues, xl)
		seen := false
		for index := range g.TraceNumbers {
			if index >= len(g.RowOfTrace) || index >= len(g.ColOfTrace) || g.RowOfTrace[index] != int32(ri) || g.ColOfTrace[index] != int32(ci) {
				continue
			}
			if seen {
				return 0, fmt.Errorf("Inline %d / Crossline %d 有重复道，无法唯一定位", il, xl)
			}
			seen = true
		}
	}
	if trace, ok := g.TraceAt(il, xl); ok {
		return trace, nil
	}
	return 0, fmt.Errorf("Inline %d / Crossline %d 没有数据道；当前剖面保持不变", il, xl)
}

var compareGeometryNavigationProc uintptr

func compareGeometryNavigationWndProc(hwnd uintptr, msg uint32, wParam, lParam, _, _ uintptr) uintptr {
	switch msg {
	case WM_KEYDOWN:
		if wParam == VK_RETURN {
			if d := activeCompareGeometryDialog; d != nil && (hwnd == d.inlineNumber || hwnd == d.crosslineNumber) {
				d.locate()
				return 0
			}
		}
	case 0x0082: // WM_NCDESTROY
		algorithmRemoveSubclass.Call(hwnd, compareGeometryNavigationProc, 1)
	}
	r, _, _ := algorithmDefSubclass.Call(hwnd, uintptr(msg), wParam, lParam)
	return r
}

func initCompareGeometryNavigation(d *compareGeometryDialog) {
	if compareGeometryNavigationProc == 0 {
		compareGeometryNavigationProc = syscall.NewCallback(compareGeometryNavigationWndProc)
	}
	for _, h := range []uintptr{d.inlineNumber, d.crosslineNumber} {
		algorithmSetSubclass.Call(h, compareGeometryNavigationProc, 1, 0)
	}
	refreshCompareNavigationFields()
}

func refreshCompareNavigationFields() {
	d := activeCompareGeometryDialog
	if d == nil || d.aPath != compareAPath || d.owner != compareHwnd {
		return
	}
	il, xl, _, ok := compareCurrentPosition(compareTD)
	if !ok {
		return
	}
	for i, h := range [2]uintptr{d.inlineNumber, d.crosslineNumber} {
		// A render/hover must not overwrite a manually edited (possibly not
		// yet valid) navigation value. Relocation synchronizes on success.
		if text := getText(h); text != d.navPublished[i] {
			continue
		}
		next := strconv.FormatInt(int64([2]int32{il, xl}[i]), 10)
		if next != d.navPublished[i] {
			setText(h, next)
			d.navPublished[i] = next
		}
	}
}

func parseCompareNavigationNumbers(ilText, xlText string) (int32, int32, error) {
	il, e1 := strconv.ParseInt(strings.TrimSpace(ilText), 10, 32)
	xl, e2 := strconv.ParseInt(strings.TrimSpace(xlText), 10, 32)
	if e1 != nil || e2 != nil {
		return 0, 0, fmt.Errorf("Inline / Crossline 编号须为有效整数")
	}
	return int32(il), int32(xl), nil
}

func locateCompareNavigation(il, xl int32) error {
	if compareTD == nil || compareTD.ga == nil {
		return fmt.Errorf("几何索引尚未就绪")
	}
	trace, err := compareExactNavigationPosition(compareTD.ga, il, xl)
	if err == nil {
		if start, end, step := compareTraceRangeForGeometry(compareTD.fa); end >= start && (trace < start || trace > end || (trace-start)%step != 0) {
			err = fmt.Errorf("该位置的道不在当前物理道选择内")
		}
	}
	if err != nil {
		return err
	}
	compareCrosshairValid = true
	switch compareMode {
	case 0:
		compareLineCoord, compareCrosshairWorldX = il, float64(xl)
	case 1:
		compareLineCoord, compareCrosshairWorldX = xl, float64(il)
	default:
		compareCrosshairWorldX, compareCrosshairWorldY = float64(xl), float64(il)
		startLoadTimeSlice(compareTD.sampleA)
		invalidateCompareBase()
		return nil
	}
	// A location outside the current horizontal zoom must not select a
	// different trace. Fit only that axis, leaving the sample window intact.
	if compareCrosshairWorldX < compareLineXMin || compareCrosshairWorldX > compareLineXMax {
		resetCompareLineBounds()
	}
	if compareCrosshairWorldY < float64(compareSampleStart) || compareCrosshairWorldY > float64(compareSampleEnd) {
		compareCrosshairWorldY = float64(compareSampleStart)
	}
	renderCompareLine()
	return nil
}
