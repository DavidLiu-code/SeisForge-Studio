//go:build windows

package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segyanalysis"
)

// The anchor is a physical pick, independent of whichever header or curve is
// visible. Its coordinates can be resolved again when B or its mapping changes.
type tracePoststackAnchor struct {
	Owner             uintptr
	SourceA           string
	Mode              int
	Role              string
	Trace             int64
	Inline, Crossline int32
	Geometry          bool
	Marker            int
	MarkerTimeUS      float64 // physical time on the panel where the pick was made
	HasMarkerTime     bool
}

// Exclude render/style changes (palette, gain, resize). Only a committed
// data/mapping/view change or an explicit analysis request changes this key.
type tracePoststackVersion struct {
	SNRFormula               uint8
	Owner                    uintptr
	Data                     *compareTimeData
	Epoch, Dataset, Geometry int64
	A, B                     uint64
	Filter                   [2]SideFilterState
	First, Last              int
	BPath                    string
	Compare                  bool
	Scope                    traceAnalysisScope
}

func currentTracePoststackVersion() tracePoststackVersion {
	k := viewFilterCurrentKey()
	v := tracePoststackVersion{
		SNRFormula: segyanalysis.ReferenceSNRFormulaVersion,
		Owner:      k.owner, Data: k.data, Epoch: k.epoch, Dataset: k.dataset, Geometry: k.geometry,
		A: k.aVersion, B: k.bVersion, Filter: viewFilter.active, First: k.first, Last: k.last,
		BPath: k.b, Compare: compareWorkspaceActive, Scope: traceAnalysisWindow,
	}
	if v.Scope.mode != traceAnalysisFollow {
		v.First, v.Last = 0, 0 // applied scope is independent of main vertical navigation
	}
	return v
}

func tracePoststackAnchorFor(selection traceAnalysisSelection, panel int) tracePoststackAnchor {
	role := "A"
	if panel == 1 {
		role = "B"
	}
	for _, t := range selection.Targets {
		if t.Role != role {
			continue
		}
		marker := t.MarkerSample
		if role == "B" && compareMode != 2 {
			offset, _ := generatedPoststackSourceStart(t.Path)
			marker += offset
		}
		return tracePoststackAnchor{Owner: compareHwnd, SourceA: compareAPath, Mode: compareMode, Role: role, Trace: t.Trace, Inline: t.Inline, Crossline: t.Crossline, Geometry: t.HasGeometry, Marker: marker}
	}
	return tracePoststackAnchor{}
}

func resolveTracePoststackAnchor(anchor tracePoststackAnchor) (traceAnalysisSelection, bool) {
	if anchor.Owner == 0 || anchor.Owner != compareHwnd || !strings.EqualFold(anchor.SourceA, compareAPath) {
		return traceAnalysisSelection{}, false
	}
	s := traceAnalysisSelection{Anchor: anchor, FromPoststackView: true, CompareGroup: compareWorkspaceActive}
	if !anchor.Geometry {
		if sf == nil || anchor.Role != "A" {
			return s, false
		}
		if t, err := compareAnalysisTarget("A", sf, anchor.Trace, anchor.Marker, 0, 0); err == nil {
			t.HasGeometry = false
			s.Targets = []traceAnalysisTarget{t}
			s.Context = fmt.Sprintf("二维剖面 · A 物理道 %d", t.Trace+1)
			if s.CompareGroup {
				s.PairNote = "无已验证的共同几何，不能配对 B"
			}
			return captureTracePoststackFilter(s), true
		}
		return s, false
	}
	il, xl := anchor.Inline, anchor.Crossline
	s.Context = fmt.Sprintf("IL %d / XL %d", il, xl)
	d := compareTD
	if d == nil {
		s.PairNote = "当前几何未就绪，不能读取此 IL/XL"
		return s, true
	}
	var aRefFirst, aRefLast int64
	haveAWindow := false
	if d.fa != nil && d.ga != nil {
		if n, ok := d.ga.TraceAt(il, xl); ok {
			if t, err := compareAnalysisTarget("A", d.fa, n, anchor.Marker, il, xl); err == nil {
				s.Targets = append(s.Targets, t)
				if header, e := d.fa.ReadTraceHeader(n); e == nil {
					count, dt := traceAnalysisHeaderAxis(d.fa.Info, header)
					if origin, e := d.fa.TraceTimeOriginUS(n); e == nil && dt > 0 && t.SampleStart < count {
						aRefFirst = origin + int64(t.SampleStart)*int64(dt)
						aRefLast = origin + int64(min(t.SampleEnd, count-1))*int64(dt)
						haveAWindow = true
					}
				}
			}
		}
	}
	hasB := s.CompareGroup && compareActiveB() && d.fb != nil && d.gb != nil
	if hasB {
		if !compareDifferenceGeometryKnown(d) && anchor.Role != "B" {
			s.PairNote = "无已验证的共同几何，不能配对 B"
		} else if n, ok := d.gb.TraceAt(il, xl); ok {
			if t, err := compareAnalysisTarget("B", d.fb, n, anchor.Marker, il, xl); err == nil {
				t.PhysicalWindow, t.FirstUS, t.LastUS = haveAWindow, aRefFirst, aRefLast
				s.Targets = append(s.Targets, t)
			}
		} else {
			s.PairNote = "B 在此 IL/XL 没有对应道"
		}
	} else if s.CompareGroup {
		s.PairNote = "B 未加载或尚未就绪"
	}
	if hasB && anchor.Role == "B" && !compareDifferenceGeometryKnown(d) {
		s.Targets = slices.DeleteFunc(s.Targets, func(t traceAnalysisTarget) bool { return t.Role == "A" })
		s.PairNote = "无已验证的共同几何，不能配对 A"
	}
	s.Difference = s.CompareGroup && len(s.Targets) == 2 && compareDifferenceGeometryKnown(d)
	if len(s.Targets) == 0 {
		s.PairNote = "当前数据在此 IL/XL 没有可用道"
		return s, true
	}
	if s.CompareGroup && len(s.Targets) == 1 && s.PairNote == "" {
		s.PairNote = "另一侧在此 IL/XL 没有对应道"
	}
	if index := slices.IndexFunc(s.Targets, func(t traceAnalysisTarget) bool { return t.Role == anchor.Role }); index < 0 {
		s.Anchor.Role, s.Anchor.Trace = s.Targets[0].Role, s.Targets[0].Trace
	} else {
		s.Anchor.Trace = s.Targets[index].Trace
	}
	for _, t := range s.Targets {
		s.Context += fmt.Sprintf(" · %s 物理道 %d", t.Role, t.Trace+1)
	}
	return captureTracePoststackFilter(s), true
}

