//go:build windows

package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

func TestPrestackCompareLayoutAppliesEachControlOnce(t *testing.T) {
	u, names := prestackNarrowFakeControls()
	for _, width := range []int{640, 940, 1360, 1680} {
		for _, hasB := range []bool{false, true} {
			for _, loading := range []bool{false, true} {
				state := prestackSession{page: 3, compareBLoading: loading, display: 1}
				if hasB {
					state.compareBDataset = &dataset.SeismicDataset{}
				}
				before := state
				calls := make(map[uintptr]int)
				prestackControlsLayoutForSize(width, 600, u, &state, func(c uintptr, _, _, _, _ int, _ bool) {
					calls[c]++
					if calls[c] != 1 {
						t.Fatalf("control %s is applied more than once: width=%d B=%v loading=%v", names[c], width, hasB, loading)
					}
				})
				for c, name := range names {
					if calls[c] != 1 {
						t.Fatalf("control %s is missing from final layout", name)
					}
				}
				if !reflect.DeepEqual(state, before) {
					t.Fatal("pure layout mutated the session")
				}
			}
		}
	}
}

func TestPrestackCompareRangeControlsAreHiddenOnEveryOtherPage(t *testing.T) {
	u, _ := prestackNarrowFakeControls()
	controls := []uintptr{u.compareRangeLimit, u.compareRangeMin, u.compareRangeMax, u.compareRangeApply}
	state := prestackSession{page: 3, compareBDataset: &dataset.SeismicDataset{}}
	previous := prestackNarrowPlacements(1680, 880, u, &state)
	for _, c := range controls {
		if !previous[c].visible {
			t.Fatal("Compare range control was not initially visible")
		}
	}
	for _, page := range []int{0, 1, 2, 4, 3, 0} {
		state.page = page
		next := prestackNarrowPlacements(1680, 880, u, &state)
		for _, c := range controls {
			p, exists := next[c]
			if !exists || p.visible != (page == 3) {
				t.Fatalf("Compare range control visibility not explicitly updated on page %d", page)
			}
		}
	}
}

func TestPrestackCompareSummaryHasTwoNonWrappingRows(t *testing.T) {
	if prestackCompareSummaryFlags&DT_SINGLELINE == 0 || prestackCompareSummaryFlags&DT_WORDBREAK != 0 || prestackCompareSummaryFlags&0x8000 == 0 {
		t.Fatal("summary must draw single-line ellipsis rather than wrapping")
	}
	for _, size := range [][2]int{{320, 240}, {640, 480}, {800, 600}, {940, 700}, {1360, 880}, {1680, 1089}} {
		layout := prestackPanelLayoutForSize(size[0], size[1], 3)
		content := prestackLayoutRECT(layout.Content)
		rows := prestackCompareSummaryRects(content)
		if rows[0].Top != content.Top || rows[0].Bottom != rows[1].Top || rows[1].Bottom-content.Top != prestackCompareSummaryHeight {
			t.Fatalf("summary is not two compact rows at %v: %+v", size, rows)
		}
		panels := prestackComparePanelRectsForContent(content, prestackCompareSummaryHeight)
		if panels[0].Right > panels[0].Left && panels[0].Top != rows[1].Bottom+6 {
			t.Fatalf("panel overlaps summary or leaves excess whitespace at %v: summary=%+v panels=%+v", size, rows, panels)
		}
	}
	state := prestackSession{
		dataset:         &dataset.SeismicDataset{Path: strings.Repeat("long-name", 20) + ".sgy"},
		gather:          prestackcore.GatherResult{TraceIndices: []int64{0, 1, 2}},
		compareBDataset: &dataset.SeismicDataset{Path: "B.sgy"},
		compareBIndex:   &prestackcore.PrestackIndex{Records: make([]prestackcore.PrestackTraceRecord, 60)},
		compareBMatch: prestackcore.CompareMatchResult{
			Pairs: make([]prestackcore.TraceMatchPair, 2), AOnly: []int64{2},
			BOnly: []int64{3}, AmbiguousB: []int64{4, 5}, InvalidB: []int64{6},
		},
		compareBAxis: prestackcore.SampleAxisCompatibility{Reason: strings.Repeat("long reason\r\n", 30)},
	}
	lines := prestackCompareSummaryLines(state)
	if len(lines) != 2 || !strings.Contains(lines[0], "当前道集 A/B 3/6 道") {
		t.Fatalf("summary must compare current gather counts, not total B file records: %q", lines)
	}
	for _, line := range lines {
		if strings.ContainsAny(line, "\r\n") {
			t.Fatalf("summary must not embed multiline diagnostics: %q", line)
		}
	}
	if !strings.Contains(lines[0], "…") || !strings.Contains(lines[1], "差 禁用") {
		t.Fatalf("long names or disabled Delta status missing: %q", lines)
	}
	state.compareBMatch.Pairs = nil
	state.compareBAxis.Compatible = true
	lines = prestackCompareSummaryLines(state)
	if !strings.Contains(lines[1], "差 禁用") || !strings.Contains(lines[1], "无匹配道") {
		t.Fatalf("compatible axes alone must not advertise Delta for no matching traces: %q", lines)
	}
	state.compareBDataset = nil
	for _, loading := range []bool{false, true} {
		state.compareBLoading = loading
		if lines = prestackCompareSummaryLines(state); len(lines) != 2 {
			t.Fatalf("unloaded/loading B summary has %d rows", len(lines))
		}
	}
}
