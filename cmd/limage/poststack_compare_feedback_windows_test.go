//go:build windows

package main

import "testing"

func TestCompareEntryReservesBWithoutShowingDifference(t *testing.T) {
	single := compareLayoutRects(1360, 880, compareDisplayPanelCount(false, false, false))
	comparison := compareLayoutRects(1360, 880, compareDisplayPanelCount(true, false, true))
	if len(single) != 1 || len(comparison) != 2 || comparison[0].Right >= comparison[1].Left || comparison[0].Right >= single[0].Right {
		t.Fatalf("Compare must immediately reflow A and reserve B: single=%v compare=%v", single, comparison)
	}
	if got := compareDisplayPanelCount(true, true, true); got != 3 {
		t.Fatalf("loaded comparison with difference: got %d panels", got)
	}
	if got := compareDisplayPanelCount(true, true, false); got != 2 {
		t.Fatalf("disabled difference: got %d panels", got)
	}
}

func TestCompareBPlaceholderDistinguishesAlgorithmAndLoading(t *testing.T) {
	for _, tt := range []struct {
		hasB, preparing, running, sameSource bool
		status, title, detail                string
	}{
		{false, false, false, true, "旧结果", "B 尚未加载", "点击「加载 B」选择文件，或在「算法处理…」中运行处理。"},
		{false, false, true, true, "计算中", "正在生成 B…", "计算中"},
		{false, false, true, false, "旧结果", "正在生成 B…", "等待当前处理任务返回结果。"},
		{true, true, false, true, "旧结果", "正在加载 B 剖面…", "A 保持可浏览；加载完成后显示 B。"},
		{true, false, false, true, "旧结果", "B 当前剖面暂无图像", "请检查上方状态，或重新加载 B。"},
	} {
		title, detail := compareBPlaceholderText(tt.hasB, tt.preparing, tt.running, tt.sameSource, tt.status)
		if title != tt.title || detail != tt.detail {
			t.Fatalf("%+v got (%s, %s)", tt, title, detail)
		}
	}
}
