//go:build windows

package main

import (
	"bytes"
	"testing"

	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

// The Compare scene is allowed to use the content rectangle only.  Keep this
// test independent from a live HWND so it also exercises the same geometry
// contract used by resize handling.
func TestPrestackComparePanelContractAcrossClientSizes(t *testing.T) {
	for _, tc := range []struct {
		name string
		w, h int
	}{
		{name: "wide", w: 1680, h: 1089},
		{name: "default", w: 1360, h: 880},
		{name: "narrow", w: 1120, h: 700},
		{name: "small", w: 800, h: 520},
		{name: "minimum", w: 320, h: 240},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := prestackPanelLayoutForSize(tc.w, tc.h, 3)
			if l.Content.Right <= l.Content.Left || l.Content.Bottom <= l.Content.Top {
				t.Fatalf("invalid compare content rectangle: %+v", l.Content)
			}
			textHeight := 112
			if tc.w < 900 {
				textHeight = 176
			}
			content := RECT{Left: int32(l.Content.Left), Top: int32(l.Content.Top), Right: int32(l.Content.Right), Bottom: int32(l.Content.Bottom)}
			panels := prestackComparePanelRectsForContent(content, textHeight)
			if panels[0].Right <= panels[0].Left || panels[0].Bottom <= panels[0].Top {
				// The UI must show an empty-state message rather than create
				// negative/overlapping panels when the client is too small.
				return
			}
			var previous layoutRect
			for i := 0; i < 3; i++ {
				p := layoutRect{Left: int(panels[i].Left), Top: int(panels[i].Top), Right: int(panels[i].Right), Bottom: int(panels[i].Bottom)}
				if p.Left < l.Content.Left || p.Right > l.Content.Right || p.Top < l.Content.Top || p.Bottom > l.Content.Bottom {
					t.Fatalf("panel %d escapes content: panel=%+v content=%+v", i, p, l.Content)
				}
				overlapX := p.Left < previous.Right && previous.Left < p.Right
				overlapY := p.Top < previous.Bottom && previous.Top < p.Bottom
				if i > 0 && overlapX && overlapY {
					t.Fatalf("panel %d overlaps previous panel: previous=%+v current=%+v", i, previous, p)
				}
				previous = p
			}
			if int(panels[2].Bottom) > l.Footer.Top {
				t.Fatalf("compare panels overlap footer: bottom=%d footerTop=%d", panels[2].Bottom, l.Footer.Top)
			}
		})
	}
}

func TestPrestackCompareRenderTokenRejectsOwnerPageAndDimensions(t *testing.T) {
	state := prestackSession{
		ownerToken: 2, workspaceGeneration: 3, datasetGeneration: 4,
		selectionGeneration: 5, mappingGeneration: 6,
		compareBGeneration: 7, compareBMappingGeneration: 8,
		compareBDatasetGeneration: 17,
		compareModeGeneration:     9, compareDisplayGeneration: 10,
		resizeGeneration: 11, sampleFirst: 12, sampleLast: 99, page: 3,
	}
	base := &prestackCompareRenderResult{
		owner: 17, ownerToken: 2, workspaceGeneration: 3,
		datasetGeneration: 4, selectionGeneration: 5, aMappingGeneration: 6,
		bGeneration: 7, bMappingGeneration: 8, compareModeGeneration: 9,
		bDatasetGeneration: 17,
		displayGeneration:  10, resizeGeneration: 11, sampleFirst: 12,
		sampleLast: 99, comparePage: 3, width: 20, height: 30,
	}
	if !prestackCompareRenderMatches(base, state, 17, 20, 30) {
		t.Fatal("current compare token was rejected")
	}
	cases := []struct {
		name   string
		mutate func(*prestackCompareRenderResult, *prestackSession, *int, *int)
	}{
		{name: "owner token", mutate: func(_ *prestackCompareRenderResult, s *prestackSession, _ *int, _ *int) { s.ownerToken++ }},
		{name: "page", mutate: func(_ *prestackCompareRenderResult, s *prestackSession, _ *int, _ *int) { s.page = 0 }},
		{name: "panel height", mutate: func(_ *prestackCompareRenderResult, _ *prestackSession, _ *int, h *int) { *h = 31 }},
		{name: "zero result width", mutate: func(r *prestackCompareRenderResult, _ *prestackSession, _ *int, _ *int) { r.width = 0 }},
		{name: "zero result height", mutate: func(r *prestackCompareRenderResult, _ *prestackSession, _ *int, _ *int) { r.height = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := *base
			s := state
			w, h := 20, 30
			tc.mutate(&r, &s, &w, &h)
			if prestackCompareRenderMatches(&r, s, 17, w, h) {
				t.Fatalf("stale compare result accepted for %s", tc.name)
			}
		})
	}
	if prestackCompareRenderMatches(nil, state, 17, 20, 30) {
		t.Fatal("nil compare result accepted")
	}
}

func TestPrestackCompareReportDoesNotContainAmplitudeOrSamples(t *testing.T) {
	result := prestackcore.CompareMatchResult{
		Strategy: prestackcore.MatchKeyCDPOffset,
		Pairs: []prestackcore.TraceMatchPair{{
			ATrace: 1, BTrace: 9, Strategy: prestackcore.MatchKeyCDPOffset,
			Key: prestackcore.TraceMatchKey{Strategy: prestackcore.MatchKeyCDPOffset, CDP: 12, Offset: -40},
		}},
		AOnly: []int64{2}, BOnly: []int64{10},
		AmbiguousA: []int64{3}, AmbiguousB: []int64{11},
		InvalidA: []int64{4}, InvalidB: []int64{12},
	}
	report := prestackcore.BuildCompareMatchReportWithOptions(result, prestackcore.SampleAxisCompatibility{}, prestackcore.CompareMatchReportOptions{
		APath: "a.sgy", BPath: "b.sgy", Selection: "CMP all", IncludePairs: true,
	})
	b, err := report.JSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"amplitude", "samples", "sample_values", "振幅"} {
		if bytes.Contains(bytes.ToLower(b), bytes.ToLower([]byte(forbidden))) {
			t.Fatalf("report contains forbidden payload marker %q: %s", forbidden, b)
		}
	}
}