func captureTracePoststackFilter(s traceAnalysisSelection) traceAnalysisSelection {
	s.UnfilteredContext = s.Context
	frame := committedViewFilter()
	for i := range s.Targets {
		t := &s.Targets[i]
		j := 0
		if t.Role == "B" {
			j = 1
		}
		state := viewFilter.active[j]
		if frame != nil && strings.EqualFold(frame.rows[j].path, t.Path) {
			state = frame.settings[j]
		}
		if state.Enabled {
			t.IsFiltered, t.Bandpass = true, state.Params
			s.Context += fmt.Sprintf(" · %s 已滤波", t.Role)
		}
		if frame != nil {
			row := &frame.rows[j]
			if strings.EqualFold(row.path, t.Path) && row.values[t.Trace] != nil {
				t.SampleStart, t.SampleEnd = row.first, row.last
				t.PhysicalWindow = false // already resolved by the committed display frame
				t.MarkerSample = clampInt(t.MarkerSample, row.first, row.last)
				if state.Enabled {
					t.Filtered = row
				}
			}
		}
	}
	return s
}

func freezeTraceAnalysisSelection(s traceAnalysisSelection, full bool) traceAnalysisSelection {
	s = cloneTraceAnalysisSelection(s)
	for i := range s.Targets {
		t := &s.Targets[i]
		if t.Filtered != nil {
			if !full {
				if row, err := t.Filtered.read(t.Trace, t.SampleStart, t.SampleEnd); err == nil {
					t.FrozenSamples = slices.Clone(row)
				}
			}
			t.Filtered = nil
		}
	}
	return s
}

func freezeTraceAnalysisInScope(s traceAnalysisSelection, scope traceAnalysisScope) traceAnalysisSelection {
	s = freezeTraceAnalysisSelection(s, scope.mode != traceAnalysisFollow)
	s.Window = scope
	if scope.mode != traceAnalysisManual {
		return s
	}
	for i := range s.Targets {
		t := &s.Targets[i]
		t.FrozenSamples = nil // a different physical window can have the same length
		t.WindowUnavailable = false
		t.PhysicalWindow, t.ManualWindow = true, true
		t.FirstUS, t.LastUS = scope.firstUS, scope.lastUS
	}
	return s
}

func tracePoststackNavigate(anchor tracePoststackAnchor, physical int64, step int64, edit bool) (tracePoststackAnchor, bool) {
	if !anchor.Geometry {
		if sf == nil {
			return anchor, false
		}
		if !edit {
			physical = anchor.Trace + step
		}
		if physical < 0 || physical >= sf.Info.TraceCount {
			return anchor, false
		}
		anchor.Trace = physical
		return anchor, true
	}
	if compareTD == nil || anchor.Mode < 0 || anchor.Mode > 1 {
		return anchor, false
	}
	g := compareTD.ga
	if anchor.Role == "B" {
		g = compareTD.gb
	}
	if g == nil {
		return anchor, false
	}
	line := anchor.Inline
	if anchor.Mode == 1 {
		line = anchor.Crossline
	}
	traces, coords, actual, err := g.LineTraceNumbers(anchor.Mode, line)
	if err != nil || actual != line {
		return anchor, false
	}
	index := slices.Index(traces, physical)
	if !edit {
		index = slices.Index(traces, anchor.Trace)
		if index >= 0 {
			index += int(step)
		}
	}
	if index < 0 || index >= len(coords) {
		return anchor, false
	}
	anchor.Trace = traces[index]
	if anchor.Mode == 0 {
		anchor.Crossline = coords[index]
	} else {
		anchor.Inline = coords[index]
	}
	return anchor, true
}

func refreshScientificTraceFromCompare() {
	if traceAnalysisHwnd == 0 || traceAnalysisSelectionCurrent.Anchor.Owner == 0 {
		return
	}
	if traceAnalysisSelectionCurrent.Anchor.Owner != compareHwnd || !strings.EqualFold(traceAnalysisSelectionCurrent.Anchor.SourceA, compareAPath) {
		pDestroyWindow.Call(traceAnalysisHwnd)
		return
	}
	version := currentTracePoststackVersion()
	if version == traceAnalysisRequestedVersion {
		return
	}
	if s, ok := resolveTracePoststackAnchor(traceAnalysisSelectionCurrent.Anchor); ok {
		traceAnalysisSelectionCurrent = s
		startTraceAnalysisLoad()
	}
}
