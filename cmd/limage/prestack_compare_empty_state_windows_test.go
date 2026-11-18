//go:build windows

package main

import (
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

func TestPrestackCompareDeltaEmptyReasons(t *testing.T) {
	matched := prestackcore.CompareLogicalRow{Classification: prestackcore.CompareRowMatched, HasA: true, HasB: true}
	aOnly := prestackcore.CompareLogicalRow{Classification: prestackcore.CompareRowAOnly, HasA: true}
	bOnly := prestackcore.CompareLogicalRow{Classification: prestackcore.CompareRowBOnly, HasB: true}
	base := prestackSession{
		compareBDataset:    &dataset.SeismicDataset{},
		compareBAxis:       prestackcore.SampleAxisCompatibility{Compatible: true},
		compareBMatch:      prestackcore.CompareMatchResult{Pairs: []prestackcore.TraceMatchPair{{ATrace: 7, BTrace: 2}}},
		compareFilterMask:  3,
		compareRows:        []prestackcore.CompareLogicalRow{matched, aOnly},
		compareDisplayRows: []prestackcore.CompareLogicalRow{matched, aOnly},
		compareWidth:       20,
	}
	tests := []struct {
		name   string
		change func(*prestackSession)
		want   string
	}{
		{"no matches", func(s *prestackSession) { s.compareBMatch.Pairs = nil }, "当前道集无匹配道"},
		{"matched filter disabled", func(s *prestackSession) { s.compareFilterMask = 2 }, "当前筛选无匹配道"},
		{"A-only rows", func(s *prestackSession) { s.compareRows = []prestackcore.CompareLogicalRow{aOnly} }, "当前筛选无匹配道"},
		{"B-only rows", func(s *prestackSession) { s.compareRows = []prestackcore.CompareLogicalRow{bOnly} }, "当前筛选无匹配道"},
		{"empty filter", func(s *prestackSession) { s.compareRows = nil }, "当前筛选无匹配道"},
		{"visible A-only", func(s *prestackSession) { s.compareDisplayRows = []prestackcore.CompareLogicalRow{aOnly} }, "当前视窗无匹配道"},
		{"empty window", func(s *prestackSession) { s.compareDisplayRows = nil }, "当前视窗无匹配道"},
		{"matched row outside sampled columns", func(s *prestackSession) {
			s.compareDisplayRows = []prestackcore.CompareLogicalRow{aOnly, matched, bOnly}
			s.compareWidth = 2
		}, "当前视窗的显示列未包含匹配道"},
		{"loading", func(s *prestackSession) { s.compareBLoading = true; s.compareBMatch.Pairs = nil }, "B 道头索引正在加载"},
		{"B failure", func(s *prestackSession) { s.compareBError = "测试读取失败"; s.compareFilterMask = 0 }, "测试读取失败"},
		{"B closed", func(s *prestackSession) { s.compareBDataset = nil; s.compareRows = nil }, "请先加载"},
		{"incompatible axis", func(s *prestackSession) {
			s.compareBAxis = prestackcore.SampleAxisCompatibility{Reason: "SampleInterval 不一致"}
			s.compareBMatch.Pairs = nil
		}, "SampleInterval 不一致"},
		{"rendering matched rows", func(s *prestackSession) { s.compareRendering = true }, "正在计算差"},
		{"empty filter while rendering", func(s *prestackSession) { s.compareRendering = true; s.compareFilterMask = 0 }, "当前筛选无匹配道"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := base
			test.change(&state)
			message := prestackCompareDeltaEmptyMessage(state)
			if !strings.Contains(message, test.want) {
				t.Fatalf("empty Delta reason = %q, want %q", message, test.want)
			}
			if strings.Contains(message, "请重新加载或刷新") {
				t.Fatalf("expected empty state must not advise reloading: %q", message)
			}
		})
	}
	if got := prestackCompareDeltaEmptyMessage(base); !strings.Contains(got, "请重新加载或刷新") {
		t.Fatalf("unexpected missing frame should keep its recovery hint: %q", got)
	}
}
